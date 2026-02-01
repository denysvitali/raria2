package raria2

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func newTestContentTypeDetector(t *testing.T, handler http.HandlerFunc) (*ContentTypeDetector, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := NewHTTPClient(2*time.Second, 0)
	client.client = server.Client()

	return NewContentTypeDetector(client), server.URL + "/resource"
}

func TestNewContentTypeDetector(t *testing.T) {
	detector := NewContentTypeDetector(NewHTTPClient(5*time.Second, 0))
	assert.NotNil(t, detector)
}

func TestContentTypeDetectorIsHTMLPage(t *testing.T) {
	tests := []struct {
		name      string
		handler   func(t *testing.T) http.HandlerFunc
		expect    bool
		expectErr bool
	}{
		{
			name:   "head_indicates_html",
			expect: true,
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, http.MethodHead, r.Method)
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					w.WriteHeader(http.StatusOK)
				}
			},
		},
		{
			name:   "fallback_head_missing_content_type",
			expect: true,
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					switch r.Method {
					case http.MethodHead:
						w.WriteHeader(http.StatusOK)
					case http.MethodGet:
						assert.Equal(t, "bytes=0-1023", r.Header.Get("Range"))
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("<!doctype html><html></html>"))
					default:
						t.Fatalf("unexpected method: %s", r.Method)
					}
				}
			},
		},
		{
			name:   "range_not_supported_triggers_full_get",
			expect: true,
			handler: func(t *testing.T) http.HandlerFunc {
				var rangeAttempt bool
				return func(w http.ResponseWriter, r *http.Request) {
					switch r.Method {
					case http.MethodHead:
						w.WriteHeader(http.StatusOK)
					case http.MethodGet:
						if r.Header.Get("Range") != "" {
							rangeAttempt = true
							w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
							return
						}
						assert.True(t, rangeAttempt, "expected range attempt first")
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("<html><body>content</body></html>"))
					default:
						t.Fatalf("unexpected method: %s", r.Method)
					}
				}
			},
		},
		{
			name: "non_html_content",
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					switch r.Method {
					case http.MethodHead:
						w.WriteHeader(http.StatusOK)
					case http.MethodGet:
						w.WriteHeader(http.StatusOK)
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"ok":true}`))
					default:
						t.Fatalf("unexpected method: %s", r.Method)
					}
				}
			},
			expect: false,
		},
		{
			name: "head_transient_error",
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detector, url := newTestContentTypeDetector(t, tt.handler(t))
			result, err := detector.IsHTMLPage(url, "test-agent")
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.expect, result)
		})
	}
}
