package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"time"
)

// Selection method constants for clear logging
const (
	AssigneeExpert        = "assignee-expert"
	ExpertAuthorOverlap   = "expert-author-overlap"
	ExpertAuthorDirectory = "expert-author-directory"
	ExpertAuthorProject   = "expert-author-project"
	
	ExpertReviewerCommenter = "expert-reviewer-commenter"
	ExpertReviewerOverlap   = "expert-reviewer-overlap"
	ExpertReviewerDirectory = "expert-reviewer-directory"
	ExpertReviewerProject   = "expert-reviewer-project"
)

// PRLineOverlap tracks how many lines a PR overlaps with current changes
type PRLineOverlap struct {
	PRNumber      int
	Author        string
	Reviewers     []string
	MergedAt      time.Time
	OverlapCount  int
	OverlapRatio  float64 // Percentage of current PR's changes that overlap
}

// findReviewersV5 uses line-overlap analysis to find the most relevant reviewers
func (rf *ReviewerFinder) findReviewersV5(ctx context.Context, pr *PullRequest) ([]ReviewerCandidate, error) {
	// Get all changed files sorted by changes
	topFiles := rf.getTopChangedFiles(pr, len(pr.ChangedFiles))
	log.Printf("Analyzing %d changed files for PR %d", len(topFiles), pr.Number)

	// Pre-fetch all PR file patches to avoid redundant API calls
	log.Printf("=== Fetching PR file patches (single API call) ===")
	patchCache, err := rf.fetchAllPRFiles(ctx, pr.Owner, pr.Repository, pr.Number)
	if err != nil {
		log.Printf("Failed to fetch PR files: %v", err)
		patchCache = make(map[string]string)
	}

	// Find expert author (code ownership context)
	log.Printf("=== Finding EXPERT AUTHOR (code ownership context) ===")
	primary, primaryMethod := rf.findPrimaryReviewerV5(ctx, pr, topFiles, patchCache)
	
	// Find expert reviewer (review activity context)
	log.Printf("=== Finding EXPERT REVIEWER (review activity context) ===")
	secondary, secondaryMethod := rf.findSecondaryReviewerV5(ctx, pr, topFiles, patchCache, primary)

	// Build final candidate list
	var candidates []ReviewerCandidate
	
	if primary != "" && primary != pr.Author {
		candidates = append(candidates, ReviewerCandidate{
			Username:        primary,
			SelectionMethod: primaryMethod,
			ContextScore:    100, // Expert Author gets higher score
		})
		log.Printf("EXPERT AUTHOR selected: %s (method: %s)", primary, primaryMethod)
	}
	
	if secondary != "" && secondary != pr.Author && secondary != primary {
		candidates = append(candidates, ReviewerCandidate{
			Username:        secondary,
			SelectionMethod: secondaryMethod,
			ContextScore:    50, // Expert Reviewer gets lower score
		})
		log.Printf("EXPERT REVIEWER selected: %s (method: %s)", secondary, secondaryMethod)
	}

	// Check if we have at least two reviewers
	if len(candidates) < 2 {
		log.Printf("WARNING: Only found %d reviewer(s), expected 2", len(candidates))
		if len(candidates) == 0 {
			return nil, fmt.Errorf("unable to find any suitable reviewers for PR %d", pr.Number)
		}
	}

	return candidates, nil
}

