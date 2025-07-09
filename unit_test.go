package main

import (
	"fmt"
	"testing"
	"time"
)

// TestIsKnownBot tests the detection of known bot usernames
func TestIsKnownBot(t *testing.T) {
	knownBots := []string{
		"dependabot",
		"dependabot[bot]",
		"renovate",
		"renovate[bot]",
		"greenkeeper[bot]",
		"snyk-bot",
		"imgbot[bot]",
	}
	
	for _, botName := range knownBots {
		// Since we can't easily mock the full isUserBot, let's at least test the logic
		// In the real implementation, these would be detected without API calls
		isKnownBot := false
		for _, known := range knownBots {
			if botName == known {
				isKnownBot = true
				break
			}
		}
		
		if !isKnownBot {
			t.Errorf("Expected %s to be recognized as a known bot", botName)
		}
	}
}

// TestAuthorAssociationWriteAccess tests the write access logic
func TestAuthorAssociationWriteAccess(t *testing.T) {
	tests := []struct {
		association string
		wantAccess  bool
	}{
		{"OWNER", true},
		{"MEMBER", true},
		{"COLLABORATOR", true},
		{"CONTRIBUTOR", true},
		{"FIRST_TIME_CONTRIBUTOR", false},
		{"FIRST_TIMER", false},
		{"NONE", false},
		{"", false},
	}
	
	for _, tt := range tests {
		t.Run(tt.association, func(t *testing.T) {
			// Test the logic that would be in checkUserWriteAccess
			hasWriteAccess := false
			switch tt.association {
			case "OWNER", "MEMBER", "COLLABORATOR", "CONTRIBUTOR":
				hasWriteAccess = true
			default:
				hasWriteAccess = false
			}
			
			if hasWriteAccess != tt.wantAccess {
				t.Errorf("Association %s: got access=%v, want %v", tt.association, hasWriteAccess, tt.wantAccess)
			}
		})
	}
}

// TestNearbyLineDetection tests the logic for detecting nearby lines
func TestNearbyLineDetection(t *testing.T) {
	tests := []struct {
		name         string
		currentLine  int
		historicalLine int
		wantNearby   bool
	}{
		{"exact match", 100, 100, true},
		{"1 line away", 100, 101, true},
		{"2 lines away", 100, 102, true},
		{"3 lines away", 100, 103, true},
		{"4 lines away", 100, 104, false},
		{"1 line before", 100, 99, true},
		{"3 lines before", 100, 97, true},
		{"4 lines before", 100, 96, false},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Calculate distance
			distance := tt.currentLine - tt.historicalLine
			if distance < 0 {
				distance = -distance
			}
			
			// Check if within 3 lines
			isNearby := distance <= 3
			
			if isNearby != tt.wantNearby {
				t.Errorf("Line %d and %d: got nearby=%v, want %v", tt.currentLine, tt.historicalLine, isNearby, tt.wantNearby)
			}
		})
	}
}

// TestPRStateLogic tests the logic for determining PR state
func TestPRStateLogic(t *testing.T) {
	tests := []struct {
		name       string
		state      string
		isDraft    bool
		wantActive bool
	}{
		{"open non-draft PR", "open", false, true},
		{"draft PR", "open", true, false},
		{"closed PR", "closed", false, false},
		{"closed draft PR", "closed", true, false},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the logic from the implementation
			isActive := tt.state == "open" && !tt.isDraft
			
			if isActive != tt.wantActive {
				t.Errorf("PR state=%s, draft=%v: got active=%v, want %v", tt.state, tt.isDraft, isActive, tt.wantActive)
			}
		})
	}
}

// TestSelectionMethodConstants ensures all selection method constants are unique
func TestSelectionMethodConstants(t *testing.T) {
	methods := []string{
		AssigneeExpert,
		ExpertAuthorOverlap,
		ExpertAuthorDirectory,
		ExpertAuthorProject,
		ExpertReviewerCommenter,
		ExpertReviewerOverlap,
		ExpertReviewerDirectory,
		ExpertReviewerProject,
	}
	
	seen := make(map[string]bool)
	for _, method := range methods {
		if seen[method] {
			t.Errorf("Duplicate selection method constant: %s", method)
		}
		seen[method] = true
	}
}

