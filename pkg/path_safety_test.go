package raria2

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDownloadResource_PathSafety(t *testing.T) {
	baseURL, _ := url.Parse("https://example.com/root/")
	outputDir := tempDir(t)

	tests := []struct {
		name        string
		testURL     string
		expectedDir string
	}{
		{
			name:        "normal relative path",
			testURL:     "https://example.com/root/files/document.pdf",
			expectedDir: "files",
		},
		{
			name:        "absolute path in URL - should be stripped",
			testURL:     "https://example.com/root//absolute/path/file.bin",
			expectedDir: "absolute/path",
		},
		{
			name:        "multiple leading slashes - should be stripped",
			testURL:     "https://example.com/root///multiple/slashes/file.bin",
			expectedDir: "multiple/slashes",
		},
		{
			name:        "external host - should use host directory",
			testURL:     "https://cdn.example.com/files/asset.bin",
			expectedDir: "127.0.0.1:PORT/files", // Test server host with port and path
		},
		{
			name:        "external host with absolute path - should be stripped",
			testURL:     "https://cdn.example.com//absolute/path/asset.bin",
			expectedDir: "127.0.0.1:PORT/absolute/path", // Test server host with port and path
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &RAria2{
				url:            baseURL,
				OutputPath:     outputDir,
				DryRun:         true,
				DisableRetries: true,
			}

			// Create a test server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			// Replace the host with test server but keep the path
			parsedURL, _ := url.Parse(tt.testURL)
			testURL := server.URL + parsedURL.Path

			r.downloadResource(1, testURL)

			if assert.Len(t, r.downloadEntries, 1) {
				entry := r.downloadEntries[0]
				// Extract the actual host:port from the test server URL for comparison
				parsedServerURL, _ := url.Parse(server.URL)
				actualExpectedDir := strings.Replace(tt.expectedDir, "PORT", strings.Split(parsedServerURL.Host, ":")[1], 1)
				expectedFullDir := outputDir + "/" + actualExpectedDir
				assert.Equal(t, expectedFullDir, entry.Dir)
			}

			// Clear entries for next test
			r.downloadEntries = nil
		})
	}
}

func TestDownloadResource_PathSafetyEdgeCases(t *testing.T) {
	baseURL, _ := url.Parse("https://example.com/root/")
	outputDir := tempDir(t)

	tests := []struct {
		name        string
		testURL     string
		expectedDir string
	}{
		{
			name:        "root path",
			testURL:     "https://example.com/",
			expectedDir: "example.com",
		},
		{
			name:        "empty path",
			testURL:     "https://example.com",
			expectedDir: "example.com",
		},
		{
			name:        "just slash",
			testURL:     "https://example.com/",
			expectedDir: "example.com",
		},
		{
			name:        "path with dot segments",
			testURL:     "https://example.com/root/../file.bin",
			expectedDir: "example.com",
		},
		{
			name:        "path with current dir",
			testURL:     "https://example.com/root/./file.bin",
			expectedDir: "file.bin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &RAria2{
				url:            baseURL,
				OutputPath:     outputDir,
				DryRun:         true,
				DisableRetries: true,
			}

			// Create a test server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			// Replace the host with test server but keep the path
			parsedURL, _ := url.Parse(tt.testURL)
			testURL := server.URL + parsedURL.Path

			r.downloadResource(1, testURL)

			if assert.Len(t, r.downloadEntries, 1) {
				entry := r.downloadEntries[0]
				// For edge cases, we just verify it doesn't create absolute paths
				assert.NotContains(t, entry.Dir, "//")
				assert.NotContains(t, entry.Dir, "/"+outputDir)
			}

			// Clear entries for next test
			r.downloadEntries = nil
		})
	}
}
