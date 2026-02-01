package raria2

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/temoto/robotstxt"
)

func TestSameUrl(t *testing.T) {
	firstUrl, _ := url.Parse("https://example.com/")
	secondUrl, _ := url.Parse("https://example.com/?C=S;O=A")
	thirdUrl, _ := url.Parse("https://example.com/?C=M;O=A")
	fourthUrl, _ := url.Parse("https://example.com/a")
	assert.True(t, SameUrl(firstUrl, secondUrl))
	assert.True(t, SameUrl(firstUrl, thirdUrl))
	assert.True(t, SameUrl(secondUrl, thirdUrl))
	assert.False(t, SameUrl(firstUrl, fourthUrl))
}

func TestSubDownloadUrlsQueuesDownloads(t *testing.T) {
	ts := newFixtureServer()
	t.Cleanup(ts.Close)

	base, _ := url.Parse(ts.URL + "/")
	r := newTestClient(base)
	r.OutputPath = tempDir(t)
	r.urlCache = NewURLCache("")
	r.MaxDepth = 1

	r.subDownloadUrls(context.Background(), 0, base.String())
	assert.NotEmpty(t, r.downloadEntries)
	assert.False(t, r.markVisited(base.String()))
}

func TestSubDownloadUrlsRespectsDepthLimit(t *testing.T) {
	ts := newFixtureServer()
	t.Cleanup(ts.Close)

	base, _ := url.Parse(ts.URL + "/")
	r := newTestClient(base)
	r.OutputPath = tempDir(t)
	r.urlCache = NewURLCache("")
	r.MaxDepth = 0

	r.subDownloadUrls(context.Background(), 0, base.String())
	assert.Len(t, r.downloadEntries, 0)
}

func TestSubDownloadUrlsContextCancellation(t *testing.T) {
	r := &RAria2{url: mustParseURL(t, "https://example.com/root/"), urlCache: NewURLCache("")}
	r.FiltersConfig()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.subDownloadUrls(ctx, 0, r.url.String())
	assert.Empty(t, r.downloadEntries)
}

func TestSubDownloadUrlsRespectsRobots(t *testing.T) {
	base := mustParseURL(t, "https://example.com/root/")
	r := &RAria2{
		url:           base,
		RespectRobots: true,
		urlCache:      NewURLCache(""),
	}
	r.FiltersConfig()
	robotsData, err := robotstxt.FromBytes([]byte("User-agent: *\nDisallow: /\n"))
	assert.NoError(t, err)
	r.robotsCache = map[string]*robotstxt.RobotsData{"example.com": robotsData}

	r.subDownloadUrls(context.Background(), 0, base.String())
	assert.Empty(t, r.downloadEntries)
}

func TestDownloadResourceInvalidURL(t *testing.T) {
	r := &RAria2{
		UserAgent:  "test",
		OutputPath: tempDir(t),
		DryRun:     true,
		url:        mustParseURL(t, "https://example.com/root/"),
	}

	r.downloadResource(0, "http://%gh&%")
	assert.Len(t, r.downloadEntries, 0)
}

func TestLoadVisitedCacheInitializesCache(t *testing.T) {
	tmp := tempDir(t)
	cacheFile := filepath.Join(tmp, "visited.txt")
	writeVisitedCache(t, cacheFile, []string{"https://example.com/path"})

	r := &RAria2{VisitedCachePath: cacheFile}
	assert.NoError(t, r.loadVisitedCache())
	if assert.NotNil(t, r.urlCache) {
		assert.False(t, r.markVisited("https://example.com/path"))
	}
}

func TestSaveVisitedCacheHandlesNil(t *testing.T) {
	r := &RAria2{}
	assert.NoError(t, r.saveVisitedCache())
}

func TestSaveVisitedCacheWritesEntries(t *testing.T) {
	tmp := tempDir(t)
	cacheFile := filepath.Join(tmp, "visited.txt")
	r := &RAria2{VisitedCachePath: cacheFile, urlCache: NewURLCache("")}
	r.markVisited("https://example.com/new")

	assert.NoError(t, r.saveVisitedCache())
	entries := readVisitedCache(t, cacheFile)
	assert.Contains(t, entries, canonicalURL("https://example.com/new"))
}

func TestWriteBatchFile(t *testing.T) {
	tmp := tempDir(t)
	path := filepath.Join(tmp, "batch.txt")
	r := &RAria2{WriteBatch: path}
	r.downloadEntries = []aria2URLEntry{{}, {}}

	content := []byte("entry-one\nentry-two\n")
	assert.NoError(t, r.writeBatchFile(content))

	written, err := os.ReadFile(path)
	assert.NoError(t, err)
	assert.Equal(t, content, written)
}

