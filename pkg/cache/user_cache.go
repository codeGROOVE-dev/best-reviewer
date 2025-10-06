package cache

import (
	"strings"
	"sync"
)

// UserType represents the type of GitHub account.
type UserType string

// GitHub account types.
const (
	UserTypeUser UserType = "User"
	UserTypeOrg  UserType = "Organization"
	UserTypeBot  UserType = "Bot"
)

// UserInfo caches information about a GitHub user.
type UserInfo struct {
	Login    string
	UserType UserType
}

// UserCache caches user type information to avoid repeated API calls.
type UserCache struct {
	users map[string]*UserInfo
	mu    sync.RWMutex
}

// NewUserCache creates a new user cache.
func NewUserCache() *UserCache {
	return &UserCache{
		users: make(map[string]*UserInfo),
	}
}

// Get retrieves user info from cache.
func (uc *UserCache) Get(username string) (*UserInfo, bool) {
	uc.mu.RLock()
	defer uc.mu.RUnlock()
	info, exists := uc.users[username]
	return info, exists
}

// Set stores user info in cache.
func (uc *UserCache) Set(username string, userType UserType) {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	uc.users[username] = &UserInfo{
		Login:    username,
		UserType: userType,
	}
}

// SetIfNotExists stores user info only if not already cached or if we have more definitive info.
func (uc *UserCache) SetIfNotExists(username string, userType UserType) {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	if existing, exists := uc.users[username]; !exists || existing.UserType == UserTypeUser {
		uc.users[username] = &UserInfo{
			Login:    username,
			UserType: userType,
		}
	}
}

// IsLikelyBot checks if a username suggests it's a bot based on common patterns.
func IsLikelyBot(username string) bool {
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
		"circleci",
		"travis",
		"jenkins",
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
