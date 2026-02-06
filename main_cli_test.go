package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIFlagsCompatibility(t *testing.T) {
	cmd, opts := newRootCommand()
	cmd.SetArgs([]string{
		"-d",
		"-o", "output",
		"-x", "9",
		"-j", "8",
		"-w", "7",
		"--max-depth", "2",
		"--http-timeout", "45s",
		"--accept", "pdf,jpg",
		"--accept", "iso",
		"--reject-mime", "application/zip",
		"https://example.com/root/",
		"--",
		"--max-download-limit=1M",
		"--split=8",
	})

	err := cmd.Execute()
	require.NoError(t, err)

	assert.True(t, opts.DryRun)
	assert.Equal(t, "output", opts.Output)
	assert.Equal(t, 9, opts.MaxConnectionPerServer)
	assert.Equal(t, 8, opts.MaxConcurrentDownload)
	assert.Equal(t, 7, opts.Threads)
	assert.Equal(t, 2, opts.MaxDepth)
	assert.Equal(t, 45*time.Second, opts.HTTPTimeout)
	assert.Equal(t, []string{"pdf", "jpg", "iso"}, opts.AcceptExtensions)
	assert.Equal(t, []string{"application/zip"}, opts.RejectMime)
	assert.Equal(t, "https://example.com/root/", opts.URL)
	assert.Equal(t, []string{"--max-download-limit=1M", "--split=8"}, opts.Aria2Args)
}

func TestCLIRequiresURL(t *testing.T) {
	cmd, _ := newRootCommand()
	cmd.SetArgs([]string{"--dry-run"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "please provide an URL")
}
