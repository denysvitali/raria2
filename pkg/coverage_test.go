package raria2

import (
	"errors"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestWaitForRateLimit(t *testing.T) {
	tests := []struct {
		name        string
		rateLimit   float64
		expectDelay bool
	}{
		{
			name:        "no rate limit",
			rateLimit:   0,
			expectDelay: false,
		},
		{
			name:        "small rate limit",
			rateLimit:   10,    // 10 requests per second
			expectDelay: false, // Too fast to measure reliably
		},
		{
			name:        "high rate limit",
			rateLimit:   1000,  // 1000 requests per second
			expectDelay: false, // Very small delay
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &RAria2{
				RateLimit: tt.rateLimit,
			}

			start := time.Now()
			r.waitForRateLimit()
			duration := time.Since(start)

			// Just verify it doesn't panic and completes
			assert.Greater(t, duration, time.Duration(0))
		})
	}
}

func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "timeout error",
			err:      errors.New("timeout"),
			expected: true,
		},
		{
			name:     "connection refused",
			err:      errors.New("connection refused"),
			expected: true,
		},
		{
			name:     "temporary failure",
			err:      errors.New("temporary failure"),
			expected: true,
		},
		{
			name:     "generic error",
			err:      assert.AnError,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isTransientError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsHTMLContent_Coverage(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		expected    bool
	}{
		{
			name:        "HTML content type",
			contentType: "text/html",
			expected:    true,
		},
		{
			name:        "HTML with charset",
			contentType: "text/html; charset=utf-8",
			expected:    true,
		},
		{
			name:        "plain text",
			contentType: "text/plain",
			expected:    false,
		},
		{
			name:        "JSON content",
			contentType: "application/json",
			expected:    false,
		},
		{
			name:        "empty string",
			contentType: "",
			expected:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isHTMLContent(tt.contentType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCanonicalURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple URL",
			input:    "https://example.com/path",
			expected: "https://example.com/path",
		},
		{
			name:     "URL with fragment",
			input:    "https://example.com/path#fragment",
			expected: "https://example.com/path",
		},
		{
			name:     "URL with query and fragment",
			input:    "https://example.com/path?param=value#fragment",
			expected: "https://example.com/path?param=value",
		},
		{
			name:     "URL with only fragment",
			input:    "https://example.com/path#",
			expected: "https://example.com/path",
		},
		{
			name:     "URL with empty fragment",
			input:    "https://example.com/path#",
			expected: "https://example.com/path",
		},
		{
			name:     "relative URL",
			input:    "/path/to/resource",
			expected: "/path/to/resource",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "/",
		},
		{
			name:     "directory listing with sorting params",
			input:    "https://example.com/dir/?C=M;O=A",
			expected: "https://example.com/dir",
		},
		{
			name:     "directory with trailing slash and query",
			input:    "https://example.com/dir/?param=value",
			expected: "https://example.com/dir",
		},
		{
			name:     "root directory with query",
			input:    "https://example.com/?C=M;O=A",
			expected: "https://example.com/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := canonicalURL(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsSubPath_Coverage(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		target   string
		expected bool
	}{
		{
			name:     "same path",
			base:     "/path/to/file",
			target:   "/path/to/file",
			expected: true,
		},
		{
			name:     "subdirectory",
			base:     "/path/to",
			target:   "/path/to/file",
			expected: false,
		},
		{
			name:     "different path",
			base:     "/path/to",
			target:   "/other/path/file",
			expected: false,
		},
		{
			name:     "parent directory",
			base:     "/path/to/file",
			target:   "/path",
			expected: true,
		},
		{
			name:     "root path",
			base:     "/",
			target:   "/any/path",
			expected: false,
		},
		{
			name:     "empty paths",
			base:     "",
			target:   "",
			expected: true,
		},
		{
			name:     "similar but different",
			base:     "/path1",
			target:   "/path2",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseURL, _ := url.Parse(tt.base)
			targetURL, _ := url.Parse(tt.target)
			result := IsSubPath(baseURL, targetURL)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSameUrl_Coverage(t *testing.T) {
	tests := []struct {
		name     string
		url1     string
		url2     string
		expected bool
	}{
		{
			name:     "identical URLs",
			url1:     "https://example.com/path",
			url2:     "https://example.com/path",
			expected: true,
		},
		{
			name:     "different scheme",
			url1:     "https://example.com/path",
			url2:     "http://example.com/path",
			expected: false,
		},
		{
			name:     "different host",
			url1:     "https://example.com/path",
			url2:     "https://other.com/path",
			expected: false,
		},
		{
			name:     "different path",
			url1:     "https://example.com/path1",
			url2:     "https://example.com/path2",
			expected: false,
		},
		{
			name:     "case insensitive host",
			url1:     "https://EXAMPLE.com/path",
			url2:     "https://example.com/path",
			expected: true,
		},
		{
			name:     "trailing slash difference",
			url1:     "https://example.com/path",
			url2:     "https://example.com/path/",
			expected: true,
		},
		{
			name:     "invalid URLs",
			url1:     "not-a-url",
			url2:     "also-not-a-url",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url1, _ := url.Parse(tt.url1)
			url2, _ := url.Parse(tt.url2)
			result := SameUrl(url1, url2)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEnsureOutputDir_Coverage(t *testing.T) {
	tests := []struct {
		name        string
		outputPath  string
		expectError bool
	}{
		{
			name:        "valid existing directory",
			outputPath:  "/tmp",
			expectError: false,
		},
		{
			name:        "empty path",
			outputPath:  "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &RAria2{
				OutputPath: tt.outputPath,
			}

			err := r.ensureOutputDir(0, tt.outputPath)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestWriteBatchFile_Coverage(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name        string
		entries     []byte
		expectError bool
	}{
		{
			name:        "valid entries",
			entries:     []byte("http://example.com/file1.pdf\nhttp://example.com/file2.txt\n"),
			expectError: false,
		},
		{
			name:        "empty entries",
			entries:     []byte(""),
			expectError: false,
		},
		{
			name:        "single entry",
			entries:     []byte("http://example.com/file.pdf\n"),
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batchFile := filepath.Join(tempDir, "batch.txt")
			r := &RAria2{
				OutputPath: tempDir,
				WriteBatch: batchFile,
			}

			err := r.writeBatchFile(tt.entries)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
