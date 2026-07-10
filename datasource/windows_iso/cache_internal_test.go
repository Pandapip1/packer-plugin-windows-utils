package windows_iso

import (
	"os"
	"testing"
	"time"
)

func TestCacheRoundTrip(t *testing.T) {
	cfg := Config{
		Version:  "Windows 10",
		Release:  "Latest",
		Edition:  "Home/Pro/Edu",
		Language: "English International",
		Arch:     "x64",
		CacheTTL: time.Hour,
	}

	if _, ok := loadCache(cfg); ok {
		t.Fatalf("expected cache miss before any save")
	}

	want := &Result{URL: "https://example.invalid/test.iso", FileName: "test.iso"}
	saveCache(cfg, want)

	got, ok := loadCache(cfg)
	if !ok {
		t.Fatalf("expected cache hit after save")
	}
	if *got != *want {
		t.Fatalf("cache round-trip mismatch: got %+v, want %+v", got, want)
	}

	// A different Config (different Edition) must not share the cache entry.
	other := cfg
	other.Edition = "Home China"
	if _, ok := loadCache(other); ok {
		t.Fatalf("expected cache miss for a different config")
	}

	// An expired entry (TTL 0) must not be returned.
	expired := cfg
	expired.CacheTTL = 0
	if _, ok := loadCache(expired); ok {
		t.Fatalf("expected cache miss for an expired TTL")
	}

	path, err := cachePath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(path) })
}