func TestWriteBatchFileFailsWithoutDirectory(t *testing.T) {
	tmp := tempDir(t)
	path := filepath.Join(tmp, "missing", "batch.txt")
	r := &RAria2{WriteBatch: path}

	err := r.writeBatchFile([]byte("data"))
	assert.Error(t, err)
}

func TestRAria2SessionEntryLimit(t *testing.T) {
	r := &RAria2{Aria2EntriesPerSession: 3}
	assert.Equal(t, 3, r.sessionEntryLimit())

	r.Aria2EntriesPerSession = 0
	assert.Equal(t, 0, r.sessionEntryLimit())

	r.Aria2EntriesPerSession = 10
	r.WriteBatch = "batch.txt"
	assert.Equal(t, 0, r.sessionEntryLimit())
}

func TestMarkVisitedInitializesCache(t *testing.T) {
	r := &RAria2{}
	assert.True(t, r.markVisited("https://example.com/a"))
	assert.False(t, r.markVisited("https://example.com/a"))
}

func runTestClient(t *testing.T, client *RAria2) error {
	t.Helper()
	return client.RunWithContext(context.Background())
}

type mockSink struct {
	writeFn func(aria2URLEntry) error
	closeFn func() error
}

func (m *mockSink) Write(entry aria2URLEntry) error {
	if m.writeFn != nil {
		return m.writeFn(entry)
	}
	return nil
}

func (m *mockSink) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}

func TestPathAllowedAcceptAndReject(t *testing.T) {
	base := mustParseURL(t, "https://example.com/root/")
	r := &RAria2{url: base}
	filters := r.FiltersConfig()
	filters.AcceptPathRegex = []*regexp.Regexp{
		regexp.MustCompile(`^/root/files/`),
	}

	assert.True(t, filters.PathAllowed(mustParseURL(t, "https://example.com/root/")))
	assert.True(t, filters.PathAllowed(mustParseURL(t, "https://example.com/root/files/doc.bin")))
	assert.False(t, filters.PathAllowed(mustParseURL(t, "https://example.com/root/misc")))

	filters.RejectPathRegex = []*regexp.Regexp{regexp.MustCompile(`\.tmp$`)}
	assert.False(t, filters.PathAllowed(mustParseURL(t, "https://example.com/root/files/data.tmp")))
}

func TestFilenameAllowedAcceptAndReject(t *testing.T) {
	base := mustParseURL(t, "https://example.com/root/")
	r := &RAria2{url: base}
	filters := r.FiltersConfig()

	// Test accept filename glob
	pattern := regexp.MustCompile(`^file.*\.bin$`)
	filters.AcceptFilenames = map[string]*regexp.Regexp{
		"file*.bin": pattern,
	}
	assert.True(t, filters.FilenameAllowed(mustParseURL(t, "https://example.com/root/file1.bin")))
	assert.True(t, filters.FilenameAllowed(mustParseURL(t, "https://example.com/root/file123.bin")))
	assert.False(t, filters.FilenameAllowed(mustParseURL(t, "https://example.com/root/other.bin")))

	// Test reject filename glob
	filters.AcceptFilenames = nil
	pattern = regexp.MustCompile(`^.*\.tmp$`)
	filters.RejectFilenames = map[string]*regexp.Regexp{
		"*.tmp": pattern,
	}
	assert.False(t, filters.FilenameAllowed(mustParseURL(t, "https://example.com/root/data.tmp")))
	assert.True(t, filters.FilenameAllowed(mustParseURL(t, "https://example.com/root/data.bin")))
}

func TestCaseInsensitivePaths(t *testing.T) {
	base := mustParseURL(t, "https://example.com/root/")
	r := &RAria2{url: base}
	filters := r.FiltersConfig()
	filters.CaseInsensitivePaths = true

	// Test case-insensitive path matching
	pattern := regexp.MustCompile(`^/root/files/.*`)
	filters.AcceptPathRegex = []*regexp.Regexp{pattern}
	assert.True(t, filters.PathAllowed(mustParseURL(t, "https://example.com/root/FILES/data.bin")))
	assert.True(t, filters.PathAllowed(mustParseURL(t, "https://example.com/root/files/data.bin")))

	// Test case-insensitive reject
	filters.AcceptPathRegex = nil
	pattern = regexp.MustCompile(`^/root/temp/.*`)
	filters.RejectPathRegex = []*regexp.Regexp{pattern}
	assert.False(t, filters.PathAllowed(mustParseURL(t, "https://example.com/root/TEMP/data.bin")))
	assert.True(t, filters.PathAllowed(mustParseURL(t, "https://example.com/root/other/data.bin")))
}

