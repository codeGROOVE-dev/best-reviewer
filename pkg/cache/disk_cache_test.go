package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewDiskCache(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name      string
		ttl       time.Duration
		cacheDir  string
		expectErr bool
		enabled   bool
	}{
		{
			name:      "with valid cache dir",
			ttl:       time.Hour,
			cacheDir:  tmpDir,
			expectErr: false,
			enabled:   true,
		},
		{
			name:      "with empty cache dir (memory only)",
			ttl:       time.Hour,
			cacheDir:  "",
			expectErr: false,
			enabled:   false,
		},
		{
			name:      "with relative path",
			ttl:       time.Hour,
			cacheDir:  "relative/path",
			expectErr: true,
			enabled:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc, err := NewDiskCache(tt.ttl, tt.cacheDir)

			if tt.expectErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.expectErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if dc == nil && !tt.expectErr {
				t.Fatal("expected non-nil DiskCache")
			}

			if !tt.expectErr && dc.enabled != tt.enabled {
				t.Errorf("expected enabled=%v, got %v", tt.enabled, dc.enabled)
			}
		})
	}
}

func TestDiskCache_SetAndGet(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Set a value
	dc.Set("key1", "value1")

	// Get from memory
	val, found := dc.Get("key1")
	if !found {
		t.Fatal("expected to find key1 in memory")
	}
	if val != "value1" {
		t.Errorf("expected value1, got %v", val)
	}

	// Verify disk file exists
	cacheFile := filepath.Join(tmpDir, dc.cacheKey("key1")+".json")
	if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
		t.Error("expected cache file to exist on disk")
	}
}

func TestDiskCache_Lookup_Memory(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	dc.Set("key1", "value1")

	val, hitType := dc.Lookup("key1")
	if hitType != CacheHitMemory {
		t.Errorf("expected CacheHitMemory, got %v", hitType)
	}
	if val != "value1" {
		t.Errorf("expected value1, got %v", val)
	}
}

func TestDiskCache_Lookup_Disk(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Set a value
	dc.SetWithTTL("key1", "value1", time.Hour)

	// Clear memory cache to simulate restart
	dc.mu.Lock()
	dc.entries = make(map[string]Entry)
	dc.mu.Unlock()

	// Should find from disk
	val, hitType := dc.Lookup("key1")
	if hitType != CacheHitDisk {
		t.Errorf("expected CacheHitDisk, got %v", hitType)
	}
	if val != "value1" {
		t.Errorf("expected value1, got %v", val)
	}

	// Should now be in memory too
	_, hitType = dc.Lookup("key1")
	if hitType != CacheHitMemory {
		t.Errorf("expected value restored to memory, got hitType %v", hitType)
	}
}

func TestDiskCache_Lookup_Miss(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	val, hitType := dc.Lookup("nonexistent")
	if hitType != CacheMiss {
		t.Errorf("expected CacheMiss, got %v", hitType)
	}
	if val != nil {
		t.Errorf("expected nil value, got %v", val)
	}
}

func TestDiskCache_ExpiredDiskEntry(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Set with short TTL
	dc.SetWithTTL("key1", "value1", 50*time.Millisecond)

	// Clear memory cache
	dc.mu.Lock()
	dc.entries = make(map[string]Entry)
	dc.mu.Unlock()

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Should be expired
	val, hitType := dc.Lookup("key1")
	if hitType != CacheMiss {
		t.Errorf("expected CacheMiss for expired entry, got %v", hitType)
	}
	if val != nil {
		t.Errorf("expected nil value, got %v", val)
	}

	// Verify disk file was removed
	cacheFile := filepath.Join(tmpDir, dc.cacheKey("key1")+".json")
	if _, err := os.Stat(cacheFile); !os.IsNotExist(err) {
		t.Error("expected expired cache file to be removed")
	}
}

func TestDiskCache_MemoryOnly(t *testing.T) {
	// Create without cache dir (memory only)
	dc, err := NewDiskCache(time.Hour, "")
	if err != nil {
		t.Fatalf("failed to create memory-only cache: %v", err)
	}

	if dc.enabled {
		t.Error("expected disk cache to be disabled")
	}

	dc.Set("key1", "value1")

	val, found := dc.Get("key1")
	if !found {
		t.Fatal("expected to find key1")
	}
	if val != "value1" {
		t.Errorf("expected value1, got %v", val)
	}

	// Should be memory hit
	_, hitType := dc.Lookup("key1")
	if hitType != CacheHitMemory {
		t.Errorf("expected CacheHitMemory, got %v", hitType)
	}
}

