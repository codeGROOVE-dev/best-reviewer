package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ReviewerFinder finds and assigns reviewers to pull requests.
type ReviewerFinder struct {
	client       *GitHubClient
	output       *outputFormatter
	minOpenTime  time.Duration
	maxOpenTime  time.Duration
	maxPRs       int
	prCountCache time.Duration
	dryRun       bool
}

// findAndAssignReviewers is the main entry point for finding and assigning reviewers.
func (rf *ReviewerFinder) findAndAssignReviewers(ctx context.Context) error {
	var prs []*PullRequest
	var err error
	var processed, assigned, skipped int

	// Get PRs based on the target flag
	switch {
	case *prURL != "":
		pr, err := rf.prFromURL(ctx, *prURL)
		if err != nil {
			return fmt.Errorf("failed to get PR: %w", err)
		}
		prs = []*PullRequest{pr}
	case *project != "":
		prs, err = rf.prsForProject(ctx, *project)
		if err != nil {
			return fmt.Errorf("failed to get PRs for project: %w", err)
		}
	case *org != "":
		// Try batch mode first for better efficiency
		prs, err = rf.prsForOrgBatched(ctx, *org)
		if err != nil {
			// Fall back to original method if batch fails
			log.Printf("[WARN] Batch mode failed, falling back to standard mode: %v", err)
			prs, err = rf.prsForOrg(ctx, *org)
			if err != nil {
				return fmt.Errorf("failed to get PRs for organization: %w", err)
			}
		}
	default:
		return errors.New("no target specified")
	}

	// Use batch processing for multiple PRs
	if len(prs) > 3 {
		log.Printf("🚀 Using batch processing for %d PRs", len(prs))
		rf.processPRsBatch(ctx, prs)
		return nil
	}

	// Process individually for small numbers of PRs
	for i, pr := range prs {
		// Show progress
		if i > 0 {
			log.Println() // Add blank line between PRs
		}

		processed++
		wasAssigned, err := rf.processPR(ctx, pr)
		switch {
		case err != nil:
			log.Printf("  ❌ Error processing PR #%d: %v", pr.Number, err)
		case wasAssigned:
			assigned++
		default:
			skipped++
		}
	}

	// Print summary
	if processed > 0 {
		log.Print(rf.output.formatSummary(processed, assigned, skipped))
	}

	return nil
}

// processPR processes a single pull request for reviewer assignment.
func (rf *ReviewerFinder) processPR(ctx context.Context, pr *PullRequest) (bool, error) {
	// Print PR header
	log.Print(rf.output.formatPRHeader(pr))

	// Skip draft PRs - they're not ready for review
	if pr.Draft {
		log.Print("  ⏭️  Skipping draft PR (not ready for review)")
		return false, nil
	}

	// Skip if PR already has reviewers (unless they're stale)
	if len(pr.Reviewers) > 0 {
		// For now, always skip if there are reviewers
		// TODO: Re-enable stale reviewer check if needed
		log.Print("  ✅ Skipping (PR already has reviewers)")
		return false, nil

		// Original stale reviewer logic (disabled for now):
		// staleReviewers, err := rf.client.staleReviewers(ctx, pr, 5*24*time.Hour)
		// if err != nil {
		// 	log.Printf("  ⚠️  Warning: could not check for stale reviewers: %v", err)
		// }
		// // If no stale reviewers, skip processing
		// if len(staleReviewers) == 0 {
		// 	log.Print("  ✅ Skipping (PR already has reviewers)")
		// 	return false, nil
		// }
		// log.Printf("  ⚠️  PR has stale reviewers (%d), proceeding to add more", len(staleReviewers))
	}

	// Check PR age constraints
	if !rf.isPRReady(pr) {
		log.Print("  ⏭️  Skipping (outside time window)")
		return false, nil
	}

	// Find reviewer candidates
	candidates := rf.findReviewers(ctx, pr)

	// Filter out existing reviewers (always do this since we only get here if no reviewers or stale reviewers)
	if len(pr.Reviewers) > 0 {
		candidates = rf.filterExistingReviewers(candidates, pr.Reviewers)
	}

	// Display candidates
	log.Print(rf.output.formatCandidates(candidates, pr.Reviewers))

	if len(candidates) == 0 {
		return false, nil
	}

	// Assign reviewers
	assigned, err := rf.assignReviewers(ctx, pr, candidates)
	return assigned, err
}

// isPRReady checks if a PR is ready for reviewer assignment based on age constraints.
func (rf *ReviewerFinder) isPRReady(pr *PullRequest) bool {
	lastActivity := pr.LastCommit
	if pr.LastReview.After(lastActivity) {
		lastActivity = pr.LastReview
	}

	timeSinceActivity := time.Since(lastActivity)

	if timeSinceActivity < rf.minOpenTime {
		return false
	}

	if timeSinceActivity > rf.maxOpenTime {
		return false
	}

	return true
}

