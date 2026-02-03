package raria2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/sirupsen/logrus"
	"github.com/temoto/robotstxt"
)

var (
	backslashToSlash         = strings.NewReplacer("\\", "/", "%5c", "/", "%5C", "/")
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
	WriteBatch             string
	RespectRobots          bool
	Filters                *FilterManager
	httpClient             *HTTPClient

	downloadEntries   []aria2URLEntry
	downloadEntriesMu sync.Mutex

	// Disable retries (useful for testing)
	DisableRetries bool

	// does not perform any resource download
	DryRun bool

	urlCache *URLCache

	// robots.txt cache per host
	robotsCache   map[string]*robotstxt.RobotsData
	robotsCacheMu sync.RWMutex

	// channel used to stream download entries to aria2c/batch writer
	downloadEntriesCh chan aria2URLEntry

	// injectable sink factory (used in tests)
	sinkFactory func(context.Context, *RAria2) (downloadSink, error)
}

func safeRelativeOutputPath(urlPath string) (string, bool) {
	if urlPath == "" {
		return "", true
	}
	// Convert Windows-style separators (literal or percent-encoded) so path.Clean can remove dot segments.
	urlPath = backslashToSlash.Replace(urlPath)
	// Normalize as a URL path by forcing a leading slash.
	// This prevents path.Clean from collapsing away a leading segment (e.g. "host/../x").
	if !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}
	clean := path.Clean(urlPath)
	if clean == "." || clean == "/" {
		return "", true
	}
	if !strings.HasPrefix(clean, "/") {
		return "", false
	}
	return strings.TrimPrefix(clean, "/"), true
}

func New(url *url.URL) *RAria2 {
	return &RAria2{
		url:                    url,
		MaxConnectionPerServer: 5,
		MaxConcurrentDownload:  5,
		MaxDepth:               -1,
		HTTPTimeout:            30 * time.Second,
		urlCache:               NewURLCache(""), // Will be initialized with path later
		Filters:                NewFilterManager(url),
	}
}

