package raria2

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/PuerkitoBio/goquery"
	"github.com/sirupsen/logrus"
)

type RAria2 struct {
	url                    *url.URL
	MaxConnectionPerServer int
	MaxConcurrentDownload  int
	OutputPath             string
	Aria2AfterURLArgs      []string

	urlList    []string
	httpClient *http.Client

	downloadEntries   []aria2URLEntry
	downloadEntriesMu sync.Mutex

	// does not perform any resource download
	dryRun bool

	urlCache   map[string]struct{}
	urlCacheMu sync.Mutex
}

func New(url *url.URL) *RAria2 {
	return &RAria2{
		url:                    url,
		MaxConnectionPerServer: 5,
		MaxConcurrentDownload:  5,
		urlCache:               make(map[string]struct{}),
	}
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

	r.httpClient = http.DefaultClient
	return r.httpClient
}

func (r *RAria2) IsHtmlPage(urlString string) (bool, error) {
	req, err := http.NewRequest("HEAD", urlString, nil)
	if err != nil {
		return false, err
	}

	res, err := r.client().Do(req)
	if err != nil {
		return false, err
	}

	contentType := res.Header.Get("Content-Type")
	contentTypeEntries := strings.Split(contentType, ";")
	for _, v := range contentTypeEntries {
		if v == "text/html" {
			return true, nil
		}
	}
	return false, nil
}

func (r *RAria2) getLinksByUrl(urlString string) ([]string, error) {
	parsedUrl, err := url.Parse(urlString)
	if err != nil {
		return []string{}, err
	}

	req, err := http.NewRequest("GET", urlString, nil)
	if err != nil {
		return nil, err
	}

	res, err := r.client().Do(req)
	if err != nil {
		return nil, err
	}

	return getLinks(parsedUrl, res.Body)
}

func (r *RAria2) Run() error {
	if err := r.ensureOutputPath(); err != nil {
		return err
	}
	dir, _ := os.Getwd()
	logrus.Infof("pwd: %v", dir)
	// Fetch the first URL
	var err error
	r.urlList, err = r.getLinksByUrl(r.url.String())
	if err != nil {
		return err
	}

	logrus.Infof("queuing %d URLs for batch download", len(r.urlList))
	for _, link := range r.urlList {
		r.subDownloadUrls(0, link)
	}

	return r.executeBatchDownload()

}

func (r *RAria2) subDownloadUrls(workerId int, cUrl string) {
	if !r.markVisited(cUrl) {
		logrus.Infof("cache hit for %v. won't re-visit", cUrl)
		return
	}

	// Fetch URLs
	isHtml, err := r.IsHtmlPage(cUrl)
	if err != nil {
		logrus.Warnf("unable to get %v content type: %v", cUrl, err)
		return
	}

	if isHtml {
		newLinks, err := r.getLinksByUrl(cUrl)
		if err != nil {
			logrus.Errorf("error in wid %d: %v", workerId, err)
			return
		}

		for _, link := range newLinks {
			r.subDownloadUrls(workerId, link)
		}
		return
	} else {
		r.downloadResource(workerId, cUrl)
		return
	}
}

