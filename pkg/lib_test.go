package raria2

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestRun_WithRootFixture(t *testing.T) {
	ts := newFixtureServer()
	t.Cleanup(ts.Close)

	webUrl, err := url.Parse(ts.URL + "/")
	assert.Nil(t, err)
	client := New(webUrl)
	client.OutputPath = t.TempDir()
	client.dryRun = true

	err = client.Run()
	assert.Nil(t, err)
}

func TestRun_FromNestedPath(t *testing.T) {
	ts := newFixtureServer()
	t.Cleanup(ts.Close)

	webUrl, err := url.Parse(ts.URL + "/more/")
	assert.Nil(t, err)
	client := New(webUrl)
	client.OutputPath = t.TempDir()
	client.dryRun = true
	client.MaxConcurrentDownload = 2
	client.MaxConnectionPerServer = 2

	err = client.Run()
	assert.Nil(t, err)
}

func TestIsHtmlPage_DistinguishesHtml(t *testing.T) {
	ts := newFixtureServer()
	t.Cleanup(ts.Close)

	baseUrl, err := url.Parse(ts.URL + "/")
	assert.Nil(t, err)
	client := New(baseUrl)
	client.dryRun = true

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
			io.WriteString(w, body)
		}
	}

	serveFile := func(content string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			if r.Method == http.MethodHead {
				return
			}
			io.WriteString(w, content)
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
	outputDir := t.TempDir()
	r := &RAria2{
		url:        baseURL,
		OutputPath: outputDir,
		dryRun:     true,
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
	outputDir := t.TempDir()
	r := &RAria2{
		url:        baseURL,
		OutputPath: outputDir,
		dryRun:     true,
	}

	r.downloadResource(1, "https://cdn.example.net/files/asset.bin")

	if assert.Len(t, r.downloadEntries, 1) {
		entry := r.downloadEntries[0]
		expectedDir := filepath.Join(outputDir, "cdn.example.net/files")
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
