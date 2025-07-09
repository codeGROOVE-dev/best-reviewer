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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GitHubClient wraps the GitHub API interactions
type GitHubClient struct {
	token      string
	httpClient *http.Client
}

// PullRequest represents a GitHub pull request
type PullRequest struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	State       string    `json:"state"`
	Draft       bool      `json:"draft"`
	Author      string    `json:"author"`
	Assignees   []string  `json:"assignees"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Repository  string    `json:"repository"`
	Owner       string    `json:"owner"`
	Reviewers   []string  `json:"reviewers"`
	LastCommit  time.Time `json:"last_commit"`
	LastReview  time.Time `json:"last_review"`
	ChangedFiles []ChangedFile `json:"changed_files"`
}

// ChangedFile represents a file changed in a PR
type ChangedFile struct {
	Filename  string `json:"filename"`
	Changes   int    `json:"changes"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Status    string `json:"status"`
}

// BlameData represents blame information for a file
type BlameData struct {
	Lines []BlameLine `json:"lines"`
}

// BlameLine represents a single line in blame data
type BlameLine struct {
	LineNumber int    `json:"line_number"`
	Author     string `json:"author"`
	CommitSHA  string `json:"commit_sha"`
	PRNumber   int    `json:"pr_number"`
}

// ReviewerCandidate represents a potential reviewer
type ReviewerCandidate struct {
	Username         string
	ContextScore     int
	ActivityScore    int
	LastActivity     time.Time
	SelectionMethod  string // Tracks how this reviewer was selected
	AuthorAssociation string // GitHub author association (OWNER, MEMBER, COLLABORATOR, etc.)
}

// newGitHubClient creates a new GitHub API client using gh auth token
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
		httpClient: &http.Client{Timeout: 120 * time.Second}, // 2 minute timeout for slow API calls
	}, nil
}

// makeRequest makes an HTTP request to the GitHub API
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
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// Log the API request
	log.Printf("GitHub API Request: %s %s", method, url)

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	duration := time.Since(start)

	if err != nil {
		log.Printf("GitHub API Error: %s %s failed after %v: %v", method, url, duration, err)
		return nil, err
	}

	log.Printf("GitHub API Response: %s %s completed in %v with status %d", method, url, duration, resp.StatusCode)
	return resp, nil
}

// makeGraphQLRequest makes a GraphQL request to GitHub API v4 with retry logic
func (c *GitHubClient) makeGraphQLRequest(ctx context.Context, query string, variables map[string]interface{}) (map[string]interface{}, error) {
	// Log the GraphQL query (truncate if too long)
	queryPreview := query
	if len(queryPreview) > 200 {
		queryPreview = queryPreview[:200] + "..."
	}
	queryPreview = strings.ReplaceAll(queryPreview, "\n", " ")
	queryPreview = strings.ReplaceAll(queryPreview, "\t", " ")
	
	log.Printf("GitHub GraphQL Request: %s (variables: %v)", queryPreview, variables)

	payload := map[string]interface{}{
		"query": query,
	}
	if variables != nil {
		payload["variables"] = variables
	}

	// Retry up to 3 times with exponential backoff
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			log.Printf("GraphQL request failed, retrying in %v (attempt %d/3)", backoff, attempt+1)
			time.Sleep(backoff)
		}

		resp, err := c.makeRequest(ctx, "POST", "https://api.github.com/graphql", payload)
		if err != nil {
			lastErr = fmt.Errorf("failed to make GraphQL request: %w", err)
			continue
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		
		// Check for rate limit
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			log.Printf("Rate limited by GitHub API (status %d)", resp.StatusCode)
			lastErr = fmt.Errorf("rate limited by GitHub API")
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("GraphQL request failed with status %d: %s", resp.StatusCode, string(body))
			continue
		}

		var result map[string]interface{}
		if err := json.Unmarshal(body, &result); err != nil {
			lastErr = fmt.Errorf("failed to decode GraphQL response: %w", err)
			continue
		}

		if errors, ok := result["errors"]; ok {
			lastErr = fmt.Errorf("GraphQL errors: %v", errors)
			continue
		}

		log.Printf("GitHub GraphQL Response: Success")
		return result, nil
	}

	log.Printf("GitHub GraphQL Error: All retry attempts exhausted")
	return nil, lastErr
}

