package raria2

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/temoto/robotstxt"
)

func TestUrlAllowedByRobots(t *testing.T) {
	tests := []struct {
		name          string
		robotsContent string
		testPath      string
		userAgent     string
		expected      bool
		respectRobots bool
	}{
		{
			name:          "robots disabled - allow all",
			testPath:      "/private",
			expected:      true,
			respectRobots: false,
		},
		{
			name:          "no robots.txt - allow all",
			robotsContent: "",
			testPath:      "/private",
			expected:      true,
			respectRobots: true,
		},
		{
			name:          "simple disallow",
			robotsContent: "User-agent: *\nDisallow: /private\n",
			testPath:      "/private",
			expected:      false,
			respectRobots: true,
		},
		{
			name:          "simple allow",
			robotsContent: "User-agent: *\nDisallow: /private\n",
			testPath:      "/public",
			expected:      true,
			respectRobots: true,
		},
		{
			name:          "specific user agent",
			robotsContent: "User-agent: *\nDisallow: /\nUser-agent: raria2\nAllow: /public\n",
			testPath:      "/public",
			userAgent:     "raria2/1.0",
			expected:      true,
			respectRobots: true,
		},
		{
			name:          "specific user agent blocked",
			robotsContent: "User-agent: *\nAllow: /\nUser-agent: badbot\nDisallow: /\n",
			testPath:      "/public",
			userAgent:     "badbot/1.0",
			expected:      false,
			respectRobots: true,
		},
		{
			name:          "wildcard paths",
			robotsContent: "User-agent: *\nDisallow: /*.pdf\n",
			testPath:      "/document.pdf",
			expected:      false,
			respectRobots: true,
		},
		{
			name:          "wildcard paths - allowed",
			robotsContent: "User-agent: *\nDisallow: /*.pdf\n",
			testPath:      "/document.html",
			expected:      true,
			respectRobots: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseURL, _ := url.Parse("https://example.com/")
			r := &RAria2{
				url:           baseURL,
				RespectRobots: tt.respectRobots,
				UserAgent:     tt.userAgent,
			}

			if tt.respectRobots {
				// Create a test server for robots.txt
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/robots.txt" {
						w.Header().Set("Content-Type", "text/plain")
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, tt.robotsContent)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
				defer server.Close()

				// Update the URL to use test server (HTTP)
				testURL, _ := url.Parse(server.URL)
				r.url = testURL

				// Test the URL with the same protocol as the server
				testURL, _ = url.Parse(server.URL + tt.testPath)
				result := r.urlAllowedByRobots(testURL)
				assert.Equal(t, tt.expected, result)
			} else {
				// When robots is disabled, should always return true
				testURL, _ := url.Parse("https://example.com" + tt.testPath)
				result := r.urlAllowedByRobots(testURL)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestGetRobotsData(t *testing.T) {
	tests := []struct {
		name          string
		robotsContent string
		statusCode    int
		expectError   bool
	}{
		{
			name:          "valid robots.txt",
			robotsContent: "User-agent: *\nDisallow: /private\n",
			statusCode:    http.StatusOK,
			expectError:   false,
		},
		{
			name:          "404 - no robots.txt",
			robotsContent: "",
			statusCode:    http.StatusNotFound,
			expectError:   false, // Should not error, just return empty robots
		},
		{
			name:          "invalid robots.txt",
			robotsContent: "Invalid robots content",
			statusCode:    http.StatusOK,
			expectError:   false, // robotstxt parser is forgiving
		},
		{
			name:          "server error",
			robotsContent: "",
			statusCode:    http.StatusInternalServerError,
			expectError:   false, // Should fail open and return empty robots.txt
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseURL, _ := url.Parse("https://example.com/")
			r := &RAria2{
				url:         baseURL,
				UserAgent:   "raria2/1.0",
				robotsCache: make(map[string]*robotstxt.RobotsData),
			}

			// Create a test server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/robots.txt" {
					w.WriteHeader(tt.statusCode)
					if tt.statusCode == http.StatusOK {
						w.Header().Set("Content-Type", "text/plain")
						fmt.Fprint(w, tt.robotsContent)
					}
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()

			// Test getting robots data
			host := strings.TrimPrefix(server.URL, "http://")
			host = strings.TrimPrefix(host, "https://")

			robots, err := r.getRobotsData(host)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, robots)
			}
		})
	}
}

func TestCacheRobotsData(t *testing.T) {
	baseURL, _ := url.Parse("https://example.com/")
	r := &RAria2{
		url:         baseURL,
		robotsCache: make(map[string]*robotstxt.RobotsData),
	}

	// Create some robots data
	robotsData, _ := robotstxt.FromBytes([]byte("User-agent: *\nDisallow: /private\n"))

	// Cache the data
	r.cacheRobotsData("example.com", robotsData)

	// Verify it's cached
	cached, exists := r.robotsCache["example.com"]
	assert.True(t, exists)
	assert.Equal(t, robotsData, cached)

	// Test that subsequent calls use cache
	robots, err := r.getRobotsData("example.com")
	assert.NoError(t, err)
	assert.Equal(t, robotsData, robots)
}

func TestUrlAllowedByRobots_Caching(t *testing.T) {
	robotsContent := "User-agent: *\nDisallow: /private\n"

	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, robotsContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	baseURL, _ := url.Parse(server.URL)
	r := &RAria2{
		url:           baseURL,
		RespectRobots: true,
		UserAgent:     "raria2/1.0",
		robotsCache:   make(map[string]*robotstxt.RobotsData),
	}

	// First call should fetch from server
	testURL, _ := url.Parse(server.URL + "/private")
	result1 := r.urlAllowedByRobots(testURL)
	assert.False(t, result1)

	// Verify robots data was cached
	host := strings.TrimPrefix(server.URL, "http://")
	host = strings.TrimPrefix(host, "https://")
	_, exists := r.robotsCache[host]
	assert.True(t, exists)

	// Second call should use cache
	result2 := r.urlAllowedByRobots(testURL)
	assert.False(t, result2)
}
