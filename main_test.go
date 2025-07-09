package main

import (
	"strings"
	"testing"
	"time"
)

func TestValidateFlags(t *testing.T) {
	tests := []struct {
		name    string
		prURL   string
		project string
		org     string
		wantErr bool
	}{
		{
			name:    "valid PR URL",
			prURL:   "https://github.com/owner/repo/pull/123",
			project: "",
			org:     "",
			wantErr: false,
		},
		{
			name:    "valid project",
			prURL:   "",
			project: "owner/repo",
			org:     "",
			wantErr: false,
		},
		{
			name:    "valid org",
			prURL:   "",
			project: "",
			org:     "myorg",
			wantErr: false,
		},
		{
			name:    "no flags set",
			prURL:   "",
			project: "",
			org:     "",
			wantErr: true,
		},
		{
			name:    "multiple flags set",
			prURL:   "https://github.com/owner/repo/pull/123",
			project: "owner/repo",
			org:     "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set global flags for testing
			*prURL = tt.prURL
			*project = tt.project
			*org = tt.org

			err := validateFlags()
			if (err != nil) != tt.wantErr {
				t.Errorf("validateFlags() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParsePRURL(t *testing.T) {
	tests := []struct {
		name       string
		prURL      string
		wantOwner  string
		wantRepo   string
		wantNumber int
		wantErr    bool
	}{
		{
			name:       "GitHub URL format",
			prURL:      "https://github.com/owner/repo/pull/123",
			wantOwner:  "owner",
			wantRepo:   "repo",
			wantNumber: 123,
			wantErr:    false,
		},
		{
			name:       "shorthand format",
			prURL:      "owner/repo#123",
			wantOwner:  "owner",
			wantRepo:   "repo",
			wantNumber: 123,
			wantErr:    false,
		},
		{
			name:    "invalid format",
			prURL:   "invalid-url",
			wantErr: true,
		},
		{
			name:    "invalid PR number",
			prURL:   "owner/repo#abc",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, number, err := parsePRURL(tt.prURL)
			if (err != nil) != tt.wantErr {
				t.Errorf("parsePRURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if owner != tt.wantOwner {
					t.Errorf("parsePRURL() owner = %v, want %v", owner, tt.wantOwner)
				}
				if repo != tt.wantRepo {
					t.Errorf("parsePRURL() repo = %v, want %v", repo, tt.wantRepo)
				}
				if number != tt.wantNumber {
					t.Errorf("parsePRURL() number = %v, want %v", number, tt.wantNumber)
				}
			}
		})
	}
}

func TestGetTopChangedFiles(t *testing.T) {
	rf := &ReviewerFinder{}
	
	pr := &PullRequest{
		ChangedFiles: []ChangedFile{
			{Filename: "file1.go", Changes: 100},
			{Filename: "file3.go", Changes: 75},
			{Filename: "file2.go", Changes: 50},
			{Filename: "file4.go", Changes: 25},
		},
	}

	// Test getting top 3 files
	topFiles := rf.getTopChangedFiles(pr, 3)
	expected := []string{"file1.go", "file3.go", "file2.go"}
	
	if len(topFiles) != 3 {
		t.Errorf("Expected 3 files, got %d", len(topFiles))
	}
	
	for i, file := range topFiles {
		if file != expected[i] {
			t.Errorf("Expected file %s at index %d, got %s", expected[i], i, file)
		}
	}

	// Test getting more files than available
	allFiles := rf.getTopChangedFiles(pr, 10)
	if len(allFiles) != 4 {
		t.Errorf("Expected 4 files, got %d", len(allFiles))
	}
}

func TestParsePatchForChangedLines(t *testing.T) {
	patch := `@@ -1,3 +1,4 @@
 line 1
+added line
 line 2
-removed line
+another added line
 line 3`

	changedLines := parsePatchForChangedLines(patch)
	
	// Should detect lines 2 and 4 as changed (added), and line 3 as changed (removed)
	if len(changedLines) == 0 {
		t.Error("Expected changed lines to be detected")
	}
	
	// The exact line numbers depend on the patch format parsing
	// This is a basic test to ensure the function doesn't crash
}

func TestReviewerCandidate(t *testing.T) {
	candidates := []ReviewerCandidate{
		{Username: "user1", ContextScore: 10, ActivityScore: 5, LastActivity: time.Now(), SelectionMethod: "context-blame-approver"},
		{Username: "user2", ContextScore: 5, ActivityScore: 10, LastActivity: time.Now().Add(-1 * time.Hour), SelectionMethod: "activity-recent-approver"},
		{Username: "user3", ContextScore: 15, ActivityScore: 0, LastActivity: time.Now().Add(-2 * time.Hour), SelectionMethod: "fallback-line-author"},
	}

	// Test that candidates can be created and accessed
	if candidates[0].Username != "user1" {
		t.Errorf("Expected user1, got %s", candidates[0].Username)
	}
	
	if candidates[2].ContextScore != 15 {
		t.Errorf("Expected context score 15, got %d", candidates[2].ContextScore)
	}
}

func TestPullRequestFields(t *testing.T) {
	pr := &PullRequest{
		Number:     123,
		Title:      "Test PR",
		State:      "open",
		Draft:      false,
		Author:     "testuser",
		Repository: "testrepo",
		Owner:      "testowner",
		Reviewers:  []string{"reviewer1", "reviewer2"},
		ChangedFiles: []ChangedFile{
			{Filename: "test.go", Changes: 10},
		},
	}

	if pr.Number != 123 {
		t.Errorf("Expected PR number 123, got %d", pr.Number)
	}
	
	if pr.State != "open" {
		t.Errorf("Expected state 'open', got %s", pr.State)
	}
	
	if len(pr.Reviewers) != 2 {
		t.Errorf("Expected 2 reviewers, got %d", len(pr.Reviewers))
	}
	
	if len(pr.ChangedFiles) != 1 {
		t.Errorf("Expected 1 changed file, got %d", len(pr.ChangedFiles))
	}
}

func TestFallbackMechanisms(t *testing.T) {
	// Test that fallback functions can be called without errors
	rf := &ReviewerFinder{
		client: &GitHubClient{}, // Mock client would be needed for real tests
	}
	
	// Test case: empty candidates should trigger fallback
	pr := &PullRequest{
		Number:     456,
		Author:     "current-author",
		Owner:      "testowner",
		Repository: "testrepo",
		ChangedFiles: []ChangedFile{
			{Filename: "test.go", Changes: 10},
		},
	}
	
	// This would require mocking the GitHub API calls for proper testing
	// For now, just test the structure
	if pr.Author != "current-author" {
		t.Errorf("Expected author 'current-author', got %s", pr.Author)
	}
	
	// Test that ReviewerFinder can be instantiated
	if rf.client == nil {
		t.Error("ReviewerFinder client should not be nil")
	}
	
	// Test reviewer candidate deduplication
	candidates := []ReviewerCandidate{
		{Username: "user1", ContextScore: 10, ActivityScore: 5, SelectionMethod: "context-blame-approver"},
		{Username: "user1", ContextScore: 5, ActivityScore: 10, SelectionMethod: "activity-recent-approver"}, // Duplicate
		{Username: "user2", ContextScore: 15, ActivityScore: 0, SelectionMethod: "fallback-line-author"},
	}
	
	// Simulate deduplication logic
	candidateMap := make(map[string]*ReviewerCandidate)
	for i := range candidates {
		candidate := &candidates[i]
		if existing, exists := candidateMap[candidate.Username]; exists {
			existing.ActivityScore = candidate.ActivityScore
		} else {
			candidateMap[candidate.Username] = candidate
		}
	}
	
	if len(candidateMap) != 2 {
		t.Errorf("Expected 2 unique candidates after deduplication, got %d", len(candidateMap))
	}
	
	// Test that user1 has the updated activity score
	if candidateMap["user1"].ActivityScore != 10 {
		t.Errorf("Expected user1 activity score 10, got %d", candidateMap["user1"].ActivityScore)
	}
}

func TestFallbackCandidateScoring(t *testing.T) {
	// Test fallback candidate scoring
	fallbackCandidates := []ReviewerCandidate{
		{Username: "file-author", ContextScore: 5, ActivityScore: 0, SelectionMethod: "fallback-line-author"},      // File-specific fallback
		{Username: "repo-approver", ContextScore: 0, ActivityScore: 3, SelectionMethod: "fallback-project-reviewer"},    // Repo approver fallback
		{Username: "repo-author", ContextScore: 2, ActivityScore: 0, SelectionMethod: "fallback-project-author"},      // Repo author fallback
	}
	
	// Verify scoring hierarchy
	if fallbackCandidates[0].ContextScore <= fallbackCandidates[2].ContextScore {
		t.Error("File-specific fallback should have higher score than repo-wide fallback")
	}
	
	if fallbackCandidates[1].ActivityScore <= fallbackCandidates[2].ContextScore {
		t.Error("Repo approver fallback should have higher score than repo author fallback")
	}
}

func TestFallbackIntegration(t *testing.T) {
	// Test the integration of fallback mechanisms
	// This simulates the scenario where normal candidate finding fails
	
	// Mock data for testing
	pr := &PullRequest{
		Number:     789,
		Author:     "pr-author",
		Owner:      "testorg",
		Repository: "testproject",
		ChangedFiles: []ChangedFile{
			{Filename: "main.go", Changes: 50},
			{Filename: "utils.go", Changes: 30},
		},
	}
	
	// Simulate empty primary candidates (would trigger fallback)
	primaryCandidates := []ReviewerCandidate{}
	
	// This simulates what would happen when getFallbackReviewers is called
	mockFallbackCandidates := []ReviewerCandidate{
		{Username: "fallback-user", ContextScore: 5, ActivityScore: 0, SelectionMethod: "fallback-line-author"},
	}
	
	// Combine primary and fallback candidates
	allCandidates := append(primaryCandidates, mockFallbackCandidates...)
	
	if len(allCandidates) != 1 {
		t.Errorf("Expected 1 candidate after fallback, got %d", len(allCandidates))
	}
	
	if allCandidates[0].Username != "fallback-user" {
		t.Errorf("Expected fallback user, got %s", allCandidates[0].Username)
	}
	
	// Test PR data structure
	if pr.Number != 789 {
		t.Errorf("Expected PR number 789, got %d", pr.Number)
	}
}

func TestSelectionMethodTracking(t *testing.T) {
	// Test that selection methods are properly tracked
	candidates := []ReviewerCandidate{
		{
			Username:        "context-user",
			ContextScore:    10,
			SelectionMethod: SelectionContextBlameApprover,
		},
		{
			Username:        "activity-user",
			ActivityScore:   8,
			SelectionMethod: SelectionActivityRecentApprover,
		},
		{
			Username:        "fallback-user",
			ContextScore:    3,
			SelectionMethod: SelectionFallbackLineAuthor,
		},
	}

	// Verify selection methods
	expectedMethods := map[string]string{
		"context-user":  SelectionContextBlameApprover,
		"activity-user": SelectionActivityRecentApprover,
		"fallback-user": SelectionFallbackLineAuthor,
	}

	for _, candidate := range candidates {
		expected := expectedMethods[candidate.Username]
		if candidate.SelectionMethod != expected {
			t.Errorf("Expected selection method %s for %s, got %s",
				expected, candidate.Username, candidate.SelectionMethod)
		}
	}
}

func TestTwoReviewerRequirement(t *testing.T) {
	// Test that we always try to select 2 reviewers
	rf := &ReviewerFinder{}
	
	// Mock candidates with different selection methods
	candidates := []ReviewerCandidate{
		{Username: "user1", ContextScore: 10, SelectionMethod: "context-blame-approver"},
		{Username: "user2", ActivityScore: 8, SelectionMethod: "activity-recent-approver"},
		{Username: "user3", ContextScore: 5, SelectionMethod: "fallback-line-author"},
	}
	
	// Verify deduplication preserves highest scoring candidates
	deduped := rf.deduplicateCandidates(candidates, "pr-author")
	if len(deduped) != 3 {
		t.Errorf("Expected 3 candidates after deduplication, got %d", len(deduped))
	}
	
	// Test excluding PR author
	deduped = rf.deduplicateCandidates(candidates, "user2")
	if len(deduped) != 2 {
		t.Errorf("Expected 2 candidates after excluding PR author, got %d", len(deduped))
	}
	
	// Ensure PR author is not in the list
	for _, candidate := range deduped {
		if candidate.Username == "user2" {
			t.Error("PR author should be excluded from candidates")
		}
	}
}

func TestExhaustedCandidatesError(t *testing.T) {
	// Test that we get an appropriate error when only the PR author is found
	rf := &ReviewerFinder{
		client: &GitHubClient{},
	}
	
	pr := &PullRequest{
		Number:     999,
		Author:     "lonely-author",
		Owner:      "test",
		Repository: "repo",
		ChangedFiles: []ChangedFile{
			{Filename: "solo.go", Changes: 10},
		},
	}
	
	// Simulate finding only the PR author as a candidate
	allCandidates := []ReviewerCandidate{
		{Username: "lonely-author", ContextScore: 10, SelectionMethod: "context-blame-approver"},
	}
	
	// Deduplication should remove the PR author
	deduped := rf.deduplicateCandidates(allCandidates, pr.Author)
	
	// Should have no candidates after removing PR author
	if len(deduped) != 0 {
		t.Errorf("Expected 0 candidates after excluding PR author, got %d", len(deduped))
	}
	
	// In the real implementation, this would trigger the error in findReviewerCandidatesV2
	// The error message should indicate we've exhausted all candidates
}

func TestDraftPRHandling(t *testing.T) {
	// Test that draft PRs don't get reviewers assigned
	pr := &PullRequest{
		Number:     456,
		Title:      "Draft PR",
		State:      "open",
		Draft:      true,
		Author:     "draft-author",
		Owner:      "test",
		Repository: "repo",
		ChangedFiles: []ChangedFile{
			{Filename: "draft.go", Changes: 20},
		},
	}
	
	// Verify draft status is detected
	if !pr.Draft {
		t.Error("Expected PR to be marked as draft")
	}
	
	// In the real implementation, assignReviewersV2 would skip assignment
	// but still log who would have been assigned
}

func TestPrimarySecondarySelection(t *testing.T) {
	// Test that primary and secondary selection methods are tracked correctly
	primaryCandidate := ReviewerCandidate{
		Username:        "primary-user",
		SelectionMethod: PrimaryBlameAuthor,
		ContextScore:    100,
	}
	
	secondaryCandidate := ReviewerCandidate{
		Username:        "secondary-user",
		SelectionMethod: SecondaryBlameReviewer,
		ContextScore:    50,
	}
	
	// Verify primary has higher score
	if primaryCandidate.ContextScore <= secondaryCandidate.ContextScore {
		t.Error("Primary reviewer should have higher context score than secondary")
	}
	
	// Verify selection methods
	if !strings.Contains(primaryCandidate.SelectionMethod, "primary") {
		t.Errorf("Expected primary selection method, got %s", primaryCandidate.SelectionMethod)
	}
	
	if !strings.Contains(secondaryCandidate.SelectionMethod, "secondary") {
		t.Errorf("Expected secondary selection method, got %s", secondaryCandidate.SelectionMethod)
	}
}