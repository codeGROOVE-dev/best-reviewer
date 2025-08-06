package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// GitHubClient handles all GitHub API interactions.
type GitHubClient struct {
	httpClient *http.Client
	cache      *cache
	userCache  *userCache
	token      string
	isAppAuth  bool // Whether using GitHub App authentication
}

// PullRequest represents a GitHub pull request.
type PullRequest struct {
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastCommit   time.Time
	LastReview   time.Time
	Title        string
	State        string
	Author       string
	Repository   string
	Owner        string
	ChangedFiles []ChangedFile
	Assignees    []string
	Reviewers    []string
	Number       int
	Draft        bool
}

// ChangedFile represents a file changed in a pull request.
type ChangedFile struct {
	Filename  string
	Patch     string
	Additions int
	Deletions int
}

// ReviewerCandidate represents a potential reviewer with scoring metadata.
type ReviewerCandidate struct {
	LastActivity      time.Time
	Username          string
	SelectionMethod   string
	AuthorAssociation string
	ContextScore      int
	ActivityScore     int
}

// PRInfo holds basic PR information for historical analysis.
type PRInfo struct {
	MergedAt  time.Time
	Author    string
	Reviewers []string
	Number    int
}

// newGitHubClient creates a new GitHub API client using gh auth token or GitHub App authentication.
func newGitHubClient(ctx context.Context, useAppAuth bool) (*GitHubClient, error) {
	var token string
	var isAppAuth bool

	if useAppAuth {
		// For GitHub App authentication, check for required environment variables
		appToken := os.Getenv("GITHUB_APP_TOKEN")
		if appToken == "" {
			return nil, errors.New("GITHUB_APP_TOKEN environment variable is required for --app mode")
		}
		token = appToken
		isAppAuth = true
		log.Print("[AUTH] Using GitHub App authentication")
	} else {
		// Use gh CLI authentication
		cmd := exec.CommandContext(ctx, "gh", "auth", "token")
		output, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("failed to get GitHub token: %w", err)
		}

		token = strings.TrimSpace(string(output))
		if token == "" {
			return nil, errors.New("no GitHub token found")
		}
		isAppAuth = false
		log.Print("[AUTH] Using gh CLI authentication")
	}

	c := &cache{
		entries: make(map[string]cacheEntry),
		ttl:     cacheTTL,
	}
	go c.cleanupExpired()

	return &GitHubClient{
		httpClient: &http.Client{Timeout: time.Duration(httpTimeout) * time.Second},
		cache:      c,
		userCache:  &userCache{users: make(map[string]*userInfo)},
		token:      token,
		isAppAuth:  isAppAuth,
	}, nil
}

// makeRequest makes an HTTP request to the GitHub API with retry logic.
func (c *GitHubClient) makeRequest(ctx context.Context, method, apiURL string, body any) (*http.Response, error) {
	log.Printf("[HTTP] %s %s", method, apiURL)

	var resp *http.Response
	err := retryWithBackoff(ctx, fmt.Sprintf("%s %s", method, apiURL), func() error {
		var bodyReader io.Reader
		if body != nil {
			bodyBytes, err := json.Marshal(body)
			if err != nil {
				return fmt.Errorf("failed to marshal request body: %w", err)
			}
			bodyReader = bytes.NewReader(bodyBytes)
		}

		req, err := http.NewRequestWithContext(ctx, method, apiURL, bodyReader)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}

		if c.isAppAuth {
			req.Header.Set("Authorization", "Bearer "+c.token)
			req.Header.Set("Accept", "application/vnd.github.v3+json")
		} else {
			req.Header.Set("Authorization", "token "+c.token)
			req.Header.Set("Accept", "application/vnd.github.v3+json")
		}
		if method == "PATCH" || method == "POST" || method == "PUT" {
			req.Header.Set("Content-Type", "application/json")
		}

		var localResp *http.Response
		localResp, err = c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("request failed: %w", err)
		}

		// Check for rate limiting or server errors that should trigger retry
		if localResp.StatusCode == http.StatusTooManyRequests ||
			(localResp.StatusCode >= http.StatusInternalServerError && localResp.StatusCode < 600) {
			body, err := io.ReadAll(localResp.Body)
			if err != nil {
				if closeErr := localResp.Body.Close(); closeErr != nil {
					log.Printf("[WARN] Failed to close response body: %v", closeErr)
				}
				return fmt.Errorf("failed to read error response: %w", err)
			}
			if err := localResp.Body.Close(); err != nil {
				log.Printf("[WARN] Failed to close response body: %v", err)
			}
			return fmt.Errorf("http %d: %s", localResp.StatusCode, string(body))
		}

		// Success - assign to outer resp variable
		resp = localResp
		return nil
	})
	if err != nil {
		return nil, err
	}

	log.Printf("[HTTP] %s %s - Status: %d", method, apiURL, resp.StatusCode)
	return resp, nil
}

