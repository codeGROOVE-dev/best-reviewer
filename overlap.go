package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// lineOverlap tracks how many lines a PR overlaps with current changes.
type lineOverlap struct {
	mergedAt     time.Time
	author       string
	reviewers    []string
	overlapScore float64
	prNumber     int
	overlapCount int
}

// findOverlappingAuthor finds authors of PRs with highest line overlap.
func (rf *ReviewerFinder) findOverlappingAuthor(ctx context.Context, pr *PullRequest, files []string, patchCache map[string]string) string {
	log.Print("  📊 Analyzing line overlap for authors...")
	overlaps := rf.analyzeLineOverlaps(ctx, pr, files, patchCache)

	sa := make(scoreAggregator)
	for _, overlap := range overlaps {
		if overlap.author == pr.Author {
			log.Printf("    Filtered (is PR author): %s", overlap.author)
			continue
		}
		if rf.isValidReviewer(ctx, pr, overlap.author) {
			if overlap.author != "" && overlap.overlapScore > 0 {
				log.Printf("    Overlap candidate: %s (score: %.2f, lines: %d, PR: #%d)",
					overlap.author, overlap.overlapScore, overlap.overlapCount, overlap.prNumber)
				sa[overlap.author] += overlap.overlapScore
			}
		}
	}

	author := sa.best()
	if author != "" {
		log.Printf("  🎯 Best overlap author: %s (total score: %.2f)", author, sa[author])
	}
	return author
}

