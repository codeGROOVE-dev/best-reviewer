package github

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
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/cache"

	"github.com/golang-jwt/jwt/v5"
)

// Authentication constants.
const (
	maxTokenLength     = 100 // Maximum expected length for GitHub tokens
	minTokenLength     = 40  // Minimum expected length for GitHub tokens
	classicTokenLength = 40  // Length of classic GitHub tokens
	maxAppID           = 999999999
	filePermSecure     = 0o077 // Mask for checking secure file permissions
	filePermReadOnly   = 0o400 // Read-only file permissions
	filePermOwnerRW    = 0o600 // Owner read-write file permissions
)

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

// newAppAuthClient creates a GitHub client with App authentication.
func newAppAuthClient(_ context.Context, appID, appKeyPath string, httpTimeout time.Duration, cacheTTL time.Duration) (*Client, error) {
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
	slog.Info("[AUTH] Successfully generated JWT for GitHub App")

	// Create and configure client
	return createAppAuthClient(creds.appID, creds.keyPath, creds.privateKeyContent, jwtToken, httpTimeout, cacheTTL), nil
}

// newPersonalTokenClient creates a GitHub client with personal token authentication.
func newPersonalTokenClient(ctx context.Context, token string, httpTimeout time.Duration, cacheTTL time.Duration) (*Client, error) {
	// If no token provided, get it from gh CLI
	if token == "" {
		cmd := exec.CommandContext(ctx, "gh", "auth", "token")
		output, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("failed to get GitHub token: %w", err)
		}
		token = strings.TrimSpace(string(output))
	}

	if err := validateToken(token); err != nil {
		return nil, err
	}

	slog.Info("[AUTH] Using personal access token authentication")

	c := cache.New(cacheTTL)

	return &Client{
		httpClient: &http.Client{Timeout: httpTimeout},
		cache:      c,
		userCache:  NewUserCache(),
		token:      token,
		isAppAuth:  false,
	}, nil
}

// appCredentials holds GitHub App authentication details.
type appCredentials struct {
	appID             string
	keyPath           string
	privateKeyContent []byte
}

// resolveAppCredentials resolves app credentials from flags or environment variables.
func resolveAppCredentials(appID, appKeyPath string) (*appCredentials, error) {
	// Use provided flags or fall back to environment variables
	if appID == "" {
		appID = os.Getenv("GITHUB_APP_ID")
	}

	var privateKeyContent []byte
	if appKeyPath != "" {
		slog.Info("[AUTH] Using private key file path from command line: %s", appKeyPath)
	} else {
		// Check for private key content first (more secure)
		if keyContent := os.Getenv("GITHUB_APP_KEY"); keyContent != "" {
			privateKeyContent = []byte(keyContent)
			slog.Info("[AUTH] Using GITHUB_APP_KEY environment variable (%d bytes)", len(privateKeyContent))
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
				slog.Info("[AUTH] Using private key file path: %s", appKeyPath)
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

// validateAppID validates the GitHub App ID.
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

// loadPrivateKey loads the private key from content or file path.
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

// readPrivateKeyFile reads and validates a private key file.
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

// validateToken validates a GitHub personal access token.
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

// createAppAuthClient creates a configured GitHub App authentication client.
func createAppAuthClient(appID, keyPath string, privateKeyContent []byte, jwtToken string, httpTimeout time.Duration, cacheTTL time.Duration) *Client {
	c := cache.New(cacheTTL)

	client := &Client{
		httpClient:         &http.Client{Timeout: httpTimeout},
		cache:              c,
		userCache:          NewUserCache(),
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

// refreshJWTIfNeeded refreshes the JWT token if it's close to expiry.
func (c *Client) refreshJWTIfNeeded() error {
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
	slog.Info("[AUTH] Refreshed GitHub App JWT")

	return nil
}

// getInstallationToken gets or refreshes an installation access token for an organization.
func (c *Client) getInstallationToken(ctx context.Context, org string) (string, error) {
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
		slog.Info("[ERROR] No installation ID found for organization %s - app may not be installed", org)
		return "", fmt.Errorf("no installation ID found for organization %s (is the app installed?)", org)
	}

	// Create installation access token
	slog.Info("[AUTH] Creating installation access token for org %s (installation ID: %d)", org, installationID)
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
		slog.Info("[ERROR] Failed to request installation token for org %s: %v", org, err)
		return "", fmt.Errorf("failed to get installation token: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Info("[WARN] Failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusCreated {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			slog.Info("[ERROR] Failed to read error response body for org %s: %v", org, err)
			return "", fmt.Errorf("failed to create installation token (status %d) and read error: %w", resp.StatusCode, err)
		}
		slog.Info("[ERROR] GitHub API error creating installation token for org %s (status %d): %s", org, resp.StatusCode, string(body))
		return "", fmt.Errorf("failed to create installation token (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		ExpiresAt time.Time `json:"expires_at"`
		Token     string    `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		slog.Info("[ERROR] Failed to decode installation token response for org %s: %v", org, err)
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}

	if tokenResp.Token == "" {
		slog.Info("[ERROR] Received empty installation token for org %s", org)
		return "", errors.New("received empty installation token")
	}

	// Cache the token (expire 5 minutes before actual expiry for safety)
	c.installationTokens[org] = tokenResp.Token
	c.installationExpiry[org] = tokenResp.ExpiresAt.Add(-5 * time.Minute)

	slog.Info("[AUTH] Successfully created installation access token for org %s (expires at %s)", org, tokenResp.ExpiresAt.Format(time.RFC3339))
	return tokenResp.Token, nil
}

// Installation represents a GitHub App installation.
type Installation struct {
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
	ID int `json:"id"`
}

// ListAppInstallations returns all organizations where this GitHub app is installed.
func (c *Client) ListAppInstallations(ctx context.Context) ([]string, error) {
	if !c.isAppAuth {
		return nil, errors.New("app installations can only be listed with GitHub App authentication")
	}

	slog.Info("[API] Fetching GitHub App installations")
	apiURL := "https://api.github.com/app/installations"
	resp, err := c.makeRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get app installations: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Info("[WARN] Failed to close response body: %v", err)
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
			slog.Info("[APP] Found installation in org: %s (ID: %d)", installation.Account.Login, installation.ID)
		} else {
			slog.Info("[APP] Found installation for user: %s (ID: %d)", installation.Account.Login, installation.ID)
		}
	}

	slog.Info("[APP] Found %d installations (organizations and users)", len(orgs))
	return orgs, nil
}
