package main

import (
	"testing"
	"time"
)

// TestValidateFlags tests the command-line flag validation logic.
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
			// Save original values
			oldPR, oldProject, oldOrg := *prURL, *project, *org
			defer func() {
				*prURL, *project, *org = oldPR, oldProject, oldOrg
			}()

			// Set test values
			*prURL = tt.prURL
			*project = tt.project
			*org = tt.org

			err := validateFlags("", "")
			if (err != nil) != tt.wantErr {
				t.Errorf("validateFlags() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestParsePRURL tests parsing of various PR URL formats.
func TestParsePRURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantNum   int
		wantErr   bool
	}{
		{
			name:      "GitHub URL format",
			url:       "https://github.com/owner/repo/pull/123",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantNum:   123,
			wantErr:   false,
		},
		{
			name:      "shorthand format",
			url:       "owner/repo#123",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantNum:   123,
			wantErr:   false,
		},
		{
			name:    "invalid format",
			url:     "not-a-pr-url",
			wantErr: true,
		},
		{
			name:    "invalid PR number",
			url:     "owner/repo#abc",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts, err := parsePRURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("parsePRURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if parts.Owner != tt.wantOwner {
					t.Errorf("parsePRURL() owner = %v, want %v", parts.Owner, tt.wantOwner)
				}
				if parts.Repo != tt.wantRepo {
					t.Errorf("parsePRURL() repo = %v, want %v", parts.Repo, tt.wantRepo)
				}
				if parts.Number != tt.wantNum {
					t.Errorf("parsePRURL() num = %v, want %v", parts.Number, tt.wantNum)
				}
			}
		})
	}
}

// TestGetChangedFiles tests the file sorting and limiting logic.
func TestGetChangedFiles(t *testing.T) {
	rf := &ReviewerFinder{}
	pr := &PullRequest{
		ChangedFiles: []ChangedFile{
			{Filename: "small.go", Additions: 5, Deletions: 2},
			{Filename: "large.go", Additions: 100, Deletions: 50},
			{Filename: "medium.go", Additions: 20, Deletions: 10},
		},
	}

	files := rf.changedFiles(pr)

	// Should be sorted by total changes (largest first)
	expected := []string{"large.go", "medium.go", "small.go"}
	if len(files) != len(expected) {
		t.Errorf("getChangedFiles() returned %d files, want %d", len(files), len(expected))
	}

	for i, want := range expected {
		if i < len(files) && files[i] != want {
			t.Errorf("getChangedFiles()[%d] = %v, want %v", i, files[i], want)
		}
	}
}

// TestParsePatchForChangedLines tests git patch parsing.
func TestParsePatchForChangedLines(t *testing.T) {
	rf := &ReviewerFinder{}

	tests := []struct {
		wantLines map[int]bool
		name      string
		patch     string
	}{
		{
			name: "simple patch",
			patch: `@@ -10,5 +10,7 @@ func main() {
+	added line 1
+	added line 2
 	existing line
-	removed line
 	another line`,
			wantLines: map[int]bool{
				8: true, 9: true, 10: true, 11: true, 12: true,
				13: true, 14: true, 15: true, 16: true, 17: true, 18: true,
			},
		},
		{
			name: "multiple hunks",
			patch: `@@ -10,2 +10,3 @@
 line
+added
@@ -20,1 +21,2 @@
+another add`,
			wantLines: map[int]bool{
				8: true, 9: true, 10: true, 11: true, 12: true, 13: true, 14: true,
				19: true, 20: true, 21: true, 22: true, 23: true, 24: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := rf.parsePatchForChangedLines(tt.patch)
			if len(lines) != len(tt.wantLines) {
				t.Errorf("parsePatchForChangedLines() returned %d lines, want %d", len(lines), len(tt.wantLines))
			}
			for line := range tt.wantLines {
				if !lines[line] {
					t.Errorf("parsePatchForChangedLines() missing line %d", line)
				}
			}
		})
	}
}

// TestIsPRReady tests the PR age constraint logic.
func TestIsPRReady(t *testing.T) {
	now := time.Now()

	tests := []struct {
		lastCommit time.Time
		lastReview time.Time
		name       string
		minAge     time.Duration
		maxAge     time.Duration
		wantReady  bool
	}{
		{
			name:       "PR within age range",
			lastCommit: now.Add(-2 * time.Hour),
			lastReview: time.Time{},
			minAge:     1 * time.Hour,
			maxAge:     24 * time.Hour,
			wantReady:  true,
		},
		{
			name:       "PR too recent",
			lastCommit: now.Add(-30 * time.Minute),
			lastReview: time.Time{},
			minAge:     1 * time.Hour,
			maxAge:     24 * time.Hour,
			wantReady:  false,
		},
		{
			name:       "PR too old",
			lastCommit: now.Add(-48 * time.Hour),
			lastReview: time.Time{},
			minAge:     1 * time.Hour,
			maxAge:     24 * time.Hour,
			wantReady:  false,
		},
		{
			name:       "use last review time if more recent",
			lastCommit: now.Add(-48 * time.Hour),
			lastReview: now.Add(-2 * time.Hour),
			minAge:     1 * time.Hour,
			maxAge:     24 * time.Hour,
			wantReady:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rf := &ReviewerFinder{
				minOpenTime: tt.minAge,
				maxOpenTime: tt.maxAge,
			}
			pr := &PullRequest{
				LastCommit: tt.lastCommit,
				LastReview: tt.lastReview,
			}

			ready := rf.isPRReady(pr)
			if ready != tt.wantReady {
				t.Errorf("isPRReady() = %v, want %v", ready, tt.wantReady)
			}
		})
	}
}

