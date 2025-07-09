package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ReviewerFinder finds and assigns reviewers to pull requests.
type ReviewerFinder struct {
	client      *GitHubClient
	dryRun      bool
	minOpenTime time.Duration
	maxOpenTime time.Duration
	output      *outputFormatter
}

// findAndAssignReviewers is the main entry point for finding and assigning reviewers.
func (rf *ReviewerFinder) findAndAssignReviewers(ctx context.Context) error {
	var prs []*PullRequest
	var err error
	var processed, assigned, skipped int

	// Get PRs based on the target flag
	switch {
	case *prURL != "":
		pr, err := rf.getPRFromURL(ctx, *prURL)
		if err != nil {
			return fmt.Errorf("failed to get PR: %w", err)
		}
		prs = []*PullRequest{pr}
	case *project != "":
		prs, err = rf.getPRsForProject(ctx, *project)
		if err != nil {
			return fmt.Errorf("failed to get PRs for project: %w", err)
		}
	case *org != "":
		prs, err = rf.getPRsForOrg(ctx, *org)
		if err != nil {
			return fmt.Errorf("failed to get PRs for organization: %w", err)
		}
	}


	for i, pr := range prs {
		// Show progress
		if i > 0 {
			fmt.Println()
		}
		
		processed++
		wasAssigned, err := rf.processPR(ctx, pr)
		if err != nil {
			fmt.Printf("  ❌ Error processing PR #%d: %v\n", pr.Number, err)
		} else if wasAssigned {
			assigned++
		} else {
			skipped++
		}
	}
	
	// Print summary
	if processed > 0 {
		fmt.Print(rf.output.formatSummary(processed, assigned, skipped))
	}

	return nil
}

// processPR processes a single pull request for reviewer assignment.
func (rf *ReviewerFinder) processPR(ctx context.Context, pr *PullRequest) (bool, error) {
	// Print PR header
	fmt.Print(rf.output.formatPRHeader(pr))

	// Check for stale reviewers (assigned over 5 days ago)
	needsAdditionalReviewers := false
	if len(pr.Reviewers) > 0 {
		staleReviewers, err := rf.client.getStaleReviewers(ctx, pr, 5*24*time.Hour)
		if err == nil && len(staleReviewers) > 0 {
			needsAdditionalReviewers = true
		}
	}

	// Check PR age constraints (skip if we need additional reviewers due to staleness)
	if !needsAdditionalReviewers && !rf.isPRReady(pr) {
		fmt.Printf("  ⏭️  Skipping (outside time window)\n")
		return false, nil
	}

	// Find reviewer candidates
	candidates, err := rf.findReviewers(ctx, pr)
	if err != nil {
		return false, fmt.Errorf("failed to find reviewer candidates: %w", err)
	}

	// If we need additional reviewers, filter out existing ones
	if needsAdditionalReviewers {
		candidates = rf.filterExistingReviewers(candidates, pr.Reviewers)
	}

	// Display candidates
	fmt.Print(rf.output.formatCandidates(candidates, pr.Reviewers))
	
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
func (rf *ReviewerFinder) filterExistingReviewers(candidates []ReviewerCandidate, existing []string) []ReviewerCandidate {
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
func (rf *ReviewerFinder) findReviewers(ctx context.Context, pr *PullRequest) ([]ReviewerCandidate, error) {
	files := rf.getChangedFiles(pr)
	
	// Pre-fetch all PR file patches
	patchCache, _ := rf.fetchAllPRFiles(ctx, pr.Owner, pr.Repository, pr.Number)

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

	return candidates, nil
}

// findExpertAuthor finds the most relevant code author for the changes.
func (rf *ReviewerFinder) findExpertAuthor(ctx context.Context, pr *PullRequest, files []string, patchCache map[string]string) (string, string) {
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
		if assignee != pr.Author && rf.isValidReviewer(ctx, pr, assignee) {
			return assignee
		}
	}
	return ""
}

// isValidReviewer checks if a user is a valid reviewer.
func (rf *ReviewerFinder) isValidReviewer(ctx context.Context, pr *PullRequest, username string) bool {
	// First check pattern-based bot detection
	if rf.isUserBot(ctx, username) {
		if rf.output != nil && rf.output.verbose {
			fmt.Printf("    Filtered (bot pattern): %s\n", username)
		}
		return false
	}
	
	// Then check GitHub API for user type
	uType, err := rf.client.getUserType(ctx, username)
	if err == nil && (uType == userTypeOrg || uType == userTypeBot) {
		if rf.output != nil && rf.output.verbose {
			fmt.Printf("    Filtered (%s): %s\n", uType, username)
		}
		return false
	}
	
	// Finally check write access
	return rf.hasWriteAccess(ctx, pr.Owner, pr.Repository, username)
}

// findExpertReviewer finds the most active reviewer for the changes.
func (rf *ReviewerFinder) findExpertReviewer(ctx context.Context, pr *PullRequest, files []string, patchCache map[string]string, excludeAuthor string) (string, string) {
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
		fmt.Print(rf.output.formatAction("dry-run", pr, reviewerNames))
		return true, nil
	}

	// Actually add reviewers
	err := rf.addReviewers(ctx, pr, reviewerNames)
	if err == nil {
		fmt.Print(rf.output.formatAction("assigned", pr, reviewerNames))
		return true, nil
	}
	return false, err
}

// addReviewers makes the API call to add reviewers to a PR.
func (rf *ReviewerFinder) addReviewers(ctx context.Context, pr *PullRequest, reviewers []string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/requested_reviewers", 
		pr.Owner, pr.Repository, pr.Number)
	
	payload := map[string]interface{}{
		"reviewers": reviewers,
	}
	
	resp, err := rf.client.makeRequest(ctx, "POST", url, payload)
	if err != nil {
		return fmt.Errorf("failed to add reviewers: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != 201 {
		return fmt.Errorf("failed to add reviewers (status %d)", resp.StatusCode)
	}
	
	return nil
}

// getChangedFiles returns the list of changed files, limiting to the most changed.
func (rf *ReviewerFinder) getChangedFiles(pr *PullRequest) []string {
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
	
	// Simple bubble sort (good enough for small lists)
	for i := 0; i < len(fileChanges); i++ {
		for j := i + 1; j < len(fileChanges); j++ {
			if fileChanges[j].changes > fileChanges[i].changes {
				fileChanges[i], fileChanges[j] = fileChanges[j], fileChanges[i]
			}
		}
	}
	
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

// getDirectories extracts unique directories from file paths.
func (rf *ReviewerFinder) getDirectories(files []string) []string {
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
	
	// Sort by depth (deeper first) - simple approach
	for i := 0; i < len(dirs); i++ {
		for j := i + 1; j < len(dirs); j++ {
			if strings.Count(dirs[j], "/") > strings.Count(dirs[i], "/") {
				dirs[i], dirs[j] = dirs[j], dirs[i]
			}
		}
	}
	
	return dirs
}

// startPolling runs the reviewer finder in polling mode.
func (rf *ReviewerFinder) startPolling(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run immediately
	if err := rf.findAndAssignReviewers(ctx); err != nil {
		fmt.Printf("Error in initial run: %v\n", err)
	}

	// Then run periodically
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := rf.findAndAssignReviewers(ctx); err != nil {
				fmt.Printf("Error in polling run: %v\n", err)
			}
		}
	}
}