package raria2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

type stubDownloadSink struct {
	onWrite func(aria2URLEntry) error
	onClose func() error
}

func (s *stubDownloadSink) Write(entry aria2URLEntry) error {
	if s.onWrite != nil {
		return s.onWrite(entry)
	}
	return nil
}

func (s *stubDownloadSink) Close() error {
	if s.onClose != nil {
		return s.onClose()
	}
	return nil
}

func TestAria2ManagerSessionEntryLimit(t *testing.T) {
	t.Run("disabled_when_non_positive", func(t *testing.T) {
		am := NewAria2Manager()
		am.Aria2EntriesPerSession = 0
		assert.Equal(t, 0, am.sessionEntryLimit())

		am.Aria2EntriesPerSession = -5
		assert.Equal(t, 0, am.sessionEntryLimit())
	})

	t.Run("disabled_when_writing_batch", func(t *testing.T) {
		am := NewAria2Manager()
		am.Aria2EntriesPerSession = 42
		am.WriteBatch = "batch.txt"
		assert.Equal(t, 0, am.sessionEntryLimit())
	})

	t.Run("returns_positive_limit", func(t *testing.T) {
		am := NewAria2Manager()
		am.Aria2EntriesPerSession = 3
		assert.Equal(t, 3, am.sessionEntryLimit())
	})
}

func TestAria2ManagerExecuteBatchDownloadRespectsSessionLimit(t *testing.T) {
	am := NewAria2Manager()
	am.Aria2EntriesPerSession = 2

	var sessions [][]aria2URLEntry
	am.sinkFactory = func(ctx context.Context, _ *RAria2) (downloadSink, error) {
		sessions = append(sessions, nil)
		idx := len(sessions) - 1
		return &stubDownloadSink{
			onWrite: func(entry aria2URLEntry) error {
				sessions[idx] = append(sessions[idx], entry)
				return nil
			},
		}, nil
	}

	entries := make(chan aria2URLEntry, 5)
	for i := 0; i < 5; i++ {
		entries <- aria2URLEntry{URL: fmt.Sprintf("https://example.com/file%02d.bin", i)}
	}
	close(entries)

	assert.NoError(t, am.ExecuteBatchDownload(context.Background(), &RAria2{}, entries))
	if assert.Len(t, sessions, 3) {
		assert.Len(t, sessions[0], 2)
		assert.Len(t, sessions[1], 2)
		assert.Len(t, sessions[2], 1)
	}
}

func TestAria2ManagerExecuteBatchDownloadPropagatesErrors(t *testing.T) {
	am := NewAria2Manager()
	am.Aria2EntriesPerSession = 10

	var closed int
	am.sinkFactory = func(ctx context.Context, _ *RAria2) (downloadSink, error) {
		calls := 0
		return &stubDownloadSink{
			onWrite: func(aria2URLEntry) error {
				calls++
				if calls == 2 {
					return errors.New("boom")
				}
				return nil
			},
			onClose: func() error {
				closed++
				return nil
			},
		}, nil
	}

	entries := make(chan aria2URLEntry, 3)
	entries <- aria2URLEntry{URL: "https://example.com/a"}
	entries <- aria2URLEntry{URL: "https://example.com/b"}
	entries <- aria2URLEntry{URL: "https://example.com/c"}
	close(entries)

	assert.Error(t, am.ExecuteBatchDownload(context.Background(), &RAria2{}, entries))
	assert.Equal(t, 1, closed, "sink should be closed once after write failure")
}

func TestAria2ManagerExecuteBatchDownloadHandlesSinkFactoryError(t *testing.T) {
	am := NewAria2Manager()
	am.sinkFactory = func(context.Context, *RAria2) (downloadSink, error) {
		return nil, errors.New("no sink")
	}

	entries := make(chan aria2URLEntry, 1)
	entries <- aria2URLEntry{URL: "https://example.com/only"}
	close(entries)

	assert.Error(t, am.ExecuteBatchDownload(context.Background(), &RAria2{}, entries))
}

func TestAria2ManagerExecuteBatchDownloadNoEntries(t *testing.T) {
	am := NewAria2Manager()
	called := false
	am.sinkFactory = func(context.Context, *RAria2) (downloadSink, error) {
		called = true
		return &stubDownloadSink{}, nil
	}

	entries := make(chan aria2URLEntry)
	close(entries)

	assert.NoError(t, am.ExecuteBatchDownload(context.Background(), &RAria2{}, entries))
	assert.False(t, called, "sinkFactory should not be invoked when no entries exist")
}

func TestAria2ManagerNewDownloadSinkPrefersWriteBatch(t *testing.T) {
	am := NewAria2Manager()
	am.WriteBatch = filepath.Join(t.TempDir(), "batch.txt")

	sink, err := am.newDownloadSink(context.Background(), &RAria2{})
	assert.NoError(t, err)

	entry := aria2URLEntry{URL: "https://example.com/file.bin", Dir: "downloads"}
	assert.NoError(t, sink.Write(entry))
	assert.NoError(t, sink.Close())

	data, readErr := os.ReadFile(am.WriteBatch)
	assert.NoError(t, readErr)
	assert.Contains(t, string(data), entry.URL)
}

func TestAria2ManagerNewDownloadSinkUsesFactory(t *testing.T) {
	am := NewAria2Manager()
	expected := &stubDownloadSink{}
	var called bool

	am.sinkFactory = func(context.Context, *RAria2) (downloadSink, error) {
		called = true
		return expected, nil
	}

	sink, err := am.newDownloadSink(context.Background(), &RAria2{})
	assert.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, expected, sink)
}

func TestAria2ManagerNewDownloadSinkDefaultsToAria2(t *testing.T) {
	am := NewAria2Manager()
	am.MaxConnectionPerServer = 1
	am.MaxConcurrentDownload = 1

	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	lastExecArgs = nil
	execCommand = fakeExecCommand

	sink, err := am.newDownloadSink(context.Background(), &RAria2{})
	assert.NoError(t, err)

	entry := aria2URLEntry{URL: "https://example.com/asset.bin", Dir: "out"}
	assert.NoError(t, sink.Write(entry))
	assert.NoError(t, sink.Close())

	if assert.NotEmpty(t, lastExecArgs) {
		assert.Equal(t, "aria2c", lastExecArgs[0])
	}
}