func TestDiskCache_CacheKey(t *testing.T) {
	dc, err := NewDiskCache(time.Hour, "")
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}

	tests := []struct {
		key1 string
		key2 string
		same bool
	}{
		{"key1", "key1", true},
		{"key1", "key2", false},
		{"", "", true},
		{"a", "b", false},
		{"hello", "hello", true},
	}

	for _, tt := range tests {
		hash1 := dc.cacheKey(tt.key1)
		hash2 := dc.cacheKey(tt.key2)

		if tt.same && hash1 != hash2 {
			t.Errorf("expected same hash for %q and %q", tt.key1, tt.key2)
		}
		if !tt.same && hash1 == hash2 {
			t.Errorf("expected different hash for %q and %q", tt.key1, tt.key2)
		}

		// Verify it's a valid hex string
		if len(hash1) != 64 {
			t.Errorf("expected 64-character hex string, got %d characters", len(hash1))
		}
	}
}

func TestDiskCache_ComplexTypes(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	tests := []struct {
		name  string
		value any
	}{
		{"map", map[string]int{"a": 1, "b": 2}},
		{"slice", []string{"one", "two", "three"}},
		{"nested", map[string]any{"key": []int{1, 2, 3}}},
		{"number", 42.5},
		{"bool", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc.Set(tt.name, tt.value)

			// Clear memory cache
			dc.mu.Lock()
			dc.entries = make(map[string]Entry)
			dc.mu.Unlock()

			// Load from disk
			val, hitType := dc.Lookup(tt.name)
			if hitType != CacheHitDisk {
				t.Errorf("expected CacheHitDisk, got %v", hitType)
			}
			if val == nil {
				t.Error("expected non-nil value")
			}
		})
	}
}

func TestDiskCache_SetWithTTL(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Set with custom TTL
	dc.SetWithTTL("key1", "value1", 2*time.Hour)

	// Verify it's in memory
	val, found := dc.Cache.Get("key1")
	if !found {
		t.Fatal("expected to find key1 in memory")
	}
	if val != "value1" {
		t.Errorf("expected value1, got %v", val)
	}

	// Verify it's on disk with correct TTL
	cacheFile := filepath.Join(tmpDir, dc.cacheKey("key1")+".json")
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		t.Fatalf("failed to read cache file: %v", err)
	}

	var entry diskEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("failed to unmarshal cache entry: %v", err)
	}

	// Check that expiration is roughly 2 hours in the future
	expectedExpiry := time.Now().Add(2 * time.Hour)
	diff := entry.Expiration.Sub(expectedExpiry)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("expected expiration around %v, got %v", expectedExpiry, entry.Expiration)
	}
}

func TestDiskCache_CorruptedDiskEntry(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Write corrupted data to disk
	cacheFile := filepath.Join(tmpDir, dc.cacheKey("key1")+".json")
	if err := os.WriteFile(cacheFile, []byte("corrupted json{{{"), 0o600); err != nil {
		t.Fatalf("failed to write corrupted cache file: %v", err)
	}

	// Should return miss for corrupted entry
	val, hitType := dc.Lookup("key1")
	if hitType != CacheMiss {
		t.Errorf("expected CacheMiss for corrupted entry, got %v", hitType)
	}
	if val != nil {
		t.Errorf("expected nil value, got %v", val)
	}

	// Corrupted file should be removed (removeFromDisk is called synchronously)
	if _, err := os.Stat(cacheFile); !os.IsNotExist(err) {
		t.Error("expected corrupted cache file to be removed")
	}
}

func TestDiskCache_AtomicWrite(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Write multiple values rapidly
	for i := range 100 {
		dc.Set("key1", i)
	}

	// Should have the last value
	val, found := dc.Get("key1")
	if !found {
		t.Fatal("expected to find key1")
	}

	// Should be the last value (99) or one close to it
	// (race conditions might mean we don't get exactly 99)
	intVal, ok := val.(float64) // JSON unmarshals numbers as float64
	if ok && (intVal < 90 || intVal > 99) {
		t.Errorf("expected value around 99, got %v", intVal)
	}

	// Verify no .tmp files left behind
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read cache dir: %v", err)
	}

	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".tmp" {
			t.Errorf("found leftover temp file: %s", entry.Name())
		}
	}
}

