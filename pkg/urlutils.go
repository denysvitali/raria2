package raria2

import (
	"net/url"
	"path"
	"strings"
)

// canonicalURL returns a normalized version of a URL for consistent comparison
// This function should be used everywhere URL comparison is needed
func canonicalURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	// Preserve whether the original URL looked like a directory (trailing slash in the *path*).
	// Important for URLs like: https://host/dir/?C=M;O=A
	// where raw does not end with '/', but the path does.
	hadTrailingSlash := parsed.Path != "/" && strings.HasSuffix(parsed.Path, "/")

	// Normalize scheme to lowercase
	parsed.Scheme = strings.ToLower(parsed.Scheme)

	// Normalize host to lowercase
	if parsed.Host != "" {
		parsed.Host = strings.ToLower(parsed.Host)

		// Normalize default ports
		switch parsed.Scheme {
		case "http":
			if parsed.Port() == "80" {
				parsed.Host = parsed.Hostname()
			}
		case "https":
			if parsed.Port() == "443" {
				parsed.Host = parsed.Hostname()
			}
		}
	}

	// Always strip fragments
	parsed.Fragment = ""

	// Normalize path by cleaning dot-segments.
	// Note: this also collapses multiple slashes.
	if parsed.Path == "" {
		parsed.Path = "/"
	} else {
		parsed.Path = path.Clean(parsed.Path)
		if parsed.Path == "." {
			parsed.Path = "/"
		}
	}
	parsed.RawPath = ""

	// Normalize path - but preserve root path
	if parsed.Path != "/" && strings.HasSuffix(parsed.Path, "/") {
		// Remove trailing slashes for consistency (except root)
		parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	}

	// For directory-style paths (ending with / in the original path) or root paths with query params,
	// clear query params (handles server directory listing sorting parameters).
	if hadTrailingSlash || (parsed.Path == "/" && parsed.RawQuery != "") {
		parsed.RawQuery = ""
	}

	return parsed.String()
}

// SameUrl checks if two URLs are considered the same using canonicalization
func SameUrl(a *url.URL, b *url.URL) bool {
	return canonicalURL(a.String()) == canonicalURL(b.String())
}

// IsSubPath checks if subject URL is a sub-path of the base URL
func IsSubPath(subject *url.URL, of *url.URL) bool {
	// Use canonical URLs for consistent comparison
	subjectCanonical := canonicalURL(subject.String())
	ofCanonical := canonicalURL(of.String())

	subjectParsed, _ := url.Parse(subjectCanonical)
	ofParsed, _ := url.Parse(ofCanonical)

	if subjectParsed.Host != ofParsed.Host {
		return false
	}

	if subjectParsed.Scheme != ofParsed.Scheme {
		return false
	}

	basePath := ofParsed.Path
	if basePath == "" {
		basePath = "/"
	}

	baseNoSlash := basePath
	if baseNoSlash != "/" {
		baseNoSlash = strings.TrimSuffix(baseNoSlash, "/")
	}

	baseWithSlash := baseNoSlash
	if baseNoSlash != "/" {
		baseWithSlash = baseNoSlash + "/"
	}

	if subjectParsed.Path == baseNoSlash {
		return true
	}

	return strings.HasPrefix(subjectParsed.Path, baseWithSlash)
}
