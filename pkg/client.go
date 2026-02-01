package raria2

import (
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
)

// HTTPClient wraps the standard HTTP client with retry logic and rate limiting
type HTTPClient struct {
	client      *http.Client
	rateLimit   float64
	lastRequest int64 // Unix timestamp with nanoseconds
	retryCount  int
	baseDelay   time.Duration
}

// NewHTTPClient creates a new HTTP client with the specified timeout and rate limit
func NewHTTPClient(timeout time.Duration, rateLimit float64) *HTTPClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &HTTPClient{
		client:     &http.Client{Timeout: timeout},
		rateLimit:  rateLimit,
		retryCount: 3,
		baseDelay:  100 * time.Millisecond,
	}
}

// Do performs an HTTP request with retry logic and rate limiting
func (c *HTTPClient) Do(req *http.Request) (*http.Response, error) {
	return c.doWithRetry(req, false)
}

// DoWithRetry performs an HTTP request with retry logic and rate limiting
func (c *HTTPClient) DoWithRetry(req *http.Request) (*http.Response, error) {
	return c.doWithRetry(req, true)
}

// doWithRetry implements the retry logic
func (c *HTTPClient) doWithRetry(req *http.Request, enableRetries bool) (*http.Response, error) {
	// If retries are disabled, just do a single request
	if !enableRetries {
		c.waitForRateLimit()
		return c.client.Do(req)
	}

	var lastErr error

	for attempt := 0; attempt < c.retryCount; attempt++ {
		if attempt > 0 {
			// Exponential backoff with jitter
			delay := c.baseDelay * time.Duration(1<<uint(attempt-1))
			jitter := time.Duration(float64(delay) * 0.1 * (0.5 + 0.5*rand.Float64()))
			time.Sleep(delay + jitter)

			logrus.Debugf("Retrying HTTP request (attempt %d/%d)", attempt+1, c.retryCount)
		}

		c.waitForRateLimit()

		resp, err := c.client.Do(req)
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
		if IsTransientError(err) {
			lastErr = err
			continue
		}

		// Non-transient error, return immediately
		return nil, err
	}

	return nil, fmt.Errorf("max retries exceeded: %w", lastErr)
}

// waitForRateLimit implements rate limiting with proper handling of fractional rates
func (c *HTTPClient) waitForRateLimit() {
	if c.rateLimit <= 0 {
		return
	}

	now := time.Now().UnixNano()
	last := atomic.LoadInt64(&c.lastRequest)

	// Calculate minimum time between requests using float arithmetic
	// This fixes the bug where fractional rates (<1 rps) would truncate to zero
	minInterval := time.Duration(float64(time.Second) / c.rateLimit)

	// Guard against zero or negative intervals (shouldn't happen with positive rateLimit)
	if minInterval <= 0 {
		return
	}

	if now-last < int64(minInterval) {
		sleepTime := time.Duration(minInterval - time.Duration(now-last))
		time.Sleep(sleepTime)
	}

	atomic.StoreInt64(&c.lastRequest, time.Now().UnixNano())
}

// IsTransientError checks if an error is transient and worth retrying
func IsTransientError(err error) bool {
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

// ContentTypeDetector handles content type detection
type ContentTypeDetector struct {
	client *HTTPClient
}

// NewContentTypeDetector creates a new content type detector
func NewContentTypeDetector(client *HTTPClient) *ContentTypeDetector {
	return &ContentTypeDetector{client: client}
}

// IsHTMLPage determines if a URL points to an HTML page
func (d *ContentTypeDetector) IsHTMLPage(urlString, userAgent string) (bool, error) {
	// First try HEAD request
	req, err := http.NewRequest("HEAD", urlString, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", userAgent)

	res, err := d.client.DoWithRetry(req)
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
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Range", "bytes=0-1023")
		res, err = d.client.DoWithRetry(req)
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
			req.Header.Set("User-Agent", userAgent)
			res, err = d.client.DoWithRetry(req)
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

// IsHTMLContent checks if content type indicates HTML
func IsHTMLContent(contentType string) bool {
	contentTypeEntries := strings.Split(contentType, ";")
	for _, v := range contentTypeEntries {
		if strings.TrimSpace(v) == "text/html" {
			return true
		}
	}
	return false
}