// pullRequest fetches a single pull request.
func (c *GitHubClient) pullRequest(ctx context.Context, owner, repo string, prNumber int) (*PullRequest, error) {
	return c.pullRequestWithUpdatedAt(ctx, owner, repo, prNumber, nil)
}

// pullRequestWithUpdatedAt fetches a single pull request with cache validation based on updated_at.
func (c *GitHubClient) pullRequestWithUpdatedAt(
	ctx context.Context, owner, repo string, prNumber int, expectedUpdatedAt *time.Time,
) (*PullRequest, error) {
	// Check cache first
	if pr, found := c.cachedPR(owner, repo, prNumber, expectedUpdatedAt); found {
		return pr, nil
	}

	log.Printf("[API] Fetching PR details for %s/%s#%d to get title, state, author, assignees, reviewers, and metadata", owner, repo, prNumber)
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repo, prNumber)
	resp, err := c.makeRequest(ctx, httpMethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[WARN] Failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get PR (status %d)", resp.StatusCode)
	}

	var prData struct {
		Title     string `json:"title"`
		State     string `json:"state"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Assignees []struct {
			Login string `json:"login"`
		} `json:"assignees"`
		RequestedReviewers []struct {
			Login string `json:"login"`
		} `json:"requested_reviewers"`
		Number int  `json:"number"`
		Draft  bool `json:"draft"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&prData); err != nil {
		return nil, fmt.Errorf("failed to decode pull request: %w", err)
	}

	createdAt, err := time.Parse(time.RFC3339, prData.CreatedAt)
	if err != nil {
		log.Printf("[WARN] Failed to parse created_at time: %v", err)
		createdAt = time.Now()
	}
	updatedAt, err := time.Parse(time.RFC3339, prData.UpdatedAt)
	if err != nil {
		log.Printf("[WARN] Failed to parse updated_at time: %v", err)
		updatedAt = time.Now()
	}

	var reviewers []string
	for _, reviewer := range prData.RequestedReviewers {
		reviewers = append(reviewers, reviewer.Login)
	}

	var assignees []string
	for _, assignee := range prData.Assignees {
		assignees = append(assignees, assignee.Login)
	}

	pr := &PullRequest{
		Number:     prData.Number,
		Title:      prData.Title,
		State:      prData.State,
		Draft:      prData.Draft,
		Author:     prData.User.Login,
		Assignees:  assignees,
		CreatedAt:  createdAt,
		UpdatedAt:  updatedAt,
		Repository: repo,
		Owner:      owner,
		Reviewers:  reviewers,
	}

	// Get changed files
	changedFiles, err := c.changedFiles(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get changed files: %w", err)
	}
	pr.ChangedFiles = changedFiles

	// Get last commit time
	lastCommit, err := c.lastCommitTime(ctx, owner, repo, prData.Head.SHA)
	if err != nil {
		log.Printf("[WARN] Failed to get last commit time for PR %d: %v (degrading gracefully)", prNumber, err)
		pr.LastCommit = updatedAt // Fallback to updated time
	} else {
		pr.LastCommit = lastCommit
	}

	// Get last review time
	lastReview, err := c.lastReviewTime(ctx, owner, repo, prNumber)
	if err != nil {
		log.Printf("[WARN] Failed to get last review time for PR %d: %v (degrading gracefully)", prNumber, err)
		// Leave LastReview as zero value if we can't get it
	} else {
		pr.LastReview = lastReview
	}

	// Cache the PR
	c.cachePR(pr)

	return pr, nil
}

