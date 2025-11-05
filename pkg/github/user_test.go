package github

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/cache"
)

func TestNewUserCache(t *testing.T) {
	uc := NewUserCache()

	if uc == nil {
		t.Fatal("expected non-nil UserCache")
	}

	if uc.users == nil {
		t.Error("expected users map to be initialized")
	}
}

func TestUserCache_SetAndGet(t *testing.T) {
	uc := NewUserCache()

	info := &UserInfo{
		IsBot:      true,
		HasAccess:  false,
		LastUpdate: time.Now(),
	}

	uc.Set("user1", info)

	retrieved, ok := uc.Get("user1")
	if !ok {
		t.Fatal("expected to find user1")
	}

	if retrieved.IsBot != info.IsBot {
		t.Errorf("expected IsBot=%v, got %v", info.IsBot, retrieved.IsBot)
	}

	if retrieved.HasAccess != info.HasAccess {
		t.Errorf("expected HasAccess=%v, got %v", info.HasAccess, retrieved.HasAccess)
	}
}

func TestUserCache_GetNonExistent(t *testing.T) {
	uc := NewUserCache()

	_, ok := uc.Get("nonexistent")
	if ok {
		t.Error("expected not to find nonexistent user")
	}
}

func TestUserCache_ConcurrentAccess(t *testing.T) {
	uc := NewUserCache()

	done := make(chan bool)

	// Concurrent writes
	for i := range 10 {
		go func(id int) {
			info := &UserInfo{
				IsBot:      id%2 == 0,
				HasAccess:  true,
				LastUpdate: time.Now(),
			}
			uc.Set("user"+string(rune(id)), info)
			done <- true
		}(i)
	}

	// Concurrent reads
	for i := range 10 {
		go func(id int) {
			uc.Get("user" + string(rune(id)))
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for range 20 {
		<-done
	}
}

func TestClient_IsUserBot(t *testing.T) {
	c := &Client{}
	ctx := context.Background()

	tests := []struct {
		name     string
		username string
		want     bool
	}{
		// Bot patterns
		{"dependabot[bot]", "dependabot[bot]", true},
		{"renovate-bot", "renovate-bot", true},
		{"security_bot", "security_bot", true},
		{"bot-scanner", "bot-scanner", true},
		{"scanner.bot", "scanner.bot", true},

		// Known bots
		{"github-actions", "github-actions", true},
		{"dependabot", "dependabot", true},
		{"renovate", "renovate", true},
		{"codecov", "codecov", true},
		{"circleci", "circleci", true},
		{"mergify", "mergify", true},

		// Service accounts
		{"deploy-automation", "deploy-automation", true},
		{"ci-service", "ci-service", true},
		{"cd-deploy", "cd-deploy", true},
		{"test-automation", "test-automation", true},

		// Regular users
		{"johndoe", "johndoe", false},
		{"alice-smith", "alice-smith", false},
		{"bob_jones", "bob_jones", false},
		{"user123", "user123", false},

		// Edge cases - contain bot-like strings but aren't bots
		{"abbott", "abbott", false},     // contains "bot" but not a pattern match
		{"robotics", "robotics", false}, // contains "bot" but not a pattern match
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.IsUserBot(ctx, tt.username)
			if got != tt.want {
				t.Errorf("IsUserBot(%q) = %v, want %v", tt.username, got, tt.want)
			}
		})
	}
}

func TestClient_IsUserBot_CaseInsensitive(t *testing.T) {
	c := &Client{}
	ctx := context.Background()

	// Test case insensitivity
	usernames := []string{
		"DEPENDABOT[BOT]",
		"Dependabot[Bot]",
		"GitHub-Actions",
		"RENOVATE-BOT",
	}

	for _, username := range usernames {
		if !c.IsUserBot(ctx, username) {
			t.Errorf("expected %q to be detected as bot (case insensitive)", username)
		}
	}
}

func TestClient_CacheUserTypeFromGraphQL_Bot(t *testing.T) {
	c := &Client{
		userCache: NewUserCache(),
	}
	ctx := context.Background()

	c.CacheUserTypeFromGraphQL(ctx, "some-bot", "Bot")

	info, ok := c.userCache.Get("some-bot")
	if !ok {
		t.Fatal("expected user to be cached")
	}

	if !info.IsBot {
		t.Error("expected IsBot to be true for type 'Bot'")
	}

	if info.LastUpdate.IsZero() {
		t.Error("expected LastUpdate to be set")
	}
}

func TestClient_CacheUserTypeFromGraphQL_Organization(t *testing.T) {
	c := &Client{
		userCache: NewUserCache(),
	}
	ctx := context.Background()

	c.CacheUserTypeFromGraphQL(ctx, "some-org", "Organization")

	info, ok := c.userCache.Get("some-org")
	if !ok {
		t.Fatal("expected user to be cached")
	}

	if info.IsBot {
		t.Error("expected IsBot to be false for type 'Organization'")
	}
}

