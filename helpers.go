package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// isUserBot checks if a user is a bot account or organization (both are invalid for review assignment)
func (rf *ReviewerFinder) isUserBot(ctx context.Context, username string) bool {
	// Common bot usernames to check first (faster than API call)
	botNames := []string{"dependabot", "dependabot[bot]", "renovate", "renovate[bot]", "greenkeeper[bot]", "snyk-bot", "imgbot[bot]"}
	for _, botName := range botNames {
		if username == botName {
			log.Printf("User %s is a known bot", username)
			return true
		}
	}
	
	// Check via API if user type is Bot or Organization
	url := fmt.Sprintf("https://api.github.com/users/%s", username)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		log.Printf("Failed to check user type for %s: %v", username, err)
		return false
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return false
	}
	
	var user struct {
		Type string `json:"type"`
	}
	
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return false
	}
	
	// Filter out both bots and organizations
	if user.Type == "Bot" {
		log.Printf("User %s is a bot (type: %s)", username, user.Type)
		return true
	}
	if user.Type == "Organization" {
		log.Printf("User %s is an organization (type: %s)", username, user.Type)
		return true
	}
	
	return false
}

// checkUserWriteAccess checks if a user has write access to the repository using author_association
func (rf *ReviewerFinder) checkUserWriteAccess(ctx context.Context, owner, repo, username string) (bool, string) {
	// Get user's author_association from their recent PRs
	association := rf.getUserAssociation(ctx, owner, repo, username)
	
	// Determine write access based on author_association
	// According to GitHub docs:
	// - OWNER: Owner of the repository
	// - MEMBER: Member of the organization that owns the repository
	// - COLLABORATOR: Outside collaborator with access to the repository
	// - CONTRIBUTOR: Has previously committed to the repository
	// - FIRST_TIME_CONTRIBUTOR: First time contributor
	// - FIRST_TIMER: First time contributor to any repo
	// - NONE: No association
	
	hasWriteAccess := false
	switch association {
	case "OWNER", "MEMBER", "COLLABORATOR":
		hasWriteAccess = true
	case "CONTRIBUTOR":
		// For contributors, check if they have recently approved PRs (indicates write access)
		hasWriteAccess = rf.hasRecentlyApprovedPRs(ctx, owner, repo, username)
		if !hasWriteAccess {
			log.Printf("User %s is a CONTRIBUTOR but has not recently approved PRs", username)
		} else {
			log.Printf("User %s is a CONTRIBUTOR with recent PR approvals (granting write access)", username)
		}
	default:
		hasWriteAccess = false
	}
	
	log.Printf("User %s has author_association: %s (write access: %v)", username, association, hasWriteAccess)
	return hasWriteAccess, association
}

// hasRecentlyApprovedPRs checks if a user has recently approved PRs in the repository
func (rf *ReviewerFinder) hasRecentlyApprovedPRs(ctx context.Context, owner, repo, username string) bool {
	log.Printf("Checking if %s has recently approved PRs in %s/%s", username, owner, repo)
	
	// Search for recently merged PRs
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls?state=closed&sort=updated&direction=desc&per_page=30", owner, repo)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		log.Printf("Failed to get recent PRs: %v", err)
		return false
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return false
	}
	
	var prs []struct {
		Number   int     `json:"number"`
		MergedAt *string `json:"merged_at"`
	}
	
	if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
		return false
	}
	
	// Check reviews for recent merged PRs
	oneYearAgo := time.Now().AddDate(-1, 0, 0)
	for _, pr := range prs {
		if pr.MergedAt == nil {
			continue
		}
		
		// Parse merge time
		mergeTime, err := time.Parse(time.RFC3339, *pr.MergedAt)
		if err != nil {
			continue
		}
		
		// Skip PRs older than one year
		if mergeTime.Before(oneYearAgo) {
			break
		}
		
		// Check if this user approved this PR
		reviewsURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/reviews", owner, repo, pr.Number)
		reviewResp, err := rf.client.makeRequest(ctx, "GET", reviewsURL, nil)
		if err != nil {
			continue
		}
		
		var reviews []struct {
			User struct {
				Login string `json:"login"`
			} `json:"user"`
			State string `json:"state"`
		}
		
		if err := json.NewDecoder(reviewResp.Body).Decode(&reviews); err != nil {
			reviewResp.Body.Close()
			continue
		}
		reviewResp.Body.Close()
		
		// Check if user approved this PR
		for _, review := range reviews {
			if review.User.Login == username && review.State == "APPROVED" {
				log.Printf("Found approval by %s on PR #%d", username, pr.Number)
				return true
			}
		}
	}
	
	log.Printf("No recent PR approvals found for %s", username)
	return false
}

