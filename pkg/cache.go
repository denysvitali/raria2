package raria2

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// URLCache manages visited URLs for crawl resumption
type URLCache struct {
	cache   map[string]struct{}
	cacheMu sync.Mutex
	path    string
}

// NewURLCache creates a new URL cache
func NewURLCache(path string) *URLCache {
	return &URLCache{
		cache: make(map[string]struct{}),
		path:  path,
	}
}

// MarkVisited marks a URL as visited and returns true if it wasn't visited before
func (uc *URLCache) MarkVisited(u string) bool {
	key := canonicalURL(u)
	uc.cacheMu.Lock()
	defer uc.cacheMu.Unlock()
	if _, exists := uc.cache[key]; exists {
		return false
	}
	uc.cache[key] = struct{}{}
	return true
}

// Load loads the visited URLs from disk
func (uc *URLCache) Load() error {
	if uc.path == "" {
		return nil
	}

	file, err := os.Open(uc.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer closeQuietly(file)

	scanner := bufio.NewScanner(file)
	var entries []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries = append(entries, line)
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	uc.cacheMu.Lock()
	for _, entry := range entries {
		uc.cache[canonicalURL(entry)] = struct{}{}
	}
	uc.cacheMu.Unlock()

	return nil
}

// Save saves the visited URLs to disk
func (uc *URLCache) Save() error {
	if uc.path == "" {
		return nil
	}

	uc.cacheMu.Lock()
	keys := make([]string, 0, len(uc.cache))
	for k := range uc.cache {
		keys = append(keys, k)
	}
	uc.cacheMu.Unlock()
	sort.Strings(keys)

	dir := filepath.Dir(uc.path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	tmpPath := uc.path + ".tmp"
	file, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	writer := bufio.NewWriter(file)
	for _, key := range keys {
		if _, err := fmt.Fprintln(writer, key); err != nil {
			closeQuietly(file)
			_ = os.Remove(tmpPath)
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		closeQuietly(file)
		_ = os.Remove(tmpPath)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	return os.Rename(tmpPath, uc.path)
}