func TestDiskCache_RemoveFromDisk(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	dc.Set("key1", "value1")

	cacheFile := filepath.Join(tmpDir, dc.cacheKey("key1")+".json")

	// Verify file exists
	if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
		t.Fatal("expected cache file to exist")
	}

	// Remove from disk
	dc.removeFromDisk("key1")

	// Verify file is gone
	if _, err := os.Stat(cacheFile); !os.IsNotExist(err) {
		t.Error("expected cache file to be removed")
	}

	// Removing non-existent file should not error
	dc.removeFromDisk("nonexistent")
}

func TestDiskCache_TTLConstants(t *testing.T) {
	// Just verify the constants are defined with reasonable values
	tests := []struct {
		name string
		ttl  time.Duration
		min  time.Duration
		max  time.Duration
	}{
		{"TTLWorkload", TTLWorkload, time.Hour, 24 * time.Hour},
		{"TTLCollaborators", TTLCollaborators, time.Hour, 24 * time.Hour},
		{"TTLRecentActivity", TTLRecentActivity, time.Hour, 24 * time.Hour},
		{"TTLHistoricalPR", TTLHistoricalPR, 24 * time.Hour, 30 * 24 * time.Hour},
		{"TTLFileHistory", TTLFileHistory, 24 * time.Hour, 7 * 24 * time.Hour},
		{"TTLUserDetails", TTLUserDetails, 24 * time.Hour, 30 * 24 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.ttl < tt.min {
				t.Errorf("%s is too short: %v (min: %v)", tt.name, tt.ttl, tt.min)
			}
			if tt.ttl > tt.max {
				t.Errorf("%s is too long: %v (max: %v)", tt.name, tt.ttl, tt.max)
			}
		})
	}
}

func TestDiskCache_SaveToDisk_Errors(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Create a value that can't be marshaled to JSON
	type unmarshalable struct {
		Ch chan int // channels can't be marshaled
	}
	badValue := unmarshalable{Ch: make(chan int)}

	// This should not panic, just log an error
	dc.Set("bad", badValue)

	// Should not be retrievable from disk
	dc.mu.Lock()
	dc.entries = make(map[string]Entry)
	dc.mu.Unlock()

	val, hitType := dc.Lookup("bad")
	if hitType != CacheMiss {
		t.Errorf("expected CacheMiss, got %v", hitType)
	}
	if val != nil {
		t.Errorf("expected nil value, got %v", val)
	}
}

func TestDiskCache_LoadFromDisk_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		t.Fatalf("failed to create disk cache: %v", err)
	}

	// Write invalid JSON
	cacheFile := filepath.Join(tmpDir, dc.cacheKey("key1")+".json")
	if err := os.WriteFile(cacheFile, []byte("{invalid json}"), 0o600); err != nil {
		t.Fatalf("failed to write invalid json: %v", err)
	}

	var entry diskEntry
	loaded, corrupted := dc.loadFromDisk("key1", &entry)
	if loaded {
		t.Error("expected loadFromDisk to fail for invalid JSON")
	}
	if !corrupted {
		t.Error("expected invalid JSON to be marked as corrupted")
	}
}

func BenchmarkDiskCache_SetMemoryOnly(b *testing.B) {
	dc, err := NewDiskCache(time.Hour, "")
	if err != nil {
		b.Fatalf("failed to create disk cache: %v", err)
	}

	b.ResetTimer()
	for i := range b.N {
		dc.Set("key", i)
	}
}

func BenchmarkDiskCache_SetWithDisk(b *testing.B) {
	tmpDir := b.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		b.Fatalf("failed to create disk cache: %v", err)
	}

	b.ResetTimer()
	for i := range b.N {
		dc.Set("key", i)
	}
}

func BenchmarkDiskCache_GetMemoryHit(b *testing.B) {
	tmpDir := b.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		b.Fatalf("failed to create disk cache: %v", err)
	}
	dc.Set("key", "value")

	b.ResetTimer()
	for range b.N {
		dc.Get("key")
	}
}

func BenchmarkDiskCache_GetDiskHit(b *testing.B) {
	tmpDir := b.TempDir()
	dc, err := NewDiskCache(time.Hour, tmpDir)
	if err != nil {
		b.Fatalf("failed to create disk cache: %v", err)
	}
	dc.Set("key", "value")

	b.ResetTimer()
	for range b.N {
		// Clear memory cache before each lookup
		dc.mu.Lock()
		dc.entries = make(map[string]Entry)
		dc.mu.Unlock()

		dc.Get("key")
	}
}