// openPullRequests fetches all open pull requests for a repository.
func (c *GitHubClient) openPullRequests(ctx context.Context, owner, repo string) ([]*PullRequest, error) {
	log.Printf("[API] Fetching all open PRs for repository %s/%s to identify candidates for reviewer assignment", owner, repo)
	var allPRs []*PullRequest
	page := 1

	for {
		log.Printf("[API] Requesting page %d of open PRs for %s/%s (pagination)", page, owner, repo)
		apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls?state=open&per_page=100&page=%d", owner, repo, page)

		// Extract API call to avoid defer in loop
		prs, shouldBreak, err := func() ([]json.RawMessage, bool, error) {
			resp, err := c.makeRequest(ctx, httpMethodGet, apiURL, nil)
			if err != nil {
				return nil, false, err
			}
			defer func() {
				if err := resp.Body.Close(); err != nil {
					log.Printf("[WARN] Failed to close response body: %v", err)
				}
			}()

			if resp.StatusCode != http.StatusOK {
				return nil, false, fmt.Errorf("failed to list PRs (status %d)", resp.StatusCode)
			}

			var prs []json.RawMessage
			if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
				return nil, false, err
			}

			return prs, len(prs) < perPageLimit, nil
		}()
		if err != nil {
			return nil, err
		}

		if len(prs) == 0 {
			break
		}

		for _, prJSON := range prs {
			var prData struct {
				Number int `json:"number"`
			}
			if err := json.Unmarshal(prJSON, &prData); err != nil {
				continue
			}

			pr, err := c.pullRequest(ctx, owner, repo, prData.Number)
			if err != nil {
				log.Printf("[ERROR] Failed to get PR %d details: %v (skipping)", prData.Number, err)
				continue
			}

			allPRs = append(allPRs, pr)
		}

		page++
		if shouldBreak {
			break
		}
	}

	return allPRs, nil
}

// changedFiles fetches the list of changed files in a PR.
func (c *GitHubClient) changedFiles(ctx context.Context, owner, repo string, prNumber int) ([]ChangedFile, error) {
	log.Printf("[API] Fetching changed files for PR %s/%s#%d to determine modified files for reviewer expertise matching", owner, repo, prNumber)
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/files?per_page=100", owner, repo, prNumber)
	resp, err := c.makeRequest(ctx, httpMethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[WARN] Failed to close response body: %v", err)
		}
	}()

	var files []struct {
		Filename  string `json:"filename"`
		Patch     string `json:"patch"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, err
	}

	changedFiles := make([]ChangedFile, 0, len(files))
	for _, f := range files {
		changedFiles = append(changedFiles, ChangedFile{
			Filename:  f.Filename,
			Additions: f.Additions,
			Deletions: f.Deletions,
			Patch:     f.Patch,
		})
	}

	return changedFiles, nil
}

// lastCommitTime returns the timestamp of the last commit.
func (c *GitHubClient) lastCommitTime(ctx context.Context, owner, repo, sha string) (time.Time, error) {
	log.Printf("[API] Fetching commit details for %s/%s@%s to get last commit timestamp for PR staleness analysis", owner, repo, sha)
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", owner, repo, sha)
	resp, err := c.makeRequest(ctx, httpMethodGet, apiURL, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[WARN] Failed to close response body: %v", err)
		}
	}()

	var commit struct {
		Commit struct {
			Author struct {
				Date string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&commit); err != nil {
		return time.Time{}, err
	}

	return time.Parse(time.RFC3339, commit.Commit.Author.Date)
}

// lastReviewTime returns the timestamp of the last review.
func (c *GitHubClient) lastReviewTime(ctx context.Context, owner, repo string, prNumber int) (time.Time, error) {
	log.Printf("[API] Fetching review history for PR %s/%s#%d to determine last review timestamp for staleness detection", owner, repo, prNumber)
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/reviews", owner, repo, prNumber)
	resp, err := c.makeRequest(ctx, httpMethodGet, apiURL, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[WARN] Failed to close response body: %v", err)
		}
	}()

	var reviews []struct {
		SubmittedAt string `json:"submitted_at"`
		State       string `json:"state"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&reviews); err != nil {
		return time.Time{}, err
	}

	var lastTime time.Time
	for _, review := range reviews {
		if review.State == "APPROVED" || review.State == "CHANGES_REQUESTED" || review.State == "COMMENTED" {
			if t, err := time.Parse(time.RFC3339, review.SubmittedAt); err == nil && t.After(lastTime) {
				lastTime = t
			}
		}
	}

	return lastTime, nil
}

