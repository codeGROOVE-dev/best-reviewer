package reviewer

import (
	"context"
	"testing"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/internal/testutil"
	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

func TestNew(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	cfg := Config{
		PRCountCache: time.Hour,
	}

	finder := New(client, cfg)

	if finder == nil {
		t.Fatal("expected non-nil Finder")
	}

	if finder.client == nil {
		t.Error("expected non-nil client")
	}

	if finder.cache == nil {
		t.Error("expected non-nil cache")
	}

	if finder.prCountCache != time.Hour {
		t.Errorf("expected prCountCache to be 1 hour, got %v", finder.prCountCache)
	}
}

func TestFinder_Find_NilPR(t *testing.T) {
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	_, err := finder.Find(context.Background(), nil)
	if err == nil {
		t.Error("expected error for nil PR")
	}
}

func TestFinder_Find_SinglePersonProject(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	pr := &types.PullRequest{
		Owner:      "test-owner",
		Repository: "test-repo",
		Number:     1,
		Author:     "alice",
	}

	// Configure only the PR author as collaborator (single-person project)
	client.SetCollaborators("test-owner", "test-repo", []string{"alice"})
	client.SetBotUser("alice", false)

	candidates, err := finder.Find(ctx, pr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(candidates) != 0 {
		t.Errorf("expected 0 candidates for single-person project, got %d", len(candidates))
	}
}

func TestFinder_Find_SmallTeam_OneMember(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	pr := &types.PullRequest{
		Owner:      "test-owner",
		Repository: "test-repo",
		Number:     1,
		Author:     "alice",
	}

	// Configure two collaborators: PR author and one other person
	client.SetCollaborators("test-owner", "test-repo", []string{"alice", "bob"})
	client.SetBotUser("alice", false)
	client.SetBotUser("bob", false)

	candidates, err := finder.Find(ctx, pr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate for small team, got %d", len(candidates))
	}

	if candidates[0].Username != "bob" {
		t.Errorf("expected candidate 'bob', got %q", candidates[0].Username)
	}

	if candidates[0].SelectionMethod != "small-team" {
		t.Errorf("expected selection method 'small-team', got %q", candidates[0].SelectionMethod)
	}

	if candidates[0].ContextScore != maxContextScore {
		t.Errorf("expected context score %d, got %d", maxContextScore, candidates[0].ContextScore)
	}
}

func TestFinder_Find_SmallTeam_TwoMembers(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	pr := &types.PullRequest{
		Owner:      "test-owner",
		Repository: "test-repo",
		Number:     1,
		Author:     "alice",
	}

	// Configure three collaborators: PR author and two others
	client.SetCollaborators("test-owner", "test-repo", []string{"alice", "bob", "charlie"})
	client.SetBotUser("alice", false)
	client.SetBotUser("bob", false)
	client.SetBotUser("charlie", false)

	candidates, err := finder.Find(ctx, pr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates for small team, got %d", len(candidates))
	}

	// Should return both bob and charlie
	usernames := make(map[string]bool)
	for _, c := range candidates {
		usernames[c.Username] = true
		if c.SelectionMethod != "small-team" {
			t.Errorf("expected selection method 'small-team', got %q", c.SelectionMethod)
		}
		if c.ContextScore != maxContextScore {
			t.Errorf("expected context score %d, got %d", maxContextScore, c.ContextScore)
		}
	}

	if !usernames["bob"] {
		t.Error("expected 'bob' in candidates")
	}
	if !usernames["charlie"] {
		t.Error("expected 'charlie' in candidates")
	}
	if usernames["alice"] {
		t.Error("PR author should not be in candidates")
	}
}

func TestFinder_Find_SmallTeam_ExcludeBots(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	pr := &types.PullRequest{
		Owner:      "test-owner",
		Repository: "test-repo",
		Number:     1,
		Author:     "alice",
	}

	// Configure collaborators including a bot
	client.SetCollaborators("test-owner", "test-repo", []string{"alice", "bob", "renovate-bot"})
	client.SetBotUser("alice", false)
	client.SetBotUser("bob", false)
	client.SetBotUser("renovate-bot", true)

	candidates, err := finder.Find(ctx, pr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (bot should be excluded), got %d", len(candidates))
	}

	if candidates[0].Username != "bob" {
		t.Errorf("expected candidate 'bob', got %q", candidates[0].Username)
	}
}

func TestFinder_checkSmallTeamProject_Cached(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	pr := &types.PullRequest{
		Owner:      "test-owner",
		Repository: "test-repo",
		Number:     1,
		Author:     "alice",
	}

	client.SetCollaborators("test-owner", "test-repo", []string{"alice", "bob"})
	client.SetBotUser("alice", false)
	client.SetBotUser("bob", false)

	// First call - should hit API
	members1, count1, err := finder.checkSmallTeamProject(ctx, pr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if count1 != 1 {
		t.Errorf("expected count 1, got %d", count1)
	}

	if len(members1) != 1 || members1[0] != "bob" {
		t.Errorf("expected members [bob], got %v", members1)
	}

	// Second call - should use cache
	members2, count2, err := finder.checkSmallTeamProject(ctx, pr)
	if err != nil {
		t.Fatalf("unexpected error on cached call: %v", err)
	}

	if count2 != count1 {
		t.Errorf("cached count mismatch: expected %d, got %d", count1, count2)
	}

	if len(members2) != len(members1) {
		t.Errorf("cached members length mismatch: expected %d, got %d", len(members1), len(members2))
	}
}

func TestFinder_checkSmallTeamProject_LargeTeam(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	pr := &types.PullRequest{
		Owner:      "test-owner",
		Repository: "test-repo",
		Number:     1,
		Author:     "alice",
	}

	// Configure large team (more than 2 valid members)
	client.SetCollaborators("test-owner", "test-repo", []string{"alice", "bob", "charlie", "dave"})
	client.SetBotUser("alice", false)
	client.SetBotUser("bob", false)
	client.SetBotUser("charlie", false)
	client.SetBotUser("dave", false)

	members, count, err := finder.checkSmallTeamProject(ctx, pr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return -1 to indicate no short-circuit needed
	if count != -1 {
		t.Errorf("expected count -1 for large team, got %d", count)
	}

	if len(members) != 0 {
		t.Errorf("expected empty members for large team, got %v", members)
	}
}

func TestFinder_isValidReviewer_Bot(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	pr := &types.PullRequest{
		Owner:      "test-owner",
		Repository: "test-repo",
	}

	client.SetBotUser("dependabot", true)

	valid := finder.isValidReviewer(ctx, pr, "dependabot")
	if valid {
		t.Error("expected bot to be invalid reviewer")
	}
}

func TestFinder_isValidReviewer_NoWriteAccess(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	pr := &types.PullRequest{
		Owner:      "test-owner",
		Repository: "test-repo",
	}

	client.SetBotUser("user1", false)
	client.SetWriteAccess("test-owner", "test-repo", "user1", false)

	valid := finder.isValidReviewer(ctx, pr, "user1")
	if valid {
		t.Error("expected user without write access to be invalid reviewer")
	}
}

func TestFinder_isValidReviewer_Valid(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	pr := &types.PullRequest{
		Owner:      "test-owner",
		Repository: "test-repo",
	}

	client.SetBotUser("user1", false)
	client.SetWriteAccess("test-owner", "test-repo", "user1", true)

	valid := finder.isValidReviewer(ctx, pr, "user1")
	if !valid {
		t.Error("expected valid user to be valid reviewer")
	}
}

// Note: Full integration tests with assignees and changed files require
// complex GraphQL mocking and are covered by integration tests
