package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// User-related constants.
const (
	prStaleDaysThreshold   = 90               // PRs older than this are considered stale
	prCountCacheTTL        = 6 * time.Hour    // PR count for workload balancing (default)
	prCountFailureCacheTTL = 10 * time.Minute // Cache failures to avoid repeated API calls
)

// UserCache provides caching for user information.
type UserCache struct {
	users map[string]*UserInfo
	mu    sync.RWMutex
}

// UserInfo holds cached information about a GitHub user.
type UserInfo struct {
	IsBot      bool
	HasAccess  bool
	LastUpdate time.Time
}

// NewUserCache creates a new user cache.
func NewUserCache() *UserCache {
	return &UserCache{
		users: make(map[string]*UserInfo),
	}
}

// Get retrieves user info from cache.
func (uc *UserCache) Get(username string) (*UserInfo, bool) {
	uc.mu.RLock()
	defer uc.mu.RUnlock()
	info, ok := uc.users[username]
	return info, ok
}

// Set stores user info in cache.
func (uc *UserCache) Set(username string, info *UserInfo) {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	uc.users[username] = info
}

// IsUserBot checks if a user is a bot.
func (c *Client) IsUserBot(_ context.Context, username string) bool {
	lower := strings.ToLower(username)

	// Check for common bot patterns
	botPatterns := []string{
		"[bot]",
		"-bot",
		"_bot",
		"bot-",
		"bot_",
		".bot",
		"github-actions",
		"dependabot",
		"renovate",
		"greenkeeper",
		"snyk",
		"codecov",
		"coveralls",
		"travis",
		"circleci",
		"jenkins",
		"buildkite",
		"semaphore",
		"appveyor",
		"azure-pipelines",
		"github-classroom",
		"imgbot",
		"allcontributors",
		"whitesource",
		"mergify",
		"sonarcloud",
		"deepsource",
		"codefactor",
		"lgtm",
		"codacy",
		"hound",
		"stale",
	}

	for _, pattern := range botPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}

	// Check for common organization/service account patterns
	orgPatterns := []string{
		"octo-sts",
		"octocat",
		"-sts",
		"-svc",
		"-service",
		"-system",
		"-automation",
		"-ci",
		"-cd",
		"-deploy",
		"-release",
		"release-manager",
		"-build",
		"-test",
		"-admin",
		"-security",
		"security-scanner",
		"-compliance",
		"compliance-checker",
	}

	for _, pattern := range orgPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}

	return false
}

// HasWriteAccess checks if a user has write access to the repository.
func (c *Client) HasWriteAccess(ctx context.Context, owner, repo, username string) bool {
	// Check if user is a collaborator with write access
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/collaborators/%s", owner, repo, username)
	resp, err := c.makeRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		slog.Info("[WARN] Failed to check write access for %s: %v", username, err)
		return false // Fail closed - assume no access on error
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Info("[WARN] Failed to close response body: %v", err)
		}
	}()

	// GitHub returns 204 No Content if user is a collaborator
	// Returns 404 if not a collaborator
	if resp.StatusCode == http.StatusNoContent {
		return true
	}

	return false
}

