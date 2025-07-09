package main

import (
	"sync"
	"time"
)

// cacheEntry holds a cached value with expiration.
type cacheEntry struct {
	value      interface{}
	expiration time.Time
}

// cache provides thread-safe caching with TTL.
type cache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	ttl     time.Duration
}

// newCache creates a new cache with the given TTL.
func newCache(ttl time.Duration) *cache {
	return &cache{
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
	}
}

// get retrieves a value from cache if not expired.
func (c *cache) get(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	
	entry, exists := c.entries[key]
	if !exists || time.Now().After(entry.expiration) {
		return nil, false
	}
	
	return entry.value, true
}

// set stores a value in cache with TTL.
func (c *cache) set(key string, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	c.entries[key] = cacheEntry{
		value:      value,
		expiration: time.Now().Add(c.ttl),
	}
}

// clear removes all entries from cache.
func (c *cache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	c.entries = make(map[string]cacheEntry)
}