// filterExistingReviewers removes candidates who are already reviewers.
func (*ReviewerFinder) filterExistingReviewers(candidates []ReviewerCandidate, existing []string) []ReviewerCandidate {
	existingMap := make(map[string]bool)
	for _, r := range existing {
		existingMap[r] = true
	}

	var filtered []ReviewerCandidate
	for _, c := range candidates {
		if !existingMap[c.Username] {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// findReviewers finds appropriate reviewers for a PR.
func (rf *ReviewerFinder) findReviewers(ctx context.Context, pr *PullRequest) []ReviewerCandidate {
	// Try progressive loading first (most efficient)
	candidates := rf.findReviewersProgressive(ctx, pr)
	if len(candidates) > 0 {
		return candidates
	}

	// Fallback to optimized method if progressive fails
	candidates = rf.findReviewersOptimized(ctx, pr)
	if len(candidates) > 0 {
		return candidates
	}

	// Final fallback to original method
	return rf.findReviewersFallback(ctx, pr)
}

// findReviewersFallback is the original reviewer finding logic as a fallback.
func (rf *ReviewerFinder) findReviewersFallback(ctx context.Context, pr *PullRequest) []ReviewerCandidate {
	files := rf.changedFiles(pr)

	// Pre-fetch all PR file patches
	patchCache, err := rf.fetchAllPRFiles(ctx, pr.Owner, pr.Repository, pr.Number)
	if err != nil {
		log.Printf("[WARN] Failed to fetch PR file patches: %v (continuing without patches)", err)
		patchCache = make(map[string]string) // Empty cache as fallback
	}

	// Find expert author (code ownership context)
	author, authorMethod := rf.findExpertAuthor(ctx, pr, files, patchCache)

	// Find expert reviewer (review activity context)
	reviewer, reviewerMethod := rf.findExpertReviewer(ctx, pr, files, patchCache, author)

	// Build final candidate list
	var candidates []ReviewerCandidate

	if author != "" && author != pr.Author {
		candidates = append(candidates, ReviewerCandidate{
			Username:        author,
			SelectionMethod: authorMethod,
			ContextScore:    100,
		})
	}

	if reviewer != "" && reviewer != pr.Author && reviewer != author {
		candidates = append(candidates, ReviewerCandidate{
			Username:        reviewer,
			SelectionMethod: reviewerMethod,
			ContextScore:    50,
		})
	}

	return candidates
}

// fullAnalysisOptimized performs a full analysis using all available optimization techniques.
func (rf *ReviewerFinder) fullAnalysisOptimized(ctx context.Context, pr *PullRequest) []ReviewerCandidate {
	// Get top contributors for the repository
	contributors := rf.topContributors(ctx, pr.Owner, pr.Repository)
	if len(contributors) == 0 {
		// Fall back to the previous optimized method
		return rf.findReviewersOptimized(ctx, pr)
	}

	// Use the simplified scorer
	scorer := &SimplifiedScorer{rf: rf}
	scores := scorer.scoreContributors(ctx, pr, contributors)

	// Convert top scores to candidates
	var candidates []ReviewerCandidate
	for i, score := range scores {
		if i >= 2 { // Take top 2
			break
		}
		if rf.isValidReviewer(ctx, pr, score.Username) {
			candidates = append(candidates, ReviewerCandidate{
				Username:        score.Username,
				SelectionMethod: "full-analysis",
				ContextScore:    int(score.Score),
			})
		}
	}

	return candidates
}

// findExpertAuthor finds the most relevant code author for the changes.
func (rf *ReviewerFinder) findExpertAuthor(
	ctx context.Context, pr *PullRequest, files []string, patchCache map[string]string,
) (author string, method string) {
	// Check assignees first
	if assignee := rf.findAssigneeExpert(ctx, pr); assignee != "" {
		return assignee, selectionAssignee
	}

	// Check line overlap
	if author := rf.findOverlappingAuthor(ctx, pr, files, patchCache); author != "" {
		return author, selectionAuthorOverlap
	}

	// Check directory authors
	if author := rf.findDirectoryAuthor(ctx, pr, files); author != "" {
		return author, selectionAuthorDirectory
	}

	// Check project authors
	if author := rf.findProjectAuthor(ctx, pr); author != "" {
		return author, selectionAuthorProject
	}

	return "", ""
}

// findAssigneeExpert checks if any PR assignees can be expert authors.
func (rf *ReviewerFinder) findAssigneeExpert(ctx context.Context, pr *PullRequest) string {
	for _, assignee := range pr.Assignees {
		if assignee == pr.Author {
			log.Printf("    Filtered (is PR author): %s", assignee)
			continue
		}
		if rf.isValidReviewer(ctx, pr, assignee) {
			return assignee
		}
	}
	return ""
}

// isValidReviewer checks if a user is a valid reviewer.
func (rf *ReviewerFinder) isValidReviewer(ctx context.Context, pr *PullRequest, username string) bool {
	// Check GitHub API for user type (this now includes comprehensive bot detection)
	uType, err := rf.client.userType(ctx, username)
	if err == nil && (uType == userTypeOrg || uType == userTypeBot) {
		log.Printf("    Filtered (%s): %s", uType, username)
		return false
	}

	// Check write access
	hasAccess := rf.hasWriteAccess(ctx, pr.Owner, pr.Repository, username)
	if !hasAccess {
		log.Printf("    Filtered (no write access): %s", username)
		return false
	}

	// Check PR count for workload balancing across the organization
	// This is a best-effort check - if it fails, we continue with the candidate
	prCount, err := rf.client.openPRCount(ctx, pr.Owner, username, rf.prCountCache)
	if err != nil {
		log.Printf("    ⚠️  Warning: could not check PR count for %s in org %s: %v (continuing without PR count filter)", username, pr.Owner, err)
		// Continue without filtering - better to have a reviewer than none at all
	} else if prCount > rf.maxPRs {
		log.Printf("    Filtered (too many open PRs %d > %d in org %s): %s", prCount, rf.maxPRs, pr.Owner, username)
		return false
	}

	return true
}

// findExpertReviewer finds the most active reviewer for the changes.
func (rf *ReviewerFinder) findExpertReviewer(
	ctx context.Context, pr *PullRequest, files []string, patchCache map[string]string, excludeAuthor string,
) (reviewer string, method string) {
	// Check recent commenters
	if reviewer := rf.findCommenterReviewer(ctx, pr, excludeAuthor); reviewer != "" {
		return reviewer, selectionReviewerCommenter
	}

	// Check line overlap
	if reviewer := rf.findOverlappingReviewer(ctx, pr, files, patchCache, excludeAuthor); reviewer != "" {
		return reviewer, selectionReviewerOverlap
	}

	// Check directory reviewers
	if reviewer := rf.findDirectoryReviewer(ctx, pr, files, excludeAuthor); reviewer != "" {
		return reviewer, selectionReviewerDirectory
	}

	// Check project reviewers
	if reviewer := rf.findProjectReviewer(ctx, pr, excludeAuthor); reviewer != "" {
		return reviewer, selectionReviewerProject
	}

	return "", ""
}

// assignReviewers assigns the selected reviewers to a PR.
func (rf *ReviewerFinder) assignReviewers(ctx context.Context, pr *PullRequest, candidates []ReviewerCandidate) (bool, error) {
	// Skip draft or closed PRs
	if pr.Draft || pr.State == "closed" {
		return false, nil
	}

	// Extract reviewer names
	var reviewerNames []string
	for _, candidate := range candidates {
		reviewerNames = append(reviewerNames, candidate.Username)
	}

	if rf.dryRun {
		log.Print(rf.output.formatAction("dry-run", pr, reviewerNames))
		return true, nil
	}

	// Actually add reviewers
	err := rf.addReviewers(ctx, pr, reviewerNames)
	if err == nil {
		log.Print(rf.output.formatAction("assigned", pr, reviewerNames))
		return true, nil
	}
	return false, err
}

// addReviewers makes the API call to add reviewers to a PR.
func (rf *ReviewerFinder) addReviewers(ctx context.Context, pr *PullRequest, reviewers []string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/requested_reviewers",
		pr.Owner, pr.Repository, pr.Number)

	payload := map[string]any{
		"reviewers": reviewers,
	}

	resp, err := rf.client.makeRequest(ctx, "POST", url, payload)
	if err != nil {
		return fmt.Errorf("failed to add reviewers: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[WARN] Failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("failed to add reviewers (status %d)", resp.StatusCode)
	}

	return nil
}

// changedFiles returns the list of changed files, limiting to the most changed.
func (*ReviewerFinder) changedFiles(pr *PullRequest) []string {
	// Sort by total changes (additions + deletions)
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

	// Extract filenames, limit to maxFilesToAnalyze
	files := make([]string, 0, len(fileChanges))
	for i, fc := range fileChanges {
		if i >= maxFilesToAnalyze {
			break
		}
		files = append(files, fc.name)
	}

	return files
}

// directories extracts unique directories from file paths.
func (*ReviewerFinder) directories(files []string) []string {
	dirMap := make(map[string]bool)
	for _, file := range files {
		parts := strings.Split(file, "/")
		for i := 1; i <= len(parts)-1; i++ {
			dir := strings.Join(parts[:i], "/")
			dirMap[dir] = true
		}
	}

	dirs := make([]string, 0, len(dirMap))
	for dir := range dirMap {
		dirs = append(dirs, dir)
	}

	// Sort by depth (deeper first), then alphabetically
	sort.Slice(dirs, func(i, j int) bool {
		depthI := strings.Count(dirs[i], "/")
		depthJ := strings.Count(dirs[j], "/")
		if depthI != depthJ {
			return depthI > depthJ
		}
		return dirs[i] < dirs[j]
	})

	return dirs
}

// startPolling runs the reviewer finder in polling mode.
func (rf *ReviewerFinder) startPolling(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run immediately
	if err := rf.findAndAssignReviewers(ctx); err != nil {
		log.Printf("Error in initial run: %v", err)
	}

	// Then run periodically
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := rf.findAndAssignReviewers(ctx); err != nil {
				log.Printf("Error in polling run: %v", err)
			}
		}
	}
}
