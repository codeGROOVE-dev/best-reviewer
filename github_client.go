package main

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// GitHubClient handles all GitHub API interactions.
type GitHubClient struct {
	tokenExpiry        time.Time
	installationTokens map[string]string
	cache              *cache
	httpClient         *http.Client
	installationExpiry map[string]time.Time
	installationIDs    map[string]int
	installationTypes  map[string]string
	userCache          *userCache
	appID              string
	token              string
	privateKeyPath     string
	currentOrg         string
	privateKeyContent  []byte
	tokenMutex         sync.RWMutex
	isAppAuth          bool
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

// generateJWT generates a JWT token for GitHub App authentication.
func generateJWT(appID string, privateKey []byte) (string, error) {
	// Parse the private key
	block, _ := pem.Decode(privateKey)
	if block == nil {
		return "", errors.New("failed to parse PEM block containing the private key")
	}

	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS8 format if PKCS1 fails
		parsedKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return "", fmt.Errorf("failed to parse private key: %w", err)
		}
		var ok bool
		key, ok = parsedKey.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("private key is not RSA")
		}
	}

	// Create the JWT claims
	now := time.Now()
	claims := jwt.MapClaims{
		"iat": now.Unix(),
		"exp": now.Add(10 * time.Minute).Unix(), // GitHub Apps JWTs expire after 10 minutes max
		"iss": appID,
	}

	// Create and sign the token
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(key)
}

// newGitHubClient creates a new GitHub API client using gh auth token or GitHub App authentication.
func newGitHubClient(ctx context.Context, useAppAuth bool, appID, appKeyPath string) (*GitHubClient, error) {
	if useAppAuth {
		return newAppAuthClient(ctx, appID, appKeyPath)
	}
	return newPersonalTokenClient(ctx)
}

func newAppAuthClient(_ context.Context, appID, appKeyPath string) (*GitHubClient, error) {
	// Resolve credentials from flags or environment variables
	creds, err := resolveAppCredentials(appID, appKeyPath)
	if err != nil {
		return nil, err
	}

	// Validate app ID
	if err := validateAppID(creds.appID); err != nil {
		return nil, err
	}

	// Load private key
	privateKey, err := loadPrivateKey(creds.privateKeyContent, creds.keyPath)
	if err != nil {
		return nil, err
	}

	// Generate JWT
	jwtToken, err := generateJWT(creds.appID, privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to generate JWT: %w", err)
	}
	log.Print("[AUTH] Successfully generated JWT for GitHub App")

	// Create and configure client
	return createAppAuthClient(creds.appID, creds.keyPath, creds.privateKeyContent, jwtToken), nil
}

func newPersonalTokenClient(ctx context.Context) (*GitHubClient, error) {
	// Get token from gh CLI
	cmd := exec.CommandContext(ctx, "gh", "auth", "token")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get GitHub token: %w", err)
	}

	token := strings.TrimSpace(string(output))
	if err := validateToken(token); err != nil {
		return nil, err
	}

	log.Print("[AUTH] Using personal access token authentication")

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
		isAppAuth:  false,
	}, nil
}

type appCredentials struct {
	appID             string
	keyPath           string
	privateKeyContent []byte
}

func resolveAppCredentials(appID, appKeyPath string) (*appCredentials, error) {
	// Use provided flags or fall back to environment variables
	if appID == "" {
		appID = os.Getenv("GITHUB_APP_ID")
	}

	var privateKeyContent []byte
	if appKeyPath != "" {
		log.Printf("[AUTH] Using private key file path from command line: %s", appKeyPath)
	} else {
		// Check for private key content first (more secure)
		if keyContent := os.Getenv("GITHUB_APP_KEY"); keyContent != "" {
			privateKeyContent = []byte(keyContent)
			log.Printf("[AUTH] Using GITHUB_APP_KEY environment variable (%d bytes)", len(privateKeyContent))
			// Clear appKeyPath to ensure we don't try to read from file
			appKeyPath = ""
		} else {
			// Fall back to key file path only if no content is provided
			appKeyPath = os.Getenv("GITHUB_APP_KEY_PATH")
			if appKeyPath == "" {
				// Also check the old environment variable for backward compatibility
				appKeyPath = os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH")
			}
			if appKeyPath != "" {
				log.Printf("[AUTH] Using private key file path: %s", appKeyPath)
			}
		}
	}

	if appID == "" {
		return nil, errors.New("GitHub App ID is required. " +
			"Use --app-id flag or set GITHUB_APP_ID environment variable")
	}
	if len(privateKeyContent) == 0 && appKeyPath == "" {
		return nil, errors.New("GitHub App private key is required. " +
			"Use --app-key-path flag, set GITHUB_APP_KEY environment variable (key content), " +
			"or set GITHUB_APP_KEY_PATH environment variable (file path)")
	}

	return &appCredentials{
		appID:             appID,
		privateKeyContent: privateKeyContent,
		keyPath:           appKeyPath,
	}, nil
}

