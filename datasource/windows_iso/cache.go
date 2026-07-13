package windows_iso

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// cacheEntry is the on-disk representation of a previously resolved result.
type cacheEntry struct {
	URL       string    `json:"url"`
	FileName  string    `json:"file_name"`
	FetchedAt time.Time `json:"fetched_at"`
}

// cacheKey deterministically identifies a Config by the fields that affect
// which SKU is resolved. CacheTTL itself is deliberately excluded so that
// changing it doesn't fragment the cache.
func cacheKey(cfg Config) string {
	h := sha256.Sum256(fmt.Appendf(nil, "%s|%s|%s|%s|%s|%s",
		cfg.Version, cfg.Release, cfg.Edition, cfg.Language, cfg.Arch, cfg.Locale))
	return hex.EncodeToString(h[:])
}

func cacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "packer-plugin-windows-utils", "windows-iso")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func cachePath(cfg Config) (string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, cacheKey(cfg)+".json"), nil
}

// loadCache returns the cached result for cfg if one exists and is younger
// than cfg.CacheTTL. Any miss (absent, unreadable, or expired) is silent —
// callers fall back to a live fetch.
func loadCache(cfg Config) (*Result, bool) {
	path, err := cachePath(cfg)
	if err != nil {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false
	}
	if time.Since(entry.FetchedAt) > cfg.CacheTTL {
		return nil, false
	}
	return &Result{URL: entry.URL, FileName: entry.FileName}, true
}

// saveCache persists result for cfg. Failures are silent — caching is a
// best-effort optimization, not something that should fail the build.
func saveCache(cfg Config, result *Result) {
	path, err := cachePath(cfg)
	if err != nil {
		return
	}
	data, err := json.MarshalIndent(cacheEntry{
		URL:       result.URL,
		FileName:  result.FileName,
		FetchedAt: time.Now(),
	}, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}
