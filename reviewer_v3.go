package main

import (
	"context"
	"fmt"
	"log"
	"path"
	"sort"
	"strings"
)

// Selection method constants for clear logging
const (
	PrimaryBlameAuthor     = "primary-blame-author"
	PrimaryDirectoryAuthor = "primary-directory-author"
	PrimaryProjectAuthor   = "primary-project-author"
	
	SecondaryBlameReviewer    = "secondary-blame-reviewer"
	SecondaryDirectoryReviewer = "secondary-directory-reviewer"
	SecondaryProjectReviewer   = "secondary-project-reviewer"
)

// findReviewersV3 finds exactly two reviewers: primary (author context) and secondary (active reviewer)
func (rf *ReviewerFinder) findReviewersV3(ctx context.Context, pr *PullRequest) ([]ReviewerCandidate, error) {
	// Get all changed files sorted by changes
	topFiles := rf.getTopChangedFiles(pr, len(pr.ChangedFiles))
	log.Printf("Analyzing %d changed files for PR %d", len(topFiles), pr.Number)

	// Pre-fetch all PR file patches to avoid redundant API calls
	log.Printf("=== Fetching PR file patches (single API call) ===")
	patchCache, err := rf.fetchAllPRFiles(ctx, pr.Owner, pr.Repository, pr.Number)
	if err != nil {
		log.Printf("Failed to fetch PR files: %v", err)
		// Create empty cache to continue with limited functionality
		patchCache = make(map[string]string)
	}

	// Pre-fetch blame data for all files to avoid redundant API calls
	log.Printf("=== Fetching blame data for %d files ===", len(topFiles))
	blameCache := make(map[string]*BlameData)
	for _, filename := range topFiles {
		blameData, err := rf.getBlameData(ctx, pr.Owner, pr.Repository, filename, pr.Number)
		if err != nil {
			log.Printf("Failed to get blame data for %s: %v", filename, err)
			continue
		}
		blameCache[filename] = blameData
	}
	log.Printf("=== Blame data fetch complete ===")

	// Find primary reviewer (author context)
	log.Printf("=== Finding PRIMARY reviewer (author context) ===")
	primary, primaryMethod := rf.findPrimaryReviewer(ctx, pr, topFiles, blameCache, patchCache)
	
	// Find secondary reviewer (active reviewer)
	log.Printf("=== Finding SECONDARY reviewer (active reviewer) ===")
	secondary, secondaryMethod := rf.findSecondaryReviewer(ctx, pr, topFiles, blameCache, patchCache, primary)

	// Build final candidate list
	var candidates []ReviewerCandidate
	
	if primary != "" && primary != pr.Author {
		candidates = append(candidates, ReviewerCandidate{
			Username:        primary,
			SelectionMethod: primaryMethod,
			ContextScore:    100, // Primary gets higher score
		})
		log.Printf("PRIMARY reviewer selected: %s (method: %s)", primary, primaryMethod)
	}
	
	if secondary != "" && secondary != pr.Author && secondary != primary {
		candidates = append(candidates, ReviewerCandidate{
			Username:        secondary,
			SelectionMethod: secondaryMethod,
			ContextScore:    50, // Secondary gets lower score
		})
		log.Printf("SECONDARY reviewer selected: %s (method: %s)", secondary, secondaryMethod)
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

// findPrimaryReviewer finds the primary reviewer based on author context
func (rf *ReviewerFinder) findPrimaryReviewer(ctx context.Context, pr *PullRequest, files []string, blameCache map[string]*BlameData, patchCache map[string]string) (string, string) {
	// Priority 1: Authors of overlapping PRs from blame data
	log.Printf("Checking blame-based authors for primary reviewer")
	if candidate := rf.findBlameBasedAuthors(ctx, pr, files, blameCache, patchCache); candidate != "" {
		return candidate, PrimaryBlameAuthor
	}

	// Priority 2: Most recent author in directory
	log.Printf("No blame-based authors found, checking directory authors")
	directories := rf.getDirectoriesFromFiles(files)
	for _, dir := range directories {
		if candidate := rf.findRecentAuthorInDirectory(ctx, pr.Owner, pr.Repository, dir); candidate != "" {
			if candidate != pr.Author && rf.hasWriteAccess(ctx, pr.Owner, pr.Repository, candidate) {
				return candidate, PrimaryDirectoryAuthor
			}
		}
	}

	// Priority 3: Most recent author in project
	log.Printf("No directory authors found, checking project authors")
	if candidate := rf.findRecentAuthorInProject(ctx, pr.Owner, pr.Repository); candidate != "" {
		if candidate != pr.Author && rf.hasWriteAccess(ctx, pr.Owner, pr.Repository, candidate) {
			return candidate, PrimaryProjectAuthor
		}
	}

	return "", ""
}

// findSecondaryReviewer finds the secondary reviewer based on review activity
func (rf *ReviewerFinder) findSecondaryReviewer(ctx context.Context, pr *PullRequest, files []string, blameCache map[string]*BlameData, patchCache map[string]string, primary string) (string, string) {
	// Priority 1: Reviewers of overlapping PRs from blame data
	log.Printf("Checking blame-based reviewers for secondary reviewer")
	if candidate := rf.findBlameBasedReviewers(ctx, pr, files, blameCache, patchCache, primary); candidate != "" {
		return candidate, SecondaryBlameReviewer
	}

	// Priority 2: Most recent reviewer in directory
	log.Printf("No blame-based reviewers found, checking directory reviewers")
	directories := rf.getDirectoriesFromFiles(files)
	for _, dir := range directories {
		if candidate := rf.findRecentReviewerInDirectory(ctx, pr.Owner, pr.Repository, dir); candidate != "" {
			if candidate != pr.Author && candidate != primary {
				return candidate, SecondaryDirectoryReviewer
			}
		}
	}

	// Priority 3: Most recent reviewer in project
	log.Printf("No directory reviewers found, checking project reviewers")
	if candidate := rf.findRecentReviewerInProject(ctx, pr.Owner, pr.Repository); candidate != "" {
		if candidate != pr.Author && candidate != primary {
			return candidate, SecondaryProjectReviewer
		}
	}

	return "", ""
}

// findBlameBasedAuthors finds authors from blame data with write access
func (rf *ReviewerFinder) findBlameBasedAuthors(ctx context.Context, pr *PullRequest, files []string, blameCache map[string]*BlameData, patchCache map[string]string) string {
	// Map to track author scores based on line overlap
	authorScores := make(map[string]int)
	
	for _, filename := range files {
		blameData, exists := blameCache[filename]
		if !exists {
			continue
		}
		
		// Get changed lines for this file from cache
		changedLines, err := rf.getChangedLinesFromCache(filename, patchCache)
		if err != nil {
			log.Printf("Failed to get changed lines for %s: %v", filename, err)
			continue
		}
		
		// Map PR numbers to overlap count
		prOverlap := make(map[int]int)
		prAuthors := make(map[int]string)
		
		// Find which PRs touched the changed lines
		for _, lineNum := range changedLines {
			for _, blameLine := range blameData.Lines {
				if blameLine.LineNumber == lineNum && blameLine.PRNumber > 0 && blameLine.PRNumber != pr.Number {
					prOverlap[blameLine.PRNumber]++
					if blameLine.Author != "" {
						prAuthors[blameLine.PRNumber] = blameLine.Author
					}
					break
				}
			}
		}
		
		if len(prOverlap) > 0 {
			log.Printf("File %s: Found %d PRs that touched changed lines", filename, len(prOverlap))
		}
		
		// Get top 5 PRs by overlap
		type prScore struct {
			prNumber int
			overlap  int
		}
		var topPRs []prScore
		for prNum, overlap := range prOverlap {
			topPRs = append(topPRs, prScore{prNum, overlap})
		}
		sort.Slice(topPRs, func(i, j int) bool {
			return topPRs[i].overlap > topPRs[j].overlap
		})
		
		// Consider top 5 PRs
		for i, pr := range topPRs {
			if i >= 5 {
				break
			}
			log.Printf("  Examining PR #%d (overlap: %d lines)", pr.prNumber, pr.overlap)
			if author, exists := prAuthors[pr.prNumber]; exists && author != "" {
				authorScores[author] += pr.overlap
				log.Printf("    -> Author: %s (cumulative score: %d)", author, authorScores[author])
			}
		}
	}
	
	// Find best author with write access
	type authorScore struct {
		username string
		score    int
	}
	var candidates []authorScore
	for author, score := range authorScores {
		if author != pr.Author {
			candidates = append(candidates, authorScore{author, score})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	
	// Log candidate summary
	if len(candidates) > 0 {
		log.Printf("Author candidates summary:")
		for i, c := range candidates {
			if i >= 5 {
				log.Printf("  ... and %d more", len(candidates)-5)
				break
			}
			log.Printf("  %d. %s (score: %d)", i+1, c.username, c.score)
		}
	}
	
	// Check write access for candidates in order
	for _, candidate := range candidates {
		hasAccess, association := rf.checkUserWriteAccess(ctx, pr.Owner, pr.Repository, candidate.username)
		if hasAccess {
			log.Printf("✓ Selected blame-based author: %s (score: %d, association: %s)", 
				candidate.username, candidate.score, association)
			return candidate.username
		}
		log.Printf("✗ Skipping author %s - no write access (association: %s)", candidate.username, association)
	}
	
	return ""
}

// findBlameBasedReviewers finds reviewers from blame data
func (rf *ReviewerFinder) findBlameBasedReviewers(ctx context.Context, pr *PullRequest, files []string, blameCache map[string]*BlameData, patchCache map[string]string, excludeUser string) string {
	// Map to track reviewer scores based on PR overlap
	reviewerScores := make(map[string]int)
	
	for _, filename := range files {
		blameData, exists := blameCache[filename]
		if !exists {
			continue
		}
		
		// Get changed lines for this file from cache
		changedLines, err := rf.getChangedLinesFromCache(filename, patchCache)
		if err != nil {
			log.Printf("Failed to get changed lines for %s: %v", filename, err)
			continue
		}
		
		// Map PR numbers to overlap count
		prOverlap := make(map[int]int)
		
		// Find which PRs touched the changed lines
		for _, lineNum := range changedLines {
			for _, blameLine := range blameData.Lines {
				if blameLine.LineNumber == lineNum && blameLine.PRNumber > 0 && blameLine.PRNumber != pr.Number {
					prOverlap[blameLine.PRNumber]++
					break
				}
			}
		}
		
		if len(prOverlap) > 0 {
			log.Printf("File %s: Found %d PRs that touched changed lines", filename, len(prOverlap))
		}
		
		// Get top 5 PRs by overlap
		type prScore struct {
			prNumber int
			overlap  int
		}
		var topPRs []prScore
		for prNum, overlap := range prOverlap {
			topPRs = append(topPRs, prScore{prNum, overlap})
		}
		sort.Slice(topPRs, func(i, j int) bool {
			return topPRs[i].overlap > topPRs[j].overlap
		})
		
		// Consider top 5 PRs and get their reviewers
		for i, prInfo := range topPRs {
			if i >= 5 {
				break
			}
			
			log.Printf("  Examining PR #%d (overlap: %d lines)", prInfo.prNumber, prInfo.overlap)
			
			approvers, err := rf.getPRApprovers(ctx, pr.Owner, pr.Repository, prInfo.prNumber)
			if err != nil {
				log.Printf("    -> Error getting approvers: %v", err)
				continue
			}
			
			if len(approvers) > 0 {
				log.Printf("    -> Reviewers: %v", approvers)
				for _, approver := range approvers {
					if approver != pr.Author && approver != excludeUser {
						reviewerScores[approver] += prInfo.overlap
						log.Printf("       %s (cumulative score: %d)", approver, reviewerScores[approver])
					}
				}
			} else {
				log.Printf("    -> No reviewers found")
			}
		}
	}
	
	// Find best reviewer
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
		log.Printf("Selected blame-based reviewer: %s (score: %d)", 
			candidates[0].username, candidates[0].score)
		return candidates[0].username
	}
	
	return ""
}

// Helper methods for directory and project fallbacks

func (rf *ReviewerFinder) getDirectoriesFromFiles(files []string) []string {
	dirMap := make(map[string]bool)
	var dirs []string
	
	for _, file := range files {
		dir := path.Dir(file)
		if dir != "." && !dirMap[dir] {
			dirMap[dir] = true
			dirs = append(dirs, dir)
		}
	}
	
	// Sort by depth (deeper directories first)
	sort.Slice(dirs, func(i, j int) bool {
		return strings.Count(dirs[i], "/") > strings.Count(dirs[j], "/")
	})
	
	return dirs
}

func (rf *ReviewerFinder) findRecentAuthorInDirectory(ctx context.Context, owner, repo, directory string) string {
	log.Printf("Finding recent author in directory %s using GraphQL", directory)
	prs, err := rf.getRecentPRsInDirectory(ctx, owner, repo, directory)
	if err != nil {
		log.Printf("Failed to get recent PRs in directory: %v", err)
		return ""
	}
	
	for _, pr := range prs {
		if pr.Author != "" {
			log.Printf("Found recent author %s in directory %s", pr.Author, directory)
			return pr.Author
		}
	}
	return ""
}

func (rf *ReviewerFinder) findRecentAuthorInProject(ctx context.Context, owner, repo string) string {
	authors := rf.findRecentAuthorsInProject(ctx, owner, repo, 1)
	if len(authors) > 0 {
		return authors[0]
	}
	return ""
}

func (rf *ReviewerFinder) findRecentAuthorsInProject(ctx context.Context, owner, repo string, limit int) []string {
	log.Printf("Finding recent authors in project using GraphQL")
	prs, err := rf.getRecentPRsInProject(ctx, owner, repo)
	if err != nil {
		log.Printf("Failed to get recent PRs in project: %v", err)
		return nil
	}
	
	// Track authors we've seen to avoid duplicates
	seen := make(map[string]bool)
	var authors []string
	
	for _, pr := range prs {
		if pr.Author != "" && !seen[pr.Author] {
			seen[pr.Author] = true
			log.Printf("Found recent author %s in project", pr.Author)
			authors = append(authors, pr.Author)
			if len(authors) >= limit {
				break
			}
		}
	}
	return authors
}

func (rf *ReviewerFinder) findRecentReviewerInDirectory(ctx context.Context, owner, repo, directory string) string {
	log.Printf("Finding recent reviewer in directory %s using GraphQL", directory)
	prs, err := rf.getRecentPRsInDirectory(ctx, owner, repo, directory)
	if err != nil {
		log.Printf("Failed to get recent PRs in directory: %v", err)
		return ""
	}
	
	for _, pr := range prs {
		if len(pr.Reviewers) > 0 {
			log.Printf("Found recent reviewer %s in directory %s", pr.Reviewers[0], directory)
			return pr.Reviewers[0]
		}
	}
	return ""
}

func (rf *ReviewerFinder) findRecentReviewerInProject(ctx context.Context, owner, repo string) string {
	log.Printf("Finding recent reviewer in project using GraphQL")
	prs, err := rf.getRecentPRsInProject(ctx, owner, repo)
	if err != nil {
		log.Printf("Failed to get recent PRs in project: %v", err)
		return ""
	}
	
	for _, pr := range prs {
		if len(pr.Reviewers) > 0 {
			log.Printf("Found recent reviewer %s in project", pr.Reviewers[0])
			return pr.Reviewers[0]
		}
	}
	return ""
}


func (rf *ReviewerFinder) hasWriteAccess(ctx context.Context, owner, repo, username string) bool {
	hasAccess, _ := rf.checkUserWriteAccess(ctx, owner, repo, username)
	return hasAccess
}

// assignReviewersV3 assigns the two selected reviewers
func (rf *ReviewerFinder) assignReviewersV3(ctx context.Context, pr *PullRequest, candidates []ReviewerCandidate) error {
	if len(candidates) == 0 {
		log.Printf("ERROR: No reviewer candidates found for PR %d", pr.Number)
		return fmt.Errorf("no reviewer candidates found for PR %d", pr.Number)
	}

	// Extract reviewer names
	var reviewerNames []string
	for _, candidate := range candidates {
		reviewerNames = append(reviewerNames, candidate.Username)
	}

	// Check if this is a draft PR
	if pr.Draft {
		log.Printf("PR %d is a draft - skipping reviewer assignment", pr.Number)
		log.Printf("Would have assigned reviewers %v to PR %d if it wasn't a draft", reviewerNames, pr.Number)
		return nil
	}

	// Check if this is a closed PR
	if pr.State == "closed" {
		log.Printf("PR %d is closed - skipping reviewer assignment", pr.Number)
		log.Printf("Would have assigned reviewers %v to PR %d if it wasn't closed", reviewerNames, pr.Number)
		return nil
	}

	if rf.dryRun {
		log.Printf("DRY RUN: Would add reviewers %v to PR %d", reviewerNames, pr.Number)
		return nil
	}

	// Actually add reviewers
	return rf.addReviewers(ctx, pr, reviewerNames)
}