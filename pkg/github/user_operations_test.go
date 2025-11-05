package github

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/cache"
	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// mustNewDiskCache creates a DiskCache for testing (in-memory only)
func mustNewDiskCache(t *testing.T) *cache.DiskCache {
	t.Helper()
	c, err := cache.NewDiskCache(time.Hour, "") // Empty dir = memory-only
	if err != nil {
		t.Fatalf("failed to create test cache: %v", err)
	}
	return c
}

func TestClient_HasWriteAccess_CachedCollaborators(t *testing.T) {
	c := &Client{
		cache: mustNewDiskCache(t),
	}

	// Cache collaborators list
	cacheKey := makeCacheKey("collaborators", "owner", "repo")
	c.cache.Set(cacheKey, []string{"alice", "bob", "charlie"})

	tests := []struct {
		username string
		want     bool
	}{
		{"alice", true},
		{"bob", true},
		{"charlie", true},
		{"unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.username, func(t *testing.T) {
			got := c.HasWriteAccess(context.Background(), "owner", "repo", tt.username)
			if got != tt.want {
				t.Errorf("HasWriteAccess(%q) = %v, want %v", tt.username, got, tt.want)
			}
		})
	}
}

func TestClient_HasWriteAccess_PermissionDenied(t *testing.T) {
	c := &Client{
		cache: mustNewDiskCache(t),
	}

	// Cache permission denied flag
	permCacheKey := makeCacheKey("collaborators-permission", "owner", "repo")
	c.cache.Set(permCacheKey, true)

	// Should fail-open (return true) when we don't have permission to check
	got := c.HasWriteAccess(context.Background(), "owner", "repo", "anyone")
	if !got {
		t.Error("HasWriteAccess should fail-open (return true) when permission denied")
	}
}

func TestClient_HasWriteAccess_NoCache(t *testing.T) {
	c := &Client{
		cache: mustNewDiskCache(t),
	}

	// No cached data - should fail-open
	got := c.HasWriteAccess(context.Background(), "owner", "repo", "anyone")
	if !got {
		t.Error("HasWriteAccess should fail-open (return true) when no cache available")
	}
}

func TestClient_cachedPR_Hit(t *testing.T) {
	c := &Client{
		cache: mustNewDiskCache(t),
	}

	expectedPR := &types.PullRequest{
		Owner:      "owner",
		Repository: "repo",
		Number:     123,
		Title:      "Test PR",
		UpdatedAt:  time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
	}

	// Cache the PR
	cacheKey := makeCacheKey("pr", "owner", "repo", "123")
	c.cache.Set(cacheKey, expectedPR)

	// Test cache hit with matching updated_at
	updatedAt := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	pr, found := c.cachedPR("owner", "repo", 123, &updatedAt)

	if !found {
		t.Fatal("expected cache hit")
	}

	if pr.Title != "Test PR" {
		t.Errorf("expected PR title 'Test PR', got %q", pr.Title)
	}
}

func TestClient_cachedPR_InvalidatedByUpdate(t *testing.T) {
	c := &Client{
		cache: mustNewDiskCache(t),
	}

	oldTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	pr := &types.PullRequest{
		Owner:      "owner",
		Repository: "repo",
		Number:     123,
		Title:      "Test PR",
		UpdatedAt:  oldTime,
	}

	// Cache the PR with old timestamp
	cacheKey := makeCacheKey("pr", "owner", "repo", "123")
	c.cache.Set(cacheKey, pr)

	// Try to retrieve with newer timestamp - should invalidate cache
	newTime := time.Date(2025, 1, 1, 13, 0, 0, 0, time.UTC)
	_, found := c.cachedPR("owner", "repo", 123, &newTime)

	if found {
		t.Error("cache should be invalidated when updated_at changed")
	}
}

func TestClient_cachedPR_NoExpectedTime(t *testing.T) {
	c := &Client{
		cache: mustNewDiskCache(t),
	}

	pr := &types.PullRequest{
		Owner:      "owner",
		Repository: "repo",
		Number:     123,
		Title:      "Test PR",
		UpdatedAt:  time.Now(),
	}

	cacheKey := makeCacheKey("pr", "owner", "repo", "123")
	c.cache.Set(cacheKey, pr)

	// Retrieve without expected time - should return cached value
	cached, found := c.cachedPR("owner", "repo", 123, nil)
	if !found {
		t.Fatal("expected cache hit")
	}

	if cached.Title != "Test PR" {
		t.Errorf("expected cached PR title 'Test PR', got %q", cached.Title)
	}
}

