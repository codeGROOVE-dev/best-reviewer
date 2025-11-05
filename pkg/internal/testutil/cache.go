package testutil

import (
	"sync"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/cache"
)

// MockCache implements cache.Store for testing.
type MockCache struct {
	entries map[string]mockEntry
	mu      sync.RWMutex
}

type mockEntry struct {
	value      any
	expiration time.Time
}

// NewMockCache creates a new MockCache.
func NewMockCache() *MockCache {
	return &MockCache{
		entries: make(map[string]mockEntry),
	}
}

// Get retrieves a value from the cache.
func (m *MockCache) Get(key string) (any, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, ok := m.entries[key]
	if !ok {
		return nil, false
	}

	// Check expiration
	if !entry.expiration.IsZero() && time.Now().After(entry.expiration) {
		return nil, false
	}

	return entry.value, true
}

// Set stores a value in the cache (no TTL).
func (m *MockCache) Set(key string, value any) {
	m.SetWithTTL(key, value, 0)
}

// SetWithTTL stores a value in the cache with a TTL.
func (m *MockCache) SetWithTTL(key string, value any, ttl time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry := mockEntry{
		value: value,
	}
	if ttl > 0 {
		entry.expiration = time.Now().Add(ttl)
	}
	m.entries[key] = entry
}

// Clear clears all entries from the cache.
func (m *MockCache) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.entries = make(map[string]mockEntry)
}

// Len returns the number of entries in the cache.
func (m *MockCache) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.entries)
}

// MockDiskCache implements cache.DiskStore for testing.
type MockDiskCache struct {
	*MockCache

	hits map[string]cache.HitType
}

// NewMockDiskCache creates a new MockDiskCache.
func NewMockDiskCache() *MockDiskCache {
	return &MockDiskCache{
		MockCache: NewMockCache(),
		hits:      make(map[string]cache.HitType),
	}
}

// Lookup retrieves a value and its hit type from the cache.
func (m *MockDiskCache) Lookup(key string) (value any, hit cache.HitType, found bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, ok := m.entries[key]
	if !ok {
		return nil, "", false
	}

	// Check expiration
	if !entry.expiration.IsZero() && time.Now().After(entry.expiration) {
		return nil, "", false
	}

	hitType := m.hits[key]
	if hitType == "" {
		hitType = cache.CacheHitMemory
	}

	return entry.value, hitType, true
}

// SetHitType configures the hit type for a key.
func (m *MockDiskCache) SetHitType(key string, hitType cache.HitType) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.hits[key] = hitType
}
