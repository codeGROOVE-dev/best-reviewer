package reviewer

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// candidateWeight represents a reviewer candidate with their weight.
type candidateWeight struct {
	username        string
	sourceScores    map[string]int // Score breakdown by source: "file-author" -> 10, "recent-merger" -> 50, etc.
	weight          int
	workloadPenalty int
	finalScore      int
}

// recentContributor represents someone who has recently contributed to the repo.
type recentContributor struct {
	lastActivity time.Time
	username     string
	prCount      int
}

// findReviewersOptimized finds reviewers using scoring with workload penalties.
func (f *Finder) findReviewersOptimized(ctx context.Context, pr *types.PullRequest) []types.ReviewerCandidate {
	// Get the 3 files with the largest delta, excluding lock files
	topFiles := f.topChangedFilesFiltered(pr, 3)
	if len(topFiles) == 0 {
		slog.Info("No changed files to analyze")
		return nil // Return nil to trigger fallback
	}

	slog.Info("Analyzing top files with largest changes", "count", len(topFiles))

	// Check assignees first - they get highest priority
	var candidates []candidateWeight
	for _, assignee := range pr.Assignees {
		if assignee == pr.Author {
			slog.Info("Skipping assignee (is PR author)", "assignee", assignee)
			continue
		}
		// Assignees get very high weight (200 points) as explicit assignment is a strong signal
		slog.Info("Adding candidate from PR assignee", "username", assignee, "weight", 200)
		candidates = append(candidates, candidateWeight{
			username:     assignee,
			weight:       200,
			sourceScores: map[string]int{"assignee": 200},
		})
	}

	// Collect candidates with weights from recent PRs that modified these files
	fileCandidates := f.collectWeightedCandidates(ctx, pr, topFiles)
	if len(fileCandidates) == 0 && len(candidates) == 0 {
		slog.Info("No candidates found from file history or assignees")
		return nil // Return nil to trigger fallback
	}

	// Merge file candidates with assignees
	candidates = append(candidates, fileCandidates...)

	slog.Info("Found weighted candidates from file history", "count", len(fileCandidates), "total_with_assignees", len(candidates))
	for i, c := range candidates {
		if i < 10 { // Log first 10
			var sourceList []string
			for source, score := range c.sourceScores {
				sourceList = append(sourceList, fmt.Sprintf("%s:%d", source, score))
			}
			slog.Info("File history candidate", "username", c.username, "weight", c.weight, "sources", strings.Join(sourceList, ","))
		}
	}

	// Add line-overlap scoring (analyzes actual lines being modified)
	patchCache, err := f.fetchAllPRFiles(ctx, pr.Owner, pr.Repository, pr.Number)
	if err != nil {
		slog.Warn("Failed to fetch PR patches for line overlap analysis", "error", err)
	} else {
		slog.Info("Analyzing line overlap with historical PRs")
		overlaps := f.analyzeLineOverlaps(ctx, pr, topFiles, patchCache)
		slog.Info("Line overlap analysis complete", "overlaps_found", len(overlaps))

		// Build candidate map from existing candidates
		candidateMap := make(map[string]*candidateWeight)
		for i := range candidates {
			candidateMap[candidates[i].username] = &candidates[i]
		}

		// Add overlap scores based on actual line overlap count
		for _, overlap := range overlaps {
			// Use the actual overlap count (number of lines) as the score, not the weighted float
			overlapScore := overlap.overlapCount
			slog.Info("Processing overlap", "pr", overlap.prNumber, "author", overlap.author, "mergedBy", overlap.mergedBy, "reviewers", overlap.reviewers, "overlap_lines", overlapScore)
			if overlapScore == 0 {
				slog.Info("Skipping overlap with zero lines", "pr", overlap.prNumber)
				continue
			}

			// Add author with full line-overlap score
			if overlap.author != "" && overlap.author != pr.Author {
				if existing, ok := candidateMap[overlap.author]; ok {
					slog.Info("Boosting candidate from line overlap (author)", "username", overlap.author, "pr", overlap.prNumber, "boost", overlapScore)
					existing.weight += overlapScore
					existing.sourceScores["line-author"] += overlapScore
				} else {
					slog.Info("Adding candidate from line overlap (author)", "username", overlap.author, "pr", overlap.prNumber, "score", overlapScore)
					candidateMap[overlap.author] = &candidateWeight{
						username:     overlap.author,
						weight:       overlapScore,
						sourceScores: map[string]int{"line-author": overlapScore},
					}
				}
			}

			// Add reviewers with full line-overlap score
			for _, reviewer := range overlap.reviewers {
				if reviewer != "" && reviewer != pr.Author {
					if existing, ok := candidateMap[reviewer]; ok {
						slog.Info("Boosting candidate from line overlap (reviewer)", "username", reviewer, "pr", overlap.prNumber, "boost", overlapScore)
						existing.weight += overlapScore
						existing.sourceScores["line-reviewer"] += overlapScore
					} else {
						slog.Info("Adding candidate from line overlap (reviewer)", "username", reviewer, "pr", overlap.prNumber, "score", overlapScore)
						candidateMap[reviewer] = &candidateWeight{
							username:     reviewer,
							weight:       overlapScore,
							sourceScores: map[string]int{"line-reviewer": overlapScore},
						}
					}
				}
			}

			// Add merger with smaller boost (30% of overlap, minimum 1 point if there's any overlap)
			if overlap.mergedBy != "" && overlap.mergedBy != pr.Author {
				// Use max(1, overlapScore*30%) to ensure at least 1 point for any overlap
				mergerScore := (overlapScore * 3) / 10
				if mergerScore == 0 && overlapScore > 0 {
					mergerScore = 1 // Minimum 1 point for merging an overlapping PR
				}
				if existing, ok := candidateMap[overlap.mergedBy]; ok {
					slog.Info("Boosting candidate from line overlap (merger)", "username", overlap.mergedBy, "pr", overlap.prNumber, "boost", mergerScore)
					existing.weight += mergerScore
					existing.sourceScores["line-merger"] += mergerScore
				} else {
					slog.Info("Adding candidate from line overlap (merger)", "username", overlap.mergedBy, "pr", overlap.prNumber, "score", mergerScore)
					candidateMap[overlap.mergedBy] = &candidateWeight{
						username:     overlap.mergedBy,
						weight:       mergerScore,
						sourceScores: map[string]int{"line-merger": mergerScore},
					}
				}
			}
		}

		// Rebuild candidates from map
		candidates = make([]candidateWeight, 0, len(candidateMap))
		for _, c := range candidateMap {
			candidates = append(candidates, *c)
		}
		slog.Info("Candidates after line overlap analysis", "count", len(candidates))
	}

	// Add candidates from recent merged PRs (last 3 merged PRs in the repo)
	recentPRs, err := f.recentPRsInProject(ctx, pr.Owner, pr.Repository)
	if err != nil {
		slog.Warn("Failed to fetch recent merged PRs", "error", err)
	} else if len(recentPRs) == 0 {
		slog.Info("No recent merged PRs found")
	} else {
		slog.Info("Found recent merged PRs", "count", len(recentPRs))
		candidateMap := make(map[string]*candidateWeight)
		// Build map from existing candidates
		for i := range candidates {
			candidateMap[candidates[i].username] = &candidates[i]
		}

		// Add weight from recent merged PRs
		for _, recentPR := range recentPRs {
			// Add merger with high weight (strong signal)
			if recentPR.MergedBy != "" && recentPR.MergedBy != pr.Author {
				if existing, ok := candidateMap[recentPR.MergedBy]; ok {
					slog.Info("Boosting existing candidate from merger", "username", recentPR.MergedBy, "pr", recentPR.Number, "boost", 50)
					existing.weight += 50 // Boost existing candidates
					existing.sourceScores["recent-merger"] += 50
				} else {
					slog.Info("Adding new candidate from merger", "username", recentPR.MergedBy, "pr", recentPR.Number, "weight", 50)
					candidateMap[recentPR.MergedBy] = &candidateWeight{
						username:     recentPR.MergedBy,
						weight:       50,
						sourceScores: map[string]int{"recent-merger": 50},
					}
				}
			}

			// Add reviewers with moderate weight
			for _, reviewer := range recentPR.Reviewers {
				if reviewer != "" && reviewer != pr.Author {
					if existing, ok := candidateMap[reviewer]; ok {
						slog.Info("Boosting existing candidate from reviewer", "username", reviewer, "pr", recentPR.Number, "boost", 25)
						existing.weight += 25
						existing.sourceScores["recent-reviewer"] += 25
					} else {
						slog.Info("Adding new candidate from reviewer", "username", reviewer, "pr", recentPR.Number, "weight", 25)
						candidateMap[reviewer] = &candidateWeight{
							username:     reviewer,
							weight:       25,
							sourceScores: map[string]int{"recent-reviewer": 25},
						}
					}
				}
			}
		}

		// Add candidates from directory activity (recent commits to directories)
		// Deduplicate directories to avoid redundant queries
		seenDirs := make(map[string]bool)
		for _, file := range topFiles {
			dir := filepath.Dir(file)
			if seenDirs[dir] {
				continue // Skip if we've already processed this directory
			}
			seenDirs[dir] = true

			// Get recent PRs in this directory
			dirPRs, err := f.recentPRsInDirectory(ctx, pr.Owner, pr.Repository, dir)
			if err == nil && len(dirPRs) > 0 {
				slog.Info("Found directory activity", "directory", dir, "pr_count", len(dirPRs))
				for _, dirPR := range dirPRs {
					// Authors of recent directory changes get weight
					if dirPR.Author != "" && dirPR.Author != pr.Author {
						if existing, ok := candidateMap[dirPR.Author]; ok {
							slog.Info("Boosting candidate from directory author", "username", dirPR.Author, "directory", dir, "boost", 30)
							existing.weight += 30
							existing.sourceScores["dir-author"] += 30
						} else {
							slog.Info("Adding candidate from directory author", "username", dirPR.Author, "directory", dir, "weight", 30)
							candidateMap[dirPR.Author] = &candidateWeight{
								username:     dirPR.Author,
								weight:       30,
								sourceScores: map[string]int{"dir-author": 30},
							}
						}
					}
					// Reviewers of recent directory changes
					for _, reviewer := range dirPR.Reviewers {
						if reviewer != "" && reviewer != pr.Author {
							if existing, ok := candidateMap[reviewer]; ok {
								slog.Info("Boosting candidate from directory reviewer", "username", reviewer, "directory", dir, "boost", 15)
								existing.weight += 15
								existing.sourceScores["dir-reviewer"] += 15
							} else {
								slog.Info("Adding candidate from directory reviewer", "username", reviewer, "directory", dir, "weight", 15)
								candidateMap[reviewer] = &candidateWeight{
									username:     reviewer,
									weight:       15,
									sourceScores: map[string]int{"dir-reviewer": 15},
								}
							}
						}
					}
				}
			}
		}

		// Rebuild candidates slice from map
		candidates = make([]candidateWeight, 0, len(candidateMap))
		for _, c := range candidateMap {
			candidates = append(candidates, *c)
		}
		slog.Info("Total candidates after all sources", "count", len(candidates))
	}

	// Filter and score candidates
	var validCandidates []candidateWeight
	for _, c := range candidates {
		// Apply hard filters (bots, write access)
		if !f.isValidReviewer(ctx, pr, c.username) {
			continue
		}

		// Calculate workload penalty
		penalty := f.calculateWorkloadPenalty(ctx, pr, c.username)
		c.workloadPenalty = penalty
		c.finalScore = c.weight - penalty

		validCandidates = append(validCandidates, c)
	}

	if len(validCandidates) == 0 {
		slog.Info("No valid candidates after filtering")
		return nil
	}

	// Sort by final score (descending)
	sort.Slice(validCandidates, func(i, j int) bool {
		return validCandidates[i].finalScore > validCandidates[j].finalScore
	})

	// Log top candidates with scores
	for i, c := range validCandidates {
		if i >= topCandidatesToLog {
			break
		}
		var sourceList []string
		for source, score := range c.sourceScores {
			sourceList = append(sourceList, fmt.Sprintf("%s:%d", source, score))
		}
		slog.Info("Scored candidate", "username", c.username, "weight", c.weight, "penalty", c.workloadPenalty, "final_score", c.finalScore, "sources", strings.Join(sourceList, ","))
	}

	// Convert top 5 to ReviewerCandidates
	var reviewers []types.ReviewerCandidate
	for i, c := range validCandidates {
		if i >= 5 {
			break
		}

		// Build score breakdown string with workload penalty
		var scoreBreakdown []string
		for source, score := range c.sourceScores {
			scoreBreakdown = append(scoreBreakdown, fmt.Sprintf("%s:+%d", source, score))
		}
		if c.workloadPenalty > 0 {
			scoreBreakdown = append(scoreBreakdown, fmt.Sprintf("workload:-%d", c.workloadPenalty))
		}
		sort.Strings(scoreBreakdown) // Sort for consistent display
		method := strings.Join(scoreBreakdown, ", ")

		reviewers = append(reviewers, types.ReviewerCandidate{
			Username:        c.username,
			SelectionMethod: method,
			ContextScore:    c.finalScore,
		})
	}

	return reviewers
}

