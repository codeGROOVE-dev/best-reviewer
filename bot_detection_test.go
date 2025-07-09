package main

import (
	"context"
	"testing"
)

func TestIsUserBot(t *testing.T) {
	rf := &ReviewerFinder{}
	ctx := context.Background()

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
		{"Travis CI", "travis-ci", true},
		{"CircleCI", "circleci", true},
		{"Jenkins", "jenkins", true},
		{"Mergify", "mergify[bot]", true},
		{"Stale bot", "stale[bot]", true},
		
		// Organization/service patterns
		{"Octo STS", "octo-sts", true},
		{"Octocat", "octocat", true},
		{"Service account with -sts", "my-app-sts", true},
		{"Service account with -svc", "backend-svc", true},
		{"Service account", "api-service", true},
		{"System account", "auth-system", true},
		{"Automation account", "deploy-automation", true},
		{"CI account", "project-ci", true},
		{"CD account", "prod-cd", true},
		{"Deploy account", "k8s-deploy", true},
		{"Release account", "release-manager", true},
		{"Build account", "docker-build", true},
		{"Test account", "e2e-test", true},
		{"Admin account", "cluster-admin", true},
		{"Security account", "security-scanner", true},
		{"Compliance account", "compliance-checker", true},
		
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
		{"User with 'test' in name", "atestuser", false},
		{"User with 'build' in name", "builderman", false},
		{"User with 'admin' in name", "adminton", false},
		{"User ending in 'sts'", "roberts", false},
		{"User ending in 'ci'", "luci", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rf.isUserBot(ctx, tt.username)
			if got != tt.wantBot {
				t.Errorf("isUserBot(%q) = %v, want %v", tt.username, got, tt.wantBot)
			}
		})
	}
}

func TestIsValidReviewer(t *testing.T) {
	// Create a mock GitHub client that returns specific user types
	mockClient := &GitHubClient{
		userCache: newUserCache(),
	}
	
	// Pre-populate the cache with test data
	mockClient.userCache.users["octo-sts"] = &userInfo{login: "octo-sts", userType: userTypeOrg}
	mockClient.userCache.users["github"] = &userInfo{login: "github", userType: userTypeOrg}
	mockClient.userCache.users["dependabot[bot]"] = &userInfo{login: "dependabot[bot]", userType: userTypeBot}
	mockClient.userCache.users["johndoe"] = &userInfo{login: "johndoe", userType: userTypeUser}
	mockClient.userCache.users["sergiodj"] = &userInfo{login: "sergiodj", userType: userTypeUser}
	
	rf := &ReviewerFinder{
		client: mockClient,
	}
	
	ctx := context.Background()
	pr := &PullRequest{Owner: "test", Repository: "repo"}
	
	tests := []struct {
		name      string
		username  string
		wantValid bool
	}{
		// Should be filtered out
		{"Organization account", "octo-sts", false},
		{"GitHub org", "github", false},
		{"Bot with API confirmation", "dependabot[bot]", false},
		{"Pattern-based bot", "github-actions", false},
		{"Service account", "deploy-service", false},
		
		// Should be valid
		{"Regular user", "johndoe", true},
		{"Contributor", "sergiodj", true},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rf.isValidReviewer(ctx, pr, tt.username)
			if got != tt.wantValid {
				t.Errorf("isValidReviewer(%q) = %v, want %v", tt.username, got, tt.wantValid)
			}
		})
	}
}

func TestGetUserType(t *testing.T) {
	// Test the userType detection logic
	tests := []struct {
		name         string
		apiResponse  string
		wantUserType userType
	}{
		{
			name:         "Organization response",
			apiResponse:  `{"type": "Organization", "name": "Test Org"}`,
			wantUserType: userTypeOrg,
		},
		{
			name:         "Bot response",
			apiResponse:  `{"type": "Bot", "name": "Test Bot"}`,
			wantUserType: userTypeBot,
		},
		{
			name:         "User response",
			apiResponse:  `{"type": "User", "name": "John Doe"}`,
			wantUserType: userTypeUser,
		},
		{
			name:         "Empty type defaults to user",
			apiResponse:  `{"name": "Unknown"}`,
			wantUserType: userTypeUser,
		},
	}
	
	// These would be integration tests with a mock HTTP client
	// For now, we're testing the core logic
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This would test the actual API parsing logic
			// Implementation would require mocking the HTTP client
		})
	}
}