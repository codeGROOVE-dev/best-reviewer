package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// ReviewerFinder finds and assigns reviewers to pull requests
type ReviewerFinder struct {
	client       *GitHubClient
	dryRun       bool
	minOpenTime  time.Duration
	maxOpenTime  time.Duration
	prPatchCache map[string]map[string]string // Cache for PR file patches
}

// startPolling starts the polling loop for monitoring PRs
func (rf *ReviewerFinder) startPolling(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Polling stopped due to context cancellation")
			return
		case <-ticker.C:
			if err := rf.findAndAssignReviewers(ctx); err != nil {
				log.Printf("Error during polling iteration: %v", err)
			}
		}
	}
}

// findAndAssignReviewers is the main entry point for finding and assigning reviewers
func (rf *ReviewerFinder) findAndAssignReviewers(ctx context.Context) error {
	var prs []*PullRequest
	var err error

	if *prURL != "" {
		// Single PR mode
		owner, repo, prNumber, err := parsePRURL(*prURL)
		if err != nil {
			return fmt.Errorf("failed to parse PR URL: %w", err)
		}
		
		pr, err := rf.client.getPullRequest(ctx, owner, repo, prNumber)
		if err != nil {
			return fmt.Errorf("failed to get pull request: %w", err)
		}
		prs = []*PullRequest{pr}
	} else if *project != "" {
		// Project mode
		prs, err = rf.getPRsForProject(ctx, *project)
		if err != nil {
			return fmt.Errorf("failed to get PRs for project: %w", err)
		}
	} else if *org != "" {
		// Organization mode
		prs, err = rf.getPRsForOrg(ctx, *org)
		if err != nil {
			return fmt.Errorf("failed to get PRs for organization: %w", err)
		}
	}

	log.Printf("Found %d pull request(s) to analyze", len(prs))

	for _, pr := range prs {
		if err := rf.processPR(ctx, pr); err != nil {
			log.Printf("Error processing PR %s/%s#%d: %v", pr.Owner, pr.Repository, pr.Number, err)
		}
	}

	return nil
}

// processPR processes a single pull request for reviewer assignment
func (rf *ReviewerFinder) processPR(ctx context.Context, pr *PullRequest) error {
	draftStatus := ""
	if pr.Draft {
		draftStatus = " [DRAFT]"
	}
	log.Printf("Processing PR %s/%s#%d%s: %s", pr.Owner, pr.Repository, pr.Number, draftStatus, pr.Title)

	// Note: We process closed PRs to show recommendations, but won't assign reviewers

	// Check for stale reviewers (assigned over 5 days ago)
	needsAdditionalReviewers := false
	if len(pr.Reviewers) > 0 {
		staleReviewers, err := rf.client.getStaleReviewers(ctx, pr, 5*24*time.Hour)
		if err != nil {
			log.Printf("Warning: Failed to check stale reviewers: %v", err)
		} else if len(staleReviewers) > 0 {
			log.Printf("Found %d stale reviewers on PR %d: %v", len(staleReviewers), pr.Number, staleReviewers)
			log.Printf("Will add additional reviewers since existing ones are stale")
			needsAdditionalReviewers = true
		}
	}

	// Check PR age constraints (skip if we need additional reviewers due to staleness)
	if !needsAdditionalReviewers {
		now := time.Now()
		lastActivity := pr.LastCommit
		if pr.LastReview.After(lastActivity) {
			lastActivity = pr.LastReview
		}

		timeSinceActivity := now.Sub(lastActivity)
		if timeSinceActivity < rf.minOpenTime {
			log.Printf("Skipping PR %d: too recent (age: %v, min: %v)", pr.Number, timeSinceActivity, rf.minOpenTime)
			return nil
		}

		if timeSinceActivity > rf.maxOpenTime {
			log.Printf("Skipping PR %d: too old (age: %v, max: %v)", pr.Number, timeSinceActivity, rf.maxOpenTime)
			return nil
		}
	}

	// Find reviewer candidates using V5 logic (line overlap analysis)
	candidates, err := rf.findReviewersV5(ctx, pr)
	if err != nil {
		return fmt.Errorf("failed to find reviewer candidates: %w", err)
	}

	// If we need additional reviewers, filter out existing ones
	if needsAdditionalReviewers && len(candidates) > 0 {
		var filteredCandidates []ReviewerCandidate
		for _, candidate := range candidates {
			isExisting := false
			for _, existing := range pr.Reviewers {
				if candidate.Username == existing {
					isExisting = true
					break
				}
			}
			if !isExisting {
				filteredCandidates = append(filteredCandidates, candidate)
			}
		}
		candidates = filteredCandidates
		log.Printf("After filtering existing reviewers, found %d new candidates for PR %d", len(candidates), pr.Number)
	}

	log.Printf("Found %d reviewer candidates for PR %d", len(candidates), pr.Number)

	// Assign reviewers based on candidates
	return rf.assignReviewersV3(ctx, pr, candidates)
}