func TestExtensionAllowedAcceptAndReject(t *testing.T) {
	base := mustParseURL(t, "https://example.com/root/")
	r := &RAria2{url: base}
	filters := r.FiltersConfig()
	filters.AcceptExtensions = map[string]struct{}{"bin": {}, "iso": {}}
	assert.True(t, filters.ExtensionAllowed(mustParseURL(t, "https://example.com/root/image.iso")))
	assert.False(t, filters.ExtensionAllowed(mustParseURL(t, "https://example.com/root/image.txt")))

	filters.AcceptExtensions = nil
	filters.RejectExtensions = map[string]struct{}{"zip": {}}
	assert.False(t, filters.ExtensionAllowed(mustParseURL(t, "https://example.com/root/archive.zip")))
	assert.True(t, filters.ExtensionAllowed(mustParseURL(t, "https://example.com/root/archive.bin")))
}

func TestMatchAnyRegexHelper(t *testing.T) {
	patterns := []*regexp.Regexp{regexp.MustCompile(`foo`), regexp.MustCompile(`bar`)}
	assert.True(t, matchAnyRegex(patterns, "xxbarxx"))
	assert.False(t, matchAnyRegex(patterns, "baz"))
}

func TestEnsureOutputDirExisting(t *testing.T) {
	tmp := tempDir(t)
	target := filepath.Join(tmp, "existing")
	assert.NoError(t, os.MkdirAll(target, 0o755))
	r := &RAria2{}
	assert.NoError(t, r.ensureOutputDir(0, target))
}

func TestEnqueueDownloadEntry(t *testing.T) {
	r := &RAria2{}
	r.enqueueDownloadEntry(aria2URLEntry{URL: "https://example.com/a", Dir: "out"})
	assert.Len(t, r.downloadEntries, 1)
	assert.Equal(t, "https://example.com/a", r.downloadEntries[0].URL)
}

func TestIsHTMLContent(t *testing.T) {
	assert.True(t, IsHTMLContent("text/html; charset=utf-8"))
	assert.True(t, IsHTMLContent("text/html"))
	assert.False(t, IsHTMLContent("application/octet-stream"))
}

func TestRun_FailsWhenAria2Missing(t *testing.T) {
	oldLookPath := lookPath
	defer func() { lookPath = oldLookPath }()
	lookPath = func(string) (string, error) {
		return "", fmt.Errorf("not found")
	}

	client := &RAria2{}
	err := runTestClient(t, client)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "aria2c is required")
}

func newTestRAria2ForHTTP(t *testing.T, handler http.HandlerFunc) (*RAria2, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	r := &RAria2{
		UserAgent:      "test-agent",
		DisableRetries: true,
	}
	r.httpClient = NewHTTPClient(2*time.Second, 0)
	r.httpClient.client = server.Client()

	return r, server.URL + "/resource"
}