// findOverlappingReviewer finds reviewers of PRs with highest line overlap.
func (rf *ReviewerFinder) findOverlappingReviewer(
	ctx context.Context, pr *PullRequest, files []string, patchCache map[string]string, excludeAuthor string,
) string {
	log.Print("  📊 Analyzing line overlap for reviewers...")
	overlaps := rf.analyzeLineOverlaps(ctx, pr, files, patchCache)

	sa := make(scoreAggregator)
	for _, overlap := range overlaps {
		for _, reviewer := range overlap.reviewers {
			if reviewer == pr.Author {
				log.Printf("    Filtered (is PR author): %s", reviewer)
				continue
			}
			if reviewer == excludeAuthor {
				log.Printf("    Filtered (is excluded author): %s", reviewer)
				continue
			}
			if rf.isValidReviewer(ctx, pr, reviewer) {
				if reviewer != "" && overlap.overlapScore > 0 {
					log.Printf("    Overlap candidate: %s (score: %.2f, lines: %d, PR: #%d)",
						reviewer, overlap.overlapScore, overlap.overlapCount, overlap.prNumber)
					sa[reviewer] += overlap.overlapScore
				}
			}
		}
	}

	reviewer := sa.best()
	if reviewer != "" {
		log.Printf("  🎯 Best overlap reviewer: %s (total score: %.2f)", reviewer, sa[reviewer])
	}
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

		// Get historical PRs that modified this file - limit to 5 PRs per file
		maxPRsToCheck := maxHistoricalPRs
		if maxHistoricalPRs < maxPRsToCheck {
			maxPRsToCheck = maxHistoricalPRs
		}
		historicalPRs, err := rf.historicalPRsForFile(ctx, pr.Owner, pr.Repository, file, maxPRsToCheck)
		if err != nil {
			continue
		}

		// Analyze each historical PR
		prsChecked := 0
		for _, histPR := range historicalPRs {
			// Limit total PRs checked across all files
			if prsChecked >= 3 {
				break
			}

			// Skip if we already have a high-scoring overlap for this PR
			hasHighOverlap := false
			for _, existing := range allOverlaps {
				if existing.prNumber == histPR.Number && existing.overlapScore > minOverlapThreshold {
					hasHighOverlap = true
					break
				}
			}
			if hasHighOverlap {
				continue
			}

			overlap := rf.calculatePROverlap(ctx, pr, histPR, file, changedLines)
			if overlap != nil && overlap.overlapCount > 0 {
				allOverlaps = append(allOverlaps, *overlap)
				prsChecked++
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
func (rf *ReviewerFinder) calculatePROverlap(
	ctx context.Context, currentPR *PullRequest, histPR PRInfo, file string, currentLines map[int]bool,
) *lineOverlap {
	// Check cache for this PR's file patch
	cacheKey := fmt.Sprintf("pr-file-patch:%s/%s:%d:%s", currentPR.Owner, currentPR.Repository, histPR.Number, file)
	var histPatch string
	if cached, found := rf.client.cache.value(cacheKey); found {
		if patch, ok := cached.(string); ok {
			histPatch = patch
		}
	} else {
		// Get the historical PR's patch for this file
		var err error
		histPatch, err = rf.client.filePatch(ctx, currentPR.Owner, currentPR.Repository, histPR.Number, file)
		if err != nil {
			return nil
		}
		// Cache the patch
		rf.client.cache.setWithTTL(cacheKey, histPatch, fileHistoryCacheTTL)
	}

	// Parse historical lines with context (will include 2 lines before/after)
	histLines := rf.parsePatchForChangedLines(histPatch)
	if len(histLines) == 0 {
		return nil
	}

	// Count overlapping lines with different weights
	exactMatches := 0   // Lines that match exactly
	contextMatches := 0 // Lines within context (already included in our parsing)
	nearbyMatches := 0  // Additional nearby lines

	// Create a more granular overlap detection
	for currentLine := range currentLines {
		bestMatch := -1
		for histLine := range histLines {
			distance := currentLine - histLine
			if distance < 0 {
				distance = -distance
			}

			if distance < bestMatch || bestMatch == -1 {
				bestMatch = distance
			}
		}

		// Categorize based on best match distance
		switch {
		case bestMatch == 0:
			exactMatches++
		case bestMatch <= 2:
			contextMatches++ // Within our context window
		case bestMatch <= nearbyLines:
			nearbyMatches++
		default:
			// Line is too far away to be considered relevant
		}
	}

	// Need at least some overlap
	totalOverlap := exactMatches + contextMatches + nearbyMatches
	if totalOverlap == 0 {
		return nil
	}

	// Calculate score with different weights for each type
	// Exact matches are most valuable, context matches are good, nearby are okay
	score := float64(exactMatches)*1.0 + float64(contextMatches)*contextMatchWeight + float64(nearbyMatches)*nearbyMatchWeight

	// Apply recency weight
	daysSinceMerge := time.Since(histPR.MergedAt).Hours() / 24
	recencyWeight := 1.0 / (1.0 + daysSinceMerge/overlapDecayDays) // Decay over 30 days
	score *= recencyWeight

	// Log detailed overlap for debugging
	if exactMatches > 0 || contextMatches > 0 {
		log.Printf("      Overlap with PR #%d: exact=%d, context=%d, nearby=%d, score=%.2f",
			histPR.Number, exactMatches, contextMatches, nearbyMatches, score)
	}

	return &lineOverlap{
		prNumber:     histPR.Number,
		author:       histPR.Author,
		reviewers:    histPR.Reviewers,
		mergedAt:     histPR.MergedAt,
		overlapCount: totalOverlap,
		overlapScore: score,
	}
}

// parsePatchForChangedLines extracts line numbers from a git patch.
func (*ReviewerFinder) parsePatchForChangedLines(patch string) map[int]bool {
	lines := make(map[int]bool)
	contextLines := 2 // Include 2 lines before and after

	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "@@") {
			continue
		}

		// Process hunk header line
		parts := strings.Split(line, " ")
		if len(parts) < 3 {
			continue
		}

		newPart := strings.TrimPrefix(parts[2], "+")

		// Parse range from hunk header
		var newStart, newCount int
		if _, err := fmt.Sscanf(newPart, "%d,%d", &newStart, &newCount); err != nil {
			// Try to parse as single line "start"
			if _, err := fmt.Sscanf(newPart, "%d", &newStart); err != nil {
				continue
			}
			newCount = 1
		}

		if newStart == 0 {
			continue
		}

		// Mark changed lines in the map
		for i := -contextLines; i < newCount+contextLines; i++ {
			lineNum := newStart + i
			if lineNum > 0 {
				lines[lineNum] = true
			}
		}
	}

	return lines
}
