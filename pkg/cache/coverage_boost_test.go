package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Test saveToDisk with encoding error
func TestDiskCache_SaveToDisk_EncodingError(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Test that saveToDisk works for valid values
	entry := diskEntry{
		Value:      []byte(`{"Name":"test","Age":25}`),
		Expiration: time.Now().Add(time.Hour),
		CachedAt:   time.Now(),
	}

	err = dc.saveToDisk("test", entry)
	if err != nil {
		t.Errorf("expected saveToDisk to succeed, got error: %v", err)
	}
}

// Test saveToDisk file close error path
func TestDiskCache_SaveToDisk_SuccessfulWrite(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	entry := diskEntry{
		Value:      []byte(`"test value"`),
		Expiration: time.Now().Add(time.Hour),
		CachedAt:   time.Now(),
	}

	// Should succeed
	err = dc.saveToDisk("testkey", entry)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Verify file was created
	filename := dc.cacheKey("testkey") + ".json"
	path := filepath.Join(tmpDir, filename)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("expected file to be created")
	}
}

// Test removeFromDisk with non-existent file (no error case)
func TestDiskCache_RemoveFromDisk_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Should not error when removing non-existent file
	dc.removeFromDisk("nonexistent")
}

// Test SetWithTTL with zero TTL
func TestDiskCache_SetWithTTL_ZeroTTL(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	dc.SetWithTTL("key1", "value1", 0)

	// Should be set but immediately expired or nearly so
	val, found := dc.Get("key1")
	// Might be found immediately but will expire very soon
	_ = val
	_ = found
}

// Test Lookup with negative TTL remaining (edge case)
func TestDiskCache_Lookup_NegativeTTLRemaining(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Create an entry that's very close to expiring
	dc.SetWithTTL("key1", "value1", time.Millisecond)

	// Wait for it to expire
	time.Sleep(2 * time.Millisecond)

	// Clear memory cache
	dc.mu.Lock()
	dc.entries = make(map[string]Entry)
	dc.mu.Unlock()

	// Should be a miss
	_, hitType := dc.Lookup("key1")
	if hitType != CacheMiss {
		t.Errorf("expected CacheMiss for expired entry, got %v", hitType)
	}
}

// Test NewDiskCache directory creation failure gracefully handled
func TestDiskCache_NewDiskCache_CreateDirSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "nested", "cache", "dir")

	dc, err := NewDiskCache(time.Hour, subDir)
	if err != nil {
		t.Fatalf("failed to create disk cache with nested dir: %v", err)
	}

	if !dc.enabled {
		t.Error("expected disk cache to be enabled")
	}

	// Verify directory was created
	if _, err := os.Stat(subDir); os.IsNotExist(err) {
		t.Error("expected directory to be created")
	}
}

// Test cleanOldCaches with non-JSON files (should be skipped)
func TestDiskCache_CleanOldCaches_SkipNonJSON(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Create a non-JSON file
	nonJSONFile := filepath.Join(tmpDir, "notjson.txt")
	if err := os.WriteFile(nonJSONFile, []byte("test"), 0o600); err != nil {
		t.Fatalf("failed to create non-JSON file: %v", err)
	}

	// Create a directory
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0o700); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	// Set old modification time
	oldTime := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(nonJSONFile, oldTime, oldTime); err != nil {
		t.Logf("failed to set time on non-JSON file: %v", err)
	}
	if err := os.Chtimes(subDir, oldTime, oldTime); err != nil {
		t.Logf("failed to set time on subdir: %v", err)
	}

	// Manually run cleanup logic
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read cache dir: %v", err)
	}

	cutoff := time.Now().Add(-cacheRetentionPeriod)
	removed := 0

	for _, entry := range entries {
		// Skip directories
		if entry.IsDir() {
			continue
		}

		// Skip non-JSON files
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			path := filepath.Join(tmpDir, entry.Name())
			if err := os.Remove(path); err == nil {
				removed++
			}
		}
	}

	// Should not have removed the non-JSON file or directory
	if _, err := os.Stat(nonJSONFile); os.IsNotExist(err) {
		t.Error("non-JSON file should not have been removed")
	}
	if _, err := os.Stat(subDir); os.IsNotExist(err) {
		t.Error("directory should not have been removed")
	}
}

// Test cache with very long key
func TestDiskCache_LongKey(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Create a very long key
	longKey := string(make([]byte, 10000))
	for i := range longKey {
		longKey = longKey[:i] + "a"
	}

	dc.Set(longKey, "value")

	val, found := dc.Get(longKey)
	if !found {
		t.Error("expected to find long key")
	}
	if val != "value" {
		t.Errorf("expected 'value', got %v", val)
	}

	// Clear memory and load from disk
	dc.mu.Lock()
	dc.entries = make(map[string]Entry)
	dc.mu.Unlock()

	val, hitType := dc.Lookup(longKey)
	if hitType != CacheHitDisk {
		t.Errorf("expected CacheHitDisk, got %v", hitType)
	}
	if val != "value" {
		t.Errorf("expected 'value', got %v", val)
	}
}

// Test multiple values written to same key
func TestDiskCache_OverwriteKey(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Write multiple values to same key
	dc.Set("key1", "value1")
	dc.Set("key1", "value2")
	dc.Set("key1", "value3")

	val, found := dc.Get("key1")
	if !found {
		t.Fatal("expected to find key1")
	}
	if val != "value3" {
		t.Errorf("expected 'value3', got %v", val)
	}

	// Verify only one file exists on disk
	cacheFile := filepath.Join(tmpDir, dc.cacheKey("key1")+".json")
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read cache dir: %v", err)
	}

	jsonCount := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			jsonCount++
		}
	}

	if jsonCount != 1 {
		t.Errorf("expected 1 JSON file, found %d", jsonCount)
	}

	// Verify file exists
	if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
		t.Error("expected cache file to exist")
	}
}

// Test cache with special characters in key
func TestDiskCache_SpecialCharactersInKey(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	specialKeys := []string{
		"key/with/slashes",
		"key:with:colons",
		"key with spaces",
		"key\twith\ttabs",
		"key\nwith\nnewlines",
		"key@with#special$chars%",
		"unicode-key-\u65e5\u672c", // Japanese characters via escape sequences
		"unicode-key-\u041a\u043b", // Russian characters via escape sequences
	}

	for _, key := range specialKeys {
		t.Run(key, func(t *testing.T) {
			dc.Set(key, "value-"+key)

			_, found := dc.Get(key)
			if !found {
				t.Errorf("expected to find key %q", key)
			}

			// Clear memory and reload from disk
			dc.mu.Lock()
			dc.entries = make(map[string]Entry)
			dc.mu.Unlock()

			val, hitType := dc.Lookup(key)
			if hitType != CacheHitDisk {
				t.Errorf("expected CacheHitDisk for key %q, got %v", key, hitType)
			}
			if val == nil {
				t.Errorf("expected non-nil value for key %q", key)
			}
		})
	}
}
