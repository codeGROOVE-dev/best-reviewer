// Package github provides GitHub API client functionality.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/cache"

	"github.com/codeGROOVE-dev/retry"
)

// Client handles all GitHub API interactions.
type Client struct {
	tokenExpiry        time.Time
	installationTokens map[string]string
	cache              *cache.DiskCache
	httpClient         *http.Client
	installationExpiry map[string]time.Time
	installationIDs    map[string]int
	installationTypes  map[string]string
	userCache          *UserCache
	appID              string
	token              string
	privateKeyPath     string
	currentOrg         string
	privateKeyContent  []byte
	tokenMutex         sync.RWMutex
	isAppAuth          bool
}

// Config holds configuration for creating a new GitHub client.
type Config struct {
	HTTPTimeout time.Duration
	CacheTTL    time.Duration
	CacheDir    string // Directory for disk cache (empty = memory-only)
	AppID       string
	AppKeyPath  string
	Token       string // Personal access token (for non-app auth)
	UseAppAuth  bool
}

// New creates a new GitHub API client using gh auth token or GitHub App authentication.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.UseAppAuth {
		return newAppAuthClient(ctx, cfg.AppID, cfg.AppKeyPath, cfg.HTTPTimeout, cfg.CacheTTL, cfg.CacheDir)
	}
	return newPersonalTokenClient(ctx, cfg.Token, cfg.HTTPTimeout, cfg.CacheTTL, cfg.CacheDir)
}

// SetCurrentOrg sets the current organization being processed.
func (c *Client) SetCurrentOrg(org string) {
	c.tokenMutex.Lock()
	defer c.tokenMutex.Unlock()
	c.currentOrg = org
}

// IsUserAccount checks if the given account is a user account (not an organization).
func (c *Client) IsUserAccount(account string) bool {
	c.tokenMutex.RLock()
	defer c.tokenMutex.RUnlock()
	return c.installationTypes[account] == "User"
}

// getToken returns the current authentication token (JWT for app auth, PAT otherwise).
func (c *Client) getToken() string {
	c.tokenMutex.RLock()
	defer c.tokenMutex.RUnlock()
	return c.token
}

// Token returns the current GitHub token for external use (e.g., sprinkler).
// For App authentication with a currentOrg set, returns the installation token.
// Otherwise returns the base token (JWT or personal access token).
func (c *Client) Token(ctx context.Context) (string, error) {
	if c.isAppAuth && c.currentOrg != "" {
		return c.getInstallationToken(ctx, c.currentOrg)
	}
	return c.getToken(), nil
}

// drainAndCloseBody drains and closes an HTTP response body to prevent resource leaks.
func drainAndCloseBody(body io.ReadCloser) {
	if _, err := io.Copy(io.Discard, body); err != nil {
		slog.Warn("Failed to drain response body", "error", err)
	}
	if err := body.Close(); err != nil {
		slog.Warn("Failed to close response body", "error", err)
	}
}

// MakeRequest makes an HTTP request to the GitHub API with retry logic.
// This is exported to allow other packages to make authenticated GitHub API requests.
func (c *Client) MakeRequest(ctx context.Context, method, apiURL string, body any) (*http.Response, error) {
	return c.makeRequest(ctx, method, apiURL, body)
}

// makeRequest makes an HTTP request to the GitHub API with retry logic.
func (c *Client) makeRequest(ctx context.Context, method, apiURL string, body any) (*http.Response, error) {
	// Refresh JWT if needed
	if c.isAppAuth {
		if err := c.refreshJWTIfNeeded(); err != nil {
			return nil, fmt.Errorf("failed to refresh JWT: %w", err)
		}
	}

	// Sanitize URL for logging - remove all sensitive query parameters
	sanitizedURL := sanitizeURLForLogging(apiURL)
	slog.Info("HTTP request", "component", "http", "method", method, "url", sanitizedURL)

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
				slog.Debug("Using installation token for org", "org", c.currentOrg)
			} else {
				// Graceful degradation: try with JWT token
				slog.Warn("Failed to get installation token, attempting with JWT (may have limited access)", "org", c.currentOrg, "error", err)
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
			slog.Warn("Rate limited - will retry with backoff", "method", method, "url", sanitizedURL, "status", 429)
			return fmt.Errorf("http %d: rate limited", localResp.StatusCode)
		}

		if localResp.StatusCode >= http.StatusInternalServerError && localResp.StatusCode < 600 {
			drainAndCloseBody(localResp.Body)
			slog.Warn("Server error - will retry with backoff", "method", method, "url", sanitizedURL, "status", localResp.StatusCode)
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
	slog.Info("HTTP response", "component", "http", "method", method, "url", sanitizedURL, "status", resp.StatusCode)
	return resp, nil
}

// Retry constants.
const (
	maxRetryAttempts  = 25              // Maximum retry attempts for API calls
	initialRetryDelay = 1 * time.Second // Initial delay for retry attempts
	maxRetryDelay     = 2 * time.Minute // Maximum delay cap (2 minutes as per requirement)
)

// retryWithBackoff executes a function with exponential backoff using the codeGROOVE retry library.
func retryWithBackoff(ctx context.Context, operation string, fn func() error) error {
	// Configure retry with exponential backoff and jitter
	return retry.Do(
		func() error {
			return fn()
		},
		retry.Context(ctx),
		retry.Attempts(uint(maxRetryAttempts)),
		retry.Delay(initialRetryDelay),
		retry.MaxDelay(maxRetryDelay),
		retry.DelayType(retry.CombineDelay(retry.BackOffDelay, retry.RandomDelay)),
		retry.MaxJitter(initialRetryDelay/4),
		retry.OnRetry(func(n uint, err error) {
			slog.Info("Retry attempt", "component", "retry", "operation", operation, "attempt", n+1, "max_attempts", maxRetryAttempts, "error", err)
		}),
		retry.LastErrorOnly(true),
		retry.RetryIf(func(err error) bool {
			// Retry on temporary errors, rate limits, and server errors
			if err == nil {
				return false
			}
			errStr := err.Error()
			// Retry on rate limiting, server errors, and network issues
			return strings.Contains(errStr, "rate limited") ||
				strings.Contains(errStr, "server error") ||
				strings.Contains(errStr, "connection refused") ||
				strings.Contains(errStr, "timeout") ||
				strings.Contains(errStr, "temporary failure") ||
				strings.Contains(errStr, "EOF")
		}),
	)
}
