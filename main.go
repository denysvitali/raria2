package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	raria2 "github.com/denysvitali/raria2/pkg"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var version = "dev"

var errUserCanceled = errors.New("operation cancelled by user")

type cliOptions struct {
	Output                 string
	DryRun                 bool
	URL                    string
	LogLevel               string
	MaxConnectionPerServer int
	MaxConcurrentDownload  int
	Threads                int
	Aria2SessionSize       int
	MaxDepth               int
	AcceptExtensions       []string
	RejectExtensions       []string
	AcceptFilenames        []string
	RejectFilenames        []string
	CaseInsensitivePaths   bool
	AcceptPaths            []string
	RejectPaths            []string
	VisitedCachePath       string
	WriteBatch             string
	HTTPTimeout            time.Duration
	UserAgent              string
	RateLimit              float64
	RespectRobots          bool
	AcceptMime             []string
	RejectMime             []string
	Aria2Args              []string
}

func main() {
	rootCmd, opts := newRootCommand()
	if err := rootCmd.Execute(); err != nil {
		logrus.Fatal(err)
	}
	if err := run(opts); err != nil {
		if errors.Is(err, errUserCanceled) {
			os.Exit(130)
		}
		logrus.Fatal(err)
	}
}

func newRootCommand() (*cobra.Command, *cliOptions) {
	opts := &cliOptions{}

	cmd := &cobra.Command{
		Use:           "raria2 [flags] URL [-- ARIA2_OPTS...]",
		Short:         "Mirror open directories using aria2",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("please provide an URL (version: %s)", version)
			}
			opts.URL = args[0]
			opts.Aria2Args = args[1:]
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			return nil
		},
	}
	cmd.Flags().SortFlags = false

	cmd.Flags().StringVarP(&opts.Output, "output", "o", "", "Output directory (defaults to host/path derived from the URL)")
	cmd.Flags().BoolVarP(&opts.DryRun, "dry-run", "d", false, "Dry Run")
	cmd.Flags().StringVar(&opts.LogLevel, "log-level", "info", "Log level (panic,fatal,error,warn,info,debug,trace)")
	cmd.Flags().IntVarP(&opts.MaxConnectionPerServer, "max-connection-per-server", "x", 5, "Parallel connections per download")
	cmd.Flags().IntVarP(&opts.MaxConcurrentDownload, "max-concurrent-downloads", "j", 5, "Maximum concurrent downloads")
	cmd.Flags().IntVarP(&opts.Threads, "threads", "w", 5, "Concurrent crawler threads")
	cmd.Flags().IntVar(&opts.Aria2SessionSize, "aria2-session-size", 100, "Number of links to send to a single aria2c process before restarting it (0 = unlimited)")
	cmd.Flags().IntVar(&opts.MaxDepth, "max-depth", -1, "Maximum HTML depth to crawl (-1 for unlimited)")
	cmd.Flags().StringSliceVar(&opts.AcceptExtensions, "accept", nil, "Comma-separated list(s) of file extensions to include (case-insensitive, without dot)")
	cmd.Flags().StringSliceVar(&opts.RejectExtensions, "reject", nil, "Comma-separated list(s) of file extensions to exclude")
	cmd.Flags().StringSliceVar(&opts.AcceptFilenames, "accept-filename", nil, "Comma-separated list(s) of filename globs to include")
	cmd.Flags().StringSliceVar(&opts.RejectFilenames, "reject-filename", nil, "Comma-separated list(s) of filename globs to exclude")
	cmd.Flags().BoolVar(&opts.CaseInsensitivePaths, "case-insensitive-paths", false, "Make path matching case-insensitive")
	cmd.Flags().StringSliceVar(&opts.AcceptPaths, "accept-path", nil, "Path glob or regex (prefix with regex:) to include")
	cmd.Flags().StringSliceVar(&opts.RejectPaths, "reject-path", nil, "Path glob or regex (prefix with regex:) to exclude")
	cmd.Flags().StringVar(&opts.VisitedCachePath, "visited-cache", "", "Optional file to persist visited URLs for resuming crawls")
	cmd.Flags().StringVar(&opts.WriteBatch, "write-batch", "", "Write aria2 input file to disk instead of executing")
	cmd.Flags().DurationVar(&opts.HTTPTimeout, "http-timeout", 30*time.Second, "HTTP client timeout")
	cmd.Flags().StringVar(&opts.UserAgent, "user-agent", "raria2/1.0", "Custom User-Agent string")
	cmd.Flags().Float64Var(&opts.RateLimit, "rate-limit", 0, "Rate limit for HTTP requests (requests per second)")
	cmd.Flags().BoolVar(&opts.RespectRobots, "respect-robots", false, "Respect robots.txt when crawling")
	cmd.Flags().StringSliceVar(&opts.AcceptMime, "accept-mime", nil, "Comma-separated list of MIME types to include")
	cmd.Flags().StringSliceVar(&opts.RejectMime, "reject-mime", nil, "Comma-separated list of MIME types to exclude")

	return cmd, opts
}

