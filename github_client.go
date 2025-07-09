package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// GitHubClient handles all GitHub API interactions.
type GitHubClient struct {
	token      string
	httpClient *http.Client
	cache      *cache
}

// PullRequest represents a GitHub pull request.
type PullRequest struct {
	Number       int
	Title        string
	State        string
	Draft        bool
	Author       string
	Assignees    []string
	Reviewers    []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastCommit   time.Time
	LastReview   time.Time
	Repository   string
	Owner        string
	ChangedFiles []ChangedFile
}

// ChangedFile represents a file changed in a pull request.
type ChangedFile struct {
	Filename  string
	Additions int
	Deletions int
	Patch     string
}

// ReviewerCandidate represents a potential reviewer with scoring metadata.
type ReviewerCandidate struct {
	Username         string
	ContextScore     int
	ActivityScore    int
	LastActivity     time.Time
	SelectionMethod  string
	AuthorAssociation string
}

// PRInfo holds basic PR information for historical analysis.
type PRInfo struct {
	Number    int
	Author    string
	Reviewers []string
	MergedAt  time.Time
}

// newGitHubClient creates a new GitHub API client using gh auth token.
func newGitHubClient() (*GitHubClient, error) {
	cmd := exec.Command("gh", "auth", "token")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get GitHub token: %w", err)
	}

	token := strings.TrimSpace(string(output))
	if token == "" {
		return nil, fmt.Errorf("no GitHub token found")
	}

	return &GitHubClient{
		token:      token,
		httpClient: &http.Client{Timeout: time.Duration(httpTimeout) * time.Second},
		cache:      newCache(cacheTTL),
	}, nil
}

// makeRequest makes an HTTP request to the GitHub API.
func (c *GitHubClient) makeRequest(ctx context.Context, method, url string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if method == "PATCH" || method == "POST" || method == "PUT" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}

// getPullRequest fetches a single pull request.
func (c *GitHubClient) getPullRequest(ctx context.Context, owner, repo string, prNumber int) (*PullRequest, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repo, prNumber)
	resp, err := c.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get PR (status %d)", resp.StatusCode)
	}

	var prData struct {
		Number    int       `json:"number"`
		Title     string    `json:"title"`
		State     string    `json:"state"`
		Draft     bool      `json:"draft"`
		CreatedAt string    `json:"created_at"`
		UpdatedAt string    `json:"updated_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
		Assignees []struct {
			Login string `json:"login"`
		} `json:"assignees"`
		RequestedReviewers []struct {
			Login string `json:"login"`
		} `json:"requested_reviewers"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&prData); err != nil {
		return nil, fmt.Errorf("failed to decode pull request: %w", err)
	}

	createdAt, _ := time.Parse(time.RFC3339, prData.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339, prData.UpdatedAt)

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
	changedFiles, err := c.getChangedFiles(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get changed files: %w", err)
	}
	pr.ChangedFiles = changedFiles

	// Get last commit time
	if lastCommit, err := c.getLastCommitTime(ctx, owner, repo, prData.Head.SHA); err == nil {
		pr.LastCommit = lastCommit
	}

	// Get last review time
	if lastReview, err := c.getLastReviewTime(ctx, owner, repo, prNumber); err == nil {
		pr.LastReview = lastReview
	}

	return pr, nil
}

// getOpenPullRequests fetches all open pull requests for a repository.
func (c *GitHubClient) getOpenPullRequests(ctx context.Context, owner, repo string) ([]*PullRequest, error) {
	var allPRs []*PullRequest
	page := 1

	for {
		url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls?state=open&per_page=100&page=%d", owner, repo, page)
		resp, err := c.makeRequest(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("failed to list PRs (status %d)", resp.StatusCode)
		}

		var prs []json.RawMessage
		if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
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

			pr, err := c.getPullRequest(ctx, owner, repo, prData.Number)
			if err != nil {
				log.Printf("Failed to get PR %d details: %v", prData.Number, err)
				continue
			}

			allPRs = append(allPRs, pr)
		}

		page++
		if len(prs) < 100 {
			break
		}
	}

	return allPRs, nil
}

// getChangedFiles fetches the list of changed files in a PR.
func (c *GitHubClient) getChangedFiles(ctx context.Context, owner, repo string, prNumber int) ([]ChangedFile, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/files?per_page=100", owner, repo, prNumber)
	resp, err := c.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var files []struct {
		Filename  string `json:"filename"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
		Patch     string `json:"patch"`
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

// getLastCommitTime gets the timestamp of the last commit.
func (c *GitHubClient) getLastCommitTime(ctx context.Context, owner, repo, sha string) (time.Time, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", owner, repo, sha)
	resp, err := c.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()

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

// getLastReviewTime gets the timestamp of the last review.
func (c *GitHubClient) getLastReviewTime(ctx context.Context, owner, repo string, prNumber int) (time.Time, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/reviews", owner, repo, prNumber)
	resp, err := c.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()

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

// getFilePatch gets the patch for a specific file in a PR.
func (c *GitHubClient) getFilePatch(ctx context.Context, owner, repo string, prNumber int, filename string) (string, error) {
	files, err := c.getChangedFiles(ctx, owner, repo, prNumber)
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
	files, err := rf.client.getChangedFiles(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, err
	}

	patchCache := make(map[string]string)
	for _, f := range files {
		patchCache[f.Filename] = f.Patch
	}

	return patchCache, nil
}

// getRecentPRCommenters gets users who recently commented on PRs.
func (rf *ReviewerFinder) getRecentPRCommenters(ctx context.Context, owner, repo string, excludeUsers []string) ([]string, error) {
	// For simplicity, return empty list - can be implemented later
	return []string{}, nil
}

// isUserBot checks if a user is a bot.
func (rf *ReviewerFinder) isUserBot(ctx context.Context, username string) bool {
	return strings.HasSuffix(username, "[bot]") || strings.HasSuffix(username, "-bot")
}

// hasWriteAccess checks if a user has write access to the repository.
func (rf *ReviewerFinder) hasWriteAccess(ctx context.Context, owner, repo, username string) bool {
	// For simplicity, assume all users have write access
	// In production, this would check collaborator status
	return true
}