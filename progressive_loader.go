package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
)

// ProgressiveLoader implements a multi-level strategy to find reviewers with minimal API calls.
type ProgressiveLoader struct {
	client *GitHubClient
}

// findReviewersProgressive uses a progressive loading strategy to minimize API calls.
func (rf *ReviewerFinder) findReviewersProgressive(ctx context.Context, pr *PullRequest) []ReviewerCandidate {
	log.Printf("  🚀 Starting progressive reviewer search for PR #%d", pr.Number)

	// Level 0: Check if PR already has reviewers (no API call)
	if len(pr.Reviewers) > 0 {
		log.Printf("  ✅ PR already has reviewers, skipping search")
		return nil // Return nil to indicate no new reviewers needed
	}

	loader := &ProgressiveLoader{client: rf.client}

	// Collect candidates from multiple sources and score them
	var allCandidates []ReviewerCandidate

	// Level 1: Check assignees (no API call)
	if candidates := loader.checkAssignees(ctx, rf, pr); len(candidates) > 0 {
		log.Printf("  ✅ Found %d candidates from assignees (Level 1)", len(candidates))
		// Assignees get highest priority, return immediately
		return candidates
	}

	// Level 2: Check CODEOWNERS if available (potentially no API call if cached)
	if candidates := loader.checkCodeOwners(ctx, rf, pr); len(candidates) > 0 {
		log.Printf("  ✅ Found %d candidates from CODEOWNERS (Level 2)", len(candidates))
		// CODEOWNERS are authoritative, return immediately
		return candidates
	}

	// For levels 3-5, gather ALL candidates and then score them
	log.Printf("  🔍 Gathering candidates from multiple sources...")

	// Level 3: Check file-specific history and blame (most specific)
	fileHistoryCandidates := loader.checkFileHistory(ctx, rf, pr)
	if len(fileHistoryCandidates) > 0 {
		log.Printf("  📄 Found %d candidates from file history/blame", len(fileHistoryCandidates))
		allCandidates = append(allCandidates, fileHistoryCandidates...)
	}

	// Level 4: Check directory reviewers (less specific)
	dirCandidates := loader.checkDirectoryReviewers(ctx, rf, pr)
	if len(dirCandidates) > 0 {
		log.Printf("  📁 Found %d candidates from directory history", len(dirCandidates))
		allCandidates = append(allCandidates, dirCandidates...)
	}

	// Level 5: Check top contributors (least specific)
	topContributorCandidates := loader.checkTopContributors(ctx, rf, pr)
	if len(topContributorCandidates) > 0 {
		log.Printf("  👥 Found %d candidates from top contributors", len(topContributorCandidates))
		allCandidates = append(allCandidates, topContributorCandidates...)
	}

	if len(allCandidates) == 0 {
		// Level 6: Full analysis as last resort
		log.Printf("  📊 No candidates found, proceeding to full analysis")
		return rf.fullAnalysisOptimized(ctx, pr)
	}

	// Deduplicate and select best candidates
	return loader.selectBestCandidates(allCandidates)
}

// checkAssignees validates PR assignees as reviewers (no API calls).
func (pl *ProgressiveLoader) checkAssignees(ctx context.Context, rf *ReviewerFinder, pr *PullRequest) []ReviewerCandidate {
	if len(pr.Assignees) == 0 {
		return nil
	}

	log.Printf("  👀 Checking %d assignees", len(pr.Assignees))

	var candidates []ReviewerCandidate
	for _, assignee := range pr.Assignees {
		if assignee == pr.Author {
			continue
		}

		// Quick validation using cached data
		if rf.isValidReviewer(ctx, pr, assignee) {
			candidates = append(candidates, ReviewerCandidate{
				Username:        assignee,
				SelectionMethod: selectionAssignee,
				ContextScore:    maxContextScore, // Assignees have highest priority
			})
		}
	}

	return candidates
}