func TestRAria2IsHtmlPage(t *testing.T) {
	tests := []struct {
		name      string
		handler   func(t *testing.T) http.HandlerFunc
		expect    bool
		expectErr bool
	}{
		{
			name:   "head_declares_html",
			expect: true,
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, http.MethodHead, r.Method)
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					w.WriteHeader(http.StatusOK)
				}
			},
		},
		{
			name:   "head_missing_content_type_triggers_range_get",
			expect: true,
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					switch r.Method {
					case http.MethodHead:
						w.WriteHeader(http.StatusOK)
					case http.MethodGet:
						assert.Equal(t, "bytes=0-1023", r.Header.Get("Range"))
						w.WriteHeader(http.StatusPartialContent)
						_, _ = w.Write([]byte("<!doctype html><html></html>"))
					default:
						t.Fatalf("unexpected method: %s", r.Method)
					}
				}
			},
		},
		{
			name:   "range_not_supported_falls_back_to_full_get",
			expect: true,
			handler: func(t *testing.T) http.HandlerFunc {
				var rangeAttempt bool
				return func(w http.ResponseWriter, r *http.Request) {
					switch r.Method {
					case http.MethodHead:
						w.WriteHeader(http.StatusOK)
					case http.MethodGet:
						if r.Header.Get("Range") != "" {
							rangeAttempt = true
							w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
							return
						}
						assert.True(t, rangeAttempt, "expected ranged request before full GET")
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("<html><body>full</body></html>"))
					default:
						t.Fatalf("unexpected method: %s", r.Method)
					}
				}
			},
		},
		{
			name:   "non_html_content",
			expect: false,
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, http.MethodHead, r.Method)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
				}
			},
		},
		{
			name:      "head_error",
			expectErr: true,
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, url := newTestRAria2ForHTTP(t, tt.handler(t))
			result, err := r.IsHtmlPage(url)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestDownloadResource_ExtensionFilters(t *testing.T) {
	baseURL, _ := url.Parse("https://example.com/root/")
	outputDir := tempDir(t)
	r := &RAria2{
		url:        baseURL,
		OutputPath: outputDir,
		DryRun:     true,
	}
	filters := r.FiltersConfig()
	filters.AcceptExtensions = map[string]struct{}{"bin": {}}

	r.downloadResource(1, "https://example.com/root/file.bin")
	assert.Len(t, r.downloadEntries, 1)

	r.downloadEntries = nil
	filters.AcceptExtensions = map[string]struct{}{"txt": {}}
	r.downloadResource(1, "https://example.com/root/file.bin")
	assert.Len(t, r.downloadEntries, 0)

	filters.AcceptExtensions = nil
	filters.RejectExtensions = map[string]struct{}{"bin": {}}
	r.downloadResource(1, "https://example.com/root/file.bin")
	assert.Len(t, r.downloadEntries, 0)
}

func TestRun_PathFilters(t *testing.T) {
	ts := newFixtureServer()
	t.Cleanup(ts.Close)

	baseURL, err := url.Parse(ts.URL + "/")
	assert.Nil(t, err)
	client := newTestClient(baseURL)
	client.OutputPath = tempDir(t)
	client.FiltersConfig().AcceptPathRegex = []*regexp.Regexp{regexp.MustCompile(`^/files/`)}

	err = runTestClient(t, client)
	assert.Nil(t, err)
	if assert.Len(t, client.downloadEntries, 2) {
		for _, entry := range client.downloadEntries {
			assert.Contains(t, entry.URL, "/files/")
		}
	}
}

func TestRun_RespectsMaxDepth(t *testing.T) {
	ts := newFixtureServer()
	t.Cleanup(ts.Close)

	baseURL, err := url.Parse(ts.URL + "/")
	assert.Nil(t, err)
	client := New(baseURL)
	client.OutputPath = tempDir(t)
	client.DryRun = true
	client.MaxDepth = 0

	err = client.RunWithContext(context.Background())
	assert.Nil(t, err)
	assert.Len(t, client.downloadEntries, 0)
}

var lastExecArgs []string

func fakeExecCommand(command string, args ...string) *exec.Cmd {
	lastExecArgs = append([]string{command}, args...)
	cs := []string{"-test.run=TestHelperProcess", "--", command}
	cs = append(cs, args...)
	cmd := exec.Command(os.Args[0], cs...)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	return cmd
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	os.Exit(0)
}

func TestHelperProcessCaptureInput(t *testing.T) {
	if os.Getenv("GO_WANT_CAPTURE_INPUT") != "1" {
		return
	}
	capturePath := os.Getenv("CAPTURE_INPUT_PATH")
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read stdin: %v", err)
		os.Exit(2)
	}
	if capturePath != "" {
		if err := os.WriteFile(capturePath, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write capture file: %v", err)
			os.Exit(3)
		}
	}
	os.Exit(0)
}

func TestExecuteBatchDownloadInvokesCommand(t *testing.T) {
	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	lastExecArgs = nil
	execCommand = fakeExecCommand

	r := &RAria2{
		MaxConnectionPerServer: 2,
		MaxConcurrentDownload:  1,
		urlCache:               NewURLCache(""),
	}

	entry := aria2URLEntry{URL: "https://example.com/file1.bin", Dir: "out"}
	r.downloadEntries = append(r.downloadEntries, entry)

	entries := make(chan aria2URLEntry, 1)
	entries <- entry
	close(entries)

	assert.NoError(t, r.executeBatchDownload(context.Background(), entries))
	if assert.NotEmpty(t, lastExecArgs) {
		assert.Equal(t, "aria2c", lastExecArgs[0])
		assert.Contains(t, lastExecArgs, "--input-file")
	}
}

func TestExecuteBatchDownloadNoEntries(t *testing.T) {
	r := &RAria2{}
	entries := make(chan aria2URLEntry)
	close(entries)
	assert.NoError(t, r.executeBatchDownload(context.Background(), entries))
}