func validateAppID(appID string) error {
	appIDNum, err := strconv.Atoi(appID)
	if err != nil {
		return fmt.Errorf("GITHUB_APP_ID must be numeric: %w", err)
	}
	if appIDNum <= 0 || appIDNum > maxAppID {
		return errors.New("GITHUB_APP_ID out of valid range")
	}
	return nil
}

func loadPrivateKey(privateKeyContent []byte, keyPath string) ([]byte, error) {
	var privateKey []byte
	var err error

	switch {
	case len(privateKeyContent) > 0:
		// Use direct key content
		privateKey = privateKeyContent
	case keyPath != "":
		// Read from file path only if path is provided
		privateKey, err = readPrivateKeyFile(keyPath)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("no private key provided (neither content nor path)")
	}

	// Validate it looks like a PEM private key
	if !bytes.Contains(privateKey, []byte("BEGIN RSA PRIVATE KEY")) &&
		!bytes.Contains(privateKey, []byte("BEGIN PRIVATE KEY")) {
		return nil, errors.New("private key does not appear to be a valid PEM private key")
	}

	return privateKey, nil
}

func readPrivateKeyFile(keyPath string) ([]byte, error) {
	// Validate and clean the private key path to prevent path traversal
	cleanPath := filepath.Clean(keyPath)
	if !filepath.IsAbs(cleanPath) {
		return nil, errors.New("GITHUB_APP_PRIVATE_KEY_PATH must be an absolute path")
	}

	// Verify file exists and has appropriate permissions
	fileInfo, err := os.Stat(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("cannot access private key file: %w", err)
	}
	if fileInfo.IsDir() {
		return nil, errors.New("GITHUB_APP_PRIVATE_KEY_PATH must be a file, not a directory")
	}

	// Check file permissions - must be exactly 0600 or 0400
	perm := fileInfo.Mode().Perm()
	if perm != filePermOwnerRW && perm != filePermReadOnly {
		return nil, fmt.Errorf("private key file has insecure permissions %04o (must be 0600 or 0400)", perm)
	}

	// Read the private key file
	return os.ReadFile(cleanPath)
}

func validateToken(token string) error {
	if token == "" {
		return errors.New("no GitHub token found")
	}
	if len(token) > maxTokenLength || len(token) < minTokenLength {
		return errors.New("invalid token length")
	}

	// Validate token format - GitHub tokens have specific prefixes
	validPrefixes := []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"}
	for _, prefix := range validPrefixes {
		if strings.HasPrefix(token, prefix) {
			return nil
		}
	}

	// Could be a classic token (40 hex chars)
	if len(token) != classicTokenLength {
		return errors.New("invalid token format")
	}
	for _, r := range token {
		if (r < 'a' || r > 'f') && (r < '0' || r > '9') {
			return errors.New("invalid classic token format")
		}
	}

	return nil
}

func createAppAuthClient(appID, keyPath string, privateKeyContent []byte, jwtToken string) *GitHubClient {
	c := &cache{
		entries: make(map[string]cacheEntry),
		ttl:     cacheTTL,
	}
	go c.cleanupExpired()

	client := &GitHubClient{
		httpClient:         &http.Client{Timeout: time.Duration(httpTimeout) * time.Second},
		cache:              c,
		userCache:          &userCache{users: make(map[string]*userInfo)},
		token:              jwtToken,
		isAppAuth:          true,
		appID:              appID,
		privateKeyPath:     keyPath,
		tokenExpiry:        time.Now().Add(9 * time.Minute), // Refresh 1 minute before expiry
		installationTokens: make(map[string]string),
		installationExpiry: make(map[string]time.Time),
		installationIDs:    make(map[string]int),
		installationTypes:  make(map[string]string),
	}

	// Store private key content if using direct content approach
	if len(privateKeyContent) > 0 {
		client.privateKeyContent = privateKeyContent
	}

	return client
}

// drainAndCloseBody drains and closes an HTTP response body to prevent resource leaks.
func drainAndCloseBody(body io.ReadCloser) {
	if _, err := io.Copy(io.Discard, body); err != nil {
		log.Printf("[WARN] Failed to drain response body: %v", err)
	}
	if err := body.Close(); err != nil {
		log.Printf("[WARN] Failed to close response body: %v", err)
	}
}