// parsePRURL parses a PR URL and returns owner, repo, and PR number
func parsePRURL(prURL string) (string, string, int, error) {
	// Handle GitHub URL format: https://github.com/owner/repo/pull/123
	githubURLPattern := regexp.MustCompile(`https://github\.com/([^/]+)/([^/]+)/pull/(\d+)`)
	if matches := githubURLPattern.FindStringSubmatch(prURL); len(matches) == 4 {
		owner, repo, prStr := matches[1], matches[2], matches[3]
		pr, err := strconv.Atoi(prStr)
		if err != nil {
			return "", "", 0, fmt.Errorf("invalid PR number: %s", prStr)
		}
		return owner, repo, pr, nil
	}

	// Handle shorthand format: owner/repo#123
	shortPattern := regexp.MustCompile(`([^/]+)/([^#]+)#(\d+)`)
	if matches := shortPattern.FindStringSubmatch(prURL); len(matches) == 4 {
		owner, repo, prStr := matches[1], matches[2], matches[3]
		pr, err := strconv.Atoi(prStr)
		if err != nil {
			return "", "", 0, fmt.Errorf("invalid PR number: %s", prStr)
		}
		return owner, repo, pr, nil
	}

	return "", "", 0, fmt.Errorf("invalid PR URL format: %s", prURL)
}

// getPullRequest fetches a pull request from GitHub
func (c *GitHubClient) getPullRequest(ctx context.Context, owner, repo string, prNumber int) (*PullRequest, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repo, prNumber)
	resp, err := c.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch pull request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to fetch pull request (status %d): %s", resp.StatusCode, string(body))
	}

	var prData struct {
		Number    int    `json:"number"`
		Title     string `json:"title"`
		State     string `json:"state"`
		Draft     bool   `json:"draft"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
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

	// Get last commit and review times
	lastCommit, err := c.getLastCommitTime(ctx, owner, repo, prData.Head.SHA)
	if err != nil {
		return nil, fmt.Errorf("failed to get last commit time: %w", err)
	}
	pr.LastCommit = lastCommit

	lastReview, err := c.getLastReviewTime(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get last review time: %w", err)
	}
	pr.LastReview = lastReview

	return pr, nil
}

// getChangedFiles gets the files changed in a PR
func (c *GitHubClient) getChangedFiles(ctx context.Context, owner, repo string, prNumber int) ([]ChangedFile, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/files", owner, repo, prNumber)
	resp, err := c.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch changed files: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to fetch changed files (status %d): %s", resp.StatusCode, string(body))
	}

	var files []struct {
		Filename  string `json:"filename"`
		Changes   int    `json:"changes"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
		Status    string `json:"status"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, fmt.Errorf("failed to decode changed files: %w", err)
	}

	var changedFiles []ChangedFile
	for _, file := range files {
		changedFiles = append(changedFiles, ChangedFile{
			Filename:  file.Filename,
			Changes:   file.Changes,
			Additions: file.Additions,
			Deletions: file.Deletions,
			Status:    file.Status,
		})
	}

	// Sort by number of changes (descending)
	sort.Slice(changedFiles, func(i, j int) bool {
		return changedFiles[i].Changes > changedFiles[j].Changes
	})

	return changedFiles, nil
}

// getLastCommitTime gets the timestamp of the last commit
func (c *GitHubClient) getLastCommitTime(ctx context.Context, owner, repo, sha string) (time.Time, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", owner, repo, sha)
	resp, err := c.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to fetch commit: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("failed to fetch commit (status %d)", resp.StatusCode)
	}

	var commit struct {
		Commit struct {
			Author struct {
				Date string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&commit); err != nil {
		return time.Time{}, fmt.Errorf("failed to decode commit: %w", err)
	}

	return time.Parse(time.RFC3339, commit.Commit.Author.Date)
}

// getLastReviewTime gets the timestamp of the last review
func (c *GitHubClient) getLastReviewTime(ctx context.Context, owner, repo string, prNumber int) (time.Time, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/reviews", owner, repo, prNumber)
	resp, err := c.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to fetch reviews: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("failed to fetch reviews (status %d)", resp.StatusCode)
	}

	var reviews []struct {
		SubmittedAt string `json:"submitted_at"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&reviews); err != nil {
		return time.Time{}, fmt.Errorf("failed to decode reviews: %w", err)
	}

	if len(reviews) == 0 {
		return time.Time{}, nil
	}

	// Find the most recent review
	var lastReview time.Time
	for _, review := range reviews {
		if review.SubmittedAt != "" {
			reviewTime, err := time.Parse(time.RFC3339, review.SubmittedAt)
			if err == nil && reviewTime.After(lastReview) {
				lastReview = reviewTime
			}
		}
	}

	return lastReview, nil
}