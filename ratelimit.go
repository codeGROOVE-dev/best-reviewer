package main

import (
	"sync"
	"time"
)

// RateLimiter implements a simple token bucket rate limiter.
type RateLimiter struct {
	tokens     int
	maxTokens  int
	refillRate time.Duration
	lastRefill time.Time
	mu         sync.Mutex
}

// NewRateLimiter creates a new rate limiter.
func NewRateLimiter(maxRequests int, perDuration time.Duration) *RateLimiter {
	return &RateLimiter{
		tokens:     maxRequests,
		maxTokens:  maxRequests,
		refillRate: perDuration / time.Duration(maxRequests),
		lastRefill: time.Now(),
	}
}

// Allow checks if a request is allowed under the rate limit.
func (r *RateLimiter) Allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Refill tokens based on time elapsed
	now := time.Now()
	elapsed := now.Sub(r.lastRefill)
	tokensToAdd := int(elapsed / r.refillRate)

	if tokensToAdd > 0 {
		r.tokens += tokensToAdd
		if r.tokens > r.maxTokens {
			r.tokens = r.maxTokens
		}
		r.lastRefill = now
	}

	// Check if we have tokens available
	if r.tokens > 0 {
		r.tokens--
		return true
	}

	return false
}

// Wait blocks until a request is allowed.
func (r *RateLimiter) Wait() {
	for !r.Allow() {
		time.Sleep(r.refillRate)
	}
}
