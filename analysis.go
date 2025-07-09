package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RelatedPR represents a PR that modified similar lines
type RelatedPR struct {
	Number       int
	LinesReviewed int
	MergedAt     time.Time
	Size         int
}

// getBlameData gets blame data for a file using GitHub API v4
func (rf *ReviewerFinder) getBlameData(ctx context.Context, owner, repo, filename string, prNumber int) (*BlameData, error) {
	log.Printf("Fetching blame data for file: %s", filename)
	
	// GraphQL query to get blame data
	query := `
	query($owner: String!, $repo: String!, $path: String!) {
		repository(owner: $owner, name: $repo) {
			object(expression: "HEAD") {
				... on Commit {
					blame(path: $path) {
						ranges {
							startingLine
							endingLine
							commit {
								oid
								author {
									user {
										login
									}
								}
								associatedPullRequests(first: 1) {
									nodes {
										number
									}
								}
							}
						}
					}
				}
			}
		}
	}`

	variables := map[string]interface{}{
		"owner": owner,
		"repo":  repo,
		"path":  filename,
	}

	result, err := rf.client.makeGraphQLRequest(ctx, query, variables)
	if err != nil {
		return nil, fmt.Errorf("failed to get blame data: %w", err)
	}

	// Parse the GraphQL response
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid GraphQL response format")
	}

	repository, ok := data["repository"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("repository not found in response")
	}

	object, ok := repository["object"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("object not found in response")
	}

	blame, ok := object["blame"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("blame not found in response")
	}

	ranges, ok := blame["ranges"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("ranges not found in blame response")
	}

	var blameData BlameData
	for _, rangeData := range ranges {
		rangeMap, ok := rangeData.(map[string]interface{})
		if !ok {
			continue
		}

		startingLine, ok := rangeMap["startingLine"].(float64)
		if !ok {
			continue
		}

		endingLine, ok := rangeMap["endingLine"].(float64)
		if !ok {
			continue
		}

		commit, ok := rangeMap["commit"].(map[string]interface{})
		if !ok {
			continue
		}

		oid, ok := commit["oid"].(string)
		if !ok {
			continue
		}

		var author string
		if authorData, ok := commit["author"].(map[string]interface{}); ok {
			if user, ok := authorData["user"].(map[string]interface{}); ok {
				if login, ok := user["login"].(string); ok {
					author = login
				}
			}
		}

		var prNumber int
		if prs, ok := commit["associatedPullRequests"].(map[string]interface{}); ok {
			if nodes, ok := prs["nodes"].([]interface{}); ok && len(nodes) > 0 {
				if node, ok := nodes[0].(map[string]interface{}); ok {
					if number, ok := node["number"].(float64); ok {
						prNumber = int(number)
					}
				}
			}
		}

		// Add blame lines for the range
		for line := int(startingLine); line <= int(endingLine); line++ {
			blameData.Lines = append(blameData.Lines, BlameLine{
				LineNumber: line,
				Author:     author,
				CommitSHA:  oid,
				PRNumber:   prNumber,
			})
		}
	}

	log.Printf("Blame data for %s: found %d lines with blame info", filename, len(blameData.Lines))
	return &blameData, nil
}