func (r *RAria2) FiltersConfig() *FilterManager {
	if r.Filters == nil {
		r.Filters = NewFilterManager(r.url)
	}
	if r.Filters != nil && r.Filters.baseURL == nil {
		r.Filters.baseURL = r.url
	}
	return r.Filters
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

func (r *RAria2) client() *HTTPClient {
	if r.httpClient != nil {
		return r.httpClient
	}

	timeout := r.HTTPTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	r.httpClient = NewHTTPClient(timeout, r.RateLimit)
	return r.httpClient
}

func (r *RAria2) IsHtmlPage(urlString string) (bool, error) {
	// First try HEAD request
	req, err := http.NewRequest("HEAD", urlString, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", r.UserAgent)

	var res *http.Response
	if r.DisableRetries {
		res, err = r.client().Do(req)
	} else {
		res, err = r.client().DoWithRetry(req)
	}
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
		if r.DisableRetries {
			res, err = r.client().Do(req)
		} else {
			res, err = r.client().DoWithRetry(req)
		}
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
			if r.DisableRetries {
				res, err = r.client().Do(req)
			} else {
				res, err = r.client().DoWithRetry(req)
			}
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
			return IsHTMLContent(contentType), nil
		}
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return false, fmt.Errorf("unexpected status code %d for %s", res.StatusCode, urlString)
	}

	return IsHTMLContent(res.Header.Get("Content-Type")), nil
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

	isHTML, err := r.IsHtmlPage(urlString)
	if err != nil {
		return nil, err
	}
	if !isHTML {
		return nil, errNotHTML
	}

	// It's HTML, so do a full GET to parse links
	req, err := http.NewRequestWithContext(ctx, "GET", urlString, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", r.UserAgent)

	var res *http.Response

	if r.DisableRetries {
		res, err = r.client().Do(req)
	} else {
		res, err = r.client().DoWithRetry(req)
	}
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
	if r.WriteBatch == "" && r.sinkFactory == nil {
		if _, err := lookPath("aria2c"); err != nil {
			return fmt.Errorf("aria2c is required but was not found in PATH: %w", err)
		}
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
	filters := r.FiltersConfig()

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

		if !filters.PathAllowed(parsedURL) {
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
			if !filters.PathAllowed(parsedLink) {
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

	filters := r.FiltersConfig()
	if !filters.PathAllowed(parsedCUrl) {
		logrus.Debugf("[W %d]: skipping %s due to path filters", workerId, cUrl)
		return
	}
	if !filters.ExtensionAllowed(parsedCUrl) {
		logrus.Debugf("[W %d]: skipping %s due to extension filters", workerId, cUrl)
		return
	}

	if !filters.FilenameAllowed(parsedCUrl) {
		logrus.Debugf("[W %d]: skipping %s due to filename filters", workerId, cUrl)
		return
	}

	// MIME type filtering
	if len(filters.AcceptMime) > 0 || len(filters.RejectMime) > 0 {
		contentType := ""
		req, err := http.NewRequest("HEAD", cUrl, nil)
		if err == nil {
			req.Header.Set("User-Agent", r.UserAgent)
			var res *http.Response
			if r.DisableRetries {
				res, err = r.client().Do(req)
			} else {
				res, err = r.client().DoWithRetry(req)
			}
			if err == nil {
				res.Body.Close()
				if res.StatusCode >= 200 && res.StatusCode < 300 {
					contentType = res.Header.Get("Content-Type")
				}
			}
		}

		// If HEAD failed or did not provide a usable content-type, sniff via a small GET.
		if contentType == "" {
			sniffReq, reqErr := http.NewRequest("GET", cUrl, nil)
			if reqErr != nil {
				logrus.Warnf("[W %d]: unable to create GET request for MIME sniffing %s: %v", workerId, cUrl, reqErr)
				return
			}
			sniffReq.Header.Set("User-Agent", r.UserAgent)
			sniffReq.Header.Set("Range", "bytes=0-1023")
			var sniffRes *http.Response
			if r.DisableRetries {
				sniffRes, reqErr = r.client().Do(sniffReq)
			} else {
				sniffRes, reqErr = r.client().DoWithRetry(sniffReq)
			}
			if reqErr != nil {
				logrus.Warnf("[W %d]: unable to sniff MIME type for %s: %v", workerId, cUrl, reqErr)
				return
			}
			defer sniffRes.Body.Close()
			if sniffRes.StatusCode >= 200 && sniffRes.StatusCode < 300 {
				limitReader := io.LimitReader(sniffRes.Body, 1024)
				bodyBytes, _ := io.ReadAll(limitReader)
				contentType = http.DetectContentType(bodyBytes)
			}
		}

		if !filters.MimeAllowed(contentType) {
			logrus.Debugf("[W %d]: skipping %s due to MIME filter: %s", workerId, cUrl, contentType)
			return
		}
	}

	// Get relative directory
	var outputPath string
	p1 := parsedCUrl.Path
	p2 := r.url.Path

	idx := strings.Index(p1, p2)
	if idx == 0 {
		candidate := strings.TrimPrefix(p1, p2)
		candidate = strings.TrimPrefix(candidate, "/")
		cleanCandidate, ok := safeRelativeOutputPath(candidate)
		if !ok {
			logrus.Warnf("[W %d]: refusing potentially unsafe output path derived from URL %s: %q", workerId, cUrl, candidate)
			return
		}
		outputPath = cleanCandidate
	} else {
		cleanPath, ok := safeRelativeOutputPath(parsedCUrl.Path)
		if !ok {
			logrus.Warnf("[W %d]: refusing potentially unsafe output path derived from URL %s: %q", workerId, cUrl, parsedCUrl.Path)
			return
		}
		if cleanPath == "" {
			outputPath = parsedCUrl.Host
		} else {
			outputPath = parsedCUrl.Host + "/" + cleanPath
		}
	}

	if r.DryRun {
		logrus.Infof("[W %d]: dry run: downloading %s to %s", workerId, cUrl, outputPath)
	}

	dirPart := path.Dir(outputPath)
	if dirPart == "." {
		dirPart = ""
	}
	outputDir := filepath.Join(r.OutputPath, filepath.FromSlash(dirPart))
	if err := r.ensureOutputDir(workerId, outputDir); err != nil {
		logrus.Warnf("unable to create %v: %v", outputDir, err)
		return
	}

	entry := aria2URLEntry{URL: cUrl, Dir: outputDir}
	r.enqueueDownloadEntry(entry)
	logrus.Infof("[W %d]: queued %s for batch download (dir=%s)", workerId, cUrl, outputDir)
}

func (r *RAria2) markVisited(u string) bool {
	if r.urlCache == nil {
		r.urlCache = NewURLCache("")
	}
	return r.urlCache.MarkVisited(u)
}

func (r *RAria2) loadVisitedCache() error {
	if r.urlCache == nil {
		r.urlCache = NewURLCache(r.VisitedCachePath)
	} else {
		r.urlCache.path = r.VisitedCachePath
	}
	return r.urlCache.Load()
}

func (r *RAria2) saveVisitedCache() error {
	if r.urlCache == nil {
		return nil
	}
	if r.urlCache.path == "" {
		r.urlCache.path = r.VisitedCachePath
	}
	return r.urlCache.Save()
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

	var resp *http.Response
	if r.DisableRetries {
		resp, err = r.client().Do(req)
	} else {
		resp, err = r.client().DoWithRetry(req)
	}
	if err != nil {
		// Try HTTPS if HTTP fails
		robotsURL = fmt.Sprintf("https://%s/robots.txt", host)
		req, err = http.NewRequest("GET", robotsURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", r.UserAgent)

		if r.DisableRetries {
			resp, err = r.client().Do(req)
		} else {
			resp, err = r.client().DoWithRetry(req)
		}
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

func (r *RAria2) newDownloadSink(ctx context.Context) (downloadSink, error) {
	if r.WriteBatch != "" {
		return newBatchFileSink(r.WriteBatch)
	}
	if r.sinkFactory != nil {
		return r.sinkFactory(ctx, r)
	}

	// Create an Aria2Manager for this operation
	am := NewAria2Manager()
	am.MaxConnectionPerServer = r.MaxConnectionPerServer
	am.MaxConcurrentDownload = r.MaxConcurrentDownload
	am.Aria2EntriesPerSession = r.Aria2EntriesPerSession
	am.DryRun = r.DryRun
	am.Aria2AfterURLArgs = r.Aria2AfterURLArgs
	am.WriteBatch = r.WriteBatch
	am.sinkFactory = r.sinkFactory

	return newAria2Sink(ctx, am)
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
