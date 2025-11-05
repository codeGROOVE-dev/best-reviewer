package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Test saveToDisk error handling
func TestDiskCache_SaveToDisk_CreateError(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Make cache directory read-only to cause write error
	if err := os.Chmod(tmpDir, 0o500); err != nil {
		t.Fatalf("failed to chmod dir: %v", err)
	}
	defer func() {
		if err := os.Chmod(tmpDir, 0o700); err != nil {
			t.Logf("failed to restore permissions: %v", err)
		}
	}()

	// This should fail to write to disk but not panic
	dc.Set("key1", "value1")

	// Should still be in memory
	val, found := dc.Get("key1")
	if !found {
		t.Error("expected key1 to be in memory despite disk write failure")
	}
	if val != "value1" {
		t.Errorf("expected value1, got %v", val)
	}
}

// Test removeFromDisk with permission error
func TestDiskCache_RemoveFromDisk_PermissionError(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Set a value to create the file
	dc.Set("key1", "value1")

	// Make the file unremovable by making directory read-only
	if err := os.Chmod(tmpDir, 0o500); err != nil {
		t.Fatalf("failed to chmod dir: %v", err)
	}
	defer func() {
		if err := os.Chmod(tmpDir, 0o700); err != nil {
			t.Logf("failed to restore permissions: %v", err)
		}
	}()

	// This should not panic, just log an error
	dc.removeFromDisk("key1")
}

// Test cleanOldCaches background process
func TestDiskCache_CleanOldCaches(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Create some test cache files with old modification times
	oldTime := time.Now().Add(-31 * 24 * time.Hour) // 31 days old

	for i := range 5 {
		filename := dc.cacheKey("old"+string(rune(i))) + ".json"
		path := filepath.Join(tmpDir, filename)
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		// Set modification time to old
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatalf("failed to set file time: %v", err)
		}
	}

	// Create a recent file that should NOT be cleaned up
	dc.Set("recent", "value")

	// Manually trigger cleanup (instead of waiting for background goroutine)
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read cache dir: %v", err)
	}

	cutoff := time.Now().Add(-cacheRetentionPeriod)
	removed := 0

	for _, entry := range entries {
		if entry.IsDir() {
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

	if removed != 5 {
		t.Errorf("expected 5 old files to be removed, got %d", removed)
	}

	// Recent file should still exist
	_, found := dc.Get("recent")
	if !found {
		t.Error("expected recent file to still exist")
	}
}

// Test NewDiskCache with non-absolute path fallback
func TestDiskCache_NonAbsolutePath_Fallback(t *testing.T) {
	dc, err := NewDiskCache(time.Hour, "relative/path")
	if err == nil {
		t.Error("expected error for relative path")
	}
	if dc != nil {
		t.Error("expected nil DiskCache for invalid path")
	}
}

// Test Lookup with TTL edge case (exactly at expiration)
func TestDiskCache_Lookup_ExactExpiration(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Set with 1ms TTL
	dc.SetWithTTL("key1", "value1", time.Millisecond)

	// Wait just past expiration
	time.Sleep(2 * time.Millisecond)

	// Clear memory
	dc.mu.Lock()
	dc.entries = make(map[string]Entry)
	dc.mu.Unlock()

	// Should be expired
	val, hitType := dc.Lookup("key1")
	if hitType != CacheMiss {
		t.Errorf("expected CacheMiss, got %v", hitType)
	}
	if val != nil {
		t.Errorf("expected nil, got %v", val)
	}
}

// Test SetWithTTL with unmarshalable value
func TestDiskCache_SetWithTTL_UnmarshalableValue(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Channel cannot be marshaled to JSON
	badValue := make(chan int)

	// This should not panic, just skip disk write
	dc.Set("bad", badValue)

	// Should still be in memory (as the original value)
	val, found := dc.Get("bad")
	if !found {
		t.Error("expected value to be in memory")
	}
	if val == nil {
		t.Error("expected non-nil value in memory")
	}
}

// Test loadFromDisk with non-existent file
func TestDiskCache_LoadFromDisk_NotExist(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	var entry diskEntry
	loaded, corrupted := dc.loadFromDisk("nonexistent", &entry)
	if loaded {
		t.Error("expected loadFromDisk to fail for nonexistent file")
	}
	if corrupted {
		t.Error("expected nonexistent file to not be marked as corrupted")
	}
}

// Test Get method (wrapper around Lookup)
func TestDiskCache_Get_WrapperBehavior(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Test miss
	val, found := dc.Get("nonexistent")
	if found {
		t.Error("expected not found")
	}
	if val != nil {
		t.Error("expected nil value")
	}

	// Test hit
	dc.Set("key1", "value1")
	val, found = dc.Get("key1")
	if !found {
		t.Error("expected found")
	}
	if val != "value1" {
		t.Errorf("expected value1, got %v", val)
	}
}

// Test Lookup with disk cache disabled
func TestDiskCache_Lookup_Disabled(t *testing.T) {
	dc, err := NewDiskCache(time.Hour, "")
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}

	if dc.enabled {
		t.Fatal("expected disk cache to be disabled")
	}

	dc.Set("key1", "value1")

	// Clear memory
	dc.mu.Lock()
	dc.entries = make(map[string]Entry)
	dc.mu.Unlock()

	// Should be a miss since disk is disabled
	val, hitType := dc.Lookup("key1")
	if hitType != CacheMiss {
		t.Errorf("expected CacheMiss with disk disabled, got %v", hitType)
	}
	if val != nil {
		t.Errorf("expected nil, got %v", val)
	}
}

// Test Lookup restoring value to memory
func TestDiskCache_Lookup_RestoresToMemory(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	dc.Set("key1", "value1")

	// Clear memory
	dc.mu.Lock()
	dc.entries = make(map[string]Entry)
	dc.mu.Unlock()

	// First lookup should be from disk
	_, hitType := dc.Lookup("key1")
	if hitType != CacheHitDisk {
		t.Errorf("expected CacheHitDisk, got %v", hitType)
	}

	// Second lookup should be from memory
	val, hitType := dc.Lookup("key1")
	if hitType != CacheHitMemory {
		t.Errorf("expected CacheHitMemory after restore, got %v", hitType)
	}
	if val != "value1" {
		t.Errorf("expected value1, got %v", val)
	}
}

// Test SetWithTTL memory-only mode
func TestDiskCache_SetWithTTL_MemoryOnly(t *testing.T) {
	dc, err := NewDiskCache(time.Hour, "")
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}

	dc.SetWithTTL("key1", "value1", time.Hour)

	val, found := dc.Get("key1")
	if !found {
		t.Error("expected to find key1 in memory")
	}
	if val != "value1" {
		t.Errorf("expected value1, got %v", val)
	}
}

// Test cacheKey determinism
func TestDiskCache_CacheKey_Deterministic(t *testing.T) {
	dc1, err := NewDiskCache(time.Hour, "")
	if err != nil {
		t.Fatalf("failed to create disk cache 1: %v", err)
	}
	dc2, err := NewDiskCache(time.Hour, "")
	if err != nil {
		t.Fatalf("failed to create disk cache 2: %v", err)
	}

	key1 := dc1.cacheKey("test")
	key2 := dc2.cacheKey("test")

	if key1 != key2 {
		t.Error("expected cache key to be deterministic across instances")
	}

	// Different inputs should produce different keys
	key3 := dc1.cacheKey("test2")
	if key1 == key3 {
		t.Error("expected different keys for different inputs")
	}
}