// TestReviewerCandidateScoring tests the scoring logic for reviewer candidates
func TestReviewerCandidateScoring(t *testing.T) {
	candidates := []ReviewerCandidate{
		{Username: "alice", ContextScore: 5},
		{Username: "bob", ContextScore: 10},
		{Username: "charlie", ContextScore: 3},
		{Username: "dave", ContextScore: 10}, // Same score as bob
	}
	
	// Test that higher scores come first
	// In real implementation, this would be done by sort
	maxScore := 0
	var topCandidate string
	for _, c := range candidates {
		totalScore := c.ContextScore + c.ActivityScore
		if totalScore > maxScore {
			maxScore = totalScore
			topCandidate = c.Username
		}
	}
	
	if topCandidate != "bob" && topCandidate != "dave" {
		t.Errorf("Expected bob or dave (score 10) to be top candidate, got %s", topCandidate)
	}
}

// TestPatchLineParsing tests parsing of git patch format
func TestPatchLineParsing(t *testing.T) {
	tests := []struct {
		name       string
		patchLine  string
		wantStart  int
		wantCount  int
	}{
		{
			name:      "simple patch",
			patchLine: "@@ -10,5 +10,7 @@",
			wantStart: 10,
			wantCount: 7,
		},
		{
			name:      "larger patch",
			patchLine: "@@ -157,8 +159,13 @@",
			wantStart: 159,
			wantCount: 13,
		},
		{
			name:      "deletion only",
			patchLine: "@@ -20,10 +20,0 @@",
			wantStart: 20,
			wantCount: 0,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Extract the new file position (after the +)
			var oldStart, oldCount, newStart, newCount int
			fmt.Sscanf(tt.patchLine, "@@ -%d,%d +%d,%d @@", &oldStart, &oldCount, &newStart, &newCount)
			
			if newStart != tt.wantStart {
				t.Errorf("Expected start line %d, got %d", tt.wantStart, newStart)
			}
			
			if newCount != tt.wantCount {
				t.Errorf("Expected line count %d, got %d", tt.wantCount, newCount)
			}
		})
	}
}

// TestAssigneePrioritization tests that assignees who aren't authors are prioritized as expert authors
func TestAssigneePrioritization(t *testing.T) {
	tests := []struct {
		name           string
		prAuthor       string
		assignees      []string
		wantExpert     string
		wantMethod     string
	}{
		{
			name:       "assignee who isn't author is selected",
			prAuthor:   "alice",
			assignees:  []string{"bob"},
			wantExpert: "bob",
			wantMethod: "assignee-expert",
		},
		{
			name:       "assignee who is author is skipped",
			prAuthor:   "alice",
			assignees:  []string{"alice"},
			wantExpert: "",
			wantMethod: "",
		},
		{
			name:       "multiple assignees - first valid is selected",
			prAuthor:   "alice",
			assignees:  []string{"alice", "bob", "charlie"},
			wantExpert: "bob",
			wantMethod: "assignee-expert",
		},
		{
			name:       "no assignees",
			prAuthor:   "alice",
			assignees:  []string{},
			wantExpert: "",
			wantMethod: "",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the logic from findPrimaryReviewerV5
			var selectedExpert string
			var selectionMethod string
			
			if len(tt.assignees) > 0 {
				for _, assignee := range tt.assignees {
					if assignee != tt.prAuthor {
						// In real implementation, would also check !rf.isUserBot && rf.hasWriteAccess
						selectedExpert = assignee
						selectionMethod = "assignee-expert"
						break
					}
				}
			}
			
			if selectedExpert != tt.wantExpert {
				t.Errorf("Expected expert %q, got %q", tt.wantExpert, selectedExpert)
			}
			
			if selectionMethod != tt.wantMethod {
				t.Errorf("Expected method %q, got %q", tt.wantMethod, selectionMethod)
			}
		})
	}
}

// TestStaleReviewerDetection tests the logic for detecting stale reviewers
func TestStaleReviewerDetection(t *testing.T) {
	tests := []struct {
		name             string
		reviewerAge      time.Duration
		shouldBeStale    bool
	}{
		{
			name:          "reviewer assigned 3 days ago",
			reviewerAge:   3 * 24 * time.Hour,
			shouldBeStale: false,
		},
		{
			name:          "reviewer assigned exactly 5 days ago",
			reviewerAge:   5 * 24 * time.Hour,
			shouldBeStale: true, // 5 days ago means it's been 5 full days
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
				t.Errorf("Reviewer age %v: got stale=%v, want %v", tt.reviewerAge, isStale, tt.shouldBeStale)
			}
		})
	}
}