// topChangedFilesFiltered returns the N files with the largest delta, excluding lock files.
func (f *Finder) topChangedFilesFiltered(pr *types.PullRequest, n int) []string {
	type fileChange struct {
		name    string
		changes int
	}

	// Files to ignore
	ignoredFiles := map[string]bool{
		"go.mod":            true,
		"go.sum":            true,
		"package-lock.json": true,
		"yarn.lock":         true,
		"Gemfile.lock":      true,
		"Cargo.lock":        true,
	}

	fileChanges := make([]fileChange, 0, len(pr.ChangedFiles))
	for _, file := range pr.ChangedFiles {
		// Skip ignored files
		if ignoredFiles[filepath.Base(file.Filename)] {
			continue
		}

		fileChanges = append(fileChanges, fileChange{
			name:    file.Filename,
			changes: file.Additions + file.Deletions,
		})
	}

	// Sort by changes (descending)
	sort.Slice(fileChanges, func(i, j int) bool {
		return fileChanges[i].changes > fileChanges[j].changes
	})

	// Extract top N filenames
	files := make([]string, 0, n)
	for i, fc := range fileChanges {
		if i >= n {
			break
		}
		files = append(files, fc.name)
		slog.Info("File with changes", "index", i+1, "filename", fc.name, "changes", fc.changes)
	}

	return files
}

