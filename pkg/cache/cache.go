// Package cache provides thread-safe caching with TTL support.
package cache

import (
	"sync"
	"time"
)

// Entry holds a cached value with expiration.
type Entry struct {
	value      any
	expiration time.Time
}

// Cache provides thread-safe caching with TTL.
type Cache struct {
	entries map[string]Entry
	mu      sync.RWMutex
	ttl     time.Duration
}

// New creates a new cache with the specified TTL.
func New(ttl time.Duration) *Cache {
	c := &Cache{
		entries: make(map[string]Entry),
		ttl:     ttl,
	}
	go c.cleanupExpired()
	return c
}

// Get retrieves a value from cache if not expired.
func (c *Cache) Get(key string) (any, bool) {
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

// Set stores a value in cache with TTL.
func (c *Cache) Set(key string, value any) {
	c.SetWithTTL(key, value, c.ttl)
}

// SetWithTTL stores a value in cache with custom TTL.
func (c *Cache) SetWithTTL(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = Entry{
		value:      value,
		expiration: time.Now().Add(ttl),
	}
}

// cleanupExpired periodically removes expired entries.
func (c *Cache) cleanupExpired() {
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