// findReviewerCandidates finds potential reviewers for a PR
func (rf *ReviewerFinder) findReviewerCandidates(ctx context.Context, pr *PullRequest) ([]ReviewerCandidate, error) {
	// Get top 3 files with most changes
	topFiles := rf.getTopChangedFiles(pr, 3)
	log.Printf("Top changed files for PR %d: %v", pr.Number, topFiles)

	// Find context-based reviewers
	contextCandidates, err := rf.findContextReviewers(ctx, pr, topFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to find context reviewers: %w", err)
	}

	// Find activity-based reviewers
	activityCandidates, err := rf.findActivityReviewers(ctx, pr, topFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to find activity reviewers: %w", err)
	}

	// Combine and deduplicate candidates
	candidateMap := make(map[string]*ReviewerCandidate)
	
	for _, candidate := range contextCandidates {
		candidateMap[candidate.Username] = &candidate
	}
	
	for _, candidate := range activityCandidates {
		if existing, exists := candidateMap[candidate.Username]; exists {
			existing.ActivityScore = candidate.ActivityScore
			if candidate.LastActivity.After(existing.LastActivity) {
				existing.LastActivity = candidate.LastActivity
			}
		} else {
			candidateMap[candidate.Username] = &candidate
		}
	}

	var candidates []ReviewerCandidate
	for _, candidate := range candidateMap {
		// Skip the PR author
		if candidate.Username == pr.Author {
			continue
		}
		candidates = append(candidates, *candidate)
	}
	
	// Use fallback mechanisms if no candidates were found
	if len(candidates) == 0 {
		log.Printf("No context or activity candidates found, using fallback mechanisms")
		fallbackCandidates, err := rf.getFallbackReviewers(ctx, pr, topFiles)
		if err != nil {
			log.Printf("Failed to get fallback reviewers: %v", err)
		} else {
			candidates = append(candidates, fallbackCandidates...)
		}
	}

	// Sort by combined score (context + activity)
	sort.Slice(candidates, func(i, j int) bool {
		scoreI := candidates[i].ContextScore + candidates[i].ActivityScore
		scoreJ := candidates[j].ContextScore + candidates[j].ActivityScore
		if scoreI == scoreJ {
			return candidates[i].LastActivity.After(candidates[j].LastActivity)
		}
		return scoreI > scoreJ
	})

	return candidates, nil
}

// getTopChangedFiles returns the top N files with the most changes
func (rf *ReviewerFinder) getTopChangedFiles(pr *PullRequest, n int) []string {
	if len(pr.ChangedFiles) <= n {
		var files []string
		for _, file := range pr.ChangedFiles {
			files = append(files, file.Filename)
		}
		return files
	}

	var files []string
	for i := 0; i < n; i++ {
		files = append(files, pr.ChangedFiles[i].Filename)
	}
	return files
}

// findContextReviewers finds reviewers based on context in changed lines
func (rf *ReviewerFinder) findContextReviewers(ctx context.Context, pr *PullRequest, files []string) ([]ReviewerCandidate, error) {
	log.Printf("Finding context reviewers for %d files", len(files))
	
	candidateScores := make(map[string]int)
	
	for _, filename := range files {
		log.Printf("Analyzing blame data for file: %s", filename)
		
		// Get blame data for the file
		blameData, err := rf.getBlameData(ctx, pr.Owner, pr.Repository, filename, pr.Number)
		if err != nil {
			log.Printf("Failed to get blame data for %s: %v", filename, err)
			continue
		}
		
		// Find PRs that modified the lines being changed
		relatedPRs, err := rf.findRelatedPRs(ctx, pr, filename, blameData)
		if err != nil {
			log.Printf("Failed to find related PRs for %s: %v", filename, err)
			continue
		}
		
		// Get approvers for those PRs and score them
		for _, relatedPR := range relatedPRs {
			approvers, err := rf.getPRApprovers(ctx, pr.Owner, pr.Repository, relatedPR.Number)
			if err != nil {
				log.Printf("Failed to get approvers for PR %d: %v", relatedPR.Number, err)
				continue
			}
			
			// Score approvers based on lines they reviewed
			for _, approver := range approvers {
				candidateScores[approver] += relatedPR.LinesReviewed
			}
		}
	}
	
	var candidates []ReviewerCandidate
	for username, score := range candidateScores {
		candidates = append(candidates, ReviewerCandidate{
			Username:     username,
			ContextScore: score,
		})
	}
	
	log.Printf("Found %d context-based candidates", len(candidates))
	return candidates, nil
}