// findRelatedPRs finds PRs that modified lines being changed in the current PR
func (rf *ReviewerFinder) findRelatedPRs(ctx context.Context, pr *PullRequest, filename string, blameData *BlameData) ([]RelatedPR, error) {
	// Get the diff for this file to see which lines are being changed
	changedLines, err := rf.getChangedLines(ctx, pr.Owner, pr.Repository, pr.Number, filename)
	if err != nil {
		return nil, fmt.Errorf("failed to get changed lines: %w", err)
	}

	log.Printf("Found %d changed lines in %s", len(changedLines), filename)

	// Map of PR number to lines reviewed
	prLineCount := make(map[int]int)

	// For each changed line, find which PR last modified it
	for _, lineNum := range changedLines {
		for _, blameLine := range blameData.Lines {
			if blameLine.LineNumber == lineNum && blameLine.PRNumber > 0 {
				prLineCount[blameLine.PRNumber]++
				break
			}
		}
	}

	// Convert to RelatedPR slice and get additional info
	var relatedPRs []RelatedPR
	for prNum, lineCount := range prLineCount {
		if prNum == pr.Number {
			continue // Skip the current PR
		}

		// Get PR details for merge time
		prDetails, err := rf.client.getPullRequest(ctx, pr.Owner, pr.Repository, prNum)
		if err != nil {
			log.Printf("Failed to get PR details for %d: %v", prNum, err)
			continue
		}

		relatedPRs = append(relatedPRs, RelatedPR{
			Number:       prNum,
			LinesReviewed: lineCount,
			MergedAt:     prDetails.UpdatedAt, // Use UpdatedAt as proxy for merge time
		})
	}

	// Sort by lines reviewed (descending) and take top 3
	sort.Slice(relatedPRs, func(i, j int) bool {
		return relatedPRs[i].LinesReviewed > relatedPRs[j].LinesReviewed
	})

	if len(relatedPRs) > 3 {
		relatedPRs = relatedPRs[:3]
	}

	log.Printf("Found %d related PRs for %s", len(relatedPRs), filename)
	return relatedPRs, nil
}

// PRFilesPatch represents a file's patch data
type PRFilesPatch struct {
	Filename string
	Patch    string
}

// fetchAllPRFiles fetches all PR files and their patches in a single API call
func (rf *ReviewerFinder) fetchAllPRFiles(ctx context.Context, owner, repo string, prNumber int) (map[string]string, error) {
	log.Printf("Fetching all PR files and patches for PR %d", prNumber)
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/files", owner, repo, prNumber)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get PR files: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to get PR files (status %d)", resp.StatusCode)
	}

	var files []struct {
		Filename string `json:"filename"`
		Patch    string `json:"patch"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, fmt.Errorf("failed to decode PR files: %w", err)
	}

	// Create a map of filename to patch
	patchCache := make(map[string]string)
	for _, file := range files {
		patchCache[file.Filename] = file.Patch
	}

	log.Printf("Cached patches for %d files", len(patchCache))
	return patchCache, nil
}

// getChangedLines gets the line numbers that are being changed in a file
// Now uses a pre-fetched cache of patches
func (rf *ReviewerFinder) getChangedLines(ctx context.Context, owner, repo string, prNumber int, filename string) ([]int, error) {
	// This is a legacy method for backward compatibility
	// It still makes individual API calls - callers should migrate to getChangedLinesFromCache
	log.Printf("WARNING: Using legacy getChangedLines for %s - consider using cached version", filename)
	
	patchCache, err := rf.fetchAllPRFiles(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, err
	}
	
	return rf.getChangedLinesFromCache(filename, patchCache)
}

// getChangedLinesFromCache gets the line numbers from a pre-fetched patch cache
func (rf *ReviewerFinder) getChangedLinesFromCache(filename string, patchCache map[string]string) ([]int, error) {
	patch, exists := patchCache[filename]
	if !exists || patch == "" {
		log.Printf("No patch found for file %s", filename)
		return []int{}, nil
	}

	// Parse the patch to find changed lines
	changedLines := parsePatchForChangedLines(patch)
	log.Printf("File %s: %d lines changed", filename, len(changedLines))
	return changedLines, nil
}

// parsePatchForChangedLines parses a Git patch to find changed line numbers
func parsePatchForChangedLines(patch string) []int {
	var changedLines []int
	lines := strings.Split(patch, "\n")
	
	var currentLine int
	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			// Parse hunk header to get starting line number
			// Format: @@ -old_start,old_count +new_start,new_count @@
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				newPart := parts[2] // +new_start,new_count
				if strings.HasPrefix(newPart, "+") {
					newStart := strings.Split(newPart[1:], ",")[0]
					if num, err := strconv.Atoi(newStart); err == nil {
						currentLine = num
					}
				}
			}
		} else if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			// This is an added line
			changedLines = append(changedLines, currentLine)
			currentLine++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			// This is a deleted line (don't increment currentLine)
			changedLines = append(changedLines, currentLine)
		} else if !strings.HasPrefix(line, "\\") && line != "" {
			// Context line
			currentLine++
		}
	}
	
	return changedLines
}

// getPRApprovers gets the users who approved a PR
func (rf *ReviewerFinder) getPRApprovers(ctx context.Context, owner, repo string, prNumber int) ([]string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/reviews", owner, repo, prNumber)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get PR reviews: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to get PR reviews (status %d)", resp.StatusCode)
	}

	var reviews []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		State string `json:"state"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&reviews); err != nil {
		return nil, fmt.Errorf("failed to decode PR reviews: %w", err)
	}

	var approvers []string
	approverSet := make(map[string]bool)

	for _, review := range reviews {
		if review.State == "APPROVED" && !approverSet[review.User.Login] {
			approvers = append(approvers, review.User.Login)
			approverSet[review.User.Login] = true
		}
	}

	return approvers, nil
}

