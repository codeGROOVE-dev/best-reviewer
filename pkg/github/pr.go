package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/cache"
	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// PR-related constants.
const (
	perPageLimit = 100 // GitHub API per_page limit
)

// PullRequest fetches a single pull request.
func (c *Client) PullRequest(ctx context.Context, owner, repo string, prNumber int) (*types.PullRequest, error) {
	// Use prx if available for enhanced PR data including test status
	if c.prxClient != nil {
		// Use current time as reference for caching - prx will intelligently cache based on PR's actual updated_at
		prxData, err := c.prxClient.PullRequestWithReferenceTime(ctx, owner, repo, prNumber, time.Now())
		if err != nil {
			slog.Warn("Failed to fetch PR via prx, falling back to REST API", "error", err, "owner", owner, "repo", repo, "pr", prNumber)
			return c.pullRequestWithUpdatedAt(ctx, owner, repo, prNumber, nil)
		}
		return c.convertPrxToPullRequest(ctx, owner, repo, prxData)
	}
	return c.pullRequestWithUpdatedAt(ctx, owner, repo, prNumber, nil)
}

// pullRequestWithUpdatedAt fetches a single pull request with cache validation based on updated_at.
func (c *Client) pullRequestWithUpdatedAt(
	ctx context.Context, owner, repo string, prNumber int, expectedUpdatedAt *time.Time,
) (*types.PullRequest, error) {
	// Check cache first
	if pr, found := c.cachedPR(owner, repo, prNumber, expectedUpdatedAt); found {
		return pr, nil
	}

	slog.Info("Fetching PR details to get title, state, author, assignees, reviewers, and metadata", "component", "api", "owner", owner, "repo", repo, "pr", prNumber)
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repo, prNumber)
	resp, err := c.makeRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("Failed to close response body", "error", err)
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
		slog.Warn("Failed to parse created_at time", "error", err)
		createdAt = time.Now()
	}
	updatedAt, err := time.Parse(time.RFC3339, prData.UpdatedAt)
	if err != nil {
		slog.Warn("Failed to parse updated_at time", "error", err)
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

	pr := &types.PullRequest{
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
	changedFiles, err := c.ChangedFiles(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get changed files: %w", err)
	}
	pr.ChangedFiles = changedFiles

	// Get last commit time
	lastCommit, err := c.lastCommitTime(ctx, owner, repo, prData.Head.SHA)
	if err != nil {
		slog.Warn("Failed to get last commit time for PR (degrading gracefully)", "pr", prNumber, "error", err)
		pr.LastCommit = updatedAt // Fallback to updated time
	} else {
		pr.LastCommit = lastCommit
	}

	// Get last review time
	lastReview, err := c.lastReviewTime(ctx, owner, repo, prNumber)
	if err != nil {
		slog.Warn("Failed to get last review time for PR (degrading gracefully)", "pr", prNumber, "error", err)
		// Leave LastReview as zero value if we can't get it
	} else {
		pr.LastReview = lastReview
	}

	// Cache the PR
	c.cachePR(pr)

	return pr, nil
}

// OpenPullRequestsForOrg fetches all open pull requests across all repositories in an organization.
func (c *Client) OpenPullRequestsForOrg(ctx context.Context, org string) ([]*types.PullRequest, error) {
	// Use GitHub search API to find all open PRs in the org without reviewers
	// review:none filters to PRs that don't have any requested reviewers
	query := fmt.Sprintf("is:pr is:open org:%s review:none", org)
	slog.Info("Searching for open PRs without reviewers across organization", "org", org)

	allPRs := make([]*types.PullRequest, 0, 100) // Pre-allocate for typical case
	page := 1
	perPage := 100

	for {
		encodedQuery := url.QueryEscape(query)
		apiURL := fmt.Sprintf("https://api.github.com/search/issues?q=%s&per_page=%d&page=%d", encodedQuery, perPage, page)

		resp, err := c.makeRequest(ctx, "GET", apiURL, nil) //nolint:bodyclose // body is closed immediately, not deferred
		if err != nil {
			return nil, fmt.Errorf("failed to search PRs: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, err := io.ReadAll(resp.Body)
			drainAndCloseBody(resp.Body)
			if err != nil {
				return nil, fmt.Errorf("failed to search PRs: status %d (could not read body: %w)", resp.StatusCode, err)
			}
			return nil, fmt.Errorf("failed to search PRs: status %d: %s", resp.StatusCode, string(body))
		}

		var searchResult struct {
			Items []struct {
				Number      int    `json:"number"`
				Title       string `json:"title"`
				State       string `json:"state"`
				Draft       bool   `json:"draft"`
				UpdatedAt   string `json:"updated_at"`
				PullRequest struct {
					URL string `json:"url"`
				} `json:"pull_request"`
				RepositoryURL string `json:"repository_url"`
			} `json:"items"`
			TotalCount int `json:"total_count"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&searchResult); err != nil {
			drainAndCloseBody(resp.Body)
			return nil, fmt.Errorf("failed to decode search results: %w", err)
		}
		drainAndCloseBody(resp.Body)

		slog.Debug("Search results page", "org", org, "page", page, "items", len(searchResult.Items), "total_count", searchResult.TotalCount)

		if len(searchResult.Items) == 0 {
			break
		}

		// For each PR, we need to fetch full details
		for _, item := range searchResult.Items {
			// Extract repo from repository_url: https://api.github.com/repos/owner/repo
			parts := strings.Split(item.RepositoryURL, "/")
			if len(parts) < 2 {
				slog.Warn("Invalid repository URL", "url", item.RepositoryURL, "org", org)
				continue
			}
			repo := parts[len(parts)-1]

			// Fetch full PR details (this uses prx which has caching and retry)
			pr, err := c.PullRequest(ctx, org, repo, item.Number)
			if err != nil {
				slog.Warn("Failed to fetch PR details", "org", org, "repo", repo, "pr", item.Number, "error", err)
				continue
			}

			allPRs = append(allPRs, pr)
		}

		if len(searchResult.Items) < perPage {
			break
		}

		page++
	}

	slog.Info("Found open PRs for org", "org", org, "count", len(allPRs))
	return allPRs, nil
}

// OpenPullRequests fetches all open pull requests for a repository.
func (c *Client) OpenPullRequests(ctx context.Context, owner, repo string) ([]*types.PullRequest, error) {
	slog.Info("Fetching all open PRs for repository to identify candidates for reviewer assignment", "component", "api", "owner", owner, "repo", repo)
	var allPRs []*types.PullRequest
	page := 1

	for {
		slog.Info("Requesting page of open PRs (pagination)", "component", "api", "owner", owner, "repo", repo, "page", page)
		apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls?state=open&per_page=100&page=%d", owner, repo, page)

		// Extract API call to avoid defer in loop
		prs, shouldBreak, err := func() ([]json.RawMessage, bool, error) {
			resp, err := c.makeRequest(ctx, "GET", apiURL, nil)
			if err != nil {
				return nil, false, err
			}
			defer func() {
				if err := resp.Body.Close(); err != nil {
					slog.Warn("Failed to close response body", "error", err)
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

			pr, err := c.PullRequest(ctx, owner, repo, prData.Number)
			if err != nil {
				slog.Error("Failed to get PR details (skipping)", "pr", prData.Number, "error", err)
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

// ChangedFiles fetches the list of changed files in a PR.
func (c *Client) ChangedFiles(ctx context.Context, owner, repo string, prNumber int) ([]types.ChangedFile, error) {
	// Check cache first
	cacheKey := fmt.Sprintf("pr-files:%s/%s:%d", owner, repo, prNumber)
	cached, hitType := c.cache.Lookup(cacheKey)
	if hitType != cache.CacheMiss {
		if files, ok := cached.([]types.ChangedFile); ok {
			slog.Info("Fetching changed files for PR to determine modified files for reviewer expertise matching", "component", "api", "owner", owner, "repo", repo, "pr", prNumber, "cache", hitType)
			return files, nil
		}
	}

	slog.Info("Fetching changed files for PR to determine modified files for reviewer expertise matching", "component", "api", "owner", owner, "repo", repo, "pr", prNumber, "cache", "miss")
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/files?per_page=100", owner, repo, prNumber)
	resp, err := c.makeRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("Failed to close response body", "error", err)
		}
	}()

	var files []struct {
		Filename  string `json:"filename"`
		Status    string `json:"status"`
		Patch     string `json:"patch"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, err
	}

	changedFiles := make([]types.ChangedFile, 0, len(files))
	for _, f := range files {
		changedFiles = append(changedFiles, types.ChangedFile{
			Filename:  f.Filename,
			Status:    f.Status,
			Additions: f.Additions,
			Deletions: f.Deletions,
			Patch:     f.Patch,
		})
	}

	// Cache the result
	// TODO: Use different TTLs based on PR state:
	// - Current PR being examined: Don't cache (or very short TTL like 1 minute)
	// - Historical merged PR: 28 days (immutable)
	// For now, using 6 hours as compromise
	c.cache.SetWithTTL(cacheKey, changedFiles, 6*time.Hour)

	return changedFiles, nil
}

// lastCommitTime returns the timestamp of the last commit.
func (c *Client) lastCommitTime(ctx context.Context, owner, repo, sha string) (time.Time, error) {
	slog.Info("Fetching commit details to get last commit timestamp for PR staleness analysis", "component", "api", "owner", owner, "repo", repo, "sha", sha)
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", owner, repo, sha)
	resp, err := c.makeRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("Failed to close response body", "error", err)
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
func (c *Client) lastReviewTime(ctx context.Context, owner, repo string, prNumber int) (time.Time, error) {
	slog.Info("Fetching review history for PR to determine last review timestamp for staleness detection", "component", "api", "owner", owner, "repo", repo, "pr", prNumber)
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/reviews", owner, repo, prNumber)
	resp, err := c.makeRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("Failed to close response body", "error", err)
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

// FilePatch returns the patch for a specific file in a PR.
func (c *Client) FilePatch(ctx context.Context, owner, repo string, prNumber int, filename string) (string, error) {
	files, err := c.ChangedFiles(ctx, owner, repo, prNumber)
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

// convertPrxToPullRequest converts prx.PullRequestData to types.PullRequest.
func (c *Client) convertPrxToPullRequest(ctx context.Context, owner, repo string, prxData any) (*types.PullRequest, error) {
	// Type assert to get the prx data structure
	// We use interface{} to avoid import cycles, so we need to use reflection or type assertion
	type prxPullRequest struct {
		Number             int
		Title              string
		State              string
		Draft              bool
		Author             string
		TestState          string
		CreatedAt          time.Time
		UpdatedAt          time.Time
		HeadSHA            string
		RequestedReviewers []string
		Assignees          []string
	}

	type prxPullRequestData struct {
		PullRequest prxPullRequest
	}

	// Convert via JSON marshaling/unmarshaling to handle the interface{}
	jsonData, err := json.Marshal(prxData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal prx data: %w", err)
	}

	var data prxPullRequestData
	if err := json.Unmarshal(jsonData, &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal prx data: %w", err)
	}

	pr := &types.PullRequest{
		Number:     data.PullRequest.Number,
		Title:      data.PullRequest.Title,
		State:      data.PullRequest.State,
		Draft:      data.PullRequest.Draft,
		Author:     data.PullRequest.Author,
		Repository: repo,
		Owner:      owner,
		CreatedAt:  data.PullRequest.CreatedAt,
		UpdatedAt:  data.PullRequest.UpdatedAt,
		TestState:  data.PullRequest.TestState,
		Reviewers:  data.PullRequest.RequestedReviewers,
		Assignees:  data.PullRequest.Assignees,
		// These fields will be populated by separate API calls if needed
		LastCommit:   time.Time{},
		LastReview:   time.Time{},
		ChangedFiles: []types.ChangedFile{},
	}

	// Fetch changed files separately if needed
	changedFiles, err := c.ChangedFiles(ctx, owner, repo, data.PullRequest.Number)
	if err != nil {
		slog.Warn("Failed to fetch changed files for prx PR", "error", err, "owner", owner, "repo", repo, "pr", data.PullRequest.Number)
	} else {
		pr.ChangedFiles = changedFiles
	}

	// Fetch last commit time separately if we have the SHA
	if data.PullRequest.HeadSHA != "" {
		lastCommit, err := c.lastCommitTime(ctx, owner, repo, data.PullRequest.HeadSHA)
		if err != nil {
			slog.Warn("Failed to fetch last commit time for prx PR", "error", err, "owner", owner, "repo", repo, "pr", data.PullRequest.Number)
		} else {
			pr.LastCommit = lastCommit
		}
	}

	// Fetch last review time separately
	lastReview, err := c.lastReviewTime(ctx, owner, repo, data.PullRequest.Number)
	if err != nil {
		slog.Warn("Failed to fetch last review time for prx PR", "error", err, "owner", owner, "repo", repo, "pr", data.PullRequest.Number)
	} else {
		pr.LastReview = lastReview
	}

	return pr, nil
}