// checkCodeOwners checks if there's a CODEOWNERS file.
func (pl *ProgressiveLoader) checkCodeOwners(ctx context.Context, rf *ReviewerFinder, pr *PullRequest) []ReviewerCandidate {
	// Check cache first
	cacheKey := fmt.Sprintf("codeowners:%s/%s", pr.Owner, pr.Repository)

	var owners map[string][]string
	if cached, found := pl.client.cache.value(cacheKey); found {
		if codeowners, ok := cached.(map[string][]string); ok {
			owners = codeowners
		}
	} else {
		// Try to fetch CODEOWNERS file
		owners = pl.fetchCodeOwners(ctx, pr.Owner, pr.Repository)
		if owners != nil {
			pl.client.cache.setWithTTL(cacheKey, owners, directoryOwnersCacheTTL)
		}
	}

	if owners == nil {
		return nil
	}

	// Match changed files to owners
	var candidates []ReviewerCandidate
	matchedOwners := make(map[string]bool)

	for _, file := range pr.ChangedFiles {
		for pattern, users := range owners {
			if matchesPattern(file.Filename, pattern) {
				for _, user := range users {
					if user != pr.Author && !matchedOwners[user] {
						matchedOwners[user] = true
						if rf.isValidReviewer(ctx, pr, user) {
							candidates = append(candidates, ReviewerCandidate{
								Username:        user,
								SelectionMethod: "codeowner",
								ContextScore:    maxContextScore - 5, // Slightly lower than assignees
							})
						}
					}
				}
			}
		}
	}

	return candidates
}

// checkDirectoryReviewers finds recent reviewers in the same directories.
func (pl *ProgressiveLoader) checkDirectoryReviewers(ctx context.Context, rf *ReviewerFinder, pr *PullRequest) []ReviewerCandidate {
	// Get unique directories from changed files
	dirs := rf.uniqueDirectories(pr.ChangedFiles)
	if len(dirs) == 0 {
		return nil
	}

	// Take the most specific (deepest) directory
	primaryDir := dirs[0]
	log.Printf("  📁 Checking recent reviewers in directory: %s", primaryDir)

	// Use cached directory history
	cacheKey := fmt.Sprintf("dir-reviewers:%s/%s:%s", pr.Owner, pr.Repository, primaryDir)

	var reviewers []string
	if cached, found := pl.client.cache.value(cacheKey); found {
		if dirReviewers, ok := cached.([]string); ok {
			reviewers = dirReviewers
		}
	} else {
		reviewers = pl.fetchDirectoryReviewers(ctx, pr.Owner, pr.Repository, primaryDir)
		pl.client.cache.setWithTTL(cacheKey, reviewers, directoryOwnersCacheTTL)
	}

	var candidates []ReviewerCandidate
	for i, reviewer := range reviewers {
		if i >= 2 { // Limit to top 2
			break
		}
		if reviewer != pr.Author && rf.isValidReviewer(ctx, pr, reviewer) {
			candidates = append(candidates, ReviewerCandidate{
				Username:        reviewer,
				SelectionMethod: selectionReviewerDirectory,
				ContextScore:    maxContextScore - 10 - i*5, // Decay by rank
			})
		}
	}

	return candidates
}

// checkTopContributors uses repository statistics to find active contributors.
func (pl *ProgressiveLoader) checkTopContributors(ctx context.Context, rf *ReviewerFinder, pr *PullRequest) []ReviewerCandidate {
	log.Printf("  📊 Checking top repository contributors")

	contributors := rf.topContributors(ctx, pr.Owner, pr.Repository)
	if len(contributors) == 0 {
		return nil
	}

	// Score contributors based on their overlap with changed files
	scorer := &SimplifiedScorer{rf: rf}
	scored := scorer.scoreContributors(ctx, pr, contributors)

	// Return top 2 scored contributors
	var candidates []ReviewerCandidate
	for i, sc := range scored {
		if i >= 2 {
			break
		}
		if sc.Username != pr.Author && rf.isValidReviewer(ctx, pr, sc.Username) {
			candidates = append(candidates, ReviewerCandidate{
				Username:        sc.Username,
				SelectionMethod: "top-contributor",
				ContextScore:    int(sc.Score),
			})
		}
	}

	return candidates
}