// collectWeightedCandidates collects candidates with weights based on lines they contributed/approved.
func (f *Finder) collectWeightedCandidates(ctx context.Context, pr *types.PullRequest, files []string) []candidateWeight {
	candidateMap := make(map[string]*candidateWeight)

	for _, file := range files {
		// Get the most recent PRs that modified this file
		historicalPRs, err := f.historicalPRsForFile(ctx, pr.Owner, pr.Repository, file, maxHistoricalPRs)
		if err != nil {
			slog.Warn("Failed to get history for file", "file", file, "error", err)
			continue
		}

		for _, histPR := range historicalPRs {
			// Skip if it's the current PR
			if histPR.Number == pr.Number {
				continue
			}

			// Get the patch for this historical PR to count lines
			patch, err := f.client.FilePatch(ctx, pr.Owner, pr.Repository, histPR.Number, file)
			if err != nil {
				continue
			}

			lineCount := f.countLinesInPatch(patch)
			if lineCount == 0 {
				continue
			}

			// Add weight for the author
			if histPR.Author != "" && histPR.Author != pr.Author {
				if existing, ok := candidateMap[histPR.Author]; ok {
					existing.weight += lineCount
					existing.sourceScores["file-author"] += lineCount
				} else {
					candidateMap[histPR.Author] = &candidateWeight{
						username:     histPR.Author,
						weight:       lineCount,
						sourceScores: map[string]int{"file-author": lineCount},
					}
				}
			}

			// Add weight for who merged the PR (strong signal of active maintainer)
			if histPR.MergedBy != "" && histPR.MergedBy != pr.Author {
				mergeWeight := lineCount * 2 // Double weight for mergers as it's a strong activity signal
				if existing, ok := candidateMap[histPR.MergedBy]; ok {
					existing.weight += mergeWeight
					existing.sourceScores["file-merger"] += mergeWeight
				} else {
					candidateMap[histPR.MergedBy] = &candidateWeight{
						username:     histPR.MergedBy,
						weight:       mergeWeight,
						sourceScores: map[string]int{"file-merger": mergeWeight},
					}
				}
			}

			// Add weight for reviewers
			for _, reviewer := range histPR.Reviewers {
				if reviewer != "" && reviewer != pr.Author {
					if existing, ok := candidateMap[reviewer]; ok {
						existing.weight += lineCount
						existing.sourceScores["file-reviewer"] += lineCount
					} else {
						candidateMap[reviewer] = &candidateWeight{
							username:     reviewer,
							weight:       lineCount,
							sourceScores: map[string]int{"file-reviewer": lineCount},
						}
					}
				}
			}
		}
	}

	// Convert map to slice
	var candidates []candidateWeight
	for _, c := range candidateMap {
		candidates = append(candidates, *c)
	}

	// Sort by weight for logging
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].weight > candidates[j].weight
	})

	// Log top candidates
	for i, c := range candidates {
		if i >= topCandidatesToLog {
			break
		}
		var sourceList []string
		for source, score := range c.sourceScores {
			sourceList = append(sourceList, fmt.Sprintf("%s:%d", source, score))
		}
		slog.Info("Candidate", "username", c.username, "weight", c.weight, "sources", strings.Join(sourceList, ","))
	}

	return candidates
}

