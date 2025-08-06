package main

import (
	"fmt"
	"log"
	"time"
)

// prCacheEntry holds a cached PR with its updated_at timestamp for validation.
type prCacheEntry struct {
	pr        *PullRequest
	updatedAt time.Time
}

// cachePR caches a pull request with its updated_at timestamp.
func (c *GitHubClient) cachePR(pr *PullRequest) {
	cacheKey := fmt.Sprintf("pr:%s/%s:%d", pr.Owner, pr.Repository, pr.Number)
	entry := prCacheEntry{
		pr:        pr,
		updatedAt: pr.UpdatedAt,
	}
	c.cache.setWithTTL(cacheKey, entry, prCacheTTL)
	log.Printf("[CACHE] Cached PR %s/%s#%d (updated: %s)", pr.Owner, pr.Repository, pr.Number, pr.UpdatedAt.Format("2006-01-02 15:04:05"))
}

// cachedPR retrieves a cached PR if it exists and is still valid.
func (c *GitHubClient) cachedPR(owner, repo string, prNumber int, expectedUpdatedAt *time.Time) (*PullRequest, bool) {
	cacheKey := fmt.Sprintf("pr:%s/%s:%d", owner, repo, prNumber)

	cached, found := c.cache.value(cacheKey)
	if !found {
		return nil, false
	}

	entry, ok := cached.(prCacheEntry)
	if !ok {
		// Old cache format, invalidate
		return nil, false
	}

	// If we have an expected updated_at time, check if cache is stale
	if expectedUpdatedAt != nil && !entry.updatedAt.Equal(*expectedUpdatedAt) {
		log.Printf("[CACHE] PR %s/%s#%d cache is stale (cached: %s, expected: %s)",
			owner, repo, prNumber,
			entry.updatedAt.Format("2006-01-02 15:04:05"),
			expectedUpdatedAt.Format("2006-01-02 15:04:05"))
		return nil, false
	}

	log.Printf("[CACHE] Using cached PR %s/%s#%d", owner, repo, prNumber)
	return entry.pr, true
}