// findRecentPRsForFile finds recent PRs that modified a specific file
func (rf *ReviewerFinder) findRecentPRsForFile(ctx context.Context, owner, repo, filename string) ([]RelatedPR, error) {
	// Use GitHub search API to find PRs that modified this file
	query := fmt.Sprintf("repo:%s/%s is:pr is:merged filename:%s", owner, repo, filename)
	
	url := fmt.Sprintf("https://api.github.com/search/issues?q=%s&sort=updated&order=desc&per_page=10", query)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to search for PRs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to search for PRs (status %d)", resp.StatusCode)
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

	var recentPRs []RelatedPR
	for _, item := range searchResult.Items {
		updatedAt, err := time.Parse(time.RFC3339, item.UpdatedAt)
		if err != nil {
			continue
		}

		// Get PR size by fetching the PR details
		prDetails, err := rf.client.getPullRequest(ctx, owner, repo, item.Number)
		if err != nil {
			log.Printf("Failed to get PR details for %d: %v", item.Number, err)
			continue
		}

		// Calculate PR size as total changes
		totalChanges := 0
		for _, file := range prDetails.ChangedFiles {
			totalChanges += file.Changes
		}

		recentPRs = append(recentPRs, RelatedPR{
			Number:   item.Number,
			MergedAt: updatedAt,
			Size:     totalChanges,
		})
	}

	// Sort by merge time (most recent first)
	sort.Slice(recentPRs, func(i, j int) bool {
		return recentPRs[i].MergedAt.After(recentPRs[j].MergedAt)
	})

	// Take the most recent PR
	if len(recentPRs) > 1 {
		recentPRs = recentPRs[:1]
	}

	return recentPRs, nil
}

// getFallbackReviewers implements the fallback strategy for finding reviewers
func (rf *ReviewerFinder) getFallbackReviewers(ctx context.Context, pr *PullRequest, files []string) ([]ReviewerCandidate, error) {
	log.Printf("Using fallback mechanisms to find reviewers for %d files", len(files))
	
	var candidates []ReviewerCandidate
	
	// Fallback 1: Most recent PR author for each file
	for _, filename := range files {
		author, err := rf.getRecentPRAuthorForFile(ctx, pr.Owner, pr.Repository, filename)
		if err != nil {
			log.Printf("Failed to get recent PR author for %s: %v", filename, err)
			continue
		}
		if author != "" && author != pr.Author {
			candidates = append(candidates, ReviewerCandidate{
				Username:     author,
				ContextScore: 5, // Lower score for fallback
				LastActivity: time.Now().Add(-24 * time.Hour), // Assume recent
			})
			log.Printf("Fallback 1: Found recent PR author %s for file %s", author, filename)
		}
	}
	
	// If we still don't have candidates, use repo-wide fallbacks
	if len(candidates) == 0 {
		log.Printf("No file-specific fallbacks found, using repo-wide fallbacks")
		
		// Fallback 2: Most recent PR approver in the repo
		repoApprover, err := rf.getRecentPRApproverForRepo(ctx, pr.Owner, pr.Repository)
		if err != nil {
			log.Printf("Failed to get recent PR approver for repo: %v", err)
		} else if repoApprover != "" && repoApprover != pr.Author {
			candidates = append(candidates, ReviewerCandidate{
				Username:      repoApprover,
				ActivityScore: 3, // Lower score for repo-wide fallback
				LastActivity:  time.Now().Add(-48 * time.Hour),
			})
			log.Printf("Fallback 2: Found recent PR approver %s for repo", repoApprover)
		}
		
		// Fallback 3: Most recent PR author in the repo
		if len(candidates) == 0 {
			repoAuthor, err := rf.getRecentPRAuthorForRepo(ctx, pr.Owner, pr.Repository)
			if err != nil {
				log.Printf("Failed to get recent PR author for repo: %v", err)
			} else if repoAuthor != "" && repoAuthor != pr.Author {
				candidates = append(candidates, ReviewerCandidate{
					Username:     repoAuthor,
					ContextScore: 2, // Lowest score for final fallback
					LastActivity: time.Now().Add(-72 * time.Hour),
				})
				log.Printf("Fallback 3: Found recent PR author %s for repo", repoAuthor)
			}
		}
	}
	
	log.Printf("Found %d fallback candidates", len(candidates))
	return candidates, nil
}