func TestExecuteBatchDownloadRespectsSessionSize(t *testing.T) {
	r := &RAria2{Aria2EntriesPerSession: 2}
	var sessionWrites []int
	r.sinkFactory = func(ctx context.Context, _ *RAria2) (downloadSink, error) {
		sessionWrites = append(sessionWrites, 0)
		idx := len(sessionWrites) - 1
		return &mockSink{
			writeFn: func(aria2URLEntry) error {
				sessionWrites[idx]++
				return nil
			},
		}, nil
	}

	entries := make(chan aria2URLEntry, 5)
	for i := 0; i < 5; i++ {
		entries <- aria2URLEntry{URL: fmt.Sprintf("https://example.com/file%02d.bin", i)}
	}
	close(entries)

	assert.NoError(t, r.executeBatchDownload(context.Background(), entries))
	assert.Equal(t, []int{2, 2, 1}, sessionWrites)
}

func TestWaitForRateLimitHonorsInterval(t *testing.T) {
	r := &RAria2{RateLimit: 2} // 2 requests per second -> 500ms interval
	r.client().waitForRateLimit()
	start := time.Now()
	r.client().waitForRateLimit()
	elapsed := time.Since(start)
	minInterval := time.Duration(float64(time.Second) / r.RateLimit)
	assert.GreaterOrEqual(t, elapsed, minInterval-50*time.Millisecond)
}

func TestGetLinksByUrlFetchesRemoteLinks(t *testing.T) {
	ts := newFixtureServer()
	t.Cleanup(ts.Close)
	base, _ := url.Parse(ts.URL + "/")
	r := newTestClient(base)

	links, err := r.getLinksByUrl(ts.URL + "/")
	assert.NoError(t, err)
	assert.Contains(t, links, ts.URL+"/files/file1.bin")
	assert.Contains(t, links, ts.URL+"/more/")
}

func TestGetLinksByUrlRejectsInvalidURL(t *testing.T) {
	r := &RAria2{}
	_, err := r.getLinksByUrl(":/broken")
	assert.Error(t, err)
}

func tempDir(t *testing.T) string {
	dir, err := os.MkdirTemp("", "raria2-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestRun_WithRootFixture(t *testing.T) {
	ts := newFixtureServer()
	t.Cleanup(ts.Close)

	webUrl, err := url.Parse(ts.URL + "/")
	assert.Nil(t, err)
	client := New(webUrl)
	client.OutputPath = tempDir(t)
	client.DryRun = true

	err = client.RunWithContext(context.Background())
	assert.Nil(t, err)
}

func TestRun_FromNestedPath(t *testing.T) {
	ts := newFixtureServer()
	t.Cleanup(ts.Close)

	webUrl, err := url.Parse(ts.URL + "/more/")
	assert.Nil(t, err)
	client := New(webUrl)
	client.OutputPath = tempDir(t)
	client.DryRun = true
	client.MaxConcurrentDownload = 2
	client.MaxConnectionPerServer = 2

	err = client.RunWithContext(context.Background())
	assert.Nil(t, err)
}

func TestIsHtmlPage_DistinguishesHtml(t *testing.T) {
	ts := newFixtureServer()
	t.Cleanup(ts.Close)
	base, _ := url.Parse(ts.URL + "/")
	client := newTestClient(base)

	isHtmlPage, err := client.IsHtmlPage(ts.URL + "/")
	assert.Nil(t, err)
	assert.True(t, isHtmlPage)

	isHtmlPage, err = client.IsHtmlPage(ts.URL + "/files/file1.bin")
	assert.Nil(t, err)
	assert.False(t, isHtmlPage)
}

func newFixtureServer() *httptest.Server {
	mux := http.NewServeMux()

	serveHTML := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if r.Method == http.MethodHead {
				return
			}
			if _, err := io.WriteString(w, body); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}
	}

	serveFile := func(content string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			if r.Method == http.MethodHead {
				return
			}
			if _, err := io.WriteString(w, content); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}
	}

	mux.HandleFunc("/", serveHTML(`<html><body>
<a href="/files/file1.bin">file1</a>
<a href="/files/file2.bin">file2</a>
<a href="/more/">more</a>
</body></html>`))

	mux.HandleFunc("/more/", serveHTML(`<html><body>
<a href="/files/file3.bin">file3</a>
<a href="/files/file4.bin">file4</a>
</body></html>`))

	mux.HandleFunc("/files/file1.bin", serveFile("file-1"))
	mux.HandleFunc("/files/file2.bin", serveFile("file-2"))
	mux.HandleFunc("/files/file3.bin", serveFile("file-3"))
	mux.HandleFunc("/files/file4.bin", serveFile("file-4"))

	return httptest.NewServer(mux)
}

