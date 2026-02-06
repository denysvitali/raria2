package raria2

import (
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
)

// FilterManager handles all filtering logic for URLs, extensions, filenames, and MIME types
type FilterManager struct {
	AcceptExtensions     map[string]struct{}
	RejectExtensions     map[string]struct{}
	AcceptFilenames      map[string]*regexp.Regexp
	RejectFilenames      map[string]*regexp.Regexp
	CaseInsensitivePaths bool
	AcceptPathRegex      []*regexp.Regexp
	RejectPathRegex      []*regexp.Regexp
	AcceptMime           map[string]struct{}
	RejectMime           map[string]struct{}
	baseURL              *url.URL

	// Cache for case-insensitive regex patterns
	ciRegexCache   map[*regexp.Regexp]*regexp.Regexp
	ciRegexCacheMu sync.RWMutex
}

// NewFilterManager creates a new filter manager
func NewFilterManager(baseURL *url.URL) *FilterManager {
	return &FilterManager{
		baseURL:      baseURL,
		ciRegexCache: make(map[*regexp.Regexp]*regexp.Regexp),
	}
}

// PathAllowed checks if a URL path passes the path filters
func (fm *FilterManager) PathAllowed(u *url.URL) bool {
	pathStr := u.Path
	if pathStr == "" {
		pathStr = "/"
	}

	// Apply case-insensitive matching if enabled
	if fm.CaseInsensitivePaths {
		pathStr = strings.ToLower(pathStr)
	}

	var rejectPatterns []*regexp.Regexp
	var acceptPatterns []*regexp.Regexp

	if fm.CaseInsensitivePaths {
		rejectPatterns = fm.caseInsensitivePatterns(fm.RejectPathRegex)
		acceptPatterns = fm.caseInsensitivePatterns(fm.AcceptPathRegex)
	} else {
		rejectPatterns = fm.RejectPathRegex
		acceptPatterns = fm.AcceptPathRegex
	}

	if matchAnyRegex(rejectPatterns, pathStr) {
		return false
	}

	if len(acceptPatterns) == 0 {
		return true
	}

	if matchAnyRegex(acceptPatterns, pathStr) {
		return true
	}

	basePath := fm.baseURL.Path
	if basePath == "" {
		basePath = "/"
	}

	// Apply case-insensitive matching to base path if enabled
	if fm.CaseInsensitivePaths {
		basePath = strings.ToLower(basePath)
	}

	trimPath := strings.TrimSuffix(pathStr, "/")
	trimBase := strings.TrimSuffix(basePath, "/")
	if trimPath == "" {
		trimPath = "/"
	}
	if trimBase == "" {
		trimBase = "/"
	}

	return trimPath == trimBase
}

// ExtensionAllowed checks if a URL extension passes the extension filters
func (fm *FilterManager) ExtensionAllowed(u *url.URL) bool {
	if len(fm.AcceptExtensions) == 0 && len(fm.RejectExtensions) == 0 {
		return true
	}

	ext := strings.ToLower(strings.TrimPrefix(path.Ext(u.Path), "."))

	if len(fm.AcceptExtensions) > 0 {
		if _, ok := fm.AcceptExtensions[ext]; !ok {
			return false
		}
	}

	if len(fm.RejectExtensions) > 0 {
		if _, ok := fm.RejectExtensions[ext]; ok {
			return false
		}
	}

	return true
}

// FilenameAllowed checks if a URL filename passes the filename filters
func (fm *FilterManager) FilenameAllowed(u *url.URL) bool {
	if len(fm.AcceptFilenames) == 0 && len(fm.RejectFilenames) == 0 {
		return true
	}

	filename := path.Base(u.Path)

	if len(fm.AcceptFilenames) > 0 {
		accepted := false
		for _, pattern := range fm.AcceptFilenames {
			if pattern.MatchString(filename) {
				accepted = true
				break
			}
		}
		if !accepted {
			return false
		}
	}

	if len(fm.RejectFilenames) > 0 {
		for _, pattern := range fm.RejectFilenames {
			if pattern.MatchString(filename) {
				return false
			}
		}
	}

	return true
}

// MimeAllowed checks if a MIME type passes the MIME filters
func (fm *FilterManager) MimeAllowed(contentType string) bool {
	if len(fm.AcceptMime) == 0 && len(fm.RejectMime) == 0 {
		return true
	}

	// Normalize content type (lowercase, remove parameters like charset)
	mimeType := strings.ToLower(strings.Split(contentType, ";")[0])
	mimeType = strings.TrimSpace(mimeType)

	// If reject filter has the MIME type, reject it first
	if len(fm.RejectMime) > 0 {
		if _, ok := fm.RejectMime[mimeType]; ok {
			return false
		}
	}

	// If accept filter is specified, only allow those MIME types
	if len(fm.AcceptMime) > 0 {
		if _, ok := fm.AcceptMime[mimeType]; !ok {
			return false
		}
	}

	return true
}

// caseInsensitivePatterns returns case-insensitive versions of the patterns
func (fm *FilterManager) caseInsensitivePatterns(patterns []*regexp.Regexp) []*regexp.Regexp {
	if len(patterns) == 0 {
		return nil
	}

	result := make([]*regexp.Regexp, 0, len(patterns))
	for _, re := range patterns {
		if re == nil {
			continue
		}
		result = append(result, fm.caseInsensitiveRegex(re))
	}

	return result
}

// caseInsensitiveRegex returns a case-insensitive version of the regex
func (fm *FilterManager) caseInsensitiveRegex(re *regexp.Regexp) *regexp.Regexp {
	fm.ciRegexCacheMu.RLock()
	if fm.ciRegexCache != nil {
		if cached, ok := fm.ciRegexCache[re]; ok {
			fm.ciRegexCacheMu.RUnlock()
			return cached
		}
	}
	fm.ciRegexCacheMu.RUnlock()

	ciRe, err := regexp.Compile("(?i)" + re.String())
	if err != nil {
		return re
	}

	fm.ciRegexCacheMu.Lock()
	if fm.ciRegexCache == nil {
		fm.ciRegexCache = make(map[*regexp.Regexp]*regexp.Regexp)
	}
	fm.ciRegexCache[re] = ciRe
	fm.ciRegexCacheMu.Unlock()

	return ciRe
}

// matchAnyRegex checks if any pattern matches the value
func matchAnyRegex(patterns []*regexp.Regexp, value string) bool {
	for _, re := range patterns {
		if re.MatchString(value) {
			return true
		}
	}
	return false
}