// getRecentPRAuthorForFile gets the most recent PR author for a specific file
func (rf *ReviewerFinder) getRecentPRAuthorForFile(ctx context.Context, owner, repo, filename string) (string, error) {
	// Use GitHub search API to find the most recent PR that modified this file
	query := fmt.Sprintf("repo:%s/%s is:pr is:merged filename:%s", owner, repo, filename)
	
	url := fmt.Sprintf("https://api.github.com/search/issues?q=%s&sort=updated&order=desc&per_page=1", query)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to search for recent PR: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("failed to search for recent PR (status %d)", resp.StatusCode)
	}

	var searchResult struct {
		Items []struct {
			Number int `json:"number"`
			User   struct {
				Login string `json:"login"`
			} `json:"user"`
		} `json:"items"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&searchResult); err != nil {
		return "", fmt.Errorf("failed to decode search results: %w", err)
	}

	if len(searchResult.Items) == 0 {
		return "", nil
	}

	return searchResult.Items[0].User.Login, nil
}

// getRecentPRApproverForRepo gets the most recent PR approver in the entire repo
func (rf *ReviewerFinder) getRecentPRApproverForRepo(ctx context.Context, owner, repo string) (string, error) {
	// Get recent merged PRs for the repo
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls?state=closed&sort=updated&direction=desc&per_page=10", owner, repo)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get recent PRs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("failed to get recent PRs (status %d)", resp.StatusCode)
	}

	var prs []struct {
		Number   int    `json:"number"`
		MergedAt string `json:"merged_at"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
		return "", fmt.Errorf("failed to decode PRs: %w", err)
	}

	// Look through recent merged PRs to find one with an approver
	for _, pr := range prs {
		if pr.MergedAt == "" {
			continue // Skip non-merged PRs
		}
		
		approvers, err := rf.getPRApprovers(ctx, owner, repo, pr.Number)
		if err != nil {
			log.Printf("Failed to get approvers for PR %d: %v", pr.Number, err)
			continue
		}
		
		if len(approvers) > 0 {
			return approvers[0], nil // Return the first approver
		}
	}

	return "", nil
}

// getRecentPRAuthorForRepo gets the most recent PR author in the entire repo
func (rf *ReviewerFinder) getRecentPRAuthorForRepo(ctx context.Context, owner, repo string) (string, error) {
	// Get the most recent merged PR for the repo
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls?state=closed&sort=updated&direction=desc&per_page=1", owner, repo)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get recent PR: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("failed to get recent PR (status %d)", resp.StatusCode)
	}

	var prs []struct {
		Number   int    `json:"number"`
		MergedAt string `json:"merged_at"`
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
		return "", fmt.Errorf("failed to decode PRs: %w", err)
	}

	if len(prs) == 0 || prs[0].MergedAt == "" {
		return "", nil
	}

	return prs[0].User.Login, nil
}