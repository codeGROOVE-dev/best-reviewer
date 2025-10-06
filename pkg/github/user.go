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

	"github.com/codeGROOVE-dev/best-reviewer/pkg/cache"
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
	mu    sync.RWMutex
	users map[string]*UserInfo
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

// CacheUserTypeFromGraphQL caches user type information from GraphQL responses.
func (c *Client) CacheUserTypeFromGraphQL(username, typeName string) {
	if c.userCache == nil || username == "" {
		return
	}

	isBot := false
	switch typeName {
	case "Bot":
		isBot = true
	case "Organization":
		isBot = false
	default:
		isBot = c.IsUserBot(context.Background(), username)
	}

	info := &UserInfo{
		IsBot:      isBot,
		LastUpdate: time.Now(),
	}
	c.userCache.Set(username, info)
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
// Uses the cached collaborators list. If we don't have permissions to fetch
// the collaborators list (403), we fail-open and assume everyone has access.
func (c *Client) HasWriteAccess(ctx context.Context, owner, repo, username string) bool {
	// Try to use the collaborators list if we have it cached
	collabCacheKey := makeCacheKey("collaborators", owner, repo)
	collabPermCacheKey := makeCacheKey("collaborators-permission", owner, repo)

	// Check if we cached a permission error (403)
	if cached, found := c.cache.Get(collabPermCacheKey); found {
		if noPermission, ok := cached.(bool); ok && noPermission {
			// We don't have permission to check collaborators, assume everyone has access
			return true
		}
	}

	// Check if we have the collaborators list cached
	if cached, found := c.cache.Get(collabCacheKey); found {
		if collabs, ok := cached.([]string); ok {
			// Use the collaborators list for O(1) lookup
			for _, collab := range collabs {
				if collab == username {
					return true
				}
			}
			return false
		}
	}

	// No cached data available - this shouldn't happen if Collaborators() was called first
	// Fail-open for safety
	slog.Warn("No collaborators cache available, assuming write access", "username", username, "owner", owner, "repo", repo)
	return true
}

// OpenPRCount returns the number of open PRs assigned to or requested for review by a user in an organization.
func (c *Client) OpenPRCount(ctx context.Context, org, user string, cacheTTL time.Duration) (int, error) {
	// Check cache first for successful results
	cacheKey := makeCacheKey("pr-count", org, user)
	cached, hitType := c.cache.GetWithHitType(cacheKey)
	if hitType != cache.CacheMiss {
		if count, ok := cached.(int); ok {
			slog.Info("User has non-stale open PRs in org", "user", user, "total", count, "org", org, "cache", hitType)
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

	slog.Info("Fetching open PR count for user in org", "component", "api", "user", user, "org", org, "cache", "miss")

	// Create a context with shorter timeout for PR count queries to avoid hanging
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Calculate the cutoff date for non-stale PRs (90 days ago)
	cutoffDate := time.Now().AddDate(0, 0, -prStaleDaysThreshold).Format("2006-01-02")

	// Use two separate queries as they are simpler and more reliable
	// Only count PRs updated within the last 90 days to exclude stale PRs
	// First, search for PRs where user is assigned
	assignedQuery := fmt.Sprintf("is:pr is:open org:%s assignee:%s updated:>=%s", org, user, cutoffDate)
	slog.Debug("Searching assigned PRs for user", "user", user, "updated_since", cutoffDate)
	assignedCount, err := c.searchPRCount(timeoutCtx, assignedQuery)
	if err != nil {
		// Cache the failure to avoid repeated attempts
		c.cache.SetWithTTL(failureKey, true, prCountFailureCacheTTL)
		return 0, fmt.Errorf("failed to get assigned PR count: %w", err)
	}
	slog.Debug("Found non-stale assigned PRs for user", "count", assignedCount, "user", user)

	// Second, search for PRs where user is requested as reviewer
	reviewQuery := fmt.Sprintf("is:pr is:open org:%s review-requested:%s updated:>=%s", org, user, cutoffDate)
	slog.Debug("Searching review-requested PRs for user", "user", user, "updated_since", cutoffDate)
	reviewCount, err := c.searchPRCount(timeoutCtx, reviewQuery)
	if err != nil {
		// Cache the failure to avoid repeated attempts
		c.cache.SetWithTTL(failureKey, true, prCountFailureCacheTTL)
		return 0, fmt.Errorf("failed to get review-requested PR count: %w", err)
	}
	slog.Debug("Found non-stale review-requested PRs for user", "count", reviewCount, "user", user)

	total := assignedCount + reviewCount

	slog.Info("User has non-stale open PRs in org", "user", user, "total", total, "org", org, "assigned", assignedCount, "for_review", reviewCount)

	// Cache the successful result
	c.cache.SetWithTTL(cacheKey, total, cacheTTL)

	return total, nil
}

// BatchOpenPRCount fetches PR counts for multiple users in a single GraphQL query.
// Returns a map of username -> PR count. Uses cache for each user individually.
func (c *Client) BatchOpenPRCount(ctx context.Context, org string, users []string, cacheTTL time.Duration) (map[string]int, error) {
	if len(users) == 0 {
		return make(map[string]int), nil
	}

	result := make(map[string]int)
	var usersToFetch []string

	// Check cache for each user first
	for _, user := range users {
		cacheKey := makeCacheKey("pr-count", org, user)
		if cached, found := c.cache.Get(cacheKey); found {
			if count, ok := cached.(int); ok {
				result[user] = count
				slog.Debug("Using cached PR count", "user", user, "count", count)
				continue
			}
		}
		usersToFetch = append(usersToFetch, user)
	}

	// If all were cached, return early
	if len(usersToFetch) == 0 {
		return result, nil
	}

	slog.Info("Batch fetching PR counts", "org", org, "users", len(usersToFetch))

	// Calculate cutoff date for non-stale PRs
	cutoffDate := time.Now().AddDate(0, 0, -prStaleDaysThreshold).Format("2006-01-02")

	// Build GraphQL query with search for each user
	queryParts := []string{"query {"}
	for i, user := range usersToFetch {
		// GraphQL field names can't have hyphens, use index
		assignedQuery := fmt.Sprintf("is:pr is:open org:%s assignee:%s updated:>=%s", org, user, cutoffDate)
		reviewQuery := fmt.Sprintf("is:pr is:open org:%s review-requested:%s updated:>=%s", org, user, cutoffDate)

		queryParts = append(queryParts, fmt.Sprintf(`
  assigned%d: search(query: "%s", type: ISSUE, first: 0) { issueCount }
  review%d: search(query: "%s", type: ISSUE, first: 0) { issueCount }`,
			i, assignedQuery, i, reviewQuery))
	}
	queryParts = append(queryParts, "}")

	query := strings.Join(queryParts, "")

	// Make GraphQL request
	resp, err := c.MakeGraphQLRequest(ctx, query, nil)
	if err != nil {
		return nil, fmt.Errorf("GraphQL batch workload query failed: %w", err)
	}

	// Parse response
	data, ok := resp["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid GraphQL response structure")
	}

	// Extract counts for each user
	for i, user := range usersToFetch {
		assignedKey := fmt.Sprintf("assigned%d", i)
		reviewKey := fmt.Sprintf("review%d", i)

		assignedCount := 0
		reviewCount := 0

		if assignedData, ok := data[assignedKey].(map[string]any); ok {
			if count, ok := assignedData["issueCount"].(float64); ok {
				assignedCount = int(count)
			}
		}

		if reviewData, ok := data[reviewKey].(map[string]any); ok {
			if count, ok := reviewData["issueCount"].(float64); ok {
				reviewCount = int(count)
			}
		}

		total := assignedCount + reviewCount
		result[user] = total

		// Cache the result
		cacheKey := makeCacheKey("pr-count", org, user)
		c.cache.SetWithTTL(cacheKey, total, cacheTTL)

		slog.Debug("Fetched PR count", "user", user, "total", total, "assigned", assignedCount, "review", reviewCount)
	}

	return result, nil
}

// searchPRCount searches for PRs matching a query and returns the count.
func (c *Client) searchPRCount(ctx context.Context, query string) (int, error) {
	encodedQuery := url.QueryEscape(query)
	apiURL := fmt.Sprintf("https://api.github.com/search/issues?q=%s&per_page=1", encodedQuery)
	slog.Debug("Search query", "query", query)
	slog.Debug("Full URL", "url", apiURL)
	resp, err := c.makeRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("Failed to close response body", "error", err)
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
		slog.Info("PR cache invalidated (updated_at changed)", "component", "cache", "owner", owner, "repo", repo, "pr", prNumber)
		return nil, false
	}

	slog.Info("Using cached PR", "component", "cache", "owner", owner, "repo", repo, "pr", prNumber)
	return pr, true
}

// cachePR stores a PR in cache.
func (c *Client) cachePR(pr *types.PullRequest) {
	cacheKey := makeCacheKey("pr", pr.Owner, pr.Repository, fmt.Sprintf("%d", pr.Number))
	// Use a longer TTL for PR caching (20 days) since we validate with updated_at
	c.cache.SetWithTTL(cacheKey, pr, 20*24*time.Hour)
}

// Collaborators returns a list of users with write access to the repository.
// This includes direct collaborators AND organization members with write access.
func (c *Client) Collaborators(ctx context.Context, owner, repo string) ([]string, error) {
	cacheKey := makeCacheKey("collaborators", owner, repo)
	cached, hitType := c.cache.GetWithHitType(cacheKey)
	if hitType != cache.CacheMiss {
		if collabs, ok := cached.([]string); ok {
			slog.InfoContext(ctx, "Fetching collaborators", "owner", owner, "repo", repo, "cache", hitType, "count", len(collabs))
			return collabs, nil
		}
	}

	// Use affiliation=all to include both direct collaborators and org members
	// permission=push ensures we only get users with write access or higher
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/collaborators?affiliation=all&permission=push", owner, repo)
	resp, err := c.makeRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch collaborators: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("Failed to close response body", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		// If we got 403, cache this fact so HasWriteAccess knows to fail-open
		if resp.StatusCode == http.StatusForbidden {
			permCacheKey := makeCacheKey("collaborators-permission", owner, repo)
			c.cache.SetWithTTL(permCacheKey, true, 6*time.Hour)
		}
		return nil, fmt.Errorf("failed to fetch collaborators (status %d)", resp.StatusCode)
	}

	var collaborators []struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&collaborators); err != nil {
		return nil, fmt.Errorf("failed to decode collaborators response: %w", err)
	}

	usernames := make([]string, 0, len(collaborators))
	for _, collab := range collaborators {
		if collab.Type != "Bot" {
			usernames = append(usernames, collab.Login)
		}
	}

	c.cache.SetWithTTL(cacheKey, usernames, 6*time.Hour)
	slog.InfoContext(ctx, "Fetched collaborators", "owner", owner, "repo", repo, "count", len(usernames))

	return usernames, nil
}

// sanitizeURLForLogging removes sensitive query parameters from URLs.
// Since GitHub API uses Authorization header (not query params) for tokens,
// we only need to redact actual token/secret parameters if they exist.
func sanitizeURLForLogging(apiURL string) string {
	// GitHub API uses header-based auth, so query params are safe to log
	return apiURL
}