// fetchCodeOwners fetches the CODEOWNERS file from the repository.
func (pl *ProgressiveLoader) fetchCodeOwners(ctx context.Context, owner, repo string) map[string][]string {
	// Try common locations
	locations := []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

	for _, loc := range locations {
		url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", owner, repo, loc)
		resp, err := pl.client.makeRequest(ctx, "GET", url, nil)
		if err != nil {
			continue
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				log.Printf("[WARN] Failed to close response body: %v", err)
			}
		}()

		if resp.StatusCode == http.StatusOK {
			// Parse CODEOWNERS file
			// This is simplified - real implementation would decode base64 content
			return parseCodeOwners(resp.Body)
		}
	}

	return nil
}

// fetchDirectoryReviewers fetches recent reviewers for a specific directory.
func (pl *ProgressiveLoader) fetchDirectoryReviewers(ctx context.Context, owner, repo, dir string) []string {
	// Use GraphQL to get recent PRs that touched this directory
	query := `
	query($owner: String!, $repo: String!) {
		repository(owner: $owner, name: $repo) {
			pullRequests(states: MERGED, first: 10, orderBy: {field: UPDATED_AT, direction: DESC}) {
				nodes {
					files(first: 50) {
						nodes { path }
					}
					reviews(first: 10, states: APPROVED) {
						nodes {
							author { login }
						}
					}
				}
			}
		}
	}`

	variables := map[string]any{
		"owner": owner,
		"repo":  repo,
	}

	result, err := pl.client.makeGraphQLRequest(ctx, query, variables)
	if err != nil {
		return nil
	}

	// Parse and count reviewers (simplified)
	reviewerCount := make(map[string]int)
	if data, ok := result["data"].(map[string]any); ok {
		if repository, ok := data["repository"].(map[string]any); ok {
			if prs, ok := repository["pullRequests"].(map[string]any); ok {
				if nodes, ok := prs["nodes"].([]any); ok {
					for _, node := range nodes {
						if pr, ok := node.(map[string]any); ok {
							// Check if PR touched the directory
							if files, ok := pr["files"].(map[string]any); ok {
								if fileNodes, ok := files["nodes"].([]any); ok {
									touchesDir := false
									for _, fileNode := range fileNodes {
										if file, ok := fileNode.(map[string]any); ok {
											if path, ok := file["path"].(string); ok {
												if strings.HasPrefix(path, dir+"/") {
													touchesDir = true
													break
												}
											}
										}
									}

									if touchesDir {
										// Count reviewers
										if reviews, ok := pr["reviews"].(map[string]any); ok {
											if reviewNodes, ok := reviews["nodes"].([]any); ok {
												for _, reviewNode := range reviewNodes {
													if review, ok := reviewNode.(map[string]any); ok {
														if author, ok := review["author"].(map[string]any); ok {
															if login, ok := author["login"].(string); ok {
																reviewerCount[login]++
															}
														}
													}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// Sort by count and return top reviewers
	return topNByCount(reviewerCount, 5)
}

// uniqueDirectories extracts unique directories from changed files.
func (rf *ReviewerFinder) uniqueDirectories(files []ChangedFile) []string {
	dirMap := make(map[string]bool)
	for _, file := range files {
		dir := filepath.Dir(file.Filename)
		if dir != "." {
			dirMap[dir] = true
		}
	}

	dirs := make([]string, 0, len(dirMap))
	for dir := range dirMap {
		dirs = append(dirs, dir)
	}

	// Sort by depth (deeper first - more specific)
	sortByDepth(dirs)

	return dirs
}

// matchesPattern checks if a file path matches a CODEOWNERS pattern.
func matchesPattern(path, pattern string) bool {
	// Simplified pattern matching
	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(path, pattern[1:])
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(path, pattern[:len(pattern)-1])
	}
	return path == pattern || strings.HasPrefix(path, pattern+"/")
}

// parseCodeOwners parses a CODEOWNERS file (simplified).
func parseCodeOwners(content io.Reader) map[string][]string {
	// This is a simplified implementation
	// Real implementation would properly parse the CODEOWNERS format
	return nil
}

// sortByDepth sorts directories by depth (deeper first).
func sortByDepth(dirs []string) {
	for i := 0; i < len(dirs); i++ {
		for j := i + 1; j < len(dirs); j++ {
			if strings.Count(dirs[i], "/") < strings.Count(dirs[j], "/") {
				dirs[i], dirs[j] = dirs[j], dirs[i]
			}
		}
	}
}

// checkFileHistory checks the history and blame for specific changed files.
func (pl *ProgressiveLoader) checkFileHistory(ctx context.Context, rf *ReviewerFinder, pr *PullRequest) []ReviewerCandidate {
	// Focus on the 3 most significant files, ignoring go.mod/go.sum
	topFiles := rf.topChangedFilesFiltered(pr, 3)
	if len(topFiles) == 0 {
		return nil
	}

	log.Printf("    Analyzing file history for top %d files", len(topFiles))

	// First, try to find candidates with line overlap (highest priority)
	var overlapCandidates []ReviewerCandidate

	// Pre-fetch all PR file patches for overlap analysis
	patchCache, err := rf.fetchAllPRFiles(ctx, pr.Owner, pr.Repository, pr.Number)
	if err == nil && len(patchCache) > 0 {
		// Check for overlapping authors
		if author := rf.findOverlappingAuthor(ctx, pr, topFiles, patchCache); author != "" {
			overlapCandidates = append(overlapCandidates, ReviewerCandidate{
				Username:        author,
				SelectionMethod: selectionAuthorOverlap,
				ContextScore:    100, // Highest score for overlap
			})
		}

		// Check for overlapping reviewers
		if reviewer := rf.findOverlappingReviewer(ctx, pr, topFiles, patchCache, ""); reviewer != "" {
			// Only add if not already in candidates
			found := false
			for _, c := range overlapCandidates {
				if c.Username == reviewer {
					found = true
					break
				}
			}
			if !found {
				overlapCandidates = append(overlapCandidates, ReviewerCandidate{
					Username:        reviewer,
					SelectionMethod: selectionReviewerOverlap,
					ContextScore:    90, // High score for reviewer overlap
				})
			}
		}
	}

	// If we found overlap candidates, return them as they are highest priority
	if len(overlapCandidates) > 0 {
		log.Printf("    ✅ Found %d candidates with line overlap", len(overlapCandidates))
		return overlapCandidates
	}

	// Otherwise, fall back to commit history analysis
	candidateScores := make(map[string]float64)
	candidateSources := make(map[string]string)

	for _, file := range topFiles {
		// Get recent commits for this file
		commits := pl.fileCommits(ctx, pr.Owner, pr.Repository, file)

		// Weight contributors by recency and frequency
		for i, commit := range commits {
			if i >= 10 { // Only look at last 10 commits
				break
			}

			recencyWeight := 1.0 / (float64(i) + 1.0) // More recent = higher weight

			if commit.Author != "" && commit.Author != pr.Author {
				candidateScores[commit.Author] += recencyWeight
				candidateSources[commit.Author] = "file-author"
			}

			// Also consider reviewers of those commits
			for _, reviewer := range commit.Reviewers {
				if reviewer != "" && reviewer != pr.Author {
					candidateScores[reviewer] += recencyWeight * 0.5 // Reviewers get half weight
					if candidateSources[reviewer] == "" {
						candidateSources[reviewer] = "file-reviewer"
					}
				}
			}
		}
	}

	// Convert to candidates
	var candidates []ReviewerCandidate
	for username, score := range candidateScores {
		if rf.isValidReviewer(ctx, pr, username) {
			candidates = append(candidates, ReviewerCandidate{
				Username:        username,
				SelectionMethod: candidateSources[username],
				ContextScore:    int(score * 20), // Scale to 0-100 range
			})
		}
	}

	// Sort by score
	sortCandidatesByScore(candidates)

	// Return top 3
	if len(candidates) > 3 {
		candidates = candidates[:3]
	}

	return candidates
}

// fileCommits gets recent commits for a specific file.
func (pl *ProgressiveLoader) fileCommits(ctx context.Context, owner, repo, file string) []FileCommit {
	// Check cache first
	cacheKey := fmt.Sprintf("file-commits:%s/%s:%s", owner, repo, file)
	if cached, found := pl.client.cache.value(cacheKey); found {
		if commits, ok := cached.([]FileCommit); ok {
			return commits
		}
	}

	// Use GitHub commits API with path filter
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits?path=%s&per_page=10", owner, repo, file)
	resp, err := pl.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		log.Printf("    ⚠️  Failed to get file commits: %v", err)
		return nil
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[WARN] Failed to close response body: %v", err)
		}
	}()

	var apiCommits []struct {
		SHA    string `json:"sha"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		Commit struct {
			Author struct {
				Date string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiCommits); err != nil {
		return nil
	}

	// Convert to our format
	var commits []FileCommit
	for _, ac := range apiCommits {
		commits = append(commits, FileCommit{
			SHA:       ac.SHA,
			Author:    ac.Author.Login,
			Reviewers: []string{}, // Would need separate API call to get reviewers
		})
	}

	// Cache the result
	pl.client.cache.setWithTTL(cacheKey, commits, fileHistoryCacheTTL)

	return commits
}

// FileCommit represents a commit to a specific file.
type FileCommit struct {
	SHA       string
	Author    string
	Reviewers []string
}

// selectBestCandidates deduplicates and selects the best candidates.
func (pl *ProgressiveLoader) selectBestCandidates(candidates []ReviewerCandidate) []ReviewerCandidate {
	// Deduplicate by taking highest score for each user
	bestByUser := make(map[string]ReviewerCandidate)

	for _, candidate := range candidates {
		// Apply source-based priority boost
		adjustedScore := candidate.ContextScore

		// Boost candidates based on specificity and relevance
		// Overlap > File > Directory
		switch candidate.SelectionMethod {
		case selectionAuthorOverlap:
			adjustedScore += 30 // Highest priority - actually touched the same lines
		case selectionReviewerOverlap:
			adjustedScore += 25 // Very high - reviewed the same lines
		case "file-author":
			adjustedScore += 15 // Good - worked on the same file
		case "file-reviewer":
			adjustedScore += 12 // Good - reviewed the same file
		case selectionAuthorDirectory:
			adjustedScore += 7 // Okay - worked in same directory
		case selectionReviewerDirectory:
			adjustedScore += 5 // Okay - reviewed in same directory
		}

		candidate.ContextScore = adjustedScore

		if existing, exists := bestByUser[candidate.Username]; exists {
			if candidate.ContextScore > existing.ContextScore {
				bestByUser[candidate.Username] = candidate
			}
		} else {
			bestByUser[candidate.Username] = candidate
		}
	}

	// Convert back to slice
	var deduplicated []ReviewerCandidate
	for _, candidate := range bestByUser {
		deduplicated = append(deduplicated, candidate)
	}

	// Sort by score
	sortCandidatesByScore(deduplicated)

	// Log the final selection with sources
	log.Printf("  🎯 Selected best candidates from %d total candidates:", len(candidates))
	for i, c := range deduplicated {
		if i >= 3 { // Show top 3 for visibility
			break
		}
		log.Printf("    %d. %s (score: %d, source: %s)", i+1, c.Username, c.ContextScore, c.SelectionMethod)
	}

	// Return top 2
	if len(deduplicated) > 2 {
		deduplicated = deduplicated[:2]
	}

	return deduplicated
}

// sortCandidatesByScore sorts candidates by score in descending order.
func sortCandidatesByScore(candidates []ReviewerCandidate) {
	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			if candidates[i].ContextScore < candidates[j].ContextScore {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}
}

// topNByCount returns the top N keys by count from a map.
func topNByCount(counts map[string]int, n int) []string {
	type kv struct {
		key   string
		count int
	}

	var sorted []kv
	for k, v := range counts {
		sorted = append(sorted, kv{k, v})
	}

	// Sort by count
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i].count < sorted[j].count {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	// Extract top N
	result := make([]string, 0, n)
	for i := 0; i < len(sorted) && i < n; i++ {
		result = append(result, sorted[i].key)
	}

	return result
}
