package cache

import "testing"

func TestIsLikelyBot(t *testing.T) {
	tests := []struct {
		name     string
		username string
		wantBot  bool
	}{
		// Bot patterns
		{"Bot with [bot] suffix", "dependabot[bot]", true},
		{"Bot with -bot suffix", "renovate-bot", true},
		{"Bot with _bot suffix", "security_bot", true},
		{"Bot with bot- prefix", "bot-user", true},
		{"Bot with bot_ prefix", "bot_scanner", true},
		{"Bot with .bot suffix", "scanner.bot", true},

		// Specific known bots
		{"GitHub Actions", "github-actions", true},
		{"GitHub Actions with bracket", "github-actions[bot]", true},
		{"Dependabot", "dependabot", true},
		{"Renovate", "renovate", true},
		{"Greenkeeper", "greenkeeper", true},
		{"Snyk", "snyk-bot", true},
		{"Codecov", "codecov", true},
		{"CircleCI", "circleci", true},
		{"Mergify", "mergify[bot]", true},
		{"Stale bot", "stale[bot]", true},

		// Automation patterns
		{"Automation account", "deploy-automation", true},
		{"Automate account", "auto-automate-bot", true},
		{"CI bot", "project-ci-bot", true},
		{"CD bot", "prod-cd-bot", true},

		// Valid human users
		{"Regular user", "johndoe", false},
		{"User with dash", "john-doe", false},
		{"User with underscore", "john_doe", false},
		{"User with numbers", "user123", false},
		{"Common contributor", "sergiodj", false},
		{"Common contributor 2", "murraybd", false},
		{"PR author", "ajayk", false},
		{"Reviewer", "tstromberg", false},
		{"Another reviewer", "vavilen84", false},

		// Edge cases - users that might look like bots but aren't
		{"User with 'bot' in name", "abbott", false},
		{"User with 'ci' in name", "luci", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsLikelyBot(tt.username)
			if got != tt.wantBot {
				t.Errorf("IsLikelyBot(%q) = %v, want %v", tt.username, got, tt.wantBot)
			}
		})
	}
}

func TestUserCache(t *testing.T) {
	cache := NewUserCache()

	// Test Set and Get
	cache.Set("user1", UserTypeUser)
	info, exists := cache.Get("user1")
	if !exists {
		t.Fatal("Expected user1 to exist in cache")
	}
	if info.Login != "user1" {
		t.Errorf("Expected login 'user1', got %q", info.Login)
	}
	if info.UserType != UserTypeUser {
		t.Errorf("Expected UserTypeUser, got %q", info.UserType)
	}

	// Test Get non-existent user
	_, exists = cache.Get("nonexistent")
	if exists {
		t.Error("Expected nonexistent user to not exist in cache")
	}

	// Test SetIfNotExists - should set when not exists
	cache.SetIfNotExists("user2", UserTypeBot)
	info, exists = cache.Get("user2")
	if !exists {
		t.Fatal("Expected user2 to exist in cache")
	}
	if info.UserType != UserTypeBot {
		t.Errorf("Expected UserTypeBot, got %q", info.UserType)
	}

	// Test SetIfNotExists - should not overwrite existing
	cache.Set("user3", UserTypeOrg)
	cache.SetIfNotExists("user3", UserTypeBot)
	info, exists = cache.Get("user3")
	if !exists {
		t.Fatal("Expected user3 to exist in cache")
	}
	if info.UserType != UserTypeOrg {
		t.Errorf("Expected UserTypeOrg to remain, got %q", info.UserType)
	}

	// Test SetIfNotExists - should overwrite if existing is User
	cache.Set("user4", UserTypeUser)
	cache.SetIfNotExists("user4", UserTypeBot)
	info, exists = cache.Get("user4")
	if !exists {
		t.Fatal("Expected user4 to exist in cache")
	}
	if info.UserType != UserTypeBot {
		t.Errorf("Expected UserTypeBot to overwrite User, got %q", info.UserType)
	}
}
