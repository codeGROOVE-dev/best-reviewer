package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// lineOverlap tracks how many lines a PR overlaps with current changes.
type lineOverlap struct {
	prNumber     int
	author       string
	reviewers    []string
	mergedAt     time.Time
	overlapCount int
	overlapScore float64
}

// findOverlappingAuthor finds authors of PRs with highest line overlap.
func (rf *ReviewerFinder) findOverlappingAuthor(ctx context.Context, pr *PullRequest, files []string, patchCache map[string]string) string {
	overlaps := rf.analyzeLineOverlaps(ctx, pr, files, patchCache)
	
	sa := make(scoreAggregator)
	for _, overlap := range overlaps {
		if overlap.author != pr.Author && rf.isValidReviewer(ctx, pr, overlap.author) {
			sa.add(overlap.author, overlap.overlapScore)
		}
	}
	
	author, _ := sa.best()
	return author
}

// findOverlappingReviewer finds reviewers of PRs with highest line overlap.
func (rf *ReviewerFinder) findOverlappingReviewer(ctx context.Context, pr *PullRequest, files []string, patchCache map[string]string, excludeAuthor string) string {
	overlaps := rf.analyzeLineOverlaps(ctx, pr, files, patchCache)
	
	sa := make(scoreAggregator)
	for _, overlap := range overlaps {
		for _, reviewer := range overlap.reviewers {
			if reviewer != pr.Author && reviewer != excludeAuthor && rf.isValidReviewer(ctx, pr, reviewer) {
				sa.add(reviewer, overlap.overlapScore)
			}
		}
	}
	
	reviewer, _ := sa.best()
	return reviewer
}

// analyzeLineOverlaps finds historical PRs that modified the same lines.
func (rf *ReviewerFinder) analyzeLineOverlaps(ctx context.Context, pr *PullRequest, files []string, patchCache map[string]string) []lineOverlap {
	var allOverlaps []lineOverlap
	
	// Analyze overlap for each file
	for _, file := range files {
		patch, exists := patchCache[file]
		if !exists || patch == "" {
			continue
		}
		
		// Parse changed lines from patch
		changedLines := rf.parsePatchForChangedLines(patch)
		if len(changedLines) == 0 {
			continue
		}
		
		// Get historical PRs that modified this file
		historicalPRs, err := rf.getHistoricalPRsForFile(ctx, pr.Owner, pr.Repository, file, maxHistoricalPRs)
		if err != nil {
			continue
		}
		
		// Analyze each historical PR
		for _, histPR := range historicalPRs {
			overlap := rf.calculatePROverlap(ctx, pr, histPR, file, changedLines)
			if overlap != nil && overlap.overlapCount > 0 {
				allOverlaps = append(allOverlaps, *overlap)
			}
		}
	}
	
	// Sort by overlap score (highest first)
	sort.Slice(allOverlaps, func(i, j int) bool {
		return allOverlaps[i].overlapScore > allOverlaps[j].overlapScore
	})
	
	return allOverlaps
}

// calculatePROverlap calculates the line overlap between current PR and a historical PR.
func (rf *ReviewerFinder) calculatePROverlap(ctx context.Context, currentPR *PullRequest, histPR PRInfo, file string, currentLines map[int]bool) *lineOverlap {
	// Get the historical PR's patch for this file
	histPatch, err := rf.client.getFilePatch(ctx, currentPR.Owner, currentPR.Repository, histPR.Number, file)
	if err != nil {
		return nil
	}
	
	histLines := rf.parsePatchForChangedLines(histPatch)
	if len(histLines) == 0 {
		return nil
	}
	
	// Count overlapping and nearby lines
	exactMatches := 0
	nearbyMatches := 0
	
	for currentLine := range currentLines {
		for histLine := range histLines {
			distance := abs(currentLine - histLine)
			if distance == 0 {
				exactMatches++
			} else if distance <= nearbyLines {
				nearbyMatches++
			}
		}
	}
	
	if exactMatches == 0 && nearbyMatches == 0 {
		return nil
	}
	
	// Calculate score with recency weight
	daysSinceMerge := time.Since(histPR.MergedAt).Hours() / 24
	recencyWeight := 1.0 / (1.0 + daysSinceMerge/30.0) // Decay over 30 days
	
	score := float64(exactMatches) + float64(nearbyMatches)*0.5
	score *= recencyWeight
	
	return &lineOverlap{
		prNumber:     histPR.Number,
		author:       histPR.Author,
		reviewers:    histPR.Reviewers,
		mergedAt:     histPR.MergedAt,
		overlapCount: exactMatches + nearbyMatches,
		overlapScore: score,
	}
}

// parsePatchForChangedLines extracts line numbers from a git patch.
func (rf *ReviewerFinder) parsePatchForChangedLines(patch string) map[int]bool {
	lines := make(map[int]bool)
	
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "@@") {
			// Parse hunk header: @@ -old_start,old_count +new_start,new_count @@
			var newStart, newCount int
			parts := strings.Split(line, " ")
			if len(parts) >= 3 {
				newPart := strings.TrimPrefix(parts[2], "+")
				if _, err := fmt.Sscanf(newPart, "%d,%d", &newStart, &newCount); err == nil {
					// Mark all lines in this hunk as changed
					for i := 0; i < newCount; i++ {
						lines[newStart+i] = true
					}
				} else if _, err := fmt.Sscanf(newPart, "%d", &newStart); err == nil {
					// Single line change
					lines[newStart] = true
				}
			}
		}
	}
	
	return lines
}

