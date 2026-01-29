package raria2

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/sirupsen/logrus"
	"github.com/temoto/robotstxt"
)

var (
	execCommand              = exec.Command
	lookPath                 = exec.LookPath
	defaultDownloadQueueSize = 256
)

type RAria2 struct {
	url                    *url.URL
	MaxConnectionPerServer int
	MaxConcurrentDownload  int
	MaxDepth               int
	OutputPath             string
	Aria2AfterURLArgs      []string
	Aria2EntriesPerSession int
	HTTPTimeout            time.Duration
	UserAgent              string
	RateLimit              float64
	VisitedCachePath       string
	AcceptExtensions       map[string]struct{}
	RejectExtensions       map[string]struct{}
	AcceptFilenames        map[string]*regexp.Regexp
	RejectFilenames        map[string]*regexp.Regexp
	CaseInsensitivePaths   bool
	AcceptPathRegex        []*regexp.Regexp
	RejectPathRegex        []*regexp.Regexp
	ciRegexCache           map[*regexp.Regexp]*regexp.Regexp
	ciRegexCacheMu         sync.RWMutex
	WriteBatch             string
	RespectRobots          bool
	AcceptMime             map[string]struct{}
	RejectMime             map[string]struct{}
	httpClient             *http.Client

	downloadEntries   []aria2URLEntry
	downloadEntriesMu sync.Mutex

	lastRequest int64 // Unix timestamp with nanoseconds

	// Disable retries (useful for testing)
	DisableRetries bool

	// does not perform any resource download
	DryRun bool

	urlCache   map[string]struct{}
	urlCacheMu sync.Mutex

	// robots.txt cache per host
	robotsCache   map[string]*robotstxt.RobotsData
	robotsCacheMu sync.RWMutex

	// channel used to stream download entries to aria2c/batch writer
	downloadEntriesCh chan aria2URLEntry

	// injectable sink factory (used in tests)
	sinkFactory func(context.Context, *RAria2) (downloadSink, error)
}

func New(url *url.URL) *RAria2 {
	return &RAria2{
		url:                    url,
		MaxConnectionPerServer: 5,
		MaxConcurrentDownload:  5,
		MaxDepth:               -1,
		HTTPTimeout:            30 * time.Second,
		urlCache:               make(map[string]struct{}),
	}
}

func (r *RAria2) sessionEntryLimit() int {
	if r.Aria2EntriesPerSession <= 0 {
		return 0
	}
	if r.WriteBatch != "" {
		logrus.Warn("--aria2-session-size is ignored when --write-batch is set")
		return 0
	}
	return r.Aria2EntriesPerSession
}

