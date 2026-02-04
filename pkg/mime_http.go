package raria2

import (
	"io"
	"net/http"
)

func (r *RAria2) sniffHTTPContentType(workerId int, cUrl string) (string, bool) {
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
			return "", false
		}
		sniffReq.Header.Set("User-Agent", r.UserAgent)
		sniffReq.Header.Set("Range", "bytes=0-1023")
		var sniffRes *http.Response
		if r.DisableRetries {
			sniffRes, err = r.client().Do(sniffReq)
		} else {
			sniffRes, err = r.client().DoWithRetry(sniffReq)
		}
		if err != nil {
			return "", false
		}
		defer sniffRes.Body.Close()

		if sniffRes.StatusCode >= 200 && sniffRes.StatusCode < 300 {
			limitReader := io.LimitReader(sniffRes.Body, 1024)
			bodyBytes, _ := io.ReadAll(limitReader)
			contentType = http.DetectContentType(bodyBytes)
		}
	}

	return contentType, true
}