// isOrgMember checks if a user is a member of the organization
func (rf *ReviewerFinder) isOrgMember(ctx context.Context, org, username string) bool {
	// Check if the owner is an organization or a user
	// If it's a user repository (not org), we can't check membership
	ownerType := rf.getOwnerType(ctx, org)
	if ownerType != "Organization" {
		// For user repositories, we can't check org membership
		log.Printf("Owner %s is not an organization (type: %s), skipping membership check", org, ownerType)
		return true // Allow access for user repositories
	}
	
	// Check organization membership
	url := fmt.Sprintf("https://api.github.com/orgs/%s/members/%s", org, username)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		log.Printf("Failed to check org membership for %s in %s: %v", username, org, err)
		return false
	}
	defer resp.Body.Close()
	
	// 204 No Content means the user is a member
	// 404 Not Found means the user is not a member
	isMember := resp.StatusCode == 204
	
	if !isMember && resp.StatusCode != 404 {
		log.Printf("Unexpected status code %d when checking org membership for %s in %s", resp.StatusCode, username, org)
	}
	
	return isMember
}

// getOwnerType checks if the owner is an organization or user
func (rf *ReviewerFinder) getOwnerType(ctx context.Context, owner string) string {
	url := fmt.Sprintf("https://api.github.com/users/%s", owner)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return "Unknown"
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return "Unknown"
	}
	
	var ownerInfo struct {
		Type string `json:"type"`
	}
	
	if err := json.NewDecoder(resp.Body).Decode(&ownerInfo); err != nil {
		return "Unknown"
	}
	
	return ownerInfo.Type
}

// getUserAssociation gets the user's association with the repository
func (rf *ReviewerFinder) getUserAssociation(ctx context.Context, owner, repo, username string) string {
	// Check recent PRs to find author_association
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls?state=all&creator=%s&per_page=1", owner, repo, username)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return "CONTRIBUTOR"
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "CONTRIBUTOR"
	}

	var prs []struct {
		AuthorAssociation string `json:"author_association"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
		return "CONTRIBUTOR"
	}

	if len(prs) > 0 {
		return prs[0].AuthorAssociation
	}

	return "CONTRIBUTOR"
}

// getMostRecentPRForFile gets the most recent PR that modified a specific file
func (rf *ReviewerFinder) getMostRecentPRForFile(ctx context.Context, owner, repo, filename string) (*RelatedPR, error) {
	// Use GitHub search API to find the most recent merged PR that modified this file
	query := fmt.Sprintf("repo:%s/%s is:pr is:merged filename:%s", owner, repo, filename)
	
	url := fmt.Sprintf("https://api.github.com/search/issues?q=%s&sort=updated&order=desc&per_page=1", query)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to search for recent PR: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to search for recent PR (status %d)", resp.StatusCode)
	}

	var searchResult struct {
		Items []struct {
			Number    int    `json:"number"`
			UpdatedAt string `json:"updated_at"`
		} `json:"items"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&searchResult); err != nil {
		return nil, fmt.Errorf("failed to decode search results: %w", err)
	}

	if len(searchResult.Items) == 0 {
		return nil, nil
	}

	prNumber := searchResult.Items[0].Number
	updatedAt, _ := time.Parse(time.RFC3339, searchResult.Items[0].UpdatedAt)

	// Get PR size
	prDetails, err := rf.client.getPullRequest(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get PR details: %w", err)
	}

	totalChanges := 0
	for _, file := range prDetails.ChangedFiles {
		totalChanges += file.Changes
	}

	return &RelatedPR{
		Number:   prNumber,
		MergedAt: updatedAt,
		Size:     totalChanges,
	}, nil
}

// getRecentPRReviewerForDirectory gets the most recent PR reviewer for a directory
func (rf *ReviewerFinder) getRecentPRReviewerForDirectory(ctx context.Context, owner, repo, directory string) (string, error) {
	// Search for PRs that modified files in this directory
	query := fmt.Sprintf("repo:%s/%s is:pr is:merged path:%s", owner, repo, directory)
	
	url := fmt.Sprintf("https://api.github.com/search/issues?q=%s&sort=updated&order=desc&per_page=5", query)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to search for directory PRs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to search for directory PRs (status %d)", resp.StatusCode)
	}

	var searchResult struct {
		Items []struct {
			Number int `json:"number"`
		} `json:"items"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&searchResult); err != nil {
		return "", fmt.Errorf("failed to decode search results: %w", err)
	}

	// Look for a PR with reviewers
	for _, item := range searchResult.Items {
		approvers, err := rf.getPRApprovers(ctx, owner, repo, item.Number)
		if err != nil {
			continue
		}
		if len(approvers) > 0 {
			return approvers[0], nil
		}
	}

	return "", nil
}

// getRecentPRAuthorForDirectory gets the most recent PR author for a directory
func (rf *ReviewerFinder) getRecentPRAuthorForDirectory(ctx context.Context, owner, repo, directory string) (string, error) {
	// Search for PRs that modified files in this directory
	query := fmt.Sprintf("repo:%s/%s is:pr is:merged path:%s", owner, repo, directory)
	
	url := fmt.Sprintf("https://api.github.com/search/issues?q=%s&sort=updated&order=desc&per_page=1", query)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to search for directory PRs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to search for directory PRs (status %d)", resp.StatusCode)
	}

	var searchResult struct {
		Items []struct {
			User struct {
				Login string `json:"login"`
			} `json:"user"`
		} `json:"items"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&searchResult); err != nil {
		return "", fmt.Errorf("failed to decode search results: %w", err)
	}

	if len(searchResult.Items) > 0 {
		return searchResult.Items[0].User.Login, nil
	}

	return "", nil
}