// refreshJWTIfNeeded refreshes the JWT token if it's close to expiry.
func (c *GitHubClient) refreshJWTIfNeeded() error {
	if !c.isAppAuth {
		return nil
	}

	c.tokenMutex.RLock()
	needsRefresh := time.Now().After(c.tokenExpiry)
	c.tokenMutex.RUnlock()

	if !needsRefresh {
		return nil
	}

	c.tokenMutex.Lock()
	defer c.tokenMutex.Unlock()

	// Double-check after acquiring write lock
	if time.Now().Before(c.tokenExpiry) {
		return nil
	}

	// Get private key - either from stored content or file
	var privateKey []byte
	var err error
	switch {
	case len(c.privateKeyContent) > 0:
		// Use stored private key content
		privateKey = c.privateKeyContent
	case c.privateKeyPath != "":
		// Read from file
		privateKey, err = os.ReadFile(c.privateKeyPath)
		if err != nil {
			return fmt.Errorf("failed to read private key for refresh: %w", err)
		}
	default:
		return errors.New("no private key available for JWT refresh")
	}

	// Generate new JWT
	newToken, err := generateJWT(c.appID, privateKey)
	if err != nil {
		return fmt.Errorf("failed to generate JWT for refresh: %w", err)
	}

	c.token = newToken
	c.tokenExpiry = time.Now().Add(9 * time.Minute)
	log.Print("[AUTH] Refreshed GitHub App JWT")

	return nil
}

// setCurrentOrg sets the current organization being processed.
func (c *GitHubClient) setCurrentOrg(org string) {
	c.tokenMutex.Lock()
	defer c.tokenMutex.Unlock()
	c.currentOrg = org
}

// isUserAccount checks if the given account is a user account (not an organization).
func (c *GitHubClient) isUserAccount(account string) bool {
	c.tokenMutex.RLock()
	defer c.tokenMutex.RUnlock()
	return c.installationTypes[account] == "User"
}

// token returns the current authentication token (JWT for app auth, PAT otherwise).
func (c *GitHubClient) getToken() string {
	c.tokenMutex.RLock()
	defer c.tokenMutex.RUnlock()
	return c.token
}

