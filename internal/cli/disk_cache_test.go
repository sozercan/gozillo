package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type cacheFixture struct {
	Name string `json:"name"`
}

func TestDiskCacheRoundTripAndExpiry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 20, 1, 0, 0, 0, time.UTC)
	cache := &diskCache{Dir: filepath.Join(t.TempDir(), "cache"), TTL: time.Hour, Now: func() time.Time { return now }}
	if err := cache.Save("key", cacheFixture{Name: "value"}); err != nil {
		t.Fatal(err)
	}
	var got cacheFixture
	hit, err := cache.Load("key", &got)
	if err != nil || !hit || got.Name != "value" {
		t.Fatalf("Load() = (%+v, %t, %v)", got, hit, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(cache.path("key"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("cache mode = %04o", info.Mode().Perm())
		}
	}
	now = now.Add(2 * time.Hour)
	hit, err = cache.Load("key", &got)
	if err != nil || hit {
		t.Fatalf("expired Load() = (%t, %v)", hit, err)
	}
}

func TestDefaultDiskCacheUsesGozilloConfigDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission behavior differs on Windows")
	}
	base := t.TempDir()
	t.Setenv("GOZILLO_CONFIG_DIR", base)
	cache, err := defaultDiskCache("search", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if cache.Dir != filepath.Join(base, "cache", "search") {
		t.Fatalf("Dir = %q", cache.Dir)
	}
}