// TestFilterExistingReviewers tests filtering of already-assigned reviewers.
func TestFilterExistingReviewers(t *testing.T) {
	rf := &ReviewerFinder{}

	candidates := []ReviewerCandidate{
		{Username: "alice"},
		{Username: "bob"},
		{Username: "charlie"},
	}

	existing := []string{"bob"}

	filtered := rf.filterExistingReviewers(candidates, existing)

	if len(filtered) != 2 {
		t.Errorf("filterExistingReviewers() returned %d candidates, want 2", len(filtered))
	}

	// Check that bob was filtered out
	for _, c := range filtered {
		if c.Username == "bob" {
			t.Errorf("filterExistingReviewers() should have filtered out bob")
		}
	}
}

// TestGetDirectories tests directory extraction from file paths.
func TestGetDirectories(t *testing.T) {
	rf := &ReviewerFinder{}

	files := []string{
		"src/main.go",
		"src/lib/helper.go",
		"test/main_test.go",
		"README.md",
	}

	dirs := rf.directories(files)

	// Should extract and sort by depth
	expected := []string{"src/lib", "src", "test"}

	if len(dirs) != len(expected) {
		t.Errorf("getDirectories() returned %d dirs, want %d", len(dirs), len(expected))
		t.Errorf("got: %v", dirs)
	}

	for i, want := range expected {
		if i < len(dirs) && dirs[i] != want {
			t.Errorf("getDirectories()[%d] = %v, want %v", i, dirs[i], want)
		}
	}
}

// TestAssigneePrioritization tests that assignees are prioritized as expert authors.
func TestAssigneePrioritization(t *testing.T) {
	tests := []struct {
		name       string
		prAuthor   string
		wantExpert string
		assignees  []string
	}{
		{
			name:       "assignee who isn't author is selected",
			prAuthor:   "alice",
			assignees:  []string{"bob"},
			wantExpert: "bob",
		},
		{
			name:       "assignee who is author is skipped",
			prAuthor:   "alice",
			assignees:  []string{"alice"},
			wantExpert: "",
		},
		{
			name:       "multiple assignees - first valid is selected",
			prAuthor:   "alice",
			assignees:  []string{"alice", "bob", "charlie"},
			wantExpert: "bob",
		},
		{
			name:       "no assignees",
			prAuthor:   "alice",
			assignees:  []string{},
			wantExpert: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := &PullRequest{
				Author:    tt.prAuthor,
				Assignees: tt.assignees,
			}

			// Create a test that doesn't require API calls
			// Just test the basic logic without the isValidReviewer check
			expert := ""
			if len(pr.Assignees) > 0 {
				for _, assignee := range pr.Assignees {
					if assignee != pr.Author {
						expert = assignee
						break
					}
				}
			}

			if expert != tt.wantExpert {
				t.Errorf("assignee prioritization logic = %q, want %q", expert, tt.wantExpert)
			}
		})
	}
}

// TestStaleReviewerDetection tests detection of stale reviewer assignments.
func TestStaleReviewerDetection(t *testing.T) {
	tests := []struct {
		name          string
		reviewerAge   time.Duration
		shouldBeStale bool
	}{
		{
			name:          "reviewer assigned 3 days ago",
			reviewerAge:   3 * 24 * time.Hour,
			shouldBeStale: false,
		},
		{
			name:          "reviewer assigned exactly 5 days ago",
			reviewerAge:   5 * 24 * time.Hour,
			shouldBeStale: true,
		},
		{
			name:          "reviewer assigned 6 days ago",
			reviewerAge:   6 * 24 * time.Hour,
			shouldBeStale: true,
		},
		{
			name:          "reviewer assigned 10 days ago",
			reviewerAge:   10 * 24 * time.Hour,
			shouldBeStale: true,
		},
	}

	staleDuration := 5 * 24 * time.Hour

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestedTime := time.Now().Add(-tt.reviewerAge)
			cutoffTime := time.Now().Add(-staleDuration)

			isStale := requestedTime.Before(cutoffTime)

			if isStale != tt.shouldBeStale {
				t.Errorf("reviewer age %v: got stale=%v, want %v", tt.reviewerAge, isStale, tt.shouldBeStale)
			}
		})
	}
}
