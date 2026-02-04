package raria2

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/temoto/robotstxt"
)

var (
	backslashToSlash         = strings.NewReplacer("\\", "/", "%5c", "/", "%5C", "/")
	execCommand              = exec.Command
	lookPath                 = exec.LookPath
	defaultDownloadQueueSize = 256
	defaultCrawlerQueueSize  = 128
)

type crawlEntry struct {
	url   string
	depth int
}

// subDownloadUrls is kept for backward compatibility with older tests that expect
// a synchronous crawl starting from a single URL.
func (r *RAria2) subDownloadUrls(ctx context.Context, workerId int, startURL string) {
	queue := []crawlEntry{{url: startURL, depth: 0}}
	enqueue := func(entry crawlEntry) bool {
		queue = append(queue, entry)
		return true
	}

	for len(queue) > 0 {
		entry := queue[0]
		queue = queue[1:]
		r.processCrawlEntry(ctx, workerId, entry, enqueue)
	}
}

type RAria2 struct {
	url                    *url.URL
	MaxConnectionPerServer int
	MaxConcurrentDownload  int
	MaxDepth               int
	Threads                int
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

	ftpList func(context.Context, *url.URL) ([]ftpListingEntry, error)
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
		Threads:                5,
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

var errNotHTML = errors.New("content is not HTML")

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

	r.crawl(ctx)

	close(entriesCh)
	r.downloadEntriesCh = nil
	downloadErr := <-downloadErrCh

	if err := r.saveVisitedCache(); err != nil {
		return fmt.Errorf("failed saving visited cache: %w", err)
	}

	return downloadErr
}

func (r *RAria2) crawl(ctx context.Context) {
	threads := r.Threads
	if threads < 1 {
		threads = 1
	}
	queueSize := defaultCrawlerQueueSize
	if threads*2 > queueSize {
		queueSize = threads * 2
	}

	inbox := make(chan crawlEntry, queueSize)
	work := make(chan crawlEntry, threads)

	var pending atomic.Int64
	doneCh := make(chan struct{})
	var doneOnce sync.Once

	enqueue := func(entry crawlEntry) bool {
		select {
		case <-ctx.Done():
			return false
		default:
		}

		pending.Add(1)
		select {
		case inbox <- entry:
			return true
		case <-ctx.Done():
			if pending.Add(-1) == 0 {
				doneOnce.Do(func() { close(doneCh) })
			}
			return false
		}
	}

	if !enqueue(crawlEntry{url: r.url.String(), depth: 0}) {
		close(work)
		return
	}

	go func() {
		defer close(work)
		queue := make([]crawlEntry, 0, queueSize)
		for {
			var out chan crawlEntry
			var next crawlEntry
			if len(queue) > 0 {
				out = work
				next = queue[0]
			}

			select {
			case <-ctx.Done():
				return
			case <-doneCh:
				return
			case entry := <-inbox:
				queue = append(queue, entry)
			case out <- next:
				queue = queue[1:]
			}
		}
	}()

	var workers sync.WaitGroup
	for i := 0; i < threads; i++ {
		workers.Add(1)
		go func(id int) {
			defer workers.Done()
			for entry := range work {
				r.processCrawlEntry(ctx, id, entry, enqueue)
				if pending.Add(-1) == 0 {
					doneOnce.Do(func() { close(doneCh) })
				}
			}
		}(i)
	}

	workers.Wait()
}

func (r *RAria2) processCrawlEntry(ctx context.Context, workerId int, entry crawlEntry, enqueue func(crawlEntry) bool) {
	select {
	case <-ctx.Done():
		return
	default:
	}

	filters := r.FiltersConfig()
	cUrl := entry.url
	parsedURL, err := url.Parse(cUrl)
	if err != nil {
		logrus.Warnf("skipping invalid URL %s: %v", cUrl, err)
		return
	}

	if !filters.PathAllowed(parsedURL) {
		logrus.Debugf("path filters skipped %s", cUrl)
		return
	}

	if r.RespectRobots && (parsedURL.Scheme == "http" || parsedURL.Scheme == "https") && !r.urlAllowedByRobots(parsedURL) {
		logrus.Debugf("robots.txt disallowed %s", cUrl)
		return
	}

	if !r.markVisited(cUrl) {
		logrus.WithField("url", cUrl).Debug("skipping already visited")
		return
	}

	// For FTP(S) we can distinguish directories from files by the trailing slash added
	// during listing. When no MIME validation is configured, avoid "tasting" files via
	// additional FTP LIST calls and queue them for download directly.
	if (parsedURL.Scheme == "ftp" || parsedURL.Scheme == "ftps") &&
		entry.depth > 0 &&
		!strings.HasSuffix(parsedURL.Path, "/") {
		if len(filters.AcceptMime) == 0 && len(filters.RejectMime) == 0 {
			r.downloadResource(workerId, cUrl)
			return
		}
	}

	newLinks, err := r.getLinksByUrlWithContext(ctx, cUrl)
	if err != nil {
		if errors.Is(err, errNotHTML) {
			r.downloadResource(workerId, cUrl)
			return
		}
		logrus.Warnf("unable to fetch %v: %v", cUrl, err)
		return
	}

	nextDepth := entry.depth + 1
	if r.MaxDepth >= 0 && nextDepth > r.MaxDepth {
		return
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
		if !enqueue(crawlEntry{url: link, depth: nextDepth}) {
			logrus.Debugf("skipping enqueue for %s due to context cancellation", link)
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
	isHTTP := parsedCUrl.Scheme == "http" || parsedCUrl.Scheme == "https"
	if len(filters.AcceptMime) > 0 || len(filters.RejectMime) > 0 {
		if !isHTTP {
			logrus.Debugf("[W %d]: skipping MIME filtering for non-HTTP(S) URL %s", workerId, cUrl)
		} else {
			contentType, ok := r.sniffHTTPContentType(workerId, cUrl)
			if !ok {
				return
			}
			if !filters.MimeAllowed(contentType) {
				logrus.Debugf("[W %d]: skipping %s due to MIME filter: %s", workerId, cUrl, contentType)
				return
			}
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