// SelectionMethod constants for logging
const (
	SelectionContextBlameApprover   = "context-blame-approver"
	SelectionActivityRecentApprover = "activity-recent-approver"
	SelectionFallbackLineAuthor     = "fallback-line-author"
	SelectionFallbackFileAuthor     = "fallback-file-author"
	SelectionFallbackDirReviewer    = "fallback-directory-reviewer"
	SelectionFallbackDirAuthor      = "fallback-directory-author"
	SelectionFallbackProjectReviewer = "fallback-project-reviewer"
	SelectionFallbackProjectAuthor   = "fallback-project-author"
)

// getRecentPRCommenters gets recent commenters on PRs in the repository who are members/collaborators
func (rf *ReviewerFinder) getRecentPRCommenters(ctx context.Context, owner, repo string, excludeUsers []string) ([]string, error) {
	log.Printf("Finding recent PR commenters who are members/collaborators")
	oneYearAgo := time.Now().AddDate(-1, 0, 0)
	
	// Get recent merged PRs
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls?state=closed&sort=updated&direction=desc&per_page=20", owner, repo)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get recent PRs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get recent PRs (status %d)", resp.StatusCode)
	}

	var prs []struct {
		Number   int    `json:"number"`
		MergedAt *string `json:"merged_at"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
		return nil, fmt.Errorf("failed to decode PRs: %w", err)
	}

	// Track unique commenters and their order
	commenters := []string{}
	seen := make(map[string]bool)
	
	// Check comments on recent merged PRs
	prCount := 0
	for _, pr := range prs {
		if pr.MergedAt == nil {
			continue
		}
		
		// Parse merge time and check if within one year
		mergeTime, err := time.Parse(time.RFC3339, *pr.MergedAt)
		if err != nil {
			log.Printf("Failed to parse merge time for PR #%d: %v", pr.Number, err)
			continue
		}
		
		if mergeTime.Before(oneYearAgo) {
			log.Printf("Reached PR #%d from %s (>1 year old) - stopping commenter search", pr.Number, mergeTime.Format("2006-01-02"))
			break // Short-circuit since PRs are sorted by updated time
		}
		
		prCount++
		log.Printf("Checking PR #%d for commenters", pr.Number)
		
		// Get PR comments
		commentsURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d/comments", owner, repo, pr.Number)
		commentsResp, err := rf.client.makeRequest(ctx, "GET", commentsURL, nil)
		if err != nil {
			log.Printf("Failed to get comments for PR #%d: %v", pr.Number, err)
			continue
		}
		
		var comments []struct {
			User struct {
				Login string `json:"login"`
			} `json:"user"`
			AuthorAssociation string `json:"author_association"`
		}
		
		if err := json.NewDecoder(commentsResp.Body).Decode(&comments); err != nil {
			commentsResp.Body.Close()
			continue
		}
		commentsResp.Body.Close()
		
		// Add commenters who are members/collaborators
		for _, comment := range comments {
			username := comment.User.Login
			if username == "" || seen[username] {
				continue
			}
			
			// Check if user should be excluded
			excluded := false
			for _, excludeUser := range excludeUsers {
				if username == excludeUser {
					excluded = true
					log.Printf("  Skipping commenter %s (excluded user)", username)
					break
				}
			}
			if excluded {
				continue
			}
			
			// Skip bot users
			if rf.isUserBot(ctx, username) {
				log.Printf("  Skipping commenter %s (is a bot)", username)
				continue
			}
			
			// Only include OWNER, MEMBER, COLLABORATOR, or CONTRIBUTOR
			switch comment.AuthorAssociation {
			case "OWNER", "MEMBER", "COLLABORATOR", "CONTRIBUTOR":
				seen[username] = true
				commenters = append(commenters, username)
				log.Printf("Found commenter %s on PR #%d (association: %s)", username, pr.Number, comment.AuthorAssociation)
			default:
				log.Printf("  Skipping commenter %s (association: %s)", username, comment.AuthorAssociation)
			}
		}
		
		// Limit to checking first 10 merged PRs
		if len(commenters) >= 5 || prCount >= 10 {
			break
		}
	}
	
	return commenters, nil
}