func TestClient_CacheUserTypeFromGraphQL_User(t *testing.T) {
	c := &Client{
		userCache: NewUserCache(),
	}
	ctx := context.Background()

	// Regular user
	c.CacheUserTypeFromGraphQL(ctx, "johndoe", "User")

	info, ok := c.userCache.Get("johndoe")
	if !ok {
		t.Fatal("expected user to be cached")
	}

	// Should call IsUserBot to determine bot status
	if info.IsBot {
		t.Error("expected IsBot to be false for regular user")
	}
}

func TestClient_CacheUserTypeFromGraphQL_UserLikeBot(t *testing.T) {
	c := &Client{
		userCache: NewUserCache(),
	}
	ctx := context.Background()

	// User with bot-like name
	c.CacheUserTypeFromGraphQL(ctx, "renovate", "User")

	info, ok := c.userCache.Get("renovate")
	if !ok {
		t.Fatal("expected user to be cached")
	}

	// Should be detected as bot based on username
	if !info.IsBot {
		t.Error("expected IsBot to be true for user with bot-like name")
	}
}

func TestClient_CacheUserTypeFromGraphQL_NilCache(t *testing.T) {
	c := &Client{
		userCache: nil,
	}
	ctx := context.Background()

	// Should not panic with nil cache
	c.CacheUserTypeFromGraphQL(ctx, "user1", "User")
}

func TestClient_CacheUserTypeFromGraphQL_EmptyUsername(t *testing.T) {
	c := &Client{
		userCache: NewUserCache(),
	}
	ctx := context.Background()

	// Should not cache with empty username
	c.CacheUserTypeFromGraphQL(ctx, "", "User")

	if len(c.userCache.users) != 0 {
		t.Error("expected no users to be cached for empty username")
	}
}

func TestUserCacheConstants(t *testing.T) {
	// Verify constants are reasonable
	if prStaleDaysThreshold <= 0 {
		t.Error("prStaleDaysThreshold should be positive")
	}

	if prStaleDaysThreshold != 90 {
		t.Errorf("expected prStaleDaysThreshold to be 90 days, got %d", prStaleDaysThreshold)
	}

	if prCountCacheTTL <= 0 {
		t.Error("prCountCacheTTL should be positive")
	}

	if prCountCacheTTL != 6*time.Hour {
		t.Errorf("expected prCountCacheTTL to be 6 hours, got %v", prCountCacheTTL)
	}

	if prCountFailureCacheTTL <= 0 {
		t.Error("prCountFailureCacheTTL should be positive")
	}

	if prCountFailureCacheTTL != 10*time.Minute {
		t.Errorf("expected prCountFailureCacheTTL to be 10 minutes, got %v", prCountFailureCacheTTL)
	}
}

func TestUserInfo(t *testing.T) {
	now := time.Now()

	info := &UserInfo{
		IsBot:      true,
		HasAccess:  false,
		LastUpdate: now,
	}

	if !info.IsBot {
		t.Error("expected IsBot to be true")
	}

	if info.HasAccess {
		t.Error("expected HasAccess to be false")
	}

	if !info.LastUpdate.Equal(now) {
		t.Errorf("expected LastUpdate to be %v, got %v", now, info.LastUpdate)
	}
}

func TestClient_IsUserBot_AllBotPatterns(t *testing.T) {
	c := &Client{}
	ctx := context.Background()

	// Test all bot patterns mentioned in the code
	botPatterns := []string{
		"user[bot]",
		"user-bot",
		"user_bot",
		"bot-user",
		"bot_user",
		"user.bot",
		"github-actions-test",
		"my-dependabot",
		"renovate-config",
		"greenkeeper-io",
		"snyk-bot",
		"codecov-io",
		"coveralls-bot",
		"travis-ci",
		"circleci-bot",
		"jenkins-ci",
		"buildkite-agent",
		"semaphore-ci",
		"appveyor-bot",
		"azure-pipelines-bot",
		"github-classroom-bot",
		"imgbot-test",
		"allcontributors-bot",
		"whitesource-bot",
		"mergify-bot",
		"sonarcloud-bot",
		"deepsource-bot",
		"codefactor-bot",
		"lgtm-bot",
		"codacy-bot",
		"hound-bot",
		"stale-bot",
	}

	for _, username := range botPatterns {
		if !c.IsUserBot(ctx, username) {
			t.Errorf("expected %q to be detected as bot", username)
		}
	}
}