// filePatch returns the patch for a specific file in a PR.
func (c *GitHubClient) filePatch(ctx context.Context, owner, repo string, prNumber int, filename string) (string, error) {
	files, err := c.changedFiles(ctx, owner, repo, prNumber)
	if err != nil {
		return "", err
	}

	for _, f := range files {
		if f.Filename == filename {
			return f.Patch, nil
		}
	}

	return "", fmt.Errorf("file %s not found in PR %d", filename, prNumber)
}

// fetchAllPRFiles fetches all file patches for a PR at once.
func (rf *ReviewerFinder) fetchAllPRFiles(ctx context.Context, owner, repo string, prNumber int) (map[string]string, error) {
	files, err := rf.client.changedFiles(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, err
	}

	patchCache := make(map[string]string)
	for _, f := range files {
		patchCache[f.Filename] = f.Patch
	}

	return patchCache, nil
}

// recentPRCommenters returns users who recently commented on PRs.
func (*ReviewerFinder) recentPRCommenters(ctx context.Context, _ string, _ string, _ []string) ([]string, error) {
	// For simplicity, return empty list - can be implemented later
	return []string{}, nil
}

// isUserBot checks if a user is a bot.
func (*ReviewerFinder) isUserBot(_ context.Context, username string) bool {
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

// hasWriteAccess checks if a user has write access to the repository.
func (*ReviewerFinder) hasWriteAccess(ctx context.Context, _ string, _ string, _ string) bool {
	// For simplicity, assume all users have write access
	// In production, this would check collaborator status
	return true
}

// openPRCount returns the number of open PRs assigned to or requested for review by a user in an organization.
func (c *GitHubClient) openPRCount(ctx context.Context, org, user string, cacheTTL time.Duration) (int, error) {
	// Check cache first for successful results
	cacheKey := makeCacheKey("pr-count", org, user)
	if cached, found := c.cache.value(cacheKey); found {
		if count, ok := cached.(int); ok {
			log.Printf("    [CACHE] User %s has %d non-stale open PRs in org %s (cached)", user, count, org)
			return count, nil
		}
	}

	// Check if we recently failed to get PR count for this user to avoid repeated failures
	failureKey := makeCacheKey("pr-count-failure", org, user)
	if _, found := c.cache.value(failureKey); found {
		return 0, errors.New("recently failed to get PR count (cached failure)")
	}

	// Validate that the organization and user are not empty
	if org == "" || user == "" {
		return 0, fmt.Errorf("invalid organization (%s) or user (%s)", org, user)
	}

	log.Printf("  [API] Fetching open PR count for user %s in org %s", user, org)

	// Create a context with shorter timeout for PR count queries to avoid hanging
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Calculate the cutoff date for non-stale PRs (90 days ago)
	cutoffDate := time.Now().AddDate(0, 0, -prStaleDaysThreshold).Format("2006-01-02")

	// Use two separate queries as they are simpler and more reliable
	// Only count PRs updated within the last 90 days to exclude stale PRs
	// First, search for PRs where user is assigned
	assignedQuery := fmt.Sprintf("is:pr is:open org:%s assignee:%s updated:>=%s", org, user, cutoffDate)
	log.Printf("  [DEBUG] Searching assigned PRs for %s (updated since %s)", user, cutoffDate)
	assignedCount, err := c.searchPRCount(timeoutCtx, assignedQuery)
	if err != nil {
		// Cache the failure to avoid repeated attempts
		c.cache.setWithTTL(failureKey, true, prCountFailureCacheTTL)
		return 0, fmt.Errorf("failed to get assigned PR count: %w", err)
	}
	log.Printf("  [DEBUG] Found %d non-stale assigned PRs for %s", assignedCount, user)

	// Second, search for PRs where user is requested as reviewer
	reviewQuery := fmt.Sprintf("is:pr is:open org:%s review-requested:%s updated:>=%s", org, user, cutoffDate)
	log.Printf("  [DEBUG] Searching review-requested PRs for %s (updated since %s)", user, cutoffDate)
	reviewCount, err := c.searchPRCount(timeoutCtx, reviewQuery)
	if err != nil {
		// Cache the failure to avoid repeated attempts
		c.cache.setWithTTL(failureKey, true, prCountFailureCacheTTL)
		return 0, fmt.Errorf("failed to get review-requested PR count: %w", err)
	}
	log.Printf("  [DEBUG] Found %d non-stale review-requested PRs for %s", reviewCount, user)

	total := assignedCount + reviewCount

	log.Printf("    📊 User %s has %d non-stale open PRs in org %s (%d assigned, %d for review)", user, total, org, assignedCount, reviewCount)

	// Cache the successful result
	c.cache.setWithTTL(cacheKey, total, cacheTTL)

	return total, nil
}

// searchPRCount searches for PRs matching a query and returns the count.
func (c *GitHubClient) searchPRCount(ctx context.Context, query string) (int, error) {
	encodedQuery := url.QueryEscape(query)
	apiURL := fmt.Sprintf("https://api.github.com/search/issues?q=%s&per_page=1", encodedQuery)
	log.Printf("  [DEBUG] Search query: %s", query)
	log.Printf("  [DEBUG] Full URL: %s", apiURL)
	resp, err := c.makeRequest(ctx, httpMethodGet, apiURL, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[WARN] Failed to close response body: %v", err)
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

// Installation represents a GitHub App installation.
//
//nolint:govet // Field alignment optimization not worth the readability cost
type Installation struct {
	ID      int `json:"id"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
}

// listAppInstallations returns all organizations where this GitHub app is installed.
func (c *GitHubClient) listAppInstallations(ctx context.Context) ([]string, error) {
	if !c.isAppAuth {
		return nil, errors.New("app installations can only be listed with GitHub App authentication")
	}

	log.Print("[API] Fetching GitHub App installations")
	apiURL := "https://api.github.com/app/installations"
	resp, err := c.makeRequest(ctx, httpMethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get app installations: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[WARN] Failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list installations (status %d)", resp.StatusCode)
	}

	var installations []Installation
	if err := json.NewDecoder(resp.Body).Decode(&installations); err != nil {
		return nil, fmt.Errorf("failed to decode installations: %w", err)
	}

	var orgs []string
	for _, installation := range installations {
		// Only include organization accounts, not user accounts
		if installation.Account.Type == "Organization" {
			orgs = append(orgs, installation.Account.Login)
			log.Printf("[APP] Found installation in org: %s (ID: %d)", installation.Account.Login, installation.ID)
		}
	}

	log.Printf("[APP] Found %d organization installations", len(orgs))
	return orgs, nil
}
