package raria2

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
)

// aria2URLEntry represents a single download entry for aria2
type aria2URLEntry struct {
	URL string
	Dir string
}

// downloadSink interface for different output destinations
type downloadSink interface {
	Write(entry aria2URLEntry) error
	Close() error
}

// Aria2Manager handles aria2 integration and batch processing
type Aria2Manager struct {
	MaxConnectionPerServer int
	MaxConcurrentDownload  int
	Aria2EntriesPerSession int
	DryRun                 bool
	Aria2AfterURLArgs      []string
	WriteBatch             string
	sinkFactory            func(context.Context, *RAria2) (downloadSink, error)
}

// NewAria2Manager creates a new aria2 manager
func NewAria2Manager() *Aria2Manager {
	return &Aria2Manager{
		MaxConnectionPerServer: 5,
		MaxConcurrentDownload:  5,
		Aria2EntriesPerSession: 100,
	}
}

// sessionEntryLimit returns the session limit based on configuration
func (am *Aria2Manager) sessionEntryLimit() int {
	if am.Aria2EntriesPerSession <= 0 {
		return 0
	}
	if am.WriteBatch != "" {
		return 0 // Session size ignored when writing batch
	}
	return am.Aria2EntriesPerSession
}

// ExecuteBatchDownload processes download entries using the appropriate sink
func (am *Aria2Manager) ExecuteBatchDownload(ctx context.Context, raria2 *RAria2, entries <-chan aria2URLEntry) error {
	var (
		sink              downloadSink
		sinkErr           error
		entryCount        int
		sessionEntryCount int
	)

	sessionLimit := am.sessionEntryLimit()
	flushSession := func() error {
		if sink == nil {
			sessionEntryCount = 0
			return nil
		}
		if err := sink.Close(); err != nil {
			return err
		}
		sink = nil
		sessionEntryCount = 0
		return nil
	}

	for entry := range entries {
		if sink == nil {
			sink, sinkErr = am.newDownloadSink(ctx, raria2)
			if sinkErr != nil {
				return sinkErr
			}
		}
		entryCount++
		sessionEntryCount++
		if err := sink.Write(entry); err != nil {
			_ = flushSession()
			return err
		}
		if sessionLimit > 0 && sessionEntryCount >= sessionLimit {
			if err := flushSession(); err != nil {
				return err
			}
		}
	}

	if entryCount == 0 {
		return nil
	}

	if err := flushSession(); err != nil {
		return err
	}

	return nil
}

// newDownloadSink creates the appropriate download sink
func (am *Aria2Manager) newDownloadSink(ctx context.Context, raria2 *RAria2) (downloadSink, error) {
	if am.WriteBatch != "" {
		return newBatchFileSink(am.WriteBatch)
	}
	if am.sinkFactory != nil {
		return am.sinkFactory(ctx, raria2)
	}
	return newAria2Sink(ctx, am)
}

// aria2Sink implements downloadSink for direct aria2 execution
type aria2Sink struct {
	writer    *bufio.Writer
	pipe      *io.PipeWriter
	waitCh    chan error
	closeOnce sync.Once
	closeErr  error
}

// newAria2Sink creates a new aria2 sink
func newAria2Sink(ctx context.Context, am *Aria2Manager) (downloadSink, error) {
	binFile := "aria2c"
	args := []string{"-x", strconv.Itoa(am.MaxConnectionPerServer)}
	if am.MaxConcurrentDownload > 0 {
		args = append(args, "-j", strconv.Itoa(am.MaxConcurrentDownload))
	}
	args = append(args, "--input-file", "-", "--deferred-input=true")

	if am.DryRun {
		args = append(args, "--dry-run=true")
	}

	if len(am.Aria2AfterURLArgs) > 0 {
		args = append(args, am.Aria2AfterURLArgs...)
	}

	if am.DryRun {
		fmt.Printf("aria2 batch cmd: %s %s\n", binFile, strings.Join(args, " "))
	}

	reader, writer := io.Pipe()
	cmd := execCommand(binFile, args...)
	cmd.Stdin = reader
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, fmt.Errorf("failed to start aria2c: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	if ctx != nil {
		go func() {
			<-ctx.Done()
			_ = cmd.Process.Kill()
		}()
	}

	return &aria2Sink{
		writer: bufio.NewWriter(writer),
		pipe:   writer,
		waitCh: waitCh,
	}, nil
}

// Write writes an entry to the aria2 sink
func (s *aria2Sink) Write(entry aria2URLEntry) error {
	if err := writeBatchEntry(s.writer, entry); err != nil {
		return err
	}
	return s.writer.Flush()
}

// Close closes the aria2 sink
func (s *aria2Sink) Close() error {
	s.closeOnce.Do(func() {
		if err := s.writer.Flush(); err != nil {
			s.closeErr = err
			return
		}
		if err := s.pipe.Close(); err != nil && s.closeErr == nil {
			s.closeErr = err
			return
		}
		if waitErr := <-s.waitCh; waitErr != nil && s.closeErr == nil {
			s.closeErr = fmt.Errorf("aria2 batch command failed: %w", waitErr)
		}
	})
	return s.closeErr
}

// batchFileSink implements downloadSink for file output
type batchFileSink struct {
	writer    *bufio.Writer
	file      *os.File
	closeOnce sync.Once
	closeErr  error
}

// newBatchFileSink creates a new batch file sink
func newBatchFileSink(path string) (downloadSink, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("failed to create batch file %s: %w", path, err)
	}
	return &batchFileSink{writer: bufio.NewWriter(file), file: file}, nil
}

// Write writes an entry to the batch file sink
func (s *batchFileSink) Write(entry aria2URLEntry) error {
	return writeBatchEntry(s.writer, entry)
}

// Close closes the batch file sink
func (s *batchFileSink) Close() error {
	s.closeOnce.Do(func() {
		if err := s.writer.Flush(); err != nil {
			s.closeErr = err
			return
		}
		if err := s.file.Close(); err != nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}

// writeBatchEntry writes a single entry to the batch writer
func writeBatchEntry(writer *bufio.Writer, entry aria2URLEntry) error {
	if _, err := fmt.Fprintln(writer, entry.URL); err != nil {
		return err
	}
	if entry.Dir != "" {
		if _, err := fmt.Fprintf(writer, "  dir=%s\n", entry.Dir); err != nil {
			return err
		}
	}
	if _, err := writer.WriteString("\n"); err != nil {
		return err
	}
	return nil
}
