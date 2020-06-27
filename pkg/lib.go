package raria2

import (
	"bufio"
	"github.com/PuerkitoBio/goquery"
	"github.com/sirupsen/logrus"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type RAria2 struct {
	url                       *url.URL
	ParallelJobs              int
	ParallelConnectionsPerJob int
	ConcurrentDownloadsPerJob int
	OutputPath string

	urlList    []string
	httpClient *http.Client

	// does not perform any resource download
	dryRun bool

	// Sync functions
	wg *sync.WaitGroup
	urlCache map[string]bool
}

func New(url *url.URL) *RAria2 {
	return &RAria2{
		url:                       url,
		ParallelJobs:              5,
		ParallelConnectionsPerJob: 5,
		ConcurrentDownloadsPerJob: 5,
	}
}

func (r *RAria2) client() *http.Client {
	if r.httpClient != nil {
		return r.httpClient
	}

	r.httpClient = http.DefaultClient
	return r.httpClient
}

func (r *RAria2) IsHtmlPage(urlString string) (bool, error){
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

	// Check that output path exists
	if _, err := os.Stat(r.OutputPath); os.IsNotExist(err) {
		err = os.Mkdir(r.OutputPath, 0755)
		if err != nil {
			return err
		}
	}
	dir, _ := os.Getwd()
	logrus.Infof("pwd: %v", dir)
	// Fetch the first URL
	var err error
	r.urlList, err = r.getLinksByUrl(r.url.String())
	if err != nil {
		return err
	}

	logrus.Infof("got %d URLs: dividing them into %d jobs", len(r.urlList), r.ParallelJobs)

	jobSplit := int(math.Ceil(float64(float32(len(r.urlList)) / float32(r.ParallelJobs))))
	r.wg = &sync.WaitGroup{}

	for i := 0; i < r.ParallelJobs; i++ {
		beginIndex := i * jobSplit
		endIndex := intMin((i+1) * jobSplit, len(r.urlList))
		if beginIndex > endIndex {
			break
		}
		r.wg.Add(1)
		go r.downloadUrls(i, r.urlList[beginIndex:endIndex])
	}

	r.wg.Wait()

	return nil

}

func (r *RAria2) downloadUrls(workerId int, urls []string) {
	// Do some work
	for _, cUrl := range urls {
		r.subDownloadUrls(workerId, cUrl)
	}
	// End of work

	logrus.Infof("worker %d ended his work (url=%v)", workerId, urls)
	r.wg.Done()
}

func (r *RAria2) subDownloadUrls(workerId int, cUrl string) {
	// Check if cache has already seen this URL
	if r.urlCache[cUrl] {
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
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		if r.dryRun {
			logrus.Infof("[W %d]: dry run: creating folder %s", workerId, outputDir)
		} else {
			err = os.MkdirAll(outputDir, 0744)
			if err != nil {
				logrus.Warnf("unable to create %v: %v", outputDir, err)
				return
			}
		}
	}

	binFile := "aria2c"
	args := []string{"-x", strconv.Itoa(r.ParallelConnectionsPerJob), "-d", outputDir}

	// cmd
	if r.dryRun {
		args = append(args, "--dry-run=true")
	}

	args = append(args, cUrl)

	if r.dryRun {
		logrus.Infof("cmd: %s %s", binFile, strings.Join(args, " "))
	}

	cmd := exec.Command(binFile, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		logrus.Warnf("unable to get stdout for subcommand: %v", err)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		logrus.Warnf("unable to get stderr for subcommand: %v", err)
		return
	}
	stdoutScanner := bufio.NewScanner(stdout)
	stderrScanner := bufio.NewScanner(stderr)

	err = cmd.Start()

	for stdoutScanner.Scan() {
		logrus.Infof("[W %d]: %s reports: %v", workerId, binFile, stdoutScanner.Text())
	}

	for stderrScanner.Scan() {
		logrus.Warnf("[W %d]: %s reports: %v", workerId, binFile, stderrScanner.Text())
	}

	if err != nil {
		logrus.Warnf("unable to run %v: %v", binFile, err)
	}

	err = cmd.Wait()

	if err != nil {
		logrus.Warnf("unable to wait %v: %v", binFile, err)
	}
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
