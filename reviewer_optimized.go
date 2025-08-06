package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
func (rf *ReviewerFinder) findReviewersOptimized(ctx context.Context, pr *PullRequest) []ReviewerCandidate {
	// Get the 3 files with the largest delta
	topFiles := rf.topChangedFiles(pr, 3)
	if len(topFiles) == 0 {
		log.Print("  ⚠️  No changed files to analyze")
		return nil // Return nil to trigger fallback
	}

	log.Printf("  📊 Analyzing top %d files with largest changes", len(topFiles))

	// Collect candidates with weights from recent PRs that modified these files
	candidates := rf.collectWeightedCandidates(ctx, pr, topFiles)
	if len(candidates) == 0 {
		log.Print("  ⚠️  No candidates found from file history")
		return nil // Return nil to trigger fallback
	}

	log.Printf("  👥 Found %d weighted candidates from file history", len(candidates))

	// Get recent contributors for the repository with progressive time windows
	windows := []int{recencyWindow1, recencyWindow2, recencyWindow3, recencyWindow4}
	var selectedReviewers []ReviewerCandidate

	for _, days := range windows {
		contributors := rf.recentContributors(ctx, pr.Owner, pr.Repository, days)
		if len(contributors) == 0 {
			log.Printf("  ⏱️  No contributors found in %d day window, expanding...", days)
			continue
		}

		log.Printf("  ⏱️  Found %d contributors in %d day window", len(contributors), days)

		// Create a map for quick lookup
		contributorMap := make(map[string]bool)
		for _, c := range contributors {
			contributorMap[c.username] = true
		}

		// Perform weighted random selection
		selected := rf.performWeightedSelection(ctx, pr, candidates, contributorMap)
		if len(selected) > 0 {
			selectedReviewers = selected
			break
		}

		log.Printf("  🎲 No successful rolls in %d day window, expanding...", days)
	}

	if len(selectedReviewers) == 0 {
		log.Print("  ⚠️  No reviewers found with recency bias, falling back to original method")
		return nil // Return nil to trigger fallback
	}

	return selectedReviewers
}

