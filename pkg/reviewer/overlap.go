package reviewer

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// lineOverlap tracks how many lines a PR overlaps with current changes.
type lineOverlap struct {
	mergedAt     time.Time
	author       string
	mergedBy     string
	reviewers    []string
	overlapScore float64
	prNumber     int
	overlapCount int
}

// analyzeLineOverlaps finds historical PRs that modified the same lines.
func (f *Finder) analyzeLineOverlaps(ctx context.Context, pr *types.PullRequest, files []string, patchCache map[string]string) []lineOverlap {
	var allOverlaps []lineOverlap

	// Analyze overlap for each file
	for _, file := range files {
		patch, exists := patchCache[file]
		if !exists || patch == "" {
			continue
		}

		// Parse changed lines from patch
		changedLines := f.parsePatchForChangedLines(patch)
		if len(changedLines) == 0 {
			continue
		}

		// Get historical PRs that modified this file - limit to maxHistoricalPRs
		maxPRsToCheck := maxHistoricalPRs
		historicalPRs, err := f.historicalPRsForFile(ctx, pr.Owner, pr.Repository, file, maxPRsToCheck)
		if err != nil {
			continue
		}

		// Analyze each historical PR
		prsChecked := 0
		for _, histPR := range historicalPRs {
			// Limit total PRs checked per file
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

			overlap := f.calculatePROverlap(ctx, pr, histPR, file, changedLines)
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
func (f *Finder) calculatePROverlap(
	ctx context.Context, currentPR *types.PullRequest, histPR types.PRInfo, file string, currentLines map[int]bool,
) *lineOverlap {
	// Check cache for this PR's file patch
	cacheKey := makeCacheKey("pr-file-patch", currentPR.Owner, currentPR.Repository, histPR.Number, file)
	var histPatch string
	if cached, found := f.cache.Get(cacheKey); found {
		if patch, ok := cached.(string); ok {
			histPatch = patch
		}
	} else {
		// Get the historical PR's patch for this file
		var err error
		histPatch, err = f.client.FilePatch(ctx, currentPR.Owner, currentPR.Repository, histPR.Number, file)
		if err != nil {
			return nil
		}
		// Cache the patch
		f.cache.SetWithTTL(cacheKey, histPatch, fileHistoryCacheTTL)
	}

	// Parse historical lines with context (will include 2 lines before/after)
	histLines := f.parsePatchForChangedLines(histPatch)
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
		slog.Info("Overlap with PR", "pr", histPR.Number, "exact", exactMatches, "context", contextMatches, "nearby", nearbyMatches, "score", score)
	}

	return &lineOverlap{
		prNumber:     histPR.Number,
		author:       histPR.Author,
		mergedBy:     histPR.MergedBy,
		reviewers:    histPR.Reviewers,
		mergedAt:     histPR.MergedAt,
		overlapCount: totalOverlap,
		overlapScore: score,
	}
}

// parsePatchForChangedLines extracts line numbers from a git patch.
func (f *Finder) parsePatchForChangedLines(patch string) map[int]bool {
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
