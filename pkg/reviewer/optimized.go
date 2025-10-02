package reviewer

import (
	"context"
	"crypto/rand"
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
	username string
	source   string // "author" or "reviewer"
	weight   int
}

// recentContributor represents someone who has recently contributed to the repo.
type recentContributor struct {
	lastActivity time.Time
	username     string
	prCount      int
}

// findReviewersOptimized finds reviewers using the optimized algorithm with recency bias.
func (f *Finder) findReviewersOptimized(ctx context.Context, pr *types.PullRequest) []types.ReviewerCandidate {
	// Get the 3 files with the largest delta, excluding lock files
	topFiles := f.topChangedFilesFiltered(pr, 3)
	if len(topFiles) == 0 {
		slog.Info("No changed files to analyze")
		return nil // Return nil to trigger fallback
	}

	slog.Info("Analyzing top files with largest changes", "count", len(topFiles))

	// Collect candidates with weights from recent PRs that modified these files
	candidates := f.collectWeightedCandidates(ctx, pr, topFiles)
	if len(candidates) == 0 {
		slog.Info("No candidates found from file history")
		return nil // Return nil to trigger fallback
	}

	slog.Info("Found weighted candidates from file history", "count", len(candidates))

	// Get recent contributors for the repository with progressive time windows
	windows := []int{recencyWindow1, recencyWindow2, recencyWindow3, recencyWindow4}
	var selectedReviewers []types.ReviewerCandidate

	for _, days := range windows {
		contributors := f.recentContributors(ctx, pr.Owner, pr.Repository, days)
		if len(contributors) == 0 {
			slog.Info("No contributors found in day window, expanding", "days", days)
			continue
		}

		slog.Info("Found contributors in day window", "count", len(contributors), "days", days)

		// Create a map for quick lookup
		contributorMap := make(map[string]bool)
		for _, c := range contributors {
			contributorMap[c.username] = true
		}

		// Perform weighted random selection
		selected := f.performWeightedSelection(ctx, pr, candidates, contributorMap)
		if len(selected) > 0 {
			selectedReviewers = selected
			break
		}

		slog.Info("No successful rolls in day window, expanding", "days", days)
	}

	if len(selectedReviewers) == 0 {
		slog.Info("No reviewers found with recency bias, falling back")
		return nil // Return nil to trigger fallback
	}

	return selectedReviewers
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
				} else {
					candidateMap[histPR.Author] = &candidateWeight{
						username: histPR.Author,
						weight:   lineCount,
						source:   "author",
					}
				}
			}

			// Add weight for reviewers
			for _, reviewer := range histPR.Reviewers {
				if reviewer != "" && reviewer != pr.Author {
					if existing, ok := candidateMap[reviewer]; ok {
						existing.weight += lineCount
					} else {
						candidateMap[reviewer] = &candidateWeight{
							username: reviewer,
							weight:   lineCount,
							source:   "reviewer",
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
		slog.Info("Candidate", "username", c.username, "weight", c.weight, "source", c.source)
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

				slog.Info("Roll - selected", "roll", i+1, "username", c.username, "weight", c.weight, "source", c.source)

				// Add to selected
				method := SelectionAuthorOverlap
				if c.source == "reviewer" {
					method = SelectionReviewerOverlap
				}

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
