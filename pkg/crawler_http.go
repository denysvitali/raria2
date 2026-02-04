package raria2

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/sirupsen/logrus"
)

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

func (r *RAria2) getLinksByUrl(urlString string) ([]string, error) {
	return r.getLinksByUrlWithContext(context.Background(), urlString)
}

func (r *RAria2) getLinksByUrlWithContext(ctx context.Context, urlString string) ([]string, error) {
	parsedUrl, err := url.Parse(urlString)
	if err != nil {
		return []string{}, err
	}

	scheme := strings.ToLower(parsedUrl.Scheme)
	if scheme == "ftp" || scheme == "ftps" {
		return r.getLinksByFTPWithContext(ctx, parsedUrl)
	}
	if scheme != "http" && scheme != "https" {
		return nil, errNotHTML
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
