package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// cacheRetentionPeriod is how long cache files are kept before cleanup.
	cacheRetentionPeriod = 30 * 24 * time.Hour // 30 days
	// cacheDirPerms is the permission for cache directories.
	cacheDirPerms = 0o700
	// cacheFilePerms is the permission for cache files.
	cacheFilePerms = 0o600
)

// Recommended TTLs for different data types
const (
	// TTLCurrentPR should NOT be used for the PR being examined - fetch it fresh every time

	// TTLWorkload is for user PR counts (changes frequently)
	TTLWorkload = 2 * time.Hour

	// TTLCollaborators is for repo collaborator lists (changes occasionally)
	TTLCollaborators = 6 * time.Hour

	// TTLRecentActivity is for recent merged PRs, directory activity (changes daily)
	TTLRecentActivity = 4 * time.Hour

	// TTLHistoricalPR is for merged PR data used in overlap/history analysis (immutable once merged)
	TTLHistoricalPR = 28 * 24 * time.Hour // 28 days

	// TTLFileHistory is for file history queries (changes slowly)
	TTLFileHistory = 3 * 24 * time.Hour // 3 days

	// TTLUserDetails is for user profile data (changes very rarely)
	TTLUserDetails = 7 * 24 * time.Hour // 7 days
)

// diskEntry represents a cache entry on disk with TTL.
type diskEntry struct {
	Value      json.RawMessage `json:"value"`
	Expiration time.Time       `json:"expiration"`
	CachedAt   time.Time       `json:"cached_at"`
}

// DiskCache provides two-tier caching: in-memory + disk persistence.
type DiskCache struct {
	*Cache // Embedded in-memory cache

	cacheDir string
	enabled  bool
}

// NewDiskCache creates a new cache with disk persistence.
// If cacheDir is empty, falls back to memory-only cache.
func NewDiskCache(ttl time.Duration, cacheDir string) (*DiskCache, error) {
	memCache := New(ttl)

	dc := &DiskCache{
		Cache:    memCache,
		cacheDir: cacheDir,
		enabled:  cacheDir != "",
	}

	if dc.enabled {
		cleanPath := filepath.Clean(cacheDir)
		if !filepath.IsAbs(cleanPath) {
			return nil, errors.New("cache directory must be absolute path")
		}

		if err := os.MkdirAll(cleanPath, cacheDirPerms); err != nil {
			slog.Warn("Failed to create cache directory, falling back to memory-only", "error", err, "path", cleanPath)
			dc.enabled = false
		} else {
			dc.cacheDir = cleanPath
			// Schedule cleanup in background
			go dc.cleanOldCaches()
		}
	}

	return dc, nil
}

// CacheHitType indicates where a cache value was found.
type CacheHitType string

const (
	CacheHitMemory CacheHitType = "memory"
	CacheHitDisk   CacheHitType = "disk"
	CacheMiss      CacheHitType = "miss"
)

// Get retrieves a value from cache (memory first, then disk).
func (c *DiskCache) Get(key string) (any, bool) {
	value, hitType := c.Lookup(key)
	return value, hitType != CacheMiss
}

// Lookup retrieves a value from cache and indicates where it was found.
func (c *DiskCache) Lookup(key string) (any, CacheHitType) {
	// Try memory cache first
	if value, found := c.Cache.Get(key); found {
		return value, CacheHitMemory
	}

	// Try disk cache if enabled
	if !c.enabled {
		slog.Debug("Disk cache disabled, returning miss", "key", key)
		return nil, CacheMiss
	}

	cacheFile := filepath.Join(c.cacheDir, c.cacheKey(key)+".json")
	slog.Debug("Checking disk cache", "key", key, "file", cacheFile)

	var entry diskEntry
	if !c.loadFromDisk(key, &entry) {
		slog.Debug("Disk cache file not found or unreadable", "key", key)
		return nil, CacheMiss
	}

	// Check expiration
	if time.Now().After(entry.Expiration) {
		slog.Debug("Disk cache entry expired", "key", key, "expired_at", entry.Expiration)
		c.removeFromDisk(key)
		return nil, CacheMiss
	}

	// Unmarshal the value (it was stored as JSON)
	var value any
	if err := json.Unmarshal(entry.Value, &value); err != nil {
		slog.Warn("Failed to unmarshal disk cache entry", "key", key, "error", err)
		c.removeFromDisk(key)
		return nil, CacheMiss
	}

	slog.Debug("Disk cache hit", "key", key, "cached_at", entry.CachedAt, "ttl_remaining", time.Until(entry.Expiration))

	// Restore to memory cache
	ttl := time.Until(entry.Expiration)
	if ttl > 0 {
		c.Cache.SetWithTTL(key, value, ttl)
	}

	return value, CacheHitDisk
}

// SetWithTTL stores a value in both memory and disk cache.
func (c *DiskCache) SetWithTTL(key string, value any, ttl time.Duration) {
	// Always store in memory
	c.Cache.SetWithTTL(key, value, ttl)

	// Store on disk if enabled
	if !c.enabled {
		slog.Debug("Disk cache disabled, skipping disk write", "key", key)
		return
	}

	slog.Debug("Writing to disk cache", "key", key, "ttl", ttl, "cache_dir", c.cacheDir)

	// Marshal value to JSON for disk storage
	valueJSON, err := json.Marshal(value)
	if err != nil {
		slog.Debug("Failed to marshal value for disk cache", "key", key, "error", err)
		return
	}

	entry := diskEntry{
		Value:      valueJSON,
		Expiration: time.Now().Add(ttl),
		CachedAt:   time.Now(),
	}

	if err := c.saveToDisk(key, entry); err != nil {
		slog.Debug("Failed to save to disk cache", "key", key, "error", err)
	} else {
		cacheFile := filepath.Join(c.cacheDir, c.cacheKey(key)+".json")
		slog.Debug("Disk cache write successful", "key", key, "file", cacheFile)
	}
}

// cacheKey generates a SHA256 hash of the key for the filename.
func (c *DiskCache) cacheKey(key string) string {
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}

// loadFromDisk loads a cache entry from disk.
func (c *DiskCache) loadFromDisk(key string, v any) bool {
	path := filepath.Join(c.cacheDir, c.cacheKey(key)+".json")

	file, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Debug("Failed to open disk cache file", "error", err, "path", path)
		}
		return false
	}
	defer func() {
		if err := file.Close(); err != nil {
			slog.Debug("Failed to close disk cache file", "error", err, "path", path)
		}
	}()

	if err := json.NewDecoder(file).Decode(v); err != nil {
		slog.Debug("Failed to decode disk cache file", "error", err, "path", path)
		return false
	}

	return true
}

// saveToDisk saves a cache entry to disk atomically.
func (c *DiskCache) saveToDisk(key string, v any) error {
	filename := c.cacheKey(key) + ".json"
	path := filepath.Join(c.cacheDir, filename)
	tmpPath := path + ".tmp"

	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, cacheFilePerms)
	if err != nil {
		return fmt.Errorf("creating cache file: %w", err)
	}

	encoder := json.NewEncoder(file)
	if err := encoder.Encode(v); err != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("encoding cache data: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing cache file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming cache file: %w", err)
	}

	return nil
}

// removeFromDisk removes a cache entry from disk.
func (c *DiskCache) removeFromDisk(key string) {
	path := filepath.Join(c.cacheDir, c.cacheKey(key)+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Debug("Failed to remove disk cache file", "error", err, "path", path)
	}
}

// cleanOldCaches periodically removes expired cache files.
func (c *DiskCache) cleanOldCaches() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		entries, err := os.ReadDir(c.cacheDir)
		if err != nil {
			slog.Error("Failed to read cache directory", "error", err)
			continue
		}

		cutoff := time.Now().Add(-cacheRetentionPeriod)
		removed := 0

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}

			info, err := entry.Info()
			if err != nil {
				continue
			}

			if info.ModTime().Before(cutoff) {
				path := filepath.Join(c.cacheDir, entry.Name())
				if err := os.Remove(path); err != nil {
					slog.Debug("Failed to remove old cache file", "path", path, "error", err)
				} else {
					removed++
				}
			}
		}

		if removed > 0 {
			slog.Info("Cleaned old cache files", "removed", removed)
		}
	}
}