// findActivityReviewers finds reviewers based on recent activity
func (rf *ReviewerFinder) findActivityReviewers(ctx context.Context, pr *PullRequest, files []string) ([]ReviewerCandidate, error) {
	log.Printf("Finding activity reviewers for %d files", len(files))
	
	candidateScores := make(map[string]int)
	candidateActivity := make(map[string]time.Time)
	
	for _, filename := range files {
		log.Printf("Finding recent PRs for file: %s", filename)
		
		// Find the most recent PR that modified this file
		recentPRs, err := rf.findRecentPRsForFile(ctx, pr.Owner, pr.Repository, filename)
		if err != nil {
			log.Printf("Failed to find recent PRs for %s: %v", filename, err)
			continue
		}
		
		// Get approvers for recent PRs and score them by PR size
		for _, recentPR := range recentPRs {
			approvers, err := rf.getPRApprovers(ctx, pr.Owner, pr.Repository, recentPR.Number)
			if err != nil {
				log.Printf("Failed to get approvers for PR %d: %v", recentPR.Number, err)
				continue
			}
			
			for _, approver := range approvers {
				candidateScores[approver] += recentPR.Size
				if recentPR.MergedAt.After(candidateActivity[approver]) {
					candidateActivity[approver] = recentPR.MergedAt
				}
			}
		}
	}
	
	var candidates []ReviewerCandidate
	for username, score := range candidateScores {
		candidates = append(candidates, ReviewerCandidate{
			Username:      username,
			ActivityScore: score,
			LastActivity:  candidateActivity[username],
		})
	}
	
	log.Printf("Found %d activity-based candidates", len(candidates))
	return candidates, nil
}

// assignReviewers assigns reviewers to a PR
func (rf *ReviewerFinder) assignReviewers(ctx context.Context, pr *PullRequest, candidates []ReviewerCandidate) error {
	if len(candidates) == 0 {
		log.Printf("No reviewer candidates found for PR %d", pr.Number)
		return nil
	}

	// Check existing reviewers and their assignment time
	existingReviewers := make(map[string]bool)
	for _, reviewer := range pr.Reviewers {
		existingReviewers[reviewer] = true
	}

	var reviewersToAdd []string
	twoDaysAgo := time.Now().Add(-48 * time.Hour)

	// Find best context reviewer
	for _, candidate := range candidates {
		if candidate.ContextScore > 0 {
			if existingReviewers[candidate.Username] {
				// If already assigned and more than 2 days, try next candidate
				if pr.LastReview.Before(twoDaysAgo) {
					continue
				}
			}
			reviewersToAdd = append(reviewersToAdd, candidate.Username)
			log.Printf("Selected context reviewer: %s (score: %d)", candidate.Username, candidate.ContextScore)
			break
		}
	}

	// Find best activity reviewer
	for _, candidate := range candidates {
		if candidate.ActivityScore > 0 && candidate.Username != reviewersToAdd[0] {
			if existingReviewers[candidate.Username] {
				// If already assigned and more than 2 days, try next candidate
				if pr.LastReview.Before(twoDaysAgo) {
					continue
				}
			}
			reviewersToAdd = append(reviewersToAdd, candidate.Username)
			log.Printf("Selected activity reviewer: %s (score: %d)", candidate.Username, candidate.ActivityScore)
			break
		}
	}

	if len(reviewersToAdd) == 0 {
		log.Printf("No new reviewers to add for PR %d", pr.Number)
		return nil
	}

	if rf.dryRun {
		log.Printf("DRY RUN: Would add reviewers %v to PR %d", reviewersToAdd, pr.Number)
		return nil
	}

	// Actually add reviewers
	return rf.addReviewers(ctx, pr, reviewersToAdd)
}

// addReviewers adds reviewers to a PR
func (rf *ReviewerFinder) addReviewers(ctx context.Context, pr *PullRequest, reviewers []string) error {
	log.Printf("Adding reviewers %v to PR %s/%s#%d", reviewers, pr.Owner, pr.Repository, pr.Number)
	
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/requested_reviewers", pr.Owner, pr.Repository, pr.Number)
	
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
	
	log.Printf("Successfully added reviewers %v to PR %d", reviewers, pr.Number)
	return nil
}

// getPRsForProject gets PRs for a specific project
func (rf *ReviewerFinder) getPRsForProject(ctx context.Context, project string) ([]*PullRequest, error) {
	parts := strings.Split(project, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid project format: %s (expected owner/repo)", project)
	}
	
	owner, repo := parts[0], parts[1]
	return rf.getOpenPRs(ctx, owner, repo)
}

// getPRsForOrg gets PRs for an entire organization
func (rf *ReviewerFinder) getPRsForOrg(ctx context.Context, org string) ([]*PullRequest, error) {
	// This would need to be implemented to search across all repos in an org
	// For now, return an error as this requires more complex logic
	return nil, fmt.Errorf("organization monitoring not yet implemented")
}

// getOpenPRs gets open PRs for a repository
func (rf *ReviewerFinder) getOpenPRs(ctx context.Context, owner, repo string) ([]*PullRequest, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls?state=open", owner, repo)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch open PRs: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to fetch open PRs (status %d)", resp.StatusCode)
	}
	
	var prs []struct {
		Number int `json:"number"`
	}
	
	if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
		return nil, fmt.Errorf("failed to decode PRs: %w", err)
	}
	
	var result []*PullRequest
	for _, pr := range prs {
		fullPR, err := rf.client.getPullRequest(ctx, owner, repo, pr.Number)
		if err != nil {
			log.Printf("Failed to get PR %d: %v", pr.Number, err)
			continue
		}
		result = append(result, fullPR)
	}
	
	return result, nil
}