// findPrimaryReviewerV5 finds the expert author based on line overlap
func (rf *ReviewerFinder) findPrimaryReviewerV5(ctx context.Context, pr *PullRequest, files []string, patchCache map[string]string) (string, string) {
	// Priority 0: Check if PR has assignees who aren't the author
	if len(pr.Assignees) > 0 {
		log.Printf("Checking PR assignees for potential expert author")
		for _, assignee := range pr.Assignees {
			if assignee != pr.Author && !rf.isUserBot(ctx, assignee) && rf.hasWriteAccess(ctx, pr.Owner, pr.Repository, assignee) {
				log.Printf("Found assignee who isn't author as expert author: %s", assignee)
				return assignee, AssigneeExpert
			}
		}
	}

	// Priority 1: Authors of PRs with highest line overlap
	log.Printf("Analyzing line overlap with historical PRs for expert author")
	if candidate := rf.findOverlappingAuthors(ctx, pr, files, patchCache); candidate != "" {
		return candidate, ExpertAuthorOverlap
	}

	// Priority 2: Most recent author in directory
	log.Printf("No overlapping authors found, checking directory authors")
	directories := rf.getDirectoriesFromFiles(files)
	for _, dir := range directories {
		if candidate := rf.findRecentAuthorInDirectory(ctx, pr.Owner, pr.Repository, dir); candidate != "" {
			if candidate != pr.Author && !rf.isUserBot(ctx, candidate) && rf.hasWriteAccess(ctx, pr.Owner, pr.Repository, candidate) {
				return candidate, ExpertAuthorDirectory
			}
		}
	}

	// Priority 3: Most recent author in project
	log.Printf("No directory authors found, checking project authors")
	projectAuthors := rf.findRecentAuthorsInProject(ctx, pr.Owner, pr.Repository, 10)
	for _, candidate := range projectAuthors {
		if candidate != pr.Author && !rf.isUserBot(ctx, candidate) && rf.hasWriteAccess(ctx, pr.Owner, pr.Repository, candidate) {
			log.Printf("Found suitable project author: %s", candidate)
			return candidate, ExpertAuthorProject
		}
		log.Printf("Skipping project author %s (PR author, bot/org, or no write access)", candidate)
	}

	return "", ""
}

// findSecondaryReviewerV5 finds the expert reviewer based on line overlap
func (rf *ReviewerFinder) findSecondaryReviewerV5(ctx context.Context, pr *PullRequest, files []string, patchCache map[string]string, primary string) (string, string) {
	// Priority 1: Recent PR commenters who are members/collaborators
	log.Printf("Checking for recent PR commenters who are members/collaborators")
	excludeUsers := []string{pr.Author}
	if primary != "" {
		excludeUsers = append(excludeUsers, primary)
	}
	commenters, err := rf.getRecentPRCommenters(ctx, pr.Owner, pr.Repository, excludeUsers)
	if err != nil {
		log.Printf("Failed to get recent commenters: %v", err)
	} else if len(commenters) > 0 {
		log.Printf("Found %d recent commenters", len(commenters))
		// Return the first commenter
		log.Printf("✓ Selected recent commenter as expert reviewer: %s", commenters[0])
		return commenters[0], ExpertReviewerCommenter
	}

	// Priority 2: Reviewers of PRs with highest line overlap
	log.Printf("No recent commenters found, analyzing line overlap with historical PRs for expert reviewer")
	if candidate := rf.findOverlappingReviewers(ctx, pr, files, patchCache, primary); candidate != "" {
		return candidate, ExpertReviewerOverlap
	}

	// Priority 3: Most recent reviewer in directory
	log.Printf("No overlapping reviewers found, checking directory reviewers")
	directories := rf.getDirectoriesFromFiles(files)
	for _, dir := range directories {
		if candidate := rf.findRecentReviewerInDirectory(ctx, pr.Owner, pr.Repository, dir); candidate != "" {
			if candidate != pr.Author && candidate != primary && !rf.isUserBot(ctx, candidate) {
				return candidate, ExpertReviewerDirectory
			}
		}
	}

	// Priority 4: Most recent reviewer in project
	log.Printf("No directory reviewers found, checking project reviewers")
	if candidate := rf.findRecentReviewerInProject(ctx, pr.Owner, pr.Repository); candidate != "" {
		if candidate != pr.Author && candidate != primary && !rf.isUserBot(ctx, candidate) {
			return candidate, ExpertReviewerProject
		}
	}

	return "", ""
}

