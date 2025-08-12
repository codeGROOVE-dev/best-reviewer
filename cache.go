package main

import (
	"sync"
	"time"
)

// cacheEntry holds a cached value with expiration.
type cacheEntry struct {
	value      any
	expiration time.Time
}

// cache provides thread-safe caching with TTL.
type cache struct {
	entries map[string]cacheEntry
	mu      sync.RWMutex
	ttl     time.Duration
}

// value retrieves a value from cache if not expired.
func (c *cache) value(key string) (any, bool) {
	c.mu.RLock()
	entry, exists := c.entries[key]
	if !exists {
		c.mu.RUnlock()
		return nil, false
	}

	// Check expiration while holding read lock
	if time.Now().After(entry.expiration) {
		c.mu.RUnlock()
		// Upgrade to write lock for deletion
		c.mu.Lock()
		// Double-check after lock upgrade to avoid race condition
		if e, exists := c.entries[key]; exists && time.Now().After(e.expiration) {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		return nil, false
	}

	value := entry.value
	c.mu.RUnlock()
	return value, true
}

// set stores a value in cache with TTL.
func (c *cache) set(key string, value any) {
	c.setWithTTL(key, value, c.ttl)
}

// setWithTTL stores a value in cache with custom TTL.
func (c *cache) setWithTTL(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = cacheEntry{
		value:      value,
		expiration: time.Now().Add(ttl),
	}
}

// cleanupExpired periodically removes expired entries.
func (c *cache) cleanupExpired() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		for key, entry := range c.entries {
			if now.After(entry.expiration) {
				delete(c.entries, key)
			}
		}
		c.mu.Unlock()
	}
}
