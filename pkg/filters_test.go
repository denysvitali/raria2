package raria2

import (
	"net/url"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterManager_PathAllowedAllowsBasePathWhenAcceptsDefined(t *testing.T) {
	base, err := url.Parse("https://example.com/root/")
	require.NoError(t, err)
	fm := NewFilterManager(base)
	fm.AcceptPathRegex = []*regexp.Regexp{regexp.MustCompile(`^/root/files/`)}

	assert.True(t, fm.PathAllowed(mustParseURL(t, "https://example.com/root/")))
	assert.True(t, fm.PathAllowed(mustParseURL(t, "https://example.com/root/files/doc.bin")))
	assert.False(t, fm.PathAllowed(mustParseURL(t, "https://example.com/other/doc.bin")))
}

func TestFilterManager_PathAllowedCaseInsensitiveCaching(t *testing.T) {
	base, err := url.Parse("https://example.com/root/")
	require.NoError(t, err)
	fm := NewFilterManager(base)
	fm.CaseInsensitivePaths = true
	pattern := regexp.MustCompile(`^/root/files/`)
	fm.AcceptPathRegex = []*regexp.Regexp{pattern}

	assert.True(t, fm.PathAllowed(mustParseURL(t, "https://example.com/ROOT/FILES/file.bin")))

	ciFirst := fm.caseInsensitiveRegex(pattern)
	ciSecond := fm.caseInsensitiveRegex(pattern)
	assert.Same(t, ciFirst, ciSecond)
}

func TestFilterManager_PathAllowedRejectsTakePrecedence(t *testing.T) {
	base, err := url.Parse("https://example.com/root/")
	require.NoError(t, err)
	fm := NewFilterManager(base)
	fm.AcceptPathRegex = []*regexp.Regexp{regexp.MustCompile(`^/root/.*`)}
	fm.RejectPathRegex = []*regexp.Regexp{regexp.MustCompile(`^/root/private/.*`)}

	assert.False(t, fm.PathAllowed(mustParseURL(t, "https://example.com/root/private/doc.bin")))
	assert.True(t, fm.PathAllowed(mustParseURL(t, "https://example.com/root/public/doc.bin")))
}
