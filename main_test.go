package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseExtensionArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected map[string]struct{}
	}{
		{
			name:     "empty args",
			args:     []string{},
			expected: map[string]struct{}{},
		},
		{
			name:     "single extension",
			args:     []string{".pdf"},
			expected: map[string]struct{}{"pdf": {}},
		},
		{
			name:     "multiple extensions",
			args:     []string{".pdf", ".txt", ".jpg"},
			expected: map[string]struct{}{"pdf": {}, "txt": {}, "jpg": {}},
		},
		{
			name:     "comma-separated extensions",
			args:     []string{".pdf,.txt,.jpg"},
			expected: map[string]struct{}{"pdf": {}, "txt": {}, "jpg": {}},
		},
		{
			name:     "mixed args",
			args:     []string{".pdf", ".txt,.jpg", ".png"},
			expected: map[string]struct{}{"pdf": {}, "txt": {}, "jpg": {}, "png": {}},
		},
		{
			name:     "args with whitespace",
			args:     []string{".pdf , .txt , .jpg"},
			expected: map[string]struct{}{"pdf": {}, "txt": {}, "jpg": {}},
		},
		{
			name:     "empty strings ignored",
			args:     []string{".pdf", "", ".txt"},
			expected: map[string]struct{}{"pdf": {}, "txt": {}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseExtensionArgs(tt.args)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseMimeArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected int // number of MIME types
	}{
		{
			name:     "empty args",
			args:     []string{},
			expected: 0,
		},
		{
			name:     "single mime type",
			args:     []string{"text/html"},
			expected: 1,
		},
		{
			name:     "multiple mime types",
			args:     []string{"text/html", "application/pdf", "image/jpeg"},
			expected: 3,
		},
		{
			name:     "comma-separated mime types",
			args:     []string{"text/html,application/pdf,image/jpeg"},
			expected: 3,
		},
		{
			name:     "mixed args",
			args:     []string{"text/html", "application/pdf,image/jpeg", "text/css"},
			expected: 4,
		},
		{
			name:     "args with whitespace",
			args:     []string{"text/html , application/pdf , image/jpeg"},
			expected: 3,
		},
		{
			name:     "empty strings ignored",
			args:     []string{"text/html", "", "application/pdf"},
			expected: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseMimeArgs(tt.args)
			assert.Len(t, result, tt.expected)
		})
	}
}

func TestParseGlobArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected int // number of patterns compiled
	}{
		{
			name:     "empty args",
			args:     []string{},
			expected: 0,
		},
		{
			name:     "single pattern",
			args:     []string{"*.pdf"},
			expected: 1,
		},
		{
			name:     "multiple patterns",
			args:     []string{"*.pdf", "*.txt", "*.jpg"},
			expected: 3,
		},
		{
			name:     "comma-separated patterns",
			args:     []string{"*.pdf,*.txt,*.jpg"},
			expected: 3,
		},
		{
			name:     "mixed args",
			args:     []string{"*.pdf", "*.txt,*.jpg", "*.png"},
			expected: 4,
		},
		{
			name:     "args with whitespace",
			args:     []string{"*.pdf , *.txt , *.jpg"},
			expected: 3,
		},
		{
			name:     "empty strings ignored",
			args:     []string{"*.pdf", "", "*.txt"},
			expected: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseGlobArgs(tt.args)
			assert.Len(t, result, tt.expected)
		})
	}
}

func TestCompilePathPatterns(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		expected int // number of compiled patterns
		wantErr  bool
	}{
		{
			name:     "empty patterns",
			patterns: []string{},
			expected: 0,
			wantErr:  false,
		},
		{
			name:     "valid patterns",
			patterns: []string{"*.pdf", "/path/*", "**/*.txt"},
			expected: 3,
			wantErr:  false,
		},
		{
			name:     "patterns with empty strings",
			patterns: []string{"*.pdf", "", "*.txt"},
			expected: 2,
			wantErr:  false,
		},
		{
			name:     "invalid regex pattern",
			patterns: []string{"[invalid"},
			expected: 1, // It still compiles, just might not work as expected
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := compilePathPatterns(tt.patterns)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, result, tt.expected)
			}
		})
	}
}

func TestCompilePathPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{
			name:    "valid simple pattern",
			pattern: "*.pdf",
			wantErr: false,
		},
		{
			name:    "valid complex pattern",
			pattern: "/path/**/*.txt",
			wantErr: false,
		},
		{
			name:    "valid pattern with anchors",
			pattern: "^/static/.*$",
			wantErr: false,
		},
		{
			name:    "invalid regex pattern",
			pattern: "[invalid",
			wantErr: false, // The function doesn't validate regex, it just wraps it
		},
		{
			name:    "empty pattern",
			pattern: "",
			wantErr: false, // The function doesn't validate empty patterns
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := compilePathPattern(tt.pattern)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

func TestGlobToRegex(t *testing.T) {
	tests := []struct {
		name     string
		glob     string
		expected string
	}{
		{
			name:     "simple wildcard",
			glob:     "*.pdf",
			expected: "^[^/]*\\.pdf$",
		},
		{
			name:     "path with wildcard",
			glob:     "/path/*.txt",
			expected: "^/path/[^/]*\\.txt$",
		},
		{
			name:     "multiple wildcards",
			glob:     "*/*.pdf",
			expected: "^[^/]*/[^/]*\\.pdf$",
		},
		{
			name:     "no wildcards",
			glob:     "file.txt",
			expected: "^file\\.txt$",
		},
		{
			name:     "empty string",
			glob:     "",
			expected: "^$",
		},
		{
			name:     "complex pattern",
			glob:     "/static/**/*.css",
			expected: "^/static/.*/[^/]*\\.css$",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := globToRegex(tt.glob)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSplitAndTrim(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "empty slice",
			input:    []string{},
			expected: nil,
		},
		{
			name:     "single item",
			input:    []string{"item1"},
			expected: []string{"item1"},
		},
		{
			name:     "comma separated",
			input:    []string{"item1,item2,item3"},
			expected: []string{"item1", "item2", "item3"},
		},
		{
			name:     "comma separated with spaces",
			input:    []string{"item1, item2 , item3"},
			expected: []string{"item1", "item2", "item3"},
		},
		{
			name:     "multiple commas",
			input:    []string{"item1,,,item2"},
			expected: []string{"item1", "item2"},
		},
		{
			name:     "leading/trailing commas",
			input:    []string{",item1,item2,"},
			expected: []string{"item1", "item2"},
		},
		{
			name:     "only commas",
			input:    []string{",,"},
			expected: nil,
		},
		{
			name:     "multiple strings",
			input:    []string{"item1,item2", "item3,item4"},
			expected: []string{"item1", "item2", "item3", "item4"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitAndTrim(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