func (r *RAria2) downloadResource(workerId int, cUrl string) {
	parsedCUrl, err := url.Parse(cUrl)
	if err != nil {
		logrus.Warnf("[W %d]: unable to download %v because it's an invalid URL: %v", workerId, cUrl, err)
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

	if r.dryRun {
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
	r.urlCacheMu.Lock()
	defer r.urlCacheMu.Unlock()
	if _, exists := r.urlCache[u]; exists {
		return false
	}
	r.urlCache[u] = struct{}{}
	return true
}

func (r *RAria2) ensureOutputDir(workerId int, dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if r.dryRun {
			logrus.Infof("[W %d]: dry run: creating folder %s", workerId, dir)
			return nil
		}
		if err := os.MkdirAll(dir, 0o744); err != nil {
			return err
		}
	}
	return nil
}

func (r *RAria2) enqueueDownloadEntry(entry aria2URLEntry) {
	r.downloadEntriesMu.Lock()
	defer r.downloadEntriesMu.Unlock()
	r.downloadEntries = append(r.downloadEntries, entry)
}

func (r *RAria2) executeBatchDownload() error {
	r.downloadEntriesMu.Lock()
	entries := make([]aria2URLEntry, len(r.downloadEntries))
	copy(entries, r.downloadEntries)
	r.downloadEntriesMu.Unlock()

	if len(entries) == 0 {
		logrus.Info("no downloadable resources found")
		return nil
	}

	var buf bytes.Buffer
	writer := bufio.NewWriter(&buf)
	for _, entry := range entries {
		if _, err := fmt.Fprintln(writer, entry.URL); err != nil {
			return err
		}
		if entry.Dir != "" {
			if _, err := fmt.Fprintf(writer, "  dir=%s\n", entry.Dir); err != nil {
				return err
			}
		}
	}

	if err := writer.Flush(); err != nil {
		return err
	}

	binFile := "aria2c"
	args := []string{"-x", strconv.Itoa(r.MaxConnectionPerServer)}
	if r.MaxConcurrentDownload > 0 {
		args = append(args, "-j", strconv.Itoa(r.MaxConcurrentDownload))
	}
	args = append(args, "--input-file", "-", "--deferred-input=true")

	if r.dryRun {
		args = append(args, "--dry-run=true")
	}

	if len(r.Aria2AfterURLArgs) > 0 {
		args = append(args, r.Aria2AfterURLArgs...)
	}

	if r.dryRun {
		logrus.Infof("aria2 batch cmd: %s %s", binFile, strings.Join(args, " "))
		return nil
	}

	cmd := exec.Command(binFile, args...)
	cmd.Stdin = bytes.NewReader(buf.Bytes())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("unable to get stdout for aria2 batch command: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("unable to get stderr for aria2 batch command: %w", err)
	}
	stdoutScanner := bufio.NewScanner(stdout)
	stderrScanner := bufio.NewScanner(stderr)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("unable to start aria2 batch command: %w", err)
	}

	for stdoutScanner.Scan() {
		logrus.Infof("aria2c reports: %v", stdoutScanner.Text())
	}

	for stderrScanner.Scan() {
		logrus.Warnf("aria2c reports: %v", stderrScanner.Text())
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("aria2 batch command failed: %w", err)
	}

	return nil
}

type aria2URLEntry struct {
	URL string
	Dir string
}

func intMin(a int, b int) int {
	if a < b {
		return a
	}

	if a > b {
		return b
	}

	return a
}

func getLinks(originalUrl *url.URL, body io.ReadCloser) ([]string, error) {
	document, err := goquery.NewDocumentFromReader(body)

	var urlList []string

	if err != nil {
		return urlList, err
	}

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
		resolvedRefUrl := originalUrl.ResolveReference(aHrefUrl)
		if SameUrl(resolvedRefUrl, originalUrl) {
			return
		}

		if IsSubPath(resolvedRefUrl, originalUrl) {
			urlList = append(urlList, resolvedRefUrl.String())
		}
	})

	return urlList, nil
}

func IsSubPath(subject *url.URL, of *url.URL) bool {
	if subject.Host != of.Host {
		return false
	}

	if subject.Scheme != of.Scheme {
		return false
	}

	return strings.HasPrefix(subject.Path, of.Path)
}

// An URL is considered to be the same in our context
// when they share the same hostname and path.
// In our case, /, /?C=N;O=D, /?C=M;O=A, ... are all considered to be the same URL.
func SameUrl(a *url.URL, b *url.URL) bool {
	if a.Host != b.Host {
		return false
	}

	if a.Path != b.Path {
		return false
	}

	if a.Port() != b.Port() {
		return false
	}

	return true
}