func TestDownloadResource_ComputesRelativeOutputDir(t *testing.T) {
	baseURL, _ := url.Parse("https://example.com/root/")
	outputDir := tempDir(t)
	r := &RAria2{
		url:        baseURL,
		OutputPath: outputDir,
		DryRun:     true,
	}

	r.downloadResource(1, "https://example.com/root/assets/img/file.png")

	if assert.Len(t, r.downloadEntries, 1) {
		entry := r.downloadEntries[0]
		assert.Equal(t, "https://example.com/root/assets/img/file.png", entry.URL)
		assert.Equal(t, filepath.Join(outputDir, "assets/img"), entry.Dir)
	}
}

func TestDownloadResource_ExternalHostFallsBackToHostDir(t *testing.T) {
	baseURL, _ := url.Parse("https://example.com/root/")
	outputDir := tempDir(t)
	r := &RAria2{
		url:            baseURL,
		OutputPath:     outputDir,
		DryRun:         true,
		DisableRetries: true, // Disable retries to avoid network timeout
	}

	// Mock the HTTP request to avoid network dependency
	originalExecCommand := execCommand
	defer func() { execCommand = originalExecCommand }()

	// Create a mock server that returns a simple response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Use the test server URL directly (it's HTTP, not HTTPS)
	testURL := server.URL + "/files/asset.bin"
	r.downloadResource(1, testURL)

	if assert.Len(t, r.downloadEntries, 1) {
		entry := r.downloadEntries[0]
		// Extract hostname from the test URL
		parsedURL, _ := url.Parse(testURL)
		expectedDir := filepath.Join(outputDir, parsedURL.Host+"/files")
		assert.Equal(t, expectedDir, entry.Dir)
	}
}

func TestGetLinks_FiltersNonSubPaths(t *testing.T) {
	original, _ := url.Parse("https://example.com/root/")
	body := io.NopCloser(strings.NewReader(`
<html><body>
  <a href="https://example.com/root/file1.bin">file1</a>
  <a href="/root/file2.bin">file2</a>
  <a href="/other/file3.bin">skip</a>
  <a href="https://other.com/root/file4.bin">skip</a>
</body></html>`))

	links, err := getLinks(original, body)
	assert.NoError(t, err)
	assert.Equal(t, []string{
		"https://example.com/root/file1.bin",
		"https://example.com/root/file2.bin",
	}, links)
}

func TestGetLinks_MirrorNforceFormat(t *testing.T) {
	base, _ := url.Parse("https://mirror.nforce.com/pub/speedtests/")
	body := io.NopCloser(strings.NewReader(`
<html><body>
  <table>
    <tr><td><a href="10mb.bin">&lt;10mb.bin&gt;</a></td></tr>
    <tr><td><a href="100mb.bin">&lt;100mb.bin&gt;</a></td></tr>
    <tr><td><a href="nested/">&lt;nested/&gt;</a></td></tr>
  </table>
</body></html>`))

	links, err := getLinks(base, body)
	assert.NoError(t, err)
	assert.Equal(t, []string{
		"https://mirror.nforce.com/pub/speedtests/10mb.bin",
		"https://mirror.nforce.com/pub/speedtests/100mb.bin",
		"https://mirror.nforce.com/pub/speedtests/nested/",
	}, links)
}

func TestGetLinks_CopypartyFormat(t *testing.T) {
	base, _ := url.Parse("https://a.ocv.me/pub/demo/")
	body := io.NopCloser(strings.NewReader(`
<html><body>
  <table>
    <tr><td>DIR</td><td><a href="docs/">&lt;docs/&gt;</a></td></tr>
    <tr><td>DIR</td><td><a href="pics-vids/">&lt;pics-vids/&gt;</a></td></tr>
    <tr><td>-</td><td><a href="showcase-hq.webm">showcase-hq.webm</a></td></tr>
  </table>
</body></html>`))

	links, err := getLinks(base, body)
	assert.NoError(t, err)
	assert.Equal(t, []string{
		"https://a.ocv.me/pub/demo/docs/",
		"https://a.ocv.me/pub/demo/pics-vids/",
		"https://a.ocv.me/pub/demo/showcase-hq.webm",
	}, links)
}