// getInstallationToken gets or refreshes an installation access token for an organization.
func (c *GitHubClient) getInstallationToken(ctx context.Context, org string) (string, error) {
	if !c.isAppAuth {
		return c.token, nil // Return regular token if not using app auth
	}

	if org == "" {
		return "", errors.New("organization name cannot be empty")
	}

	c.tokenMutex.RLock()
	// Check if we have a valid cached token
	if token, ok := c.installationTokens[org]; ok {
		if expiry, ok := c.installationExpiry[org]; ok && time.Now().Before(expiry) {
			c.tokenMutex.RUnlock()
			return token, nil
		}
	}
	c.tokenMutex.RUnlock()

	// Need to create/refresh token - refresh JWT first if needed
	if err := c.refreshJWTIfNeeded(); err != nil {
		return "", fmt.Errorf("failed to refresh JWT: %w", err)
	}

	c.tokenMutex.Lock()
	defer c.tokenMutex.Unlock()

	// Double-check cache after acquiring write lock
	if token, ok := c.installationTokens[org]; ok {
		if expiry, ok := c.installationExpiry[org]; ok && time.Now().Before(expiry) {
			return token, nil
		}
	}

	// Get installation ID for this org
	installationID, ok := c.installationIDs[org]
	if !ok {
		log.Printf("[ERROR] No installation ID found for organization %s - app may not be installed", org)
		return "", fmt.Errorf("no installation ID found for organization %s (is the app installed?)", org)
	}

	// Create installation access token
	log.Printf("[AUTH] Creating installation access token for org %s (installation ID: %d)", org, installationID)
	apiURL := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", installationID)

	// Use JWT for this request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("[ERROR] Failed to request installation token for org %s: %v", org, err)
		return "", fmt.Errorf("failed to get installation token: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[WARN] Failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusCreated {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("[ERROR] Failed to read error response body for org %s: %v", org, err)
			return "", fmt.Errorf("failed to create installation token (status %d) and read error: %w", resp.StatusCode, err)
		}
		log.Printf("[ERROR] GitHub API error creating installation token for org %s (status %d): %s", org, resp.StatusCode, string(body))
		return "", fmt.Errorf("failed to create installation token (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		ExpiresAt time.Time `json:"expires_at"`
		Token     string    `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		log.Printf("[ERROR] Failed to decode installation token response for org %s: %v", org, err)
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}

	if tokenResp.Token == "" {
		log.Printf("[ERROR] Received empty installation token for org %s", org)
		return "", errors.New("received empty installation token")
	}

	// Cache the token (expire 5 minutes before actual expiry for safety)
	c.installationTokens[org] = tokenResp.Token
	c.installationExpiry[org] = tokenResp.ExpiresAt.Add(-5 * time.Minute)

	log.Printf("[AUTH] Successfully created installation access token for org %s (expires at %s)", org, tokenResp.ExpiresAt.Format(time.RFC3339))
	return tokenResp.Token, nil
}

// makeRequest makes an HTTP request to the GitHub API with retry logic.
func (c *GitHubClient) makeRequest(ctx context.Context, method, apiURL string, body any) (*http.Response, error) {
	// Refresh JWT if needed
	if c.isAppAuth {
		if err := c.refreshJWTIfNeeded(); err != nil {
			return nil, fmt.Errorf("failed to refresh JWT: %w", err)
		}
	}
	// Sanitize URL for logging - remove all sensitive query parameters
	sanitizedURL := apiURL
	if idx := strings.Index(apiURL, "?"); idx != -1 {
		sanitizedURL = apiURL[:idx] + "?[REDACTED]"
	}
	log.Printf("[HTTP] %s %s", method, sanitizedURL)

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

		// Use the appropriate token based on authentication type and current org
		authToken := c.token
		if c.isAppAuth && c.currentOrg != "" {
			// For app auth with a specific org, use installation token
			installToken, err := c.getInstallationToken(ctx, c.currentOrg)
			if err == nil {
				authToken = installToken
				log.Printf("[DEBUG] Using installation token for org %s", c.currentOrg)
			} else {
				// Graceful degradation: try with JWT token
				log.Printf("[WARN] Failed to get installation token for %s, attempting with JWT (may have limited access): %v", c.currentOrg, err)
			}
		}

		if c.isAppAuth {
			req.Header.Set("Authorization", "Bearer "+authToken)
			req.Header.Set("Accept", "application/vnd.github.v3+json")
		} else {
			req.Header.Set("Authorization", "token "+authToken)
			req.Header.Set("Accept", "application/vnd.github.v3+json")
		}
		if method == "PATCH" || method == "POST" || method == "PUT" {
			req.Header.Set("Content-Type", "application/json")
		}

		var localResp *http.Response
		localResp, err = c.httpClient.Do(req) //nolint:bodyclose // body is closed via defer or passed to caller
		if err != nil {
			return fmt.Errorf("request failed: %w", err)
		}

		// Check for rate limiting or server errors that should trigger retry
		if localResp.StatusCode == http.StatusTooManyRequests {
			drainAndCloseBody(localResp.Body)
			log.Printf("[WARN] Rate limited (429) on %s %s - will retry with backoff", method, sanitizedURL)
			return fmt.Errorf("http %d: rate limited", localResp.StatusCode)
		}

		if localResp.StatusCode >= http.StatusInternalServerError && localResp.StatusCode < 600 {
			drainAndCloseBody(localResp.Body)
			log.Printf("[WARN] Server error (%d) on %s %s - will retry with backoff", localResp.StatusCode, method, sanitizedURL)
			return fmt.Errorf("http %d: server error", localResp.StatusCode)
		}

		// Success - assign to outer resp variable and let caller handle body
		resp = localResp
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Log response status with sanitized URL
	log.Printf("[HTTP] %s %s - Status: %d", method, sanitizedURL, resp.StatusCode)
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
func (rf *ReviewerFinder) hasWriteAccess(ctx context.Context, owner, repo, username string) bool {
	// Check if user is a collaborator with write access
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/collaborators/%s", owner, repo, username)
	resp, err := rf.client.makeRequest(ctx, httpMethodGet, apiURL, nil)
	if err != nil {
		log.Printf("[WARN] Failed to check write access for %s: %v", username, err)
		return false // Fail closed - assume no access on error
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[WARN] Failed to close response body: %v", err)
		}
	}()

	// GitHub returns 204 No Content if user is a collaborator
	// Returns 404 if not a collaborator
	if resp.StatusCode == http.StatusNoContent {
		return true
	}

	return false
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
type Installation struct {
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
	ID int `json:"id"`
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
		// Include both organization and user accounts
		orgs = append(orgs, installation.Account.Login)
		// Store the installation ID and type for later use
		c.installationIDs[installation.Account.Login] = installation.ID
		c.installationTypes[installation.Account.Login] = installation.Account.Type

		if installation.Account.Type == "Organization" {
			log.Printf("[APP] Found installation in org: %s (ID: %d)", installation.Account.Login, installation.ID)
		} else {
			log.Printf("[APP] Found installation for user: %s (ID: %d)", installation.Account.Login, installation.ID)
		}
	}

	log.Printf("[APP] Found %d installations (organizations and users)", len(orgs))
	return orgs, nil
}