func TestClient_IsUserBot_AllOrgPatterns(t *testing.T) {
	c := &Client{}
	ctx := context.Background()

	// Test organization/service account patterns
	orgPatterns := []string{
		"octo-sts-test",
		"octocat-service",
		"app-sts",
		"service-svc",
		"api-service",
		"database-system",
		"deploy-automation",
		"test-ci",
		"prod-cd",
		"app-deploy",
		"feature-release",
		"release-manager-prod",
		"app-build",
		"integration-test",
		"system-admin",
		"app-security",
		"security-scanner-prod",
		"compliance-automation",
	}

	for _, username := range orgPatterns {
		if !c.IsUserBot(ctx, username) {
			t.Errorf("expected %q to be detected as bot/service account", username)
		}
	}
}

func TestClient_IsUserBot_AutomationPatterns(t *testing.T) {
	c := &Client{}
	ctx := context.Background()

	automationPatterns := []string{
		"test-automation",
		"auto-deploy",
		"automate-release",
	}

	for _, username := range automationPatterns {
		if !c.IsUserBot(ctx, username) {
			t.Errorf("expected %q to be detected as automation account", username)
		}
	}
}

func TestClient_IsUserBot_HumanNames(t *testing.T) {
	c := &Client{}
	ctx := context.Background()

	// Names that should NOT be detected as bots
	humanNames := []string{
		"alice",
		"bob",
		"charlie",
		"dave",
		"eve",
		"john-smith",
		"jane_doe",
		"user123",
		"developer1",
		"contributor42",
		"maintainer",
		"reviewer",
		"author",
	}

	for _, username := range humanNames {
		if c.IsUserBot(ctx, username) {
			t.Errorf("expected %q NOT to be detected as bot", username)
		}
	}
}

func TestMakeCacheKey(t *testing.T) {
	tests := []struct {
		parts []string
		want  string
	}{
		{[]string{"owner", "repo", "123"}, "owner:repo:123"},
		{[]string{"single"}, "single"},
		{[]string{"a", "b", "c", "d"}, "a:b:c:d"},
		{[]string{}, ""},
		{[]string{"", "b"}, ":b"},
		{[]string{"a", ""}, "a:"},
	}

	for _, tt := range tests {
		got := makeCacheKey(tt.parts...)
		if got != tt.want {
			t.Errorf("makeCacheKey(%v) = %q, want %q", tt.parts, got, tt.want)
		}
	}
}

func TestClient_Collaborators_Forbidden(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Status:     "403 Forbidden",
				Body:       io.NopCloser(strings.NewReader(`{"message":"Forbidden"}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	diskCache, err := cache.NewDiskCache(time.Hour, t.TempDir())
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}

	c := &Client{
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		cache:      diskCache,
	}

	ctx := context.Background()
	_, err = c.Collaborators(ctx, "owner", "repo")

	if err == nil {
		t.Error("expected error for 403 response")
	}

	if !strings.Contains(err.Error(), "status 403") {
		t.Errorf("expected error to mention status 403, got %q", err.Error())
	}

	// Verify that permission cache key was set
	permCacheKey := makeCacheKey("collaborators-permission", "owner", "repo")
	cached, found := c.cache.Get(permCacheKey)
	if !found {
		t.Error("expected permission cache key to be set on 403")
	}
	if val, ok := cached.(bool); !ok || !val {
		t.Error("expected permission cache to be true")
	}
}

func TestClient_searchPRCount_RateLimitExceeded(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Status:     "403 Forbidden",
				Body:       io.NopCloser(strings.NewReader(`{"message":"Rate limit exceeded"}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
	}

	ctx := context.Background()
	_, err := c.searchPRCount(ctx, "is:pr author:testuser")

	if err == nil {
		t.Error("expected error for rate limit exceeded")
	}

	if !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Errorf("expected error to mention rate limit, got %q", err.Error())
	}
}

func TestClient_searchPRCount_NonOKStatus(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Status:     "400 Bad Request",
				Body:       io.NopCloser(strings.NewReader(`{"message":"Invalid query"}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
	}

	ctx := context.Background()
	_, err := c.searchPRCount(ctx, "invalid query")

	if err == nil {
		t.Error("expected error for bad request")
	}

	if !strings.Contains(err.Error(), "search failed") {
		t.Errorf("expected error to mention search failed, got %q", err.Error())
	}
}

func TestClient_searchPRCount_InvalidJSON(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(`invalid json`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
	}

	ctx := context.Background()
	_, err := c.searchPRCount(ctx, "is:pr")

	if err == nil {
		t.Error("expected error for invalid JSON")
	}

	if !strings.Contains(err.Error(), "failed to decode") {
		t.Errorf("expected error to mention decode failure, got %q", err.Error())
	}
}
