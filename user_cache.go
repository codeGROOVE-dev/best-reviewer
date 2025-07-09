package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	mu    sync.RWMutex
	users map[string]*userInfo
}

// newUserCache creates a new user cache.
func newUserCache() *userCache {
	return &userCache{
		users: make(map[string]*userInfo),
	}
}

// getUserType gets the type of a GitHub account (User, Organization, or Bot).
func (c *GitHubClient) getUserType(ctx context.Context, username string) (userType, error) {
	// Check cache first
	if c.userCache != nil {
		c.userCache.mu.RLock()
		if info, exists := c.userCache.users[username]; exists {
			c.userCache.mu.RUnlock()
			return info.userType, nil
		}
		c.userCache.mu.RUnlock()
	}

	// Make API call to get user info
	url := fmt.Sprintf("https://api.github.com/users/%s", username)
	resp, err := c.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return userTypeUser, err // Default to user on error
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		// User doesn't exist
		return userTypeBot, nil
	}

	if resp.StatusCode != 200 {
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
		uType = userTypeUser
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

	return uType, nil
}