func run(opts *cliOptions) error {
	level, err := logrus.ParseLevel(opts.LogLevel)
	if err != nil {
		return fmt.Errorf("invalid log level %q: %w", opts.LogLevel, err)
	}
	logrus.SetLevel(level)

	parsedURL, err := url.Parse(opts.URL)
	if err != nil {
		return fmt.Errorf("invalid URL provided: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	go func() {
		sig := <-sigChan
		logrus.Infof("Received signal %v, shutting down gracefully...", sig)
		cancel()
	}()

	client := raria2.New(parsedURL)
	client.OutputPath = opts.Output
	client.Aria2AfterURLArgs = opts.Aria2Args
	client.DryRun = opts.DryRun
	client.HTTPTimeout = opts.HTTPTimeout
	client.UserAgent = opts.UserAgent
	client.RateLimit = opts.RateLimit
	client.MaxDepth = opts.MaxDepth
	client.RespectRobots = opts.RespectRobots
	client.VisitedCachePath = opts.VisitedCachePath
	client.WriteBatch = opts.WriteBatch

	if err := setPositiveValue("--max-connection-per-server", opts.MaxConnectionPerServer); err != nil {
		return err
	}
	client.MaxConnectionPerServer = opts.MaxConnectionPerServer

	if err := setPositiveValue("--max-concurrent-downloads", opts.MaxConcurrentDownload); err != nil {
		return err
	}
	client.MaxConcurrentDownload = opts.MaxConcurrentDownload

	if err := setPositiveValue("--threads", opts.Threads); err != nil {
		return err
	}
	client.Threads = opts.Threads
	client.Aria2EntriesPerSession = opts.Aria2SessionSize

	filters := client.FiltersConfig()
	filters.AcceptMime = parseMimeArgs(opts.AcceptMime)
	filters.RejectMime = parseMimeArgs(opts.RejectMime)
	filters.AcceptExtensions = parseExtensionArgs(opts.AcceptExtensions)
	filters.RejectExtensions = parseExtensionArgs(opts.RejectExtensions)
	filters.AcceptFilenames = parseGlobArgs(opts.AcceptFilenames)
	filters.RejectFilenames = parseGlobArgs(opts.RejectFilenames)
	filters.CaseInsensitivePaths = opts.CaseInsensitivePaths

	acceptRegex, err := compilePathPatterns(opts.AcceptPaths)
	if err != nil {
		return fmt.Errorf("invalid --accept-path pattern: %w", err)
	}
	rejectRegex, err := compilePathPatterns(opts.RejectPaths)
	if err != nil {
		return fmt.Errorf("invalid --reject-path pattern: %w", err)
	}
	filters.AcceptPathRegex = acceptRegex
	filters.RejectPathRegex = rejectRegex

	if err := client.RunWithContext(ctx); err != nil {
		if ctx.Err() == context.Canceled {
			logrus.Info("Operation cancelled by user")
			return errUserCanceled
		}
		return err
	}

	return nil
}

func setPositiveValue(name string, value int) error {
	if value < 1 {
		return fmt.Errorf("invalid value for %s: %d", name, value)
	}
	return nil
}

func parseExtensionArgs(values []string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, value := range splitAndTrim(values) {
		value = strings.TrimPrefix(value, ".")
		if value != "" {
			set[strings.ToLower(value)] = struct{}{}
		}
	}
	return set
}

func parseMimeArgs(values []string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, value := range splitAndTrim(values) {
		value = strings.TrimSpace(value)
		if value != "" {
			set[strings.ToLower(value)] = struct{}{}
		}
	}
	return set
}

func parseGlobArgs(values []string) map[string]*regexp.Regexp {
	patterns := make(map[string]*regexp.Regexp)
	for _, value := range splitAndTrim(values) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		re, err := compilePathPattern("glob:" + value)
		if err != nil {
			logrus.Warnf("invalid filename glob pattern %q: %v", value, err)
			continue
		}
		patterns[value] = re
	}
	if len(patterns) == 0 {
		return nil
	}
	return patterns
}

func compilePathPatterns(patterns []string) ([]*regexp.Regexp, error) {
	var compiled []*regexp.Regexp
	for _, raw := range splitAndTrim(patterns) {
		if raw == "" {
			continue
		}
		re, err := compilePathPattern(raw)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}

func compilePathPattern(pattern string) (*regexp.Regexp, error) {
	const regexPrefix = "regex:"
	const globPrefix = "glob:"
	switch {
	case strings.HasPrefix(pattern, regexPrefix):
		return regexp.Compile(pattern[len(regexPrefix):])
	case strings.HasPrefix(pattern, globPrefix):
		pattern = pattern[len(globPrefix):]
	}
	return regexp.Compile(globToRegex(pattern))
}

func globToRegex(glob string) string {
	var b strings.Builder
	b.WriteString("^")

	for i := 0; i < len(glob); {
		ch := glob[i]
		switch ch {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				b.WriteString(".*")
				i += 2
				continue
			}
			b.WriteString("[^/]*")
			i++
			continue
		case '?':
			b.WriteString("[^/]")
			i++
			continue
		case '/':
			b.WriteByte('/')
			i++
			continue
		default:
			switch ch {
			case '.', '+', '(', ')', '|', '[', ']', '{', '}', '^', '$', '\\':
				b.WriteByte('\\')
			}
			b.WriteByte(ch)
			i++
		}
	}

	b.WriteString("$")
	return b.String()
}

func splitAndTrim(values []string) []string {
	var result []string
	for _, value := range values {
		parts := strings.Split(value, ",")
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				result = append(result, trimmed)
			}
		}
	}
	return result
}