// OpenPRCount returns the number of open PRs assigned to or requested for review by a user in an organization.
func (c *Client) OpenPRCount(ctx context.Context, org, user string, cacheTTL time.Duration) (int, error) {
	// Check cache first for successful results
	cacheKey := makeCacheKey("pr-count", org, user)
	if cached, found := c.cache.Get(cacheKey); found {
		if count, ok := cached.(int); ok {
			slog.Info("    [CACHE] User %s has %d non-stale open PRs in org %s (cached)", user, count, org)
			return count, nil
		}
	}

	// Check if we recently failed to get PR count for this user to avoid repeated failures
	failureKey := makeCacheKey("pr-count-failure", org, user)
	if _, found := c.cache.Get(failureKey); found {
		return 0, errors.New("recently failed to get PR count (cached failure)")
	}

	// Validate that the organization and user are not empty
	if org == "" || user == "" {
		return 0, fmt.Errorf("invalid organization (%s) or user (%s)", org, user)
	}

	slog.Info("  [API] Fetching open PR count for user %s in org %s", user, org)

	// Create a context with shorter timeout for PR count queries to avoid hanging
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Calculate the cutoff date for non-stale PRs (90 days ago)
	cutoffDate := time.Now().AddDate(0, 0, -prStaleDaysThreshold).Format("2006-01-02")

	// Use two separate queries as they are simpler and more reliable
	// Only count PRs updated within the last 90 days to exclude stale PRs
	// First, search for PRs where user is assigned
	assignedQuery := fmt.Sprintf("is:pr is:open org:%s assignee:%s updated:>=%s", org, user, cutoffDate)
	slog.Info("  [DEBUG] Searching assigned PRs for %s (updated since %s)", user, cutoffDate)
	assignedCount, err := c.searchPRCount(timeoutCtx, assignedQuery)
	if err != nil {
		// Cache the failure to avoid repeated attempts
		c.cache.SetWithTTL(failureKey, true, prCountFailureCacheTTL)
		return 0, fmt.Errorf("failed to get assigned PR count: %w", err)
	}
	slog.Info("  [DEBUG] Found %d non-stale assigned PRs for %s", assignedCount, user)

	// Second, search for PRs where user is requested as reviewer
	reviewQuery := fmt.Sprintf("is:pr is:open org:%s review-requested:%s updated:>=%s", org, user, cutoffDate)
	slog.Info("  [DEBUG] Searching review-requested PRs for %s (updated since %s)", user, cutoffDate)
	reviewCount, err := c.searchPRCount(timeoutCtx, reviewQuery)
	if err != nil {
		// Cache the failure to avoid repeated attempts
		c.cache.SetWithTTL(failureKey, true, prCountFailureCacheTTL)
		return 0, fmt.Errorf("failed to get review-requested PR count: %w", err)
	}
	slog.Info("  [DEBUG] Found %d non-stale review-requested PRs for %s", reviewCount, user)

	total := assignedCount + reviewCount

	slog.Info("    📊 User %s has %d non-stale open PRs in org %s (%d assigned, %d for review)", user, total, org, assignedCount, reviewCount)

	// Cache the successful result
	c.cache.SetWithTTL(cacheKey, total, cacheTTL)

	return total, nil
}

// searchPRCount searches for PRs matching a query and returns the count.
func (c *Client) searchPRCount(ctx context.Context, query string) (int, error) {
	encodedQuery := url.QueryEscape(query)
	apiURL := fmt.Sprintf("https://api.github.com/search/issues?q=%s&per_page=1", encodedQuery)
	slog.Info("  [DEBUG] Search query: %s", query)
	slog.Info("  [DEBUG] Full URL: %s", apiURL)
	resp, err := c.makeRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Info("[WARN] Failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode == http.StatusForbidden {
		return 0, fmt.Errorf("search API rate limit exceeded (status %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("search failed (status %d)", resp.StatusCode)
	}

	var searchResult struct {
		TotalCount int `json:"total_count"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&searchResult); err != nil {
		return 0, fmt.Errorf("failed to decode search result: %w", err)
	}

	return searchResult.TotalCount, nil
}

// makeCacheKey creates a cache key from multiple parts.
func makeCacheKey(parts ...string) string {
	return strings.Join(parts, ":")
}

// cachedPR retrieves a PR from cache if valid.
func (c *Client) cachedPR(owner, repo string, prNumber int, expectedUpdatedAt *time.Time) (*types.PullRequest, bool) {
	cacheKey := makeCacheKey("pr", owner, repo, fmt.Sprintf("%d", prNumber))
	cached, found := c.cache.Get(cacheKey)
	if !found {
		return nil, false
	}

	pr, ok := cached.(*types.PullRequest)
	if !ok {
		return nil, false
	}

	// If we have an expected updated_at time, validate the cache
	if expectedUpdatedAt != nil && !pr.UpdatedAt.Equal(*expectedUpdatedAt) {
		slog.Info("[CACHE] PR %s/%s#%d cache invalidated (updated_at changed)", owner, repo, prNumber)
		return nil, false
	}

	slog.Info("[CACHE] Using cached PR %s/%s#%d", owner, repo, prNumber)
	return pr, true
}

// cachePR stores a PR in cache.
func (c *Client) cachePR(pr *types.PullRequest) {
	cacheKey := makeCacheKey("pr", pr.Owner, pr.Repository, fmt.Sprintf("%d", pr.Number))
	// Use a longer TTL for PR caching (20 days) since we validate with updated_at
	c.cache.SetWithTTL(cacheKey, pr, 20*24*time.Hour)
}

// sanitizeURLForLogging removes sensitive query parameters from URLs.
func sanitizeURLForLogging(apiURL string) string {
	if idx := strings.Index(apiURL, "?"); idx != -1 {
		return apiURL[:idx] + "?[REDACTED]"
	}
	return apiURL
}