// topChangedFiles returns the N files with the largest delta (additions + deletions).
func (*ReviewerFinder) topChangedFiles(pr *PullRequest, n int) []string {
	type fileChange struct {
		name    string
		changes int
	}

	fileChanges := make([]fileChange, 0, len(pr.ChangedFiles))
	for _, f := range pr.ChangedFiles {
		fileChanges = append(fileChanges, fileChange{
			name:    f.Filename,
			changes: f.Additions + f.Deletions,
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
		log.Printf("    File %d: %s (%d changes)", i+1, fc.name, fc.changes)
	}

	return files
}

// topChangedFilesFiltered returns the N files with the largest delta, excluding go.mod/go.sum.
func (*ReviewerFinder) topChangedFilesFiltered(pr *PullRequest, n int) []string {
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
	for _, f := range pr.ChangedFiles {
		// Skip ignored files
		if ignoredFiles[filepath.Base(f.Filename)] {
			continue
		}

		fileChanges = append(fileChanges, fileChange{
			name:    f.Filename,
			changes: f.Additions + f.Deletions,
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
		log.Printf("    File %d: %s (%d changes)", i+1, fc.name, fc.changes)
	}

	return files
}

// collectWeightedCandidates collects candidates with weights based on lines they contributed/approved.
func (rf *ReviewerFinder) collectWeightedCandidates(ctx context.Context, pr *PullRequest, files []string) []candidateWeight {
	candidateMap := make(map[string]*candidateWeight)

	for _, file := range files {
		// Get the most recent PRs that modified this file
		historicalPRs, err := rf.historicalPRsForFile(ctx, pr.Owner, pr.Repository, file, maxHistoricalPRs)
		if err != nil {
			log.Printf("    ⚠️  Failed to get history for %s: %v", file, err)
			continue
		}

		for _, histPR := range historicalPRs {
			// Skip if it's the current PR
			if histPR.Number == pr.Number {
				continue
			}

			// Get the patch for this historical PR to count lines
			patch, err := rf.client.filePatch(ctx, pr.Owner, pr.Repository, histPR.Number, file)
			if err != nil {
				continue
			}

			lineCount := rf.countLinesInPatch(patch)
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
		log.Printf("    Candidate: %s (weight: %d lines, source: %s)", c.username, c.weight, c.source)
	}

	return candidates
}

// countLinesInPatch counts the number of added/modified lines in a patch.
func (*ReviewerFinder) countLinesInPatch(patch string) int {
	lineCount := 0
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			lineCount++
		}
	}
	return lineCount
}

// recentContributors gets contributors who have been active in the last N days.
func (rf *ReviewerFinder) recentContributors(ctx context.Context, owner, repo string, days int) []recentContributor {
	cacheKey := makeCacheKey("recent-contributors", owner, repo, days)

	// Check cache
	if cached, ok := rf.client.cache.value(cacheKey); ok {
		if contributors, ok := cached.([]recentContributor); ok {
			return contributors
		}
	}

	cutoff := time.Now().AddDate(0, 0, -days)
	log.Printf("  🔍 Fetching contributors active since %s (%d days ago)", cutoff.Format("2006-01-02"), days)

	// Use GraphQL to get recent PRs efficiently
	prs, err := rf.recentPRsSince(ctx, owner, repo, cutoff)
	if err != nil {
		log.Printf("  ⚠️  Failed to fetch recent PRs: %v", err)
		return nil
	}

	contributorMap := make(map[string]*recentContributor)

	for _, pr := range prs {
		// Track PR author
		if pr.Author != "" {
			if c, ok := contributorMap[pr.Author]; ok {
				c.prCount++
				if pr.MergedAt.After(c.lastActivity) {
					c.lastActivity = pr.MergedAt
				}
			} else {
				contributorMap[pr.Author] = &recentContributor{
					username:     pr.Author,
					lastActivity: pr.MergedAt,
					prCount:      1,
				}
			}
		}

		// Track reviewers
		for _, reviewer := range pr.Reviewers {
			if reviewer != "" {
				if c, ok := contributorMap[reviewer]; ok {
					c.prCount++
					if pr.MergedAt.After(c.lastActivity) {
						c.lastActivity = pr.MergedAt
					}
				} else {
					contributorMap[reviewer] = &recentContributor{
						username:     reviewer,
						lastActivity: pr.MergedAt,
						prCount:      1,
					}
				}
			}
		}
	}

	// Convert to slice
	var contributors []recentContributor
	for _, c := range contributorMap {
		contributors = append(contributors, *c)
	}

	// Cache the result
	rf.client.cache.set(cacheKey, contributors)

	return contributors
}

// performWeightedSelection performs weighted random selection of reviewers.
func (rf *ReviewerFinder) performWeightedSelection(
	ctx context.Context, pr *PullRequest, candidates []candidateWeight, recentContributors map[string]bool,
) []ReviewerCandidate {
	// Calculate total weight
	totalWeight := 0
	for _, c := range candidates {
		totalWeight += c.weight
	}

	if totalWeight == 0 {
		return nil
	}

	selectedMap := make(map[string]ReviewerCandidate)

	// Perform weighted random selection
	for i := 0; i < selectionRolls && len(selectedMap) < 2; i++ {
		// Random number between 0 and totalWeight
		bigWeight := big.NewInt(int64(totalWeight))
		bigRoll, err := rand.Int(rand.Reader, bigWeight)
		if err != nil {
			log.Printf("    ⚠️  Failed to generate random number: %v", err)
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
					log.Printf("    🎲 Roll %d: %s (weight: %d) - not recent, skipping", i+1, c.username, c.weight)
					break
				}

				// Check if already selected
				if _, exists := selectedMap[c.username]; exists {
					log.Printf("    🎲 Roll %d: %s (weight: %d) - already selected", i+1, c.username, c.weight)
					break
				}

				// Validate the reviewer
				if !rf.isValidReviewer(ctx, pr, c.username) {
					log.Printf("    🎲 Roll %d: %s (weight: %d) - invalid reviewer", i+1, c.username, c.weight)
					break
				}

				log.Printf("    ✅ Roll %d: Selected %s (weight: %d, source: %s)", i+1, c.username, c.weight, c.source)

				// Add to selected
				method := selectionAuthorOverlap
				if c.source == "reviewer" {
					method = selectionReviewerOverlap
				}

				selectedMap[c.username] = ReviewerCandidate{
					Username:        c.username,
					SelectionMethod: method,
					ContextScore:    maxContextScore,
				}
				break
			}
		}
	}

	// Convert map to slice
	var selected []ReviewerCandidate
	for _, s := range selectedMap {
		selected = append(selected, s)
	}

	return selected
}

// recentPRsSince fetches PRs merged since the given date.
func (rf *ReviewerFinder) recentPRsSince(ctx context.Context, owner, repo string, since time.Time) ([]PRInfo, error) {
	query := rf.buildRecentPRsQuery()
	variables := map[string]any{
		"owner": owner,
		"repo":  repo,
		"since": since.Format(time.RFC3339),
	}

	graphResult, err := rf.client.makeGraphQLRequest(ctx, query, variables)
	if err != nil {
		return nil, err
	}

	nodes, err := rf.extractPRNodes(graphResult)
	if err != nil {
		return nil, err
	}

	return rf.parsePRNodes(nodes, since)
}

// buildRecentPRsQuery builds the GraphQL query for recent PRs.
func (*ReviewerFinder) buildRecentPRsQuery() string {
	return `
	query($owner: String!, $repo: String!, $since: DateTime!) {
		repository(owner: $owner, name: $repo) {
			pullRequests(
				states: MERGED
				orderBy: {field: UPDATED_AT, direction: DESC}
				first: 100
			) {
				nodes {
					number
					author {
						login
					}
					mergedAt
					reviews(first: 10) {
						nodes {
							author {
								login
							}
						}
					}
					timelineItems(first: 100, itemTypes: [PULL_REQUEST_REVIEW]) {
						nodes {
							... on PullRequestReview {
								author {
									login
								}
								state
							}
						}
					}
				}
			}
		}
	}`
}

// extractPRNodes extracts PR nodes from GraphQL response.
func (*ReviewerFinder) extractPRNodes(graphResult map[string]any) ([]any, error) {
	data, ok := graphResult["data"].(map[string]any)
	if !ok {
		return nil, errors.New("unexpected GraphQL response format")
	}

	repository, ok := data["repository"].(map[string]any)
	if !ok {
		return nil, errors.New("repository not found in response")
	}

	pullRequests, ok := repository["pullRequests"].(map[string]any)
	if !ok {
		return nil, errors.New("pullRequests not found in response")
	}

	nodes, ok := pullRequests["nodes"].([]any)
	if !ok {
		return nil, errors.New("nodes not found in pullRequests")
	}

	return nodes, nil
}

// parsePRNodes parses PR nodes into PRInfo structures.
func (rf *ReviewerFinder) parsePRNodes(nodes []any, since time.Time) ([]PRInfo, error) {
	var prs []PRInfo

	for _, nodeAny := range nodes {
		node, ok := nodeAny.(map[string]any)
		if !ok {
			continue
		}

		pr, shouldSkip := rf.parseSinglePRNode(node, since)
		if shouldSkip {
			continue
		}

		prs = append(prs, pr)
	}

	return prs, nil
}

// parseSinglePRNode parses a single PR node.
func (rf *ReviewerFinder) parseSinglePRNode(node map[string]any, since time.Time) (PRInfo, bool) {
	mergedAt, ok := rf.extractMergedTime(node)
	if !ok || mergedAt.Before(since) {
		return PRInfo{}, true
	}

	number, ok := node["number"].(float64)
	if !ok {
		return PRInfo{}, true
	}

	pr := PRInfo{
		Number:   int(number),
		MergedAt: mergedAt,
	}

	pr.Author = rf.extractPRAuthor(node)
	pr.Reviewers = rf.collectPRReviewers(node, pr.Author)

	return pr, false
}

// extractMergedTime extracts the merged time from a PR node.
func (*ReviewerFinder) extractMergedTime(node map[string]any) (time.Time, bool) {
	mergedAtStr, ok := node["mergedAt"].(string)
	if !ok || mergedAtStr == "" {
		return time.Time{}, false
	}

	mergedAt, err := time.Parse(time.RFC3339, mergedAtStr)
	if err != nil {
		return time.Time{}, false
	}

	return mergedAt, true
}

// extractPRAuthor extracts the author from a PR node.
func (*ReviewerFinder) extractPRAuthor(node map[string]any) string {
	author, ok := node["author"].(map[string]any)
	if !ok {
		return ""
	}

	login, _ := author["login"].(string) //nolint:errcheck,revive // Intentionally ignoring type assertion check - returns empty string on failure
	return login
}

// collectPRReviewers collects all unique reviewers from a PR node.
func (rf *ReviewerFinder) collectPRReviewers(node map[string]any, prAuthor string) []string {
	reviewerMap := make(map[string]bool)

	rf.collectReviewersFromReviews(node, prAuthor, reviewerMap)
	rf.collectReviewersFromTimeline(node, prAuthor, reviewerMap)

	var reviewers []string
	for reviewer := range reviewerMap {
		reviewers = append(reviewers, reviewer)
	}

	return reviewers
}

// collectReviewersFromReviews collects reviewers from the reviews field.
func (*ReviewerFinder) collectReviewersFromReviews(node map[string]any, prAuthor string, reviewerMap map[string]bool) {
	reviews, ok := node["reviews"].(map[string]any)
	if !ok {
		return
	}

	reviewNodes, ok := reviews["nodes"].([]any)
	if !ok {
		return
	}

	for _, reviewAny := range reviewNodes {
		review, ok := reviewAny.(map[string]any)
		if !ok {
			continue
		}

		author, ok := review["author"].(map[string]any)
		if !ok {
			continue
		}

		login, ok := author["login"].(string)
		if ok && login != "" && login != prAuthor {
			reviewerMap[login] = true
		}
	}
}

// collectReviewersFromTimeline collects reviewers from timeline items.
func (*ReviewerFinder) collectReviewersFromTimeline(node map[string]any, prAuthor string, reviewerMap map[string]bool) {
	timelineItems, ok := node["timelineItems"].(map[string]any)
	if !ok {
		return
	}

	timelineNodes, ok := timelineItems["nodes"].([]any)
	if !ok {
		return
	}

	for _, itemAny := range timelineNodes {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}

		state, ok := item["state"].(string)
		if !ok || state != "APPROVED" {
			continue
		}

		author, ok := item["author"].(map[string]any)
		if !ok {
			continue
		}

		login, ok := author["login"].(string)
		if ok && login != "" && login != prAuthor {
			reviewerMap[login] = true
		}
	}
}

// makeCacheKey creates a cache key from components.
func makeCacheKey(parts ...any) string {
	strParts := make([]string, len(parts))
	for i, part := range parts {
		strParts[i] = fmt.Sprint(part)
	}
	return strings.Join(strParts, ":")
}
