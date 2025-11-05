package reviewer

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// candidateWeight represents a reviewer candidate with their weight.
type candidateWeight struct {
	sourceScores    map[string]int // Score breakdown by source: "file-author" -> 10, "recent-merger" -> 50, etc.
	username        string
	weight          int
	workloadPenalty int
	finalScore      int
}

// findReviewersOptimized finds reviewers using scoring with workload penalties.
//
//nolint:gocognit,revive,maintidx // High complexity and length inherent to multi-source reviewer scoring algorithm
func (f *Finder) findReviewersOptimized(ctx context.Context, pr *types.PullRequest) []types.ReviewerCandidate {
	// Get the 3 files with the largest delta, excluding lock files
	// Build candidate map to accumulate scores from all sources
	candidateMap := make(map[string]*candidateWeight)

	// Source 1: Assignees (highest priority - explicit assignment)
	for _, assignee := range pr.Assignees {
		if assignee == pr.Author {
			slog.Info("Skipping assignee (is PR author)", "assignee", assignee)
			continue
		}
		slog.Info("Adding candidate from PR assignee", "username", assignee, "weight", 200)
		candidateMap[assignee] = &candidateWeight{
			username:     assignee,
			weight:       200,
			sourceScores: map[string]int{"assignee": 200},
		}
	}

	// Source 2: File history via blame (if we have changed files)
	topFiles := f.topChangedFilesFiltered(pr, 3)
	if len(topFiles) > 0 {
		slog.Info("Analyzing top files with largest changes", "count", len(topFiles))
		fileCandidates := f.collectWeightedCandidates(ctx, pr, topFiles)
		slog.Info("Found weighted candidates from file history", "count", len(fileCandidates))

		// Merge file candidates into map
		for _, fc := range fileCandidates {
			if existing, exists := candidateMap[fc.username]; exists {
				// Merge scores
				existing.weight += fc.weight
				for source, score := range fc.sourceScores {
					existing.sourceScores[source] = score
				}
			} else {
				candidateMap[fc.username] = &fc
			}
		}
	} else {
		slog.Info("No changed files to analyze, relying on other signals")
	}

	// Source 3: Directory-level contributions (last 10 commits to each directory)
	//nolint:nestif // Nested logic required for multi-directory contributor analysis
	if len(topFiles) > 0 {
		// Get unique directories from top changed files
		seenDirs := make(map[string]bool)
		var dirs []string
		for _, file := range topFiles {
			dir := filepath.Dir(file)
			if !seenDirs[dir] {
				seenDirs[dir] = true
				dirs = append(dirs, dir)
			}
		}

		// Query each directory for recent commits
		for _, dir := range dirs {
			dirPRs, err := f.recentCommitsInDirectory(ctx, pr.Owner, pr.Repository, dir)
			if err != nil {
				slog.Warn("Failed to fetch directory commits, continuing without", "dir", dir, "error", err)
				continue
			}

			if len(dirPRs) == 0 {
				continue
			}

			slog.Info("Found recent commits/PRs in directory", "dir", dir, "count", len(dirPRs))
			// Add directory contributors with moderate weight (+3 per PR involvement)
			for _, dirPR := range dirPRs {
				dirWeight := 3

				if dirPR.Author != "" && !f.client.IsUserBot(ctx, dirPR.Author) {
					if existing, exists := candidateMap[dirPR.Author]; exists {
						existing.weight += dirWeight
						if existing.sourceScores["dir-author"] == 0 {
							existing.sourceScores["dir-author"] = dirWeight
						} else {
							existing.sourceScores["dir-author"] += dirWeight
						}
					} else {
						candidateMap[dirPR.Author] = &candidateWeight{
							username:     dirPR.Author,
							weight:       dirWeight,
							sourceScores: map[string]int{"dir-author": dirWeight},
						}
					}
				}

				if dirPR.MergedBy != "" && !f.client.IsUserBot(ctx, dirPR.MergedBy) {
					mergeWeight := dirWeight * 2
					if existing, exists := candidateMap[dirPR.MergedBy]; exists {
						existing.weight += mergeWeight
						if existing.sourceScores["dir-merger"] == 0 {
							existing.sourceScores["dir-merger"] = mergeWeight
						} else {
							existing.sourceScores["dir-merger"] += mergeWeight
						}
					} else {
						candidateMap[dirPR.MergedBy] = &candidateWeight{
							username:     dirPR.MergedBy,
							weight:       mergeWeight,
							sourceScores: map[string]int{"dir-merger": mergeWeight},
						}
					}
				}

				for _, reviewer := range dirPR.Reviewers {
					if reviewer != "" && !f.client.IsUserBot(ctx, reviewer) {
						if existing, exists := candidateMap[reviewer]; exists {
							existing.weight += dirWeight
							if existing.sourceScores["dir-reviewer"] == 0 {
								existing.sourceScores["dir-reviewer"] = dirWeight
							} else {
								existing.sourceScores["dir-reviewer"] += dirWeight
							}
						} else {
							candidateMap[reviewer] = &candidateWeight{
								username:     reviewer,
								weight:       dirWeight,
								sourceScores: map[string]int{"dir-reviewer": dirWeight},
							}
						}
					}
				}
			}
		}
	}

	// Source 4: Recent project activity (last 200 PRs)
	recentPRs, err := f.recentPRsInProject(ctx, pr.Owner, pr.Repository)
	if err != nil {
		slog.Warn("Failed to fetch recent PRs, continuing without recent activity signal", "error", err)
		recentPRs = nil
	}

	recentActivityScores := make(map[string]int)
	if len(recentPRs) > 0 {
		for _, recentPR := range recentPRs {
			if recentPR.Author != "" && !f.client.IsUserBot(ctx, recentPR.Author) {
				recentActivityScores[recentPR.Author]++ // +1 for authoring
			}
			if recentPR.MergedBy != "" && !f.client.IsUserBot(ctx, recentPR.MergedBy) {
				recentActivityScores[recentPR.MergedBy]++ // +1 for merging
			}
			for _, reviewer := range recentPR.Reviewers {
				if reviewer != "" && !f.client.IsUserBot(ctx, reviewer) {
					recentActivityScores[reviewer]++ // +1 for reviewing
				}
			}
		}
		slog.Info("Built recent activity scores", "contributors", len(recentActivityScores), "from_prs", len(recentPRs))
	}

	// Merge recent activity scores into candidate map (scaled down by 10x to avoid overwhelming other signals)
	for username, activityScore := range recentActivityScores {
		// Scale down by 10x - recent activity is a weak signal compared to file/line expertise
		scaledScore := activityScore / 10
		if scaledScore == 0 && activityScore > 0 {
			scaledScore = 1 // Ensure at least 1 point if they have any activity
		}

		if existing, exists := candidateMap[username]; exists {
			// Add to existing candidate
			existing.weight += scaledScore
			existing.sourceScores["recent-activity"] = scaledScore
			slog.Debug("Added recent activity to existing candidate", "username", username, "activity_score", scaledScore)
		} else {
			// Create new candidate from recent activity alone
			slog.Info("Adding candidate from recent activity only", "username", username, "activity_score", scaledScore)
			candidateMap[username] = &candidateWeight{
				username:     username,
				weight:       scaledScore,
				sourceScores: map[string]int{"recent-activity": scaledScore},
			}
		}
	}

	slog.Info("Total candidates after all sources", "count", len(candidateMap))

	// Filter candidates (bots, write access, recent activity, PR author)
	var validCandidates []candidateWeight
	for _, c := range candidateMap {
		// Filter out PR author
		if c.username == pr.Author {
			slog.Info("Filtered out candidate", "username", c.username, "reason", "is PR author", "weight", c.weight)
			continue
		}
		if !f.isValidReviewer(ctx, pr, c.username) {
			slog.Info("Filtered out candidate", "username", c.username, "reason", "failed validation")
			continue
		}
		// Filter by recent activity (must be in last 200 PRs)
		if len(recentActivityScores) > 0 && recentActivityScores[c.username] == 0 {
			slog.Info("Filtered out candidate", "username", c.username, "reason", "not in recent 200 PRs")
			continue
		}

		c.finalScore = c.weight // Initial score without workload penalty
		validCandidates = append(validCandidates, *c)
	}

	if len(validCandidates) == 0 {
		slog.Info("No valid candidates after filtering")
		return nil
	}

	slog.Info("Valid candidates after filtering", "count", len(validCandidates))

	// Sort by expertise score (without workload) to identify top candidates
	sort.Slice(validCandidates, func(i, j int) bool {
		return validCandidates[i].weight > validCandidates[j].weight
	})

	// Log top candidates before workload check
	for i, c := range validCandidates {
		if i >= 10 {
			break
		}
		slog.Info("Valid candidate before workload", "rank", i+1, "username", c.username, "weight", c.weight)
	}

	// Only check workload for top 5 candidates (optimization to reduce API calls)
	workloadCheckLimit := 5
	if len(validCandidates) < workloadCheckLimit {
		workloadCheckLimit = len(validCandidates)
	}

	// Batch fetch workload for top candidates
	topUsernames := make([]string, workloadCheckLimit)
	for i := range workloadCheckLimit {
		topUsernames[i] = validCandidates[i].username
	}

	workloadCounts, err := f.client.BatchOpenPRCount(ctx, pr.Owner, topUsernames, f.prCountCache)
	if err != nil {
		slog.Warn("Failed to batch fetch workload, continuing without penalties", "error", err)
		workloadCounts = make(map[string]int)
	}

	// Apply workload penalties to top candidates (10 points per PR, capped at 50% of score)
	for i := range workloadCheckLimit {
		username := validCandidates[i].username
		prCount := workloadCounts[username]
		rawPenalty := prCount * 10

		// Cap penalty at 50% of expertise score to avoid driving highly contexted people negative
		maxPenalty := validCandidates[i].weight / 2
		penalty := rawPenalty
		if penalty > maxPenalty {
			penalty = maxPenalty
			slog.Info("Capped workload penalty",
				"username", username, "pr_count", prCount,
				"raw_penalty", rawPenalty, "capped_penalty", penalty,
				"weight", validCandidates[i].weight)
		}

		validCandidates[i].workloadPenalty = penalty
		validCandidates[i].finalScore = validCandidates[i].weight - penalty
		slog.Info("Applied workload penalty",
			"username", username, "pr_count", prCount, "penalty", penalty,
			"weight", validCandidates[i].weight, "final_score", validCandidates[i].finalScore)
	}

	// Re-sort by final score (with workload penalties applied to top 10)
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
		slog.Info("Scored candidate",
			"username", c.username, "weight", c.weight,
			"penalty", c.workloadPenalty, "final_score", c.finalScore,
			"sources", strings.Join(sourceList, ","))
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
func (*Finder) topChangedFilesFiltered(pr *types.PullRequest, n int) []string {
	type fileChange struct {
		name    string
		changes int
	}

	// Files to ignore (lock/generated files)
	ignoredFiles := map[string]bool{
		"go.sum":            true,
		"package-lock.json": true,
		"yarn.lock":         true,
		"Gemfile.lock":      true,
		"Cargo.lock":        true,
		"poetry.lock":       true,
	}

	// First pass: collect all non-ignored files
	var nonIgnoredFiles []fileChange
	var ignoredFilesList []fileChange
	for _, file := range pr.ChangedFiles {
		fc := fileChange{
			name:    file.Filename,
			changes: file.Additions + file.Deletions,
		}
		if ignoredFiles[filepath.Base(file.Filename)] {
			ignoredFilesList = append(ignoredFilesList, fc)
		} else {
			nonIgnoredFiles = append(nonIgnoredFiles, fc)
		}
	}

	// If we have non-ignored files, use only those. Otherwise use all files (including ignored ones).
	var fileChanges []fileChange
	if len(nonIgnoredFiles) > 0 {
		fileChanges = nonIgnoredFiles
	} else {
		slog.Info("Only lock/generated files changed, analyzing them", "count", len(ignoredFilesList))
		fileChanges = ignoredFilesList
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

// collectWeightedCandidates collects candidates using GitHub blame API to find line-level experts.
//
//nolint:gocognit // High complexity required for line-level blame analysis and scoring
func (f *Finder) collectWeightedCandidates(ctx context.Context, pr *types.PullRequest, files []string) []candidateWeight {
	candidateMap := make(map[string]*candidateWeight)

	// Use blame API to find who last touched the lines being modified
	for _, file := range files {
		// Get the changed lines for this file in the current PR
		changedLines, err := f.getChangedLines(pr, file)
		if err != nil {
			slog.Warn("Failed to get changed lines", "file", file, "error", err)
			continue
		}

		if len(changedLines) == 0 {
			slog.Debug("No changed lines found", "file", file)
			continue
		}

		slog.Info("Analyzing changed lines with blame", "file", file, "line_ranges", len(changedLines))

		// Use blame to find PRs that touched these lines (overlapping) and file (non-overlapping)
		overlappingPRs, filePRs, err := f.blameForLines(ctx, pr.Owner, pr.Repository, file, changedLines)
		if err != nil {
			slog.Warn("Failed to get blame", "file", file, "error", err)
			continue
		}

		// Score candidates from overlapping blame results (full weight)
		for _, blamePR := range overlappingPRs {
			lineCount := blamePR.LineCount
			if lineCount == 0 {
				lineCount = 1
			}

			// Full weight for overlapping lines
			if blamePR.Author != "" && !f.client.IsUserBot(ctx, blamePR.Author) {
				if existing, ok := candidateMap[blamePR.Author]; ok {
					existing.weight += lineCount
					existing.sourceScores["blame-author"] += lineCount
				} else {
					candidateMap[blamePR.Author] = &candidateWeight{
						username:     blamePR.Author,
						weight:       lineCount,
						sourceScores: map[string]int{"blame-author": lineCount},
					}
				}
			}

			if blamePR.MergedBy != "" && !f.client.IsUserBot(ctx, blamePR.MergedBy) {
				mergeWeight := lineCount * 2
				if existing, ok := candidateMap[blamePR.MergedBy]; ok {
					existing.weight += mergeWeight
					existing.sourceScores["blame-merger"] += mergeWeight
				} else {
					candidateMap[blamePR.MergedBy] = &candidateWeight{
						username:     blamePR.MergedBy,
						weight:       mergeWeight,
						sourceScores: map[string]int{"blame-merger": mergeWeight},
					}
				}
			}

			for _, reviewer := range blamePR.Reviewers {
				if reviewer != "" && !f.client.IsUserBot(ctx, reviewer) {
					if existing, ok := candidateMap[reviewer]; ok {
						existing.weight += lineCount
						existing.sourceScores["blame-reviewer"] += lineCount
					} else {
						candidateMap[reviewer] = &candidateWeight{
							username:     reviewer,
							weight:       lineCount,
							sourceScores: map[string]int{"blame-reviewer": lineCount},
						}
					}
				}
			}
		}

		// Score candidates from file-level contributions (lower weight - recent file editors)
		for _, filePR := range filePRs {
			// Give +5 points for recently touching the file (even if not exact lines)
			fileWeight := 5

			if filePR.Author != "" && !f.client.IsUserBot(ctx, filePR.Author) {
				if existing, ok := candidateMap[filePR.Author]; ok {
					existing.weight += fileWeight
					existing.sourceScores["file-author"] += fileWeight
				} else {
					candidateMap[filePR.Author] = &candidateWeight{
						username:     filePR.Author,
						weight:       fileWeight,
						sourceScores: map[string]int{"file-author": fileWeight},
					}
				}
			}

			if filePR.MergedBy != "" && !f.client.IsUserBot(ctx, filePR.MergedBy) {
				mergeWeight := fileWeight * 2
				if existing, ok := candidateMap[filePR.MergedBy]; ok {
					existing.weight += mergeWeight
					existing.sourceScores["file-merger"] += mergeWeight
				} else {
					candidateMap[filePR.MergedBy] = &candidateWeight{
						username:     filePR.MergedBy,
						weight:       mergeWeight,
						sourceScores: map[string]int{"file-merger": mergeWeight},
					}
				}
			}

			for _, reviewer := range filePR.Reviewers {
				if reviewer != "" && !f.client.IsUserBot(ctx, reviewer) {
					if existing, ok := candidateMap[reviewer]; ok {
						existing.weight += fileWeight
						existing.sourceScores["file-reviewer"] += fileWeight
					} else {
						candidateMap[reviewer] = &candidateWeight{
							username:     reviewer,
							weight:       fileWeight,
							sourceScores: map[string]int{"file-reviewer": fileWeight},
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

// getChangedLines extracts the line ranges that were modified in a file for this PR.
// Returns array of [startLine, endLine] pairs.
//
//nolint:unparam // Error return kept for interface consistency and future extensibility
func (*Finder) getChangedLines(pr *types.PullRequest, filename string) ([][2]int, error) {
	// Find the file in the PR's changed files
	var targetFile *types.ChangedFile
	for i := range pr.ChangedFiles {
		if pr.ChangedFiles[i].Filename == filename {
			targetFile = &pr.ChangedFiles[i]
			break
		}
	}

	if targetFile == nil || targetFile.Patch == "" {
		return nil, nil
	}

	// Parse the patch to extract changed line numbers
	var lineRanges [][2]int
	lines := strings.Split(targetFile.Patch, "\n")

	for _, line := range lines {
		// Look for hunk headers like @@ -10,5 +10,7 @@
		if strings.HasPrefix(line, "@@") {
			// Extract the new file line range (second set of numbers)
			parts := strings.Split(line, " ")
			for i, part := range parts {
				if !strings.HasPrefix(part, "+") || i == 0 {
					continue
				}
				// Format is +start,count or just +start
				numPart := strings.TrimPrefix(part, "+")

				var start, count int
				if strings.Contains(numPart, ",") {
					if _, err := fmt.Sscanf(numPart, "%d,%d", &start, &count); err != nil {
						continue
					}
				} else {
					if _, err := fmt.Sscanf(numPart, "%d", &start); err != nil {
						continue
					}
					count = 1
				}

				if start > 0 {
					end := start + count - 1
					if end < start {
						end = start
					}
					lineRanges = append(lineRanges, [2]int{start, end})
				}
				break
			}
		}
	}

	return lineRanges, nil
}