// findOverlappingAuthors finds authors of PRs that touched the same lines
func (rf *ReviewerFinder) findOverlappingAuthors(ctx context.Context, pr *PullRequest, files []string, patchCache map[string]string) string {
	// Get all overlapping PRs across all files
	allOverlaps := []PRLineOverlap{}
	
	for _, filename := range files {
		// Get the lines changed in the current PR
		changedLines, err := rf.getChangedLinesFromCache(filename, patchCache)
		if err != nil || len(changedLines) == 0 {
			continue
		}
		
		log.Printf("File %s: Analyzing %d changed lines", filename, len(changedLines))
		
		// Find historical PRs and calculate overlap
		overlaps, err := rf.calculatePROverlaps(ctx, pr, filename, changedLines)
		if err != nil {
			log.Printf("Failed to calculate overlaps for %s: %v", filename, err)
			continue
		}
		
		allOverlaps = append(allOverlaps, overlaps...)
	}
	
	// Aggregate overlaps by PR (a PR might touch multiple files)
	prOverlapMap := make(map[int]*PRLineOverlap)
	log.Printf("Aggregating %d file-level overlaps", len(allOverlaps))
	for _, overlap := range allOverlaps {
		if existing, ok := prOverlapMap[overlap.PRNumber]; ok {
			existing.OverlapCount += overlap.OverlapCount
			log.Printf("  PR #%d: adding %d to existing score (now %d)", 
				overlap.PRNumber, overlap.OverlapCount, existing.OverlapCount)
		} else {
			// Make a copy to avoid pointer issues
			overlapCopy := overlap
			prOverlapMap[overlap.PRNumber] = &overlapCopy
			log.Printf("  PR #%d: new entry with score %d", overlap.PRNumber, overlap.OverlapCount)
		}
	}
	
	// Convert to slice and sort by overlap count
	var sortedOverlaps []PRLineOverlap
	for _, overlap := range prOverlapMap {
		sortedOverlaps = append(sortedOverlaps, *overlap)
	}
	sort.Slice(sortedOverlaps, func(i, j int) bool {
		return sortedOverlaps[i].OverlapCount > sortedOverlaps[j].OverlapCount
	})
	
	// Log top overlapping PRs
	if len(sortedOverlaps) > 0 {
		log.Printf("Top PRs by line overlap:")
		for i, overlap := range sortedOverlaps {
			if i >= 5 {
				break
			}
			daysSince := int(time.Since(overlap.MergedAt).Hours() / 24)
			log.Printf("  %d. PR #%d by %s (%d relevance score, %d days ago)", 
				i+1, overlap.PRNumber, overlap.Author, overlap.OverlapCount, daysSince)
		}
	}
	
	// Check authors of top 5 PRs for write access
	checked := make(map[string]bool)
	log.Printf("Checking write access for authors of %d overlapping PRs", len(sortedOverlaps))
	for i, overlap := range sortedOverlaps {
		if i >= 5 {
			break
		}
		
		author := overlap.Author
		log.Printf("  Checking PR #%d author: %s", overlap.PRNumber, author)
		
		if author == "" {
			log.Printf("    -> Skipping: empty author")
			continue
		}
		if author == pr.Author {
			log.Printf("    -> Skipping: is PR author")
			continue
		}
		if checked[author] {
			log.Printf("    -> Skipping: already checked")
			continue
		}
		checked[author] = true
		
		// Skip bot users and organizations
		if rf.isUserBot(ctx, author) {
			log.Printf("    -> Skipping: is a bot or organization")
			continue
		}
		
		hasAccess, association := rf.checkUserWriteAccess(ctx, pr.Owner, pr.Repository, author)
		if hasAccess {
			log.Printf("✓ Selected overlapping author: %s from PR #%d (%d lines overlap, association: %s)", 
				author, overlap.PRNumber, overlap.OverlapCount, association)
			return author
		}
		log.Printf("✗ Skipping author %s - no write access (association: %s)", author, association)
	}
	
	return ""
}

