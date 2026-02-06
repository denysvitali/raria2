package raria2

import (
	"os"
	"path/filepath"
	"testing"
)

func TestURLCacheMarkVisited(t *testing.T) {
	cache := NewURLCache("")
	if !cache.MarkVisited("https://example.com/a") {
		t.Fatalf("first visit should return true")
	}
	if cache.MarkVisited("https://example.com/a") {
		t.Fatalf("second visit should return false")
	}
}

func TestURLCacheLoadPopulatesEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "visited.txt")
	content := "# comment\n\nhttps://example.com/a\nhttps://example.com/b\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed writing fixture: %v", err)
	}

	cache := NewURLCache(path)
	if err := cache.Load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if !cache.MarkVisited("https://example.com/c") {
		t.Fatalf("expected new entry to be marked")
	}
	if cache.MarkVisited("https://example.com/a") {
		t.Fatalf("expected existing entry to return false")
	}
}

func TestURLCacheLoadMissingFile(t *testing.T) {
	cache := NewURLCache(filepath.Join(t.TempDir(), "missing.txt"))
	if err := cache.Load(); err != nil {
		t.Fatalf("load should ignore missing file: %v", err)
	}
}

func TestURLCacheSaveCreatesDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "visited.txt")
	cache := NewURLCache(path)
	cache.MarkVisited("https://example.com/z")
	cache.MarkVisited("https://example.com/a")

	if err := cache.Save(); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read saved cache: %v", err)
	}
	contents := string(data)
	if contents != "https://example.com/a\nhttps://example.com/z\n" {
		t.Fatalf("unexpected file contents: %q", contents)
	}
}

func TestDirPath(t *testing.T) {
	cases := map[string]string{
		"file.txt":            ".",
		"dir/file.txt":        "dir",
		"nested/dir/file.txt": filepath.FromSlash("nested/dir"),
		"":                    ".",
		"single/":             "single",
	}
	if os.PathSeparator == '\\' {
		cases[`dir\\file.txt`] = "dir"
		cases[`nested\\dir\\file.txt`] = filepath.FromSlash("nested/dir")
	}

	for input, expected := range cases {
		if got := filepath.Dir(input); got != expected {
			t.Fatalf("filepath.Dir(%q) = %q, want %q", input, got, expected)
		}
	}
}