func TestEnsureOutputPathDerivesFromURL(t *testing.T) {
	origDir, err := os.Getwd()
	assert.NoError(t, err)
	tmp := tempDir(t)
	assert.NoError(t, os.Chdir(tmp))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	base := mustParseURL(t, "https://example.com/root/path/")
	r := &RAria2{url: base, urlCache: NewURLCache("")}

	err = r.ensureOutputPath()
	assert.NoError(t, err)
	expected := filepath.Join("example.com", filepath.FromSlash("root/path"))
	assert.Equal(t, expected, r.OutputPath)
	_, statErr := os.Stat(expected)
	assert.NoError(t, statErr)
}

func TestEnsureOutputPathHonorsCustomDir(t *testing.T) {
	tmp := tempDir(t)
	custom := filepath.Join(tmp, "custom-out")
	base := mustParseURL(t, "https://example.com/root/")
	r := &RAria2{url: base, OutputPath: custom, urlCache: NewURLCache("")}
	assert.NoError(t, os.MkdirAll(custom, 0o755))

	assert.NoError(t, r.ensureOutputPath())
	assert.Equal(t, custom, r.OutputPath)
}

func TestEnsureOutputPathFailWithoutURL(t *testing.T) {
	r := &RAria2{urlCache: NewURLCache("")}
	err := r.ensureOutputPath()
	assert.Error(t, err)
}