// findOverlappingReviewers finds reviewers of PRs that touched the same lines
func (rf *ReviewerFinder) findOverlappingReviewers(ctx context.Context, pr *PullRequest, files []string, patchCache map[string]string, excludeUser string) string {
	// Get all overlapping PRs across all files
	allOverlaps := []PRLineOverlap{}
	
	for _, filename := range files {
		// Get the lines changed in the current PR
		changedLines, err := rf.getChangedLinesFromCache(filename, patchCache)
		if err != nil || len(changedLines) == 0 {
			continue
		}
		
		log.Printf("File %s: Analyzing %d changed lines for reviewers", filename, len(changedLines))
		
		// Find historical PRs and calculate overlap
		overlaps, err := rf.calculatePROverlaps(ctx, pr, filename, changedLines)
		if err != nil {
			log.Printf("Failed to calculate overlaps for %s: %v", filename, err)
			continue
		}
		
		allOverlaps = append(allOverlaps, overlaps...)
	}
	
	// Aggregate overlaps by PR
	prOverlapMap := make(map[int]*PRLineOverlap)
	log.Printf("Aggregating %d file-level overlaps for reviewers", len(allOverlaps))
	for _, overlap := range allOverlaps {
		if existing, ok := prOverlapMap[overlap.PRNumber]; ok {
			existing.OverlapCount += overlap.OverlapCount
			log.Printf("  PR #%d: adding %d to existing score (now %d)", 
				overlap.PRNumber, overlap.OverlapCount, existing.OverlapCount)
		} else {
			// Make a copy to avoid pointer issues
			overlapCopy := overlap
			prOverlapMap[overlap.PRNumber] = &overlapCopy
			log.Printf("  PR #%d: new entry with score %d (reviewers: %v)", 
				overlap.PRNumber, overlap.OverlapCount, overlap.Reviewers)
		}
	}
	
	// Convert to slice and sort by overlap count
	var sortedOverlaps []PRLineOverlap
	for _, overlap := range prOverlapMap {
		sortedOverlaps = append(sortedOverlaps, *overlap)
	}
	sort.Slice(sortedOverlaps, func(i, j int) bool {
		return sortedOverlaps[i].OverlapCount > sortedOverlaps[j].OverlapCount
	})
	
	// Log top overlapping PRs
	if len(sortedOverlaps) > 0 {
		log.Printf("Top PRs by line overlap (for reviewers):")
		for i, overlap := range sortedOverlaps {
			if i >= 5 {
				break
			}
			daysSince := int(time.Since(overlap.MergedAt).Hours() / 24)
			log.Printf("  %d. PR #%d (%d relevance score, %d days ago, reviewers: %v)", 
				i+1, overlap.PRNumber, overlap.OverlapCount, daysSince, overlap.Reviewers)
		}
	}
	
	// Find reviewers from top 5 PRs
	reviewerScores := make(map[string]int)
	for i, overlap := range sortedOverlaps {
		if i >= 5 {
			break
		}
		
		// Weight reviewers by the overlap count of their PR
		for _, reviewer := range overlap.Reviewers {
			if reviewer != "" && reviewer != pr.Author && reviewer != excludeUser {
				// Skip bot users and organizations
				if rf.isUserBot(ctx, reviewer) {
					log.Printf("    Skipping reviewer %s: is a bot or organization", reviewer)
					continue
				}
				reviewerScores[reviewer] += overlap.OverlapCount
			}
		}
	}
	
	// Sort reviewers by score
	type reviewerScore struct {
		username string
		score    int
	}
	var candidates []reviewerScore
	for reviewer, score := range reviewerScores {
		candidates = append(candidates, reviewerScore{reviewer, score})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	
	if len(candidates) > 0 {
		log.Printf("Reviewer candidates by overlap score:")
		for i, c := range candidates {
			if i >= 5 {
				break
			}
			log.Printf("  %d. %s (score: %d)", i+1, c.username, c.score)
		}
		
		selected := candidates[0]
		log.Printf("✓ Selected overlapping reviewer: %s (overlap score: %d)", 
			selected.username, selected.score)
		return selected.username
	}
	
	return ""
}

// calculatePROverlaps finds PRs that touched the same lines and calculates overlap
func (rf *ReviewerFinder) calculatePROverlaps(ctx context.Context, currentPR *PullRequest, filename string, changedLines []int) ([]PRLineOverlap, error) {
	// Get historical PRs for this file
	historicalPRs, err := rf.getHistoricalPRsForFile(ctx, currentPR.Owner, currentPR.Repository, filename, 30)
	if err != nil {
		return nil, err
	}
	
	var overlaps []PRLineOverlap
	
	// For each historical PR, calculate line overlap
	for _, prInfo := range historicalPRs {
		if prInfo.Number == currentPR.Number {
			continue
		}
		
		// Get the diff for this historical PR
		prPatch, err := rf.getPRFilePatch(ctx, currentPR.Owner, currentPR.Repository, prInfo.Number, filename)
		if err != nil {
			log.Printf("Failed to get patch for PR #%d: %v", prInfo.Number, err)
			continue
		}
		
		if prPatch == "" {
			continue
		}
		
		// Calculate which lines this PR changed
		historicalChangedLines := parsePatchForChangedLines(prPatch)
		
		// Calculate overlap (including nearby lines)
		overlapCount := 0
		nearbyCount := 0
		lineSet := make(map[int]bool)
		for _, line := range historicalChangedLines {
			lineSet[line] = true
		}
		
		// Check for exact matches and nearby lines (within 3 lines)
		for _, line := range changedLines {
			if lineSet[line] {
				overlapCount++
			} else {
				// Check if any historical change is within 3 lines
				for histLine := range lineSet {
					distance := line - histLine
					if distance < 0 {
						distance = -distance
					}
					if distance <= 3 {
						nearbyCount++
						break
					}
				}
			}
		}
		
		// Consider both exact overlaps and nearby lines
		totalRelevance := overlapCount + nearbyCount
		if totalRelevance > 0 {
			overlap := PRLineOverlap{
				PRNumber:     prInfo.Number,
				Author:       prInfo.Author,
				Reviewers:    prInfo.Reviewers,
				MergedAt:     prInfo.MergedAt,
				OverlapCount: totalRelevance, // Use combined score
				OverlapRatio: float64(overlapCount) / float64(len(changedLines)),
			}
			overlaps = append(overlaps, overlap)
			
			if overlapCount > 0 {
				log.Printf("  PR #%d: %d lines exact overlap, %d nearby (%.1f%% exact overlap)", 
					prInfo.Number, overlapCount, nearbyCount, overlap.OverlapRatio*100)
			} else {
				log.Printf("  PR #%d: %d lines nearby (no exact overlap)", 
					prInfo.Number, nearbyCount)
			}
		}
	}
	
	return overlaps, nil
}

// getPRFilePatch gets the patch for a specific file in a PR
func (rf *ReviewerFinder) getPRFilePatch(ctx context.Context, owner, repo string, prNumber int, filename string) (string, error) {
	// Check if we have a cached version first
	cacheKey := fmt.Sprintf("%s/%s/%d", owner, repo, prNumber)
	if rf.prPatchCache == nil {
		rf.prPatchCache = make(map[string]map[string]string)
	}
	
	if filePatches, ok := rf.prPatchCache[cacheKey]; ok {
		if patch, ok := filePatches[filename]; ok {
			return patch, nil
		}
	}
	
	// Fetch from API
	log.Printf("Fetching patch for PR #%d file %s", prNumber, filename)
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/files", owner, repo, prNumber)
	resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("failed to get PR files (status %d)", resp.StatusCode)
	}
	
	var files []struct {
		Filename string `json:"filename"`
		Patch    string `json:"patch"`
	}
	
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return "", err
	}
	
	// Cache all patches from this PR
	if rf.prPatchCache[cacheKey] == nil {
		rf.prPatchCache[cacheKey] = make(map[string]string)
	}
	
	for _, file := range files {
		rf.prPatchCache[cacheKey][file.Filename] = file.Patch
	}
	
	// Return the requested file's patch
	return rf.prPatchCache[cacheKey][filename], nil
}