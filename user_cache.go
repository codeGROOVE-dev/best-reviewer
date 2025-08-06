package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
)

// userType represents the type of GitHub account.
type userType string

const (
	userTypeUser userType = "User"
	userTypeOrg  userType = "Organization"
	userTypeBot  userType = "Bot"
)

// userInfo caches information about a GitHub user.
type userInfo struct {
	login    string
	userType userType
}

// userCache caches user type information to avoid repeated API calls.
type userCache struct {
	users map[string]*userInfo
	mu    sync.RWMutex
}

// userType returns the type of a GitHub account (User, Organization, or Bot).
func (c *GitHubClient) userType(ctx context.Context, username string) (userType, error) {
	// Check cache first
	if c.userCache != nil {
		c.userCache.mu.RLock()
		if info, exists := c.userCache.users[username]; exists {
			c.userCache.mu.RUnlock()
			log.Printf("[CACHE] User type cache hit for %s: %s", username, info.userType)
			return info.userType, nil
		}
		c.userCache.mu.RUnlock()
	}

	log.Printf("[USER] Fetching user type for: %s", username)
	// Make API call to get user info
	url := fmt.Sprintf("https://api.github.com/users/%s", username)
	resp, err := c.makeRequest(ctx, httpMethodGet, url, nil)
	if err != nil {
		return userTypeUser, err // Default to user on error
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[WARN] Failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode == http.StatusNotFound {
		// User doesn't exist
		log.Printf("[USER] User %s not found (404), treating as bot", username)
		return userTypeBot, nil
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("[WARN] Failed to get user info for %s (status %d), defaulting to user type", username, resp.StatusCode)
		return userTypeUser, fmt.Errorf("failed to get user info (status %d)", resp.StatusCode)
	}

	var data struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return userTypeUser, err
	}

	// Determine user type
	var uType userType
	switch data.Type {
	case "Organization":
		uType = userTypeOrg
	case "Bot":
		uType = userTypeBot
	default:
		// Even if API says "User", check if username indicates it's a bot
		if isLikelyBot(username) {
			uType = userTypeBot
			log.Printf("[USER] User %s has type 'User' but name suggests bot, treating as bot", username)
		} else {
			uType = userTypeUser
		}
	}

	// Cache the result
	if c.userCache != nil {
		c.userCache.mu.Lock()
		c.userCache.users[username] = &userInfo{
			login:    username,
			userType: uType,
		}
		c.userCache.mu.Unlock()
	}

	log.Printf("[USER] User %s identified as %s", username, uType)
	return uType, nil
}

// isLikelyBot checks if a username suggests it's a bot based on common patterns.
func isLikelyBot(username string) bool {
	lower := strings.ToLower(username)

	// Check for bot suffixes
	if strings.HasSuffix(lower, "[bot]") ||
		strings.HasSuffix(lower, "-bot") ||
		strings.HasSuffix(lower, "_bot") ||
		strings.HasSuffix(lower, ".bot") {
		return true
	}

	// Check for bot prefixes
	if strings.HasPrefix(lower, "bot-") ||
		strings.HasPrefix(lower, "bot_") {
		return true
	}

	// Check for known bot names
	knownBots := []string{
		"dependabot",
		"renovate",
		"github-actions",
		"stale",
		"mergify",
		"codecov",
		"coveralls",
		"snyk",
		"whitesource",
		"greenkeeper",
		"imgbot",
		"allcontributors",
		"netlify",
		"vercel",
		"cypress",
		"semantic-release",
		"release-drafter",
		"probot",
		"octokitbot",
	}

	for _, bot := range knownBots {
		if strings.Contains(lower, bot) {
			return true
		}
	}

	// Check for automation-related patterns
	if strings.Contains(lower, "automation") ||
		strings.Contains(lower, "automate") ||
		strings.Contains(lower, "ci-bot") ||
		strings.Contains(lower, "cd-bot") {
		return true
	}

	return false
}

// cacheUserTypeFromGraphQL caches user type information from GraphQL responses.
func (c *GitHubClient) cacheUserTypeFromGraphQL(username string, typeName string) {
	if c.userCache == nil || username == "" {
		return
	}

	var uType userType
	switch typeName {
	case "Bot":
		uType = userTypeBot
	case "Organization":
		uType = userTypeOrg
	default:
		// Check if username suggests it's a bot even if type is "User"
		if isLikelyBot(username) {
			uType = userTypeBot
		} else {
			uType = userTypeUser
		}
	}

	c.userCache.mu.Lock()
	defer c.userCache.mu.Unlock()

	// Only update if not already cached or if we have more definitive info
	if existing, exists := c.userCache.users[username]; !exists || existing.userType == userTypeUser {
		c.userCache.users[username] = &userInfo{
			login:    username,
			userType: uType,
		}
	}
}