func (r *RAria2) ensureOutputPath() error {
	if r.OutputPath == "" {
		if r.url == nil {
			return fmt.Errorf("unable to derive output path: source URL is nil")
		}
		host := r.url.Host
		path := strings.Trim(r.url.Path, "/")
		if host == "" {
			host = "download"
		}
		if path == "" {
			r.OutputPath = host
		} else {
			r.OutputPath = filepath.Join(host, filepath.FromSlash(path))
		}
	}
	if _, err := os.Stat(r.OutputPath); os.IsNotExist(err) {
		if err := os.MkdirAll(r.OutputPath, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (r *RAria2) client() *http.Client {
	if r.httpClient != nil {
		return r.httpClient
	}

	timeout := r.HTTPTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	r.httpClient = &http.Client{Timeout: timeout}
	return r.httpClient
}

func (r *RAria2) waitForRateLimit() {
	if r.RateLimit <= 0 {
		return
	}

	now := time.Now().UnixNano()
	last := atomic.LoadInt64(&r.lastRequest)

	// Calculate minimum time between requests
	minInterval := time.Second / time.Duration(r.RateLimit)

	if now-last < int64(minInterval) {
		sleepTime := time.Duration(minInterval - time.Duration(now-last))
		time.Sleep(sleepTime)
	}

	atomic.StoreInt64(&r.lastRequest, time.Now().UnixNano())
}

func (r *RAria2) doHTTPRequestWithRetry(req *http.Request) (*http.Response, error) {
	// If retries are disabled, just do a single request
	if r.DisableRetries {
		r.waitForRateLimit()
		return r.client().Do(req)
	}

	const maxRetries = 3
	const baseDelay = 100 * time.Millisecond

	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff with jitter
			delay := baseDelay * time.Duration(1<<uint(attempt-1))
			jitter := time.Duration(float64(delay) * 0.1 * (0.5 + 0.5*rand.Float64()))
			time.Sleep(delay + jitter)

			logrus.Debugf("Retrying HTTP request (attempt %d/%d)", attempt+1, maxRetries)
		}

		r.waitForRateLimit()

		resp, err := r.client().Do(req)
		if err == nil {
			// Check for transient HTTP errors
			if resp.StatusCode >= 500 || resp.StatusCode == 429 {
				lastErr = fmt.Errorf("HTTP %d: transient error", resp.StatusCode)
				resp.Body.Close()
				continue
			}
			return resp, nil
		}

		// Check if error is transient (network-related)
		if isTransientError(err) {
			lastErr = err
			continue
		}

		// Non-transient error, return immediately
		return nil, err
	}

	return nil, fmt.Errorf("max retries exceeded: %w", lastErr)
}

func isTransientError(err error) bool {
	errStr := err.Error()

	// Common transient error patterns
	transientPatterns := []string{
		"connection refused",
		"connection reset",
		"connection timed out",
		"timeout",
		"network is unreachable",
		"temporary failure",
		"service unavailable",
		"bad gateway",
		"gateway timeout",
	}

	for _, pattern := range transientPatterns {
		if strings.Contains(strings.ToLower(errStr), pattern) {
			return true
		}
	}

	return false
}

func (r *RAria2) IsHtmlPage(urlString string) (bool, error) {
	// First try HEAD request
	req, err := http.NewRequest("HEAD", urlString, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", r.UserAgent)

	res, err := r.doHTTPRequestWithRetry(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()

	// If HEAD fails with 405/403 or missing Content-Type, fall back to GET
	if res.StatusCode == 405 || res.StatusCode == 403 ||
		res.Header.Get("Content-Type") == "" {

		// Try GET with Range header first for efficiency
		req, err = http.NewRequest("GET", urlString, nil)
		if err != nil {
			return false, err
		}
		req.Header.Set("User-Agent", r.UserAgent)
		req.Header.Set("Range", "bytes=0-1023")
		res, err = r.doHTTPRequestWithRetry(req)
		if err != nil {
			return false, err
		}
		defer res.Body.Close()

		// If Range not supported, read first 1KB normally
		if res.StatusCode == 416 || res.StatusCode == 400 {
			req, err = http.NewRequest("GET", urlString, nil)
			if err != nil {
				return false, err
			}
			req.Header.Set("User-Agent", r.UserAgent)
			res, err = r.doHTTPRequestWithRetry(req)
			if err != nil {
				return false, err
			}
			defer res.Body.Close()
		}

		// For successful GET (either Range or full), read first 1KB to detect content type
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			limitReader := io.LimitReader(res.Body, 1024)
			bodyBytes, _ := io.ReadAll(limitReader)
			contentType := http.DetectContentType(bodyBytes)
			return isHTMLContent(contentType), nil
		}
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return false, fmt.Errorf("unexpected status code %d for %s", res.StatusCode, urlString)
	}

	return isHTMLContent(res.Header.Get("Content-Type")), nil
}

var errNotHTML = errors.New("content is not HTML")

func (r *RAria2) getLinksByUrl(urlString string) ([]string, error) {
	return r.getLinksByUrlWithContext(context.Background(), urlString)
}

func (r *RAria2) getLinksByUrlWithContext(ctx context.Context, urlString string) ([]string, error) {
	parsedUrl, err := url.Parse(urlString)
	if err != nil {
		return []string{}, err
	}

	// First try HEAD request to check if it's HTML
	req, err := http.NewRequestWithContext(ctx, "HEAD", urlString, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", r.UserAgent)

	res, err := r.doHTTPRequestWithRetry(req)
	if err != nil {
		return nil, err
	}
	res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status code %d for %s", res.StatusCode, urlString)
	}

	if !isHTMLContent(res.Header.Get("Content-Type")) {
		return nil, errNotHTML
	}

	// It's HTML, so do a full GET to parse links
	req, err = http.NewRequestWithContext(ctx, "GET", urlString, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", r.UserAgent)

	res, err = r.doHTTPRequestWithRetry(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status code %d for %s", res.StatusCode, urlString)
	}

	return getLinks(parsedUrl, res.Body)
}

func (r *RAria2) RunWithContext(ctx context.Context) error {
	if _, err := lookPath("aria2c"); err != nil {
		return fmt.Errorf("aria2c is required but was not found in PATH: %w", err)
	}

	if err := r.loadVisitedCache(); err != nil {
		return fmt.Errorf("failed loading visited cache: %w", err)
	}

	if err := r.ensureOutputPath(); err != nil {
		return err
	}
	dir, _ := os.Getwd()
	logrus.Infof("pwd: %v", dir)

	entriesCh := make(chan aria2URLEntry, defaultDownloadQueueSize)
	downloadErrCh := make(chan error, 1)
	r.downloadEntriesCh = entriesCh

	go func() {
		downloadErrCh <- r.executeBatchDownload(ctx, entriesCh)
	}()

	r.subDownloadUrls(ctx, 0, r.url.String())

	close(entriesCh)
	r.downloadEntriesCh = nil
	downloadErr := <-downloadErrCh

	if err := r.saveVisitedCache(); err != nil {
		return fmt.Errorf("failed saving visited cache: %w", err)
	}

	return downloadErr
}

func (r *RAria2) subDownloadUrls(ctx context.Context, workerId int, startURL string) {
	type crawlEntry struct {
		url   string
		depth int
	}

	queue := []crawlEntry{{url: startURL, depth: 0}}

	for len(queue) > 0 {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			logrus.Info("Crawling cancelled by context")
			return
		default:
		}

		entry := queue[0]
		queue = queue[1:]
		cUrl := entry.url
		parsedURL, err := url.Parse(cUrl)
		if err != nil {
			logrus.Warnf("skipping invalid URL %s: %v", cUrl, err)
			continue
		}

		if !r.pathAllowed(parsedURL) {
			logrus.Debugf("path filters skipped %s", cUrl)
			continue
		}

		if r.RespectRobots && !r.urlAllowedByRobots(parsedURL) {
			logrus.Debugf("robots.txt disallowed %s", cUrl)
			continue
		}

		if !r.markVisited(cUrl) {
			logrus.WithField("url", cUrl).Debug("skipping already visited")
			continue
		}

		newLinks, err := r.getLinksByUrlWithContext(ctx, cUrl)
		if err != nil {
			if errors.Is(err, errNotHTML) {
				r.downloadResource(workerId, cUrl)
				continue
			}
			logrus.Warnf("unable to fetch %v: %v", cUrl, err)
			continue
		}

		nextDepth := entry.depth + 1
		if r.MaxDepth >= 0 && nextDepth > r.MaxDepth {
			continue
		}

		for _, link := range newLinks {
			parsedLink, err := url.Parse(link)
			if err != nil {
				logrus.Debugf("skipping invalid discovered URL %s: %v", link, err)
				continue
			}
			if !r.pathAllowed(parsedLink) {
				logrus.Debugf("path filters skipped %s", link)
				continue
			}
			queue = append(queue, crawlEntry{url: link, depth: nextDepth})
		}
	}
}

func (r *RAria2) downloadResource(workerId int, cUrl string) {
	parsedCUrl, err := url.Parse(cUrl)
	if err != nil {
		logrus.Warnf("[W %d]: unable to download %v because it's an invalid URL: %v", workerId, cUrl, err)
		return
	}

	if !r.pathAllowed(parsedCUrl) {
		logrus.Debugf("[W %d]: skipping %s due to path filters", workerId, cUrl)
		return
	}

	if !r.extensionAllowed(parsedCUrl) {
		logrus.Debugf("[W %d]: skipping %s due to extension filters", workerId, cUrl)
		return
	}

	if !r.filenameAllowed(parsedCUrl) {
		logrus.Debugf("[W %d]: skipping %s due to filename filters", workerId, cUrl)
		return
	}

	// Check MIME type filtering - need to fetch headers first
	req, err := http.NewRequest("HEAD", cUrl, nil)
	if err != nil {
		logrus.Warnf("[W %d]: unable to create HEAD request for %s: %v", workerId, cUrl, err)
		return
	}
	req.Header.Set("User-Agent", r.UserAgent)

	res, err := r.doHTTPRequestWithRetry(req)
	if err != nil {
		logrus.Warnf("[W %d]: unable to fetch headers for %s: %v", workerId, cUrl, err)
		return
	}
	res.Body.Close()

	// Check MIME type filtering
	contentType := res.Header.Get("Content-Type")
	if !r.mimeAllowed(contentType) {
		logrus.Debugf("[W %d]: skipping %s due to MIME filter: %s", workerId, cUrl, contentType)
		return
	}

	// Get relative directory
	var outputPath string
	p1 := parsedCUrl.Path
	p2 := r.url.Path

	idx := strings.Index(p1, p2)
	if idx == 0 {
		outputPath = strings.TrimPrefix(p1, p2)
	} else {
		outputPath = parsedCUrl.Host + "/" + parsedCUrl.Path
	}

	// Safety: ensure outputPath doesn't start with "/" to prevent path traversal
	outputPath = strings.TrimPrefix(outputPath, "/")

	if r.DryRun {
		logrus.Infof("[W %d]: dry run: downloading %s to %s", workerId, cUrl, outputPath)
	}

	outputDir := filepath.Join(r.OutputPath, filepath.Dir(outputPath))
	if err := r.ensureOutputDir(workerId, outputDir); err != nil {
		logrus.Warnf("unable to create %v: %v", outputDir, err)
		return
	}

	entry := aria2URLEntry{URL: cUrl, Dir: outputDir}
	r.enqueueDownloadEntry(entry)
	logrus.Infof("[W %d]: queued %s for batch download (dir=%s)", workerId, cUrl, outputDir)
}

func (r *RAria2) markVisited(u string) bool {
	key := canonicalURL(u)
	r.urlCacheMu.Lock()
	defer r.urlCacheMu.Unlock()
	if _, exists := r.urlCache[key]; exists {
		return false
	}
	r.urlCache[key] = struct{}{}
	return true
}

func (r *RAria2) loadVisitedCache() error {
	if r.VisitedCachePath == "" {
		return nil
	}

	file, err := os.Open(r.VisitedCachePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var entries []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries = append(entries, line)
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	r.urlCacheMu.Lock()
	for _, entry := range entries {
		r.urlCache[canonicalURL(entry)] = struct{}{}
	}
	r.urlCacheMu.Unlock()

	return nil
}

func (r *RAria2) saveVisitedCache() error {
	if r.VisitedCachePath == "" {
		return nil
	}

	r.urlCacheMu.Lock()
	keys := make([]string, 0, len(r.urlCache))
	for k := range r.urlCache {
		keys = append(keys, k)
	}
	r.urlCacheMu.Unlock()
	sort.Strings(keys)

	dir := filepath.Dir(r.VisitedCachePath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	tmpPath := r.VisitedCachePath + ".tmp"
	file, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	writer := bufio.NewWriter(file)
	for _, key := range keys {
		if _, err := fmt.Fprintln(writer, key); err != nil {
			file.Close()
			_ = os.Remove(tmpPath)
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	return os.Rename(tmpPath, r.VisitedCachePath)
}

// canonicalURL returns a normalized version of a URL for consistent comparison
// This function should be used everywhere URL comparison is needed
func canonicalURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	// Preserve whether the original URL looked like a directory (trailing slash in the *path*).
	// Important for URLs like: https://host/dir/?C=M;O=A
	// where raw does not end with '/', but the path does.
	hadTrailingSlash := parsed.Path != "/" && strings.HasSuffix(parsed.Path, "/")

	// Normalize scheme to lowercase
	parsed.Scheme = strings.ToLower(parsed.Scheme)

	// Normalize host to lowercase
	if parsed.Host != "" {
		parsed.Host = strings.ToLower(parsed.Host)

		// Normalize default ports
		switch parsed.Scheme {
		case "http":
			if parsed.Port() == "80" {
				parsed.Host = parsed.Hostname()
			}
		case "https":
			if parsed.Port() == "443" {
				parsed.Host = parsed.Hostname()
			}
		}
	}

	// Always strip fragments
	parsed.Fragment = ""

	// Normalize path - but preserve root path
	if parsed.Path == "" {
		parsed.Path = "/"
	} else if parsed.Path != "/" && strings.HasSuffix(parsed.Path, "/") {
		// Remove trailing slashes for consistency (except root)
		parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	}

	// For directory-style paths (ending with / in the original path) or root paths with query params,
	// clear query params (handles server directory listing sorting parameters).
	if hadTrailingSlash || (parsed.Path == "/" && parsed.RawQuery != "") {
		parsed.RawQuery = ""
	}

	return parsed.String()
}

func (r *RAria2) pathAllowed(u *url.URL) bool {
	pathStr := u.Path
	if pathStr == "" {
		pathStr = "/"
	}

	// Apply case-insensitive matching if enabled
	if r.CaseInsensitivePaths {
		pathStr = strings.ToLower(pathStr)
	}

	var rejectPatterns []*regexp.Regexp
	var acceptPatterns []*regexp.Regexp

	if r.CaseInsensitivePaths {
		rejectPatterns = r.caseInsensitivePatterns(r.RejectPathRegex)
		acceptPatterns = r.caseInsensitivePatterns(r.AcceptPathRegex)
	} else {
		rejectPatterns = r.RejectPathRegex
		acceptPatterns = r.AcceptPathRegex
	}

	if matchAnyRegex(rejectPatterns, pathStr) {
		return false
	}

	if len(acceptPatterns) == 0 {
		return true
	}

	if matchAnyRegex(acceptPatterns, pathStr) {
		return true
	}

	basePath := r.url.Path
	if basePath == "" {
		basePath = "/"
	}

	// Apply case-insensitive matching to base path if enabled
	if r.CaseInsensitivePaths {
		basePath = strings.ToLower(basePath)
	}

	trimPath := strings.TrimSuffix(pathStr, "/")
	trimBase := strings.TrimSuffix(basePath, "/")
	if trimPath == "" {
		trimPath = "/"
	}
	if trimBase == "" {
		trimBase = "/"
	}

	return trimPath == trimBase
}

func (r *RAria2) caseInsensitivePatterns(patterns []*regexp.Regexp) []*regexp.Regexp {
	if len(patterns) == 0 {
		return nil
	}

	result := make([]*regexp.Regexp, 0, len(patterns))
	for _, re := range patterns {
		if re == nil {
			continue
		}
		result = append(result, r.caseInsensitiveRegex(re))
	}

	return result
}

func (r *RAria2) caseInsensitiveRegex(re *regexp.Regexp) *regexp.Regexp {
	r.ciRegexCacheMu.RLock()
	if r.ciRegexCache != nil {
		if cached, ok := r.ciRegexCache[re]; ok {
			r.ciRegexCacheMu.RUnlock()
			return cached
		}
	}
	r.ciRegexCacheMu.RUnlock()

	ciRe, err := regexp.Compile("(?i)" + re.String())
	if err != nil {
		return re
	}

	r.ciRegexCacheMu.Lock()
	if r.ciRegexCache == nil {
		r.ciRegexCache = make(map[*regexp.Regexp]*regexp.Regexp)
	}
	r.ciRegexCache[re] = ciRe
	r.ciRegexCacheMu.Unlock()

	return ciRe
}

func (r *RAria2) extensionAllowed(u *url.URL) bool {
	if len(r.AcceptExtensions) == 0 && len(r.RejectExtensions) == 0 {
		return true
	}

	ext := strings.ToLower(strings.TrimPrefix(path.Ext(u.Path), "."))

	if len(r.AcceptExtensions) > 0 {
		if _, ok := r.AcceptExtensions[ext]; !ok {
			return false
		}
	}

	if len(r.RejectExtensions) > 0 {
		if _, ok := r.RejectExtensions[ext]; ok {
			return false
		}
	}

	return true
}

func (r *RAria2) urlAllowedByRobots(u *url.URL) bool {
	if !r.RespectRobots {
		return true
	}

	robots, err := r.getRobotsData(u.Host)
	if err != nil {
		logrus.Debugf("failed to fetch robots.txt for %s: %v", u.Host, err)
		return true // fail open
	}

	// Check if URL is allowed for our user agent
	return robots.TestAgent(u.Path, r.UserAgent)
}

func (r *RAria2) getRobotsData(host string) (*robotstxt.RobotsData, error) {
	r.robotsCacheMu.RLock()
	if r.robotsCache != nil {
		if cached, ok := r.robotsCache[host]; ok {
			r.robotsCacheMu.RUnlock()
			return cached, nil
		}
	}
	r.robotsCacheMu.RUnlock()

	// Fetch robots.txt - try HTTP first, then HTTPS if needed
	robotsURL := fmt.Sprintf("http://%s/robots.txt", host)
	req, err := http.NewRequest("GET", robotsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", r.UserAgent)

	resp, err := r.doHTTPRequestWithRetry(req)
	if err != nil {
		// Try HTTPS if HTTP fails
		robotsURL = fmt.Sprintf("https://%s/robots.txt", host)
		req, err = http.NewRequest("GET", robotsURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", r.UserAgent)

		resp, err = r.doHTTPRequestWithRetry(req)
		if err != nil {
			// No robots.txt or error fetching it
			robotsData := robotstxt.RobotsData{}
			r.cacheRobotsData(host, &robotsData)
			return &robotsData, nil
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// No robots.txt or error fetching it
		robotsData := robotstxt.RobotsData{}
		r.cacheRobotsData(host, &robotsData)
		return &robotsData, nil
	}

	robotsData, err := robotstxt.FromResponse(resp)
	if err != nil {
		return nil, err
	}

	r.cacheRobotsData(host, robotsData)
	return robotsData, nil
}

func (r *RAria2) cacheRobotsData(host string, data *robotstxt.RobotsData) {
	r.robotsCacheMu.Lock()
	if r.robotsCache == nil {
		r.robotsCache = make(map[string]*robotstxt.RobotsData)
	}
	r.robotsCache[host] = data
	r.robotsCacheMu.Unlock()
}

func (r *RAria2) filenameAllowed(u *url.URL) bool {
	if len(r.AcceptFilenames) == 0 && len(r.RejectFilenames) == 0 {
		return true
	}

	filename := path.Base(u.Path)

	if len(r.AcceptFilenames) > 0 {
		accepted := false
		for _, pattern := range r.AcceptFilenames {
			if pattern.MatchString(filename) {
				accepted = true
				break
			}
		}
		if !accepted {
			return false
		}
	}

	if len(r.RejectFilenames) > 0 {
		for _, pattern := range r.RejectFilenames {
			if pattern.MatchString(filename) {
				return false
			}
		}
	}

	return true
}

func (r *RAria2) mimeAllowed(contentType string) bool {
	if len(r.AcceptMime) == 0 && len(r.RejectMime) == 0 {
		return true
	}

	// Normalize content type (lowercase, remove parameters like charset)
	mimeType := strings.ToLower(strings.Split(contentType, ";")[0])
	mimeType = strings.TrimSpace(mimeType)

	// If reject filter has the MIME type, reject it first
	if len(r.RejectMime) > 0 {
		if _, ok := r.RejectMime[mimeType]; ok {
			return false
		}
	}

	// If accept filter is specified, only allow those MIME types
	if len(r.AcceptMime) > 0 {
		if _, ok := r.AcceptMime[mimeType]; !ok {
			return false
		}
	}

	return true
}

func matchAnyRegex(patterns []*regexp.Regexp, value string) bool {
	for _, re := range patterns {
		if re.MatchString(value) {
			return true
		}
	}
	return false
}

func (r *RAria2) ensureOutputDir(workerId int, dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if r.DryRun {
			logrus.Infof("[W %d]: dry run: creating folder %s", workerId, dir)
			return nil
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (r *RAria2) enqueueDownloadEntry(entry aria2URLEntry) {
	r.downloadEntriesMu.Lock()
	defer r.downloadEntriesMu.Unlock()
	r.downloadEntries = append(r.downloadEntries, entry)
	if ch := r.downloadEntriesCh; ch != nil {
		ch <- entry
	}
}

func (r *RAria2) executeBatchDownload(ctx context.Context, entries <-chan aria2URLEntry) error {
	var (
		sink              downloadSink
		sinkErr           error
		entryCount        int
		sessionEntryCount int
	)

	sessionLimit := r.sessionEntryLimit()
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
			sink, sinkErr = r.newDownloadSink(ctx)
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
		logrus.Info("no downloadable resources found")
		return nil
	}

	if err := flushSession(); err != nil {
		return err
	}

	if r.WriteBatch != "" {
		logrus.Infof("wrote aria2 batch file with %d download entries to %s", entryCount, r.WriteBatch)
	}

	return nil
}

type aria2URLEntry struct {
	URL string
	Dir string
}

type downloadSink interface {
	Write(entry aria2URLEntry) error
	Close() error
}

func (r *RAria2) newDownloadSink(ctx context.Context) (downloadSink, error) {
	if r.WriteBatch != "" {
		return newBatchFileSink(r.WriteBatch)
	}
	if r.sinkFactory != nil {
		return r.sinkFactory(ctx, r)
	}
	return newAria2Sink(ctx, r)
}

type aria2Sink struct {
	writer    *bufio.Writer
	pipe      *io.PipeWriter
	waitCh    chan error
	closeOnce sync.Once
	closeErr  error
}

func newAria2Sink(ctx context.Context, r *RAria2) (downloadSink, error) {
	binFile := "aria2c"
	args := []string{"-x", strconv.Itoa(r.MaxConnectionPerServer)}
	if r.MaxConcurrentDownload > 0 {
		args = append(args, "-j", strconv.Itoa(r.MaxConcurrentDownload))
	}
	args = append(args, "--input-file", "-", "--deferred-input=true")

	if r.DryRun {
		args = append(args, "--dry-run=true")
	}

	if len(r.Aria2AfterURLArgs) > 0 {
		args = append(args, r.Aria2AfterURLArgs...)
	}

	if r.DryRun {
		logrus.Infof("aria2 batch cmd: %s %s", binFile, strings.Join(args, " "))
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

func (s *aria2Sink) Write(entry aria2URLEntry) error {
	if err := writeBatchEntry(s.writer, entry); err != nil {
		return err
	}
	return s.writer.Flush()
}

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

type batchFileSink struct {
	writer    *bufio.Writer
	file      *os.File
	closeOnce sync.Once
	closeErr  error
}

func newBatchFileSink(path string) (downloadSink, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("failed to create batch file %s: %w", path, err)
	}
	return &batchFileSink{writer: bufio.NewWriter(file), file: file}, nil
}

func (s *batchFileSink) Write(entry aria2URLEntry) error {
	return writeBatchEntry(s.writer, entry)
}

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

func (r *RAria2) writeBatchFile(content []byte) error {
	file, err := os.Create(r.WriteBatch)
	if err != nil {
		return fmt.Errorf("failed to create batch file %s: %w", r.WriteBatch, err)
	}
	defer file.Close()

	if _, err := file.Write(content); err != nil {
		return fmt.Errorf("failed to write batch file %s: %w", r.WriteBatch, err)
	}

	logrus.Infof("wrote aria2 batch file with %d download entries to %s",
		len(r.downloadEntries), r.WriteBatch)
	return nil
}

func getLinks(originalUrl *url.URL, body io.ReadCloser) ([]string, error) {
	document, err := goquery.NewDocumentFromReader(body)
	if err != nil {
		return []string{}, err
	}

	var urlList []string

	document.Find("a[href]").Each(func(i int, selection *goquery.Selection) {
		val, exists := selection.Attr("href")
		if !exists {
			return
		}

		aHrefUrl, err := url.Parse(val)
		if err != nil {
			logrus.Infof("skipping %v because it is not a valid URL", val)
			return
		}

		resolvedUrl := originalUrl.ResolveReference(aHrefUrl)

		if SameUrl(resolvedUrl, originalUrl) {
			return
		}

		if IsSubPath(resolvedUrl, originalUrl) {
			urlList = append(urlList, resolvedUrl.String())
		}
	})

	return urlList, nil
}

func isHTMLContent(contentType string) bool {
	contentTypeEntries := strings.Split(contentType, ";")
	for _, v := range contentTypeEntries {
		if strings.TrimSpace(v) == "text/html" {
			return true
		}
	}
	return false
}

func IsSubPath(subject *url.URL, of *url.URL) bool {
	// Use canonical URLs for consistent comparison
	subjectCanonical := canonicalURL(subject.String())
	ofCanonical := canonicalURL(of.String())

	subjectParsed, _ := url.Parse(subjectCanonical)
	ofParsed, _ := url.Parse(ofCanonical)

	if subjectParsed.Host != ofParsed.Host {
		return false
	}

	if subjectParsed.Scheme != ofParsed.Scheme {
		return false
	}

	basePath := ofParsed.Path
	if basePath == "" {
		basePath = "/"
	}

	baseNoSlash := basePath
	if baseNoSlash != "/" {
		baseNoSlash = strings.TrimSuffix(baseNoSlash, "/")
	}

	baseWithSlash := baseNoSlash
	if baseNoSlash != "/" {
		baseWithSlash = baseNoSlash + "/"
	}

	if subjectParsed.Path == baseNoSlash {
		return true
	}

	return strings.HasPrefix(subjectParsed.Path, baseWithSlash)
}

// SameUrl checks if two URLs are considered the same using canonicalization
func SameUrl(a *url.URL, b *url.URL) bool {
	return canonicalURL(a.String()) == canonicalURL(b.String())
}
