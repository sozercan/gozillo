package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const diskCacheVersion = 1

type diskCache struct {
	Dir string
	TTL time.Duration
	Now func() time.Time
}

type diskCacheEntry struct {
	Version  int             `json:"version"`
	StoredAt time.Time       `json:"storedAt"`
	Key      string          `json:"key"`
	Value    json.RawMessage `json:"value"`
}

func defaultDiskCache(namespace string, ttl time.Duration) (*diskCache, error) {
	if ttl <= 0 {
		return nil, nil
	}
	if strings.TrimSpace(namespace) == "" || strings.ContainsAny(namespace, `/\\`) {
		return nil, errors.New("cache namespace is invalid")
	}
	base := strings.TrimSpace(os.Getenv("GOZILLO_CACHE_DIR"))
	if base != "" {
		return &diskCache{Dir: filepath.Join(base, namespace), TTL: ttl, Now: time.Now}, nil
	}
	base = strings.TrimSpace(os.Getenv("GOZILLO_CONFIG_DIR"))
	if base == "" {
		var err error
		base, err = os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("resolve cache directory: %w", err)
		}
		base = filepath.Join(base, "gozillo")
	}
	return &diskCache{Dir: filepath.Join(base, "cache", namespace), TTL: ttl, Now: time.Now}, nil
}

func (cache *diskCache) Load(key string, destination any) (bool, error) {
	if cache == nil || cache.TTL <= 0 {
		return false, nil
	}
	if strings.TrimSpace(key) == "" || destination == nil {
		return false, errors.New("cache load requires a key and destination")
	}
	path := cache.path(key)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat cache entry: %w", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return false, fmt.Errorf("cache entry permissions are too broad (%04o)", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read cache entry: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var entry diskCacheEntry
	if err := decoder.Decode(&entry); err != nil {
		return false, fmt.Errorf("decode cache entry: %w", err)
	}
	if entry.Version != diskCacheVersion || entry.Key != key || entry.StoredAt.IsZero() {
		return false, nil
	}
	now := cache.now()
	if entry.StoredAt.After(now.Add(5*time.Minute)) || now.Sub(entry.StoredAt) > cache.TTL {
		return false, nil
	}
	if err := json.Unmarshal(entry.Value, destination); err != nil {
		return false, fmt.Errorf("decode cached value: %w", err)
	}
	return true, nil
}

func (cache *diskCache) Save(key string, value any) error {
	if cache == nil || cache.TTL <= 0 {
		return nil
	}
	if strings.TrimSpace(key) == "" || value == nil {
		return errors.New("cache save requires a key and value")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode cached value: %w", err)
	}
	entry := diskCacheEntry{
		Version:  diskCacheVersion,
		StoredAt: cache.now().UTC(),
		Key:      key,
		Value:    payload,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode cache entry: %w", err)
	}
	if err := os.MkdirAll(cache.Dir, 0o700); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(cache.Dir, 0o700); err != nil {
			return fmt.Errorf("restrict cache directory: %w", err)
		}
	}
	temporary, err := os.CreateTemp(cache.Dir, ".gozillo-cache-*")
	if err != nil {
		return fmt.Errorf("create cache temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict cache file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write cache entry: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync cache entry: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close cache entry: %w", err)
	}
	if err := os.Rename(temporaryPath, cache.path(key)); err != nil {
		return fmt.Errorf("replace cache entry: %w", err)
	}
	return nil
}

func (cache *diskCache) path(key string) string {
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(cache.Dir, hex.EncodeToString(digest[:])+".json")
}

func (cache *diskCache) now() time.Time {
	if cache != nil && cache.Now != nil {
		return cache.Now()
	}
	return time.Now()
}
