package cache

import "time"

// Store defines the interface for cache operations.
type Store interface {
	Get(key string) (any, bool)
	Set(key string, value any)
	SetWithTTL(key string, value any, ttl time.Duration)
}

// DiskStore extends Store with disk-specific lookup operations.
type DiskStore interface {
	Store
	Lookup(key string) (value any, hit HitType, found bool)
}