func TestClient_cachedPR_Miss(t *testing.T) {
	c := &Client{
		cache: mustNewDiskCache(t),
	}

	// No cached PR
	_, found := c.cachedPR("owner", "repo", 999, nil)
	if found {
		t.Error("expected cache miss for non-existent PR")
	}
}

func TestClient_cachePR(t *testing.T) {
	c := &Client{
		cache: mustNewDiskCache(t),
	}

	pr := &types.PullRequest{
		Owner:      "owner",
		Repository: "repo",
		Number:     123,
		Title:      "Test PR",
	}

	c.cachePR(pr)

	// Verify it was cached
	cacheKey := makeCacheKey("pr", "owner", "repo", "123")
	cached, found := c.cache.Get(cacheKey)
	if !found {
		t.Fatal("PR should be cached")
	}

	cachedPR, ok := cached.(*types.PullRequest)
	if !ok {
		t.Fatal("cached value should be *types.PullRequest")
	}

	if cachedPR.Title != "Test PR" {
		t.Errorf("expected cached title 'Test PR', got %q", cachedPR.Title)
	}
}

func TestClient_OpenPRCount_Cached(t *testing.T) {
	c := &Client{
		cache: mustNewDiskCache(t),
	}

	// Pre-cache a PR count
	cacheKey := makeCacheKey("pr-count", "myorg", "alice")
	c.cache.Set(cacheKey, 5)

	count, err := c.OpenPRCount(context.Background(), "myorg", "alice", time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if count != 5 {
		t.Errorf("expected cached count 5, got %d", count)
	}
}

func TestClient_OpenPRCount_CachedFailure(t *testing.T) {
	c := &Client{
		cache: mustNewDiskCache(t),
	}

	// Cache a failure
	failureKey := makeCacheKey("pr-count-failure", "myorg", "alice")
	c.cache.Set(failureKey, true)

	_, err := c.OpenPRCount(context.Background(), "myorg", "alice", time.Hour)
	if err == nil {
		t.Error("expected error for cached failure")
	}

	if err.Error() != "recently failed to get PR count (cached failure)" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestClient_OpenPRCount_InvalidParams(t *testing.T) {
	c := &Client{
		cache: mustNewDiskCache(t),
	}

	tests := []struct {
		name string
		org  string
		user string
	}{
		{"empty org", "", "alice"},
		{"empty user", "myorg", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.OpenPRCount(context.Background(), tt.org, tt.user, time.Hour)
			if err == nil {
				t.Error("expected error for invalid params")
			}
		})
	}
}

func TestClient_BatchOpenPRCount_AllCached(t *testing.T) {
	c := &Client{
		cache: mustNewDiskCache(t),
	}

	// Pre-cache PR counts for all users
	c.cache.Set(makeCacheKey("pr-count", "myorg", "alice"), 3)
	c.cache.Set(makeCacheKey("pr-count", "myorg", "bob"), 5)
	c.cache.Set(makeCacheKey("pr-count", "myorg", "charlie"), 1)

	result, err := c.BatchOpenPRCount(context.Background(), "myorg", []string{"alice", "bob", "charlie"}, time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := map[string]int{
		"alice":   3,
		"bob":     5,
		"charlie": 1,
	}

	for user, count := range expected {
		if result[user] != count {
			t.Errorf("expected count %d for %s, got %d", count, user, result[user])
		}
	}
}

func TestClient_BatchOpenPRCount_EmptyUsers(t *testing.T) {
	c := &Client{
		cache: mustNewDiskCache(t),
	}

	result, err := c.BatchOpenPRCount(context.Background(), "myorg", []string{}, time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 0 {
		t.Errorf("expected empty result for empty users list, got %v", result)
	}
}

func TestMakeCacheKey_Consistency(t *testing.T) {
	// Verify cache keys are consistent and don't conflict
	key1 := makeCacheKey("pr", "owner", "repo", strconv.Itoa(123))
	key2 := makeCacheKey("pr", "owner", "repo", "123")

	if key1 != key2 {
		t.Errorf("cache keys should be identical: %q != %q", key1, key2)
	}

	// Verify different keys don't collide
	prKey := makeCacheKey("pr", "owner", "repo", "1")
	countKey := makeCacheKey("pr-count", "owner", "user1")

	if prKey == countKey {
		t.Error("different cache key types should not collide")
	}
}
