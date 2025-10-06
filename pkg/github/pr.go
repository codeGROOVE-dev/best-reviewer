package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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
	cached, hitType := c.cache.GetWithHitType(cacheKey)
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