func TestEnsureOutputDirCreatesDirectories(t *testing.T) {
	tmp := tempDir(t)
	target := filepath.Join(tmp, "nested", "dir")
	r := &RAria2{urlCache: NewURLCache("")}

	assert.NoError(t, r.ensureOutputDir(0, target))
	info, err := os.Stat(target)
	assert.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestEnsureOutputDirDryRunSkipsCreation(t *testing.T) {
	tmp := tempDir(t)
	target := filepath.Join(tmp, "dry", "dir")
	r := &RAria2{DryRun: true, urlCache: NewURLCache("")}

	assert.NoError(t, r.ensureOutputDir(1, target))
	_, err := os.Stat(target)
	assert.Error(t, err)
	assert.True(t, os.IsNotExist(err))
}

func TestMarkVisitedCachesURLs(t *testing.T) {
	r := &RAria2{urlCache: NewURLCache("")}
	assert.True(t, r.markVisited("https://example.com"))
	assert.False(t, r.markVisited("https://example.com"))
}

func TestVisitedCachePersistence(t *testing.T) {
	tmp := tempDir(t)
	cacheFile := filepath.Join(tmp, "visited.txt")
	initial := []string{
		"https://example.com/root/",
		"https://example.com/root/file.bin",
	}
	writeVisitedCache(t, cacheFile, initial)

	r := &RAria2{urlCache: NewURLCache(""), VisitedCachePath: cacheFile}
	assert.NoError(t, r.loadVisitedCache())

	assert.False(t, r.markVisited(initial[0]))
	assert.False(t, r.markVisited(initial[1]))
	assert.True(t, r.markVisited("https://example.com/root/new.bin"))

	assert.NoError(t, r.saveVisitedCache())
	fileEntries := readVisitedCache(t, cacheFile)
	assert.Contains(t, fileEntries, canonicalURL("https://example.com/root/new.bin"))
}

func writeVisitedCache(t *testing.T, path string, entries []string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create cache file: %v", err)
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	for _, entry := range entries {
		if _, err := writer.WriteString(entry + "\n"); err != nil {
			t.Fatalf("failed writing cache entry: %v", err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}
}

func readVisitedCache(t *testing.T, path string) []string {
	t.Helper()
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read cache file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(bytes)), "\n")
	return lines
}

func TestWriteBatch(t *testing.T) {
	tmp := tempDir(t)
	r := &RAria2{
		WriteBatch: filepath.Join(tmp, "batch.txt"),
	}

	entries := make(chan aria2URLEntry)
	go func() {
		entry1 := aria2URLEntry{URL: "https://example.com/file1.bin", Dir: "downloads"}
		entry2 := aria2URLEntry{URL: "https://example.com/file2.bin", Dir: ""}
		r.downloadEntries = append(r.downloadEntries, entry1, entry2)
		entries <- entry1
		entries <- entry2
		close(entries)
	}()

	err := r.executeBatchDownload(context.Background(), entries)
	assert.NoError(t, err)

	// Check that the file was created and contains the expected content
	content, err := os.ReadFile(r.WriteBatch)
	assert.NoError(t, err)

	expected := "https://example.com/file1.bin\n  dir=downloads\n\nhttps://example.com/file2.bin\n\n"
	assert.Equal(t, expected, string(content))
}

func TestExecuteBatchDownloadDryRun(t *testing.T) {
	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	lastExecArgs = nil
	execCommand = fakeExecCommand

	r := &RAria2{
		DryRun:                 true,
		MaxConnectionPerServer: 2,
		MaxConcurrentDownload:  3,
		urlCache:               NewURLCache(""),
	}

	entry := aria2URLEntry{URL: "https://example.com/file.bin", Dir: "out"}
	r.downloadEntries = append(r.downloadEntries, entry)
	entries := make(chan aria2URLEntry, 1)
	entries <- entry
	close(entries)

	assert.NoError(t, r.executeBatchDownload(context.Background(), entries))
	assert.Len(t, r.downloadEntries, 1)
}

func TestWriteBatchEntryFormatsDir(t *testing.T) {
	var buf strings.Builder
	writer := bufio.NewWriter(&buf)
	entry := aria2URLEntry{URL: "https://example.com/file.bin", Dir: "out"}
	assert.NoError(t, writeBatchEntry(writer, entry))
	assert.NoError(t, writer.Flush())
	assert.Equal(t, "https://example.com/file.bin\n  dir=out\n\n", buf.String())

	buf.Reset()
	writer.Reset(&buf)
	entry.Dir = ""
	assert.NoError(t, writeBatchEntry(writer, entry))
	assert.NoError(t, writer.Flush())
	assert.Equal(t, "https://example.com/file.bin\n\n", buf.String())
}

func TestBatchFileSinkWritesEntries(t *testing.T) {
	tmp := tempDir(t)
	path := filepath.Join(tmp, "batch.txt")
	sink, err := newBatchFileSink(path)
	assert.NoError(t, err)

	entry := aria2URLEntry{URL: "https://example.com/file.bin", Dir: "downloads"}
	assert.NoError(t, sink.Write(entry))
	assert.NoError(t, sink.Close())

	content, err := os.ReadFile(path)
	assert.NoError(t, err)
	assert.Equal(t, "https://example.com/file.bin\n  dir=downloads\n\n", string(content))
}

func TestAria2SinkStreamsEntries(t *testing.T) {
	oldExec := execCommand
	defer func() { execCommand = oldExec }()
	tmp := tempDir(t)
	capture := filepath.Join(tmp, "aria2-input.txt")
	execCommand = func(command string, args ...string) *exec.Cmd {
		cs := []string{"-test.run=TestHelperProcessCaptureInput", "--", command}
		cs = append(cs, args...)
		cmd := exec.Command(os.Args[0], cs...)
		cmd.Env = append(os.Environ(),
			"GO_WANT_CAPTURE_INPUT=1",
			"CAPTURE_INPUT_PATH="+capture,
		)
		return cmd
	}

	r := &RAria2{MaxConnectionPerServer: 1, MaxConcurrentDownload: 1}
	am := NewAria2Manager()
	am.MaxConnectionPerServer = r.MaxConnectionPerServer
	am.MaxConcurrentDownload = r.MaxConcurrentDownload
	sink, err := newAria2Sink(context.Background(), am)
	assert.NoError(t, err)

	entry := aria2URLEntry{URL: "https://example.com/file.bin", Dir: "out"}
	assert.NoError(t, sink.Write(entry))
	assert.NoError(t, sink.Close())

	content, err := os.ReadFile(capture)
	assert.NoError(t, err)
	assert.Equal(t, "https://example.com/file.bin\n  dir=out\n\n", string(content))
}

func TestIsSubPath(t *testing.T) {
	subject := mustParseURL(t, "https://example.com/root/path")
	of := mustParseURL(t, "https://example.com/root/")
	assert.True(t, IsSubPath(subject, of))

	differentHost := mustParseURL(t, "https://cdn.example.com/root/path")
	assert.False(t, IsSubPath(differentHost, of))

	differentScheme := mustParseURL(t, "http://example.com/root/path")
	assert.False(t, IsSubPath(differentScheme, of))

	outside := mustParseURL(t, "https://example.com/other")
	assert.False(t, IsSubPath(outside, of))
}

func newTestClient(baseURL *url.URL) *RAria2 {
	r := New(baseURL)
	r.DryRun = true
	r.DisableRetries = true
	r.RateLimit = 0 // Disable rate limiting for tests
	return r
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("failed to parse URL %q: %v", raw, err)
	}
	return parsed
}