// countLinesInPatch counts the number of added/modified lines in a patch.
func (f *Finder) countLinesInPatch(patch string) int {
	lineCount := 0
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			lineCount++
		}
	}
	return lineCount
}

// recentContributors gets contributors who have been active in the last N days.
func (f *Finder) recentContributors(ctx context.Context, owner, repo string, days int) []recentContributor {
	cacheKey := makeCacheKey("recent-contributors", owner, repo, days)

	// Check cache
	if cached, ok := f.cache.Get(cacheKey); ok {
		if contributors, ok := cached.([]recentContributor); ok {
			return contributors
		}
	}

	cutoff := time.Now().AddDate(0, 0, -days)
	slog.Info("Fetching contributors active since cutoff", "cutoff", cutoff.Format("2006-01-02"), "days_ago", days)

	// TODO: Use GraphQL to get recent PRs efficiently
	// For now, return empty to trigger fallback
	return nil
}

// performWeightedSelection performs weighted random selection of reviewers.
func (f *Finder) performWeightedSelection(
	ctx context.Context, pr *types.PullRequest, candidates []candidateWeight, recentContributors map[string]bool,
) []types.ReviewerCandidate {
	// Calculate total weight
	totalWeight := 0
	for _, c := range candidates {
		totalWeight += c.weight
	}

	if totalWeight == 0 {
		return nil
	}

	selectedMap := make(map[string]types.ReviewerCandidate)

	// Perform weighted random selection
	for i := 0; i < selectionRolls && len(selectedMap) < 2; i++ {
		// Random number between 0 and totalWeight
		bigWeight := big.NewInt(int64(totalWeight))
		bigRoll, err := rand.Int(rand.Reader, bigWeight)
		if err != nil {
			slog.Warn("Failed to generate random number", "error", err)
			continue
		}
		roll := int(bigRoll.Int64())

		// Find the selected candidate
		cumulative := 0
		for _, c := range candidates {
			cumulative += c.weight
			if roll < cumulative {
				// Check if this candidate is a recent contributor
				if !recentContributors[c.username] {
					slog.Info("Roll - not recent, skipping", "roll", i+1, "username", c.username, "weight", c.weight)
					break
				}

				// Check if already selected
				if _, exists := selectedMap[c.username]; exists {
					slog.Info("Roll - already selected", "roll", i+1, "username", c.username, "weight", c.weight)
					break
				}

				// Validate the reviewer
				if !f.isValidReviewer(ctx, pr, c.username) {
					slog.Info("Roll - invalid reviewer", "roll", i+1, "username", c.username, "weight", c.weight)
					break
				}

				var sourceList []string
				for source, score := range c.sourceScores {
					sourceList = append(sourceList, fmt.Sprintf("%s:%d", source, score))
				}
				slog.Info("Roll - selected", "roll", i+1, "username", c.username, "weight", c.weight, "sources", strings.Join(sourceList, ","))

				// Add to selected - build method from source breakdown
				var scoreBreakdown []string
				for source, score := range c.sourceScores {
					scoreBreakdown = append(scoreBreakdown, fmt.Sprintf("%s:+%d", source, score))
				}
				sort.Strings(scoreBreakdown)
				method := strings.Join(scoreBreakdown, ", ")

				selectedMap[c.username] = types.ReviewerCandidate{
					Username:        c.username,
					SelectionMethod: method,
					ContextScore:    maxContextScore,
				}
				break
			}
		}
	}

	// Convert map to slice
	var selected []types.ReviewerCandidate
	for _, s := range selectedMap {
		selected = append(selected, s)
	}

	return selected
}
