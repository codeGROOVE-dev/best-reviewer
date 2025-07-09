package main

import (
	"context"
	"fmt"
	"log"
	"path"
	"sort"
	"strings"
	"time"
)

// findReviewerCandidatesV2 implements the exact reviewer finding logic as specified
func (rf *ReviewerFinder) findReviewerCandidatesV2(ctx context.Context, pr *PullRequest) ([]ReviewerCandidate, error) {
	// Get top 3 files with most changes
	topFiles := rf.getTopChangedFiles(pr, 3)
	log.Printf("Top changed files for PR %d: %v", pr.Number, topFiles)

	// Pre-fetch blame data for all top files to avoid redundant API calls
	blameCache := make(map[string]*BlameData)
	for _, filename := range topFiles {
		blameData, err := rf.getBlameData(ctx, pr.Owner, pr.Repository, filename, pr.Number)
		if err != nil {
			log.Printf("Failed to get blame data for %s: %v", filename, err)
			continue
		}
		blameCache[filename] = blameData
	}

	var allCandidates []ReviewerCandidate

	// PRIMARY METHOD 1: Context-based reviewers (from blame data)
	contextCandidates := rf.findContextReviewersV2WithCache(ctx, pr, topFiles, blameCache)
	allCandidates = append(allCandidates, contextCandidates...)

	// PRIMARY METHOD 2: Activity-based reviewers (most recent PR approvers)
	activityCandidates := rf.findActivityReviewersV2WithCache(ctx, pr, topFiles, blameCache)
	allCandidates = append(allCandidates, activityCandidates...)

	// FALLBACK 1: Line authors with write access
	if len(allCandidates) < 2 {
		log.Printf("Found only %d candidates, trying fallback to line authors", len(allCandidates))
		lineAuthorCandidates := rf.findLineAuthorsWithWriteAccessWithCache(ctx, pr, topFiles, blameCache)
		allCandidates = append(allCandidates, lineAuthorCandidates...)
	}

	// FALLBACK 2: Recent file authors with write access
	if len(allCandidates) < 2 {
		log.Printf("Found only %d candidates, trying fallback to recent file authors", len(allCandidates))
		fileAuthorCandidates := rf.findRecentFileAuthorsWithWriteAccess(ctx, pr, topFiles)
		allCandidates = append(allCandidates, fileAuthorCandidates...)
	}

	// FALLBACK 3: Directory-based fallbacks
	if len(allCandidates) < 2 {
		log.Printf("Found only %d candidates, trying directory-based fallbacks", len(allCandidates))
		directoryCandidates := rf.findDirectoryBasedReviewers(ctx, pr, topFiles)
		allCandidates = append(allCandidates, directoryCandidates...)
	}

	// FALLBACK 4: Project-wide fallbacks
	if len(allCandidates) < 2 {
		log.Printf("Found only %d candidates, trying project-wide fallbacks", len(allCandidates))
		projectCandidates := rf.findProjectWideReviewers(ctx, pr)
		allCandidates = append(allCandidates, projectCandidates...)
	}

	// Deduplicate and exclude PR author
	candidates := rf.deduplicateCandidates(allCandidates, pr.Author)

	// Check if we only found the PR author
	if len(allCandidates) > 0 && len(candidates) == 0 {
		return nil, fmt.Errorf("exhausted all reviewer candidates: the only candidate found was the PR author (%s)", pr.Author)
	}

	// Sort by score
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

// findContextReviewersV2WithCache finds reviewers based on blame data using cache
func (rf *ReviewerFinder) findContextReviewersV2WithCache(ctx context.Context, pr *PullRequest, files []string, blameCache map[string]*BlameData) []ReviewerCandidate {
	log.Printf("Finding context reviewers using blame data for %d files", len(files))
	
	candidateScores := make(map[string]int)
	candidateInfo := make(map[string]*ReviewerCandidate)
	
	for _, filename := range files {
		log.Printf("Analyzing blame data for file: %s", filename)
		
		// Get blame data from cache
		blameData, exists := blameCache[filename]
		if !exists {
			log.Printf("No blame data in cache for %s", filename)
			continue
		}
		
		// Get changed lines for this file
		changedLines, err := rf.getChangedLines(ctx, pr.Owner, pr.Repository, pr.Number, filename)
		if err != nil {
			log.Printf("Failed to get changed lines for %s: %v", filename, err)
			continue
		}
		
		// Find top 3 PRs that last changed these lines
		prLineCount := make(map[int]int)
		for _, lineNum := range changedLines {
			for _, blameLine := range blameData.Lines {
				if blameLine.LineNumber == lineNum && blameLine.PRNumber > 0 {
					prLineCount[blameLine.PRNumber]++
					break
				}
			}
		}
		
		// Get top 3 PRs by line count
		type prLines struct {
			prNumber int
			lines    int
		}
		var topPRs []prLines
		for pr, lines := range prLineCount {
			topPRs = append(topPRs, prLines{pr, lines})
		}
		sort.Slice(topPRs, func(i, j int) bool {
			return topPRs[i].lines > topPRs[j].lines
		})
		if len(topPRs) > 3 {
			topPRs = topPRs[:3]
		}
		
		// Get approvers for these PRs
		for _, prInfo := range topPRs {
			approvers, err := rf.getPRApprovers(ctx, pr.Owner, pr.Repository, prInfo.prNumber)
			if err != nil {
				log.Printf("Failed to get approvers for PR %d: %v", prInfo.prNumber, err)
				continue
			}
			
			for _, approver := range approvers {
				candidateScores[approver] += prInfo.lines
				if _, exists := candidateInfo[approver]; !exists {
					candidateInfo[approver] = &ReviewerCandidate{
						Username:        approver,
						ContextScore:    0,
						SelectionMethod: "context-blame-approver",
						LastActivity:    time.Now().Add(-24 * time.Hour), // Default
					}
				}
			}
		}
	}
	
	// Convert to candidates list
	var candidates []ReviewerCandidate
	for username, score := range candidateScores {
		candidate := *candidateInfo[username]
		candidate.ContextScore = score
		candidates = append(candidates, candidate)
		log.Printf("Context reviewer candidate: %s (score: %d, method: %s)", 
			username, score, candidate.SelectionMethod)
	}
	
	return candidates
}

// findContextReviewersV2 finds reviewers based on blame data
func (rf *ReviewerFinder) findContextReviewersV2(ctx context.Context, pr *PullRequest, files []string) []ReviewerCandidate {
	log.Printf("Finding context reviewers using blame data for %d files", len(files))
	
	candidateScores := make(map[string]int)
	candidateInfo := make(map[string]*ReviewerCandidate)
	
	for _, filename := range files {
		log.Printf("Analyzing blame data for file: %s", filename)
		
		// Get blame data for the file
		blameData, err := rf.getBlameData(ctx, pr.Owner, pr.Repository, filename, pr.Number)
		if err != nil {
			log.Printf("Failed to get blame data for %s: %v", filename, err)
			continue
		}
		
		// Get changed lines for this file
		changedLines, err := rf.getChangedLines(ctx, pr.Owner, pr.Repository, pr.Number, filename)
		if err != nil {
			log.Printf("Failed to get changed lines for %s: %v", filename, err)
			continue
		}
		
		// Find top 3 PRs that last changed these lines
		prLineCount := make(map[int]int)
		for _, lineNum := range changedLines {
			for _, blameLine := range blameData.Lines {
				if blameLine.LineNumber == lineNum && blameLine.PRNumber > 0 {
					prLineCount[blameLine.PRNumber]++
					break
				}
			}
		}
		
		// Get top 3 PRs by line count
		type prLines struct {
			prNumber int
			lines    int
		}
		var topPRs []prLines
		for pr, lines := range prLineCount {
			topPRs = append(topPRs, prLines{pr, lines})
		}
		sort.Slice(topPRs, func(i, j int) bool {
			return topPRs[i].lines > topPRs[j].lines
		})
		if len(topPRs) > 3 {
			topPRs = topPRs[:3]
		}
		
		// Get approvers for these PRs
		for _, prInfo := range topPRs {
			approvers, err := rf.getPRApprovers(ctx, pr.Owner, pr.Repository, prInfo.prNumber)
			if err != nil {
				log.Printf("Failed to get approvers for PR %d: %v", prInfo.prNumber, err)
				continue
			}
			
			for _, approver := range approvers {
				candidateScores[approver] += prInfo.lines
				if _, exists := candidateInfo[approver]; !exists {
					candidateInfo[approver] = &ReviewerCandidate{
						Username:        approver,
						ContextScore:    0,
						SelectionMethod: "context-blame-approver",
						LastActivity:    time.Now().Add(-24 * time.Hour), // Default
					}
				}
			}
		}
	}
	
	// Convert to candidates list
	var candidates []ReviewerCandidate
	for username, score := range candidateScores {
		candidate := *candidateInfo[username]
		candidate.ContextScore = score
		candidates = append(candidates, candidate)
		log.Printf("Context reviewer candidate: %s (score: %d, method: %s)", 
			username, score, candidate.SelectionMethod)
	}
	
	return candidates
}

// findActivityReviewersV2WithCache finds the most recent PR approvers for files using cached blame data
func (rf *ReviewerFinder) findActivityReviewersV2WithCache(ctx context.Context, pr *PullRequest, files []string, blameCache map[string]*BlameData) []ReviewerCandidate {
	log.Printf("Finding activity reviewers for %d files", len(files))
	
	candidateMap := make(map[string]*ReviewerCandidate)
	
	for _, filename := range files {
		log.Printf("Finding recent PR approvers for file: %s", filename)
		
		// Get blame data from cache
		blameData, exists := blameCache[filename]
		if !exists {
			log.Printf("No blame data in cache for %s", filename)
			continue
		}
		
		// Find unique PR numbers from blame data
		prNumbers := make(map[int]bool)
		for _, blameLine := range blameData.Lines {
			if blameLine.PRNumber > 0 && blameLine.PRNumber != pr.Number {
				prNumbers[blameLine.PRNumber] = true
			}
		}
		
		// Convert to slice and sort by PR number (descending - most recent first)
		var sortedPRs []int
		for prNum := range prNumbers {
			sortedPRs = append(sortedPRs, prNum)
		}
		sort.Slice(sortedPRs, func(i, j int) bool {
			return sortedPRs[i] > sortedPRs[j]
		})
		
		// Look at the most recent PRs (up to 5) to find one with approvers
		checked := 0
		for _, prNum := range sortedPRs {
			if checked >= 5 {
				break
			}
			checked++
			
			// Get approvers for this PR
			approvers, err := rf.getPRApprovers(ctx, pr.Owner, pr.Repository, prNum)
			if err != nil {
				log.Printf("Failed to get approvers for PR %d: %v", prNum, err)
				continue
			}
			
			if len(approvers) > 0 {
				// Get PR details for size
				prDetails, err := rf.client.getPullRequest(ctx, pr.Owner, pr.Repository, prNum)
				if err != nil {
					log.Printf("Failed to get PR details for %d: %v", prNum, err)
					continue
				}
				
				// Calculate PR size
				totalChanges := 0
				for _, file := range prDetails.ChangedFiles {
					totalChanges += file.Changes
				}
				
				// Add approvers as candidates
				for _, approver := range approvers {
					if existing, exists := candidateMap[approver]; exists {
						// Update if this PR is larger
						if totalChanges > existing.ActivityScore {
							existing.ActivityScore = totalChanges
							existing.LastActivity = prDetails.UpdatedAt
						}
					} else {
						candidateMap[approver] = &ReviewerCandidate{
							Username:        approver,
							ActivityScore:   totalChanges,
							SelectionMethod: "activity-recent-approver",
							LastActivity:    prDetails.UpdatedAt,
						}
					}
				}
				
				log.Printf("Found approvers for PR %d (size: %d): %v", prNum, totalChanges, approvers)
				break // Found approvers for this file, move to next file
			}
		}
	}
	
	// Convert map to slice
	var candidates []ReviewerCandidate
	for _, candidate := range candidateMap {
		candidates = append(candidates, *candidate)
		log.Printf("Activity reviewer candidate: %s (PR size: %d, method: %s)", 
			candidate.Username, candidate.ActivityScore, candidate.SelectionMethod)
	}
	
	// Sort by PR size (activity score)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ActivityScore > candidates[j].ActivityScore
	})
	
	return candidates
}

// findActivityReviewersV2 finds the most recent PR approvers for files
func (rf *ReviewerFinder) findActivityReviewersV2(ctx context.Context, pr *PullRequest, files []string) []ReviewerCandidate {
	log.Printf("Finding activity reviewers for %d files", len(files))
	
	candidateMap := make(map[string]*ReviewerCandidate)
	
	for _, filename := range files {
		log.Printf("Finding recent PR approvers for file: %s", filename)
		
		// Get blame data for the file (we may already have this from context search)
		blameData, err := rf.getBlameData(ctx, pr.Owner, pr.Repository, filename, pr.Number)
		if err != nil {
			log.Printf("Failed to get blame data for %s: %v", filename, err)
			continue
		}
		
		// Find unique PR numbers from blame data
		prNumbers := make(map[int]bool)
		for _, blameLine := range blameData.Lines {
			if blameLine.PRNumber > 0 && blameLine.PRNumber != pr.Number {
				prNumbers[blameLine.PRNumber] = true
			}
		}
		
		// Convert to slice and sort by PR number (descending - most recent first)
		var sortedPRs []int
		for prNum := range prNumbers {
			sortedPRs = append(sortedPRs, prNum)
		}
		sort.Slice(sortedPRs, func(i, j int) bool {
			return sortedPRs[i] > sortedPRs[j]
		})
		
		// Look at the most recent PRs (up to 5) to find one with approvers
		checked := 0
		for _, prNum := range sortedPRs {
			if checked >= 5 {
				break
			}
			checked++
			
			// Get approvers for this PR
			approvers, err := rf.getPRApprovers(ctx, pr.Owner, pr.Repository, prNum)
			if err != nil {
				log.Printf("Failed to get approvers for PR %d: %v", prNum, err)
				continue
			}
			
			if len(approvers) > 0 {
				// Get PR details for size
				prDetails, err := rf.client.getPullRequest(ctx, pr.Owner, pr.Repository, prNum)
				if err != nil {
					log.Printf("Failed to get PR details for %d: %v", prNum, err)
					continue
				}
				
				// Calculate PR size
				totalChanges := 0
				for _, file := range prDetails.ChangedFiles {
					totalChanges += file.Changes
				}
				
				// Add approvers as candidates
				for _, approver := range approvers {
					if existing, exists := candidateMap[approver]; exists {
						// Update if this PR is larger
						if totalChanges > existing.ActivityScore {
							existing.ActivityScore = totalChanges
							existing.LastActivity = prDetails.UpdatedAt
						}
					} else {
						candidateMap[approver] = &ReviewerCandidate{
							Username:        approver,
							ActivityScore:   totalChanges,
							SelectionMethod: "activity-recent-approver",
							LastActivity:    prDetails.UpdatedAt,
						}
					}
				}
				
				log.Printf("Found approvers for PR %d (size: %d): %v", prNum, totalChanges, approvers)
				break // Found approvers for this file, move to next file
			}
		}
	}
	
	// Convert map to slice
	var candidates []ReviewerCandidate
	for _, candidate := range candidateMap {
		candidates = append(candidates, *candidate)
		log.Printf("Activity reviewer candidate: %s (PR size: %d, method: %s)", 
			candidate.Username, candidate.ActivityScore, candidate.SelectionMethod)
	}
	
	// Sort by PR size (activity score)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ActivityScore > candidates[j].ActivityScore
	})
	
	return candidates
}

// findLineAuthorsWithWriteAccessWithCache finds authors of changed lines who have write access using cached blame data
func (rf *ReviewerFinder) findLineAuthorsWithWriteAccessWithCache(ctx context.Context, pr *PullRequest, files []string, blameCache map[string]*BlameData) []ReviewerCandidate {
	log.Printf("Finding line authors with write access for fallback")
	
	authorLineCount := make(map[string]int)
	
	for _, filename := range files {
		// Get blame data from cache
		blameData, exists := blameCache[filename]
		if !exists {
			log.Printf("No blame data in cache for %s", filename)
			continue
		}
		
		changedLines, err := rf.getChangedLines(ctx, pr.Owner, pr.Repository, pr.Number, filename)
		if err != nil {
			log.Printf("Failed to get changed lines for %s: %v", filename, err)
			continue
		}
		
		// Count lines per author
		for _, lineNum := range changedLines {
			for _, blameLine := range blameData.Lines {
				if blameLine.LineNumber == lineNum && blameLine.Author != "" {
					authorLineCount[blameLine.Author]++
					break
				}
			}
		}
	}
	
	// Get top author and check write access
	var topAuthor string
	maxLines := 0
	for author, lines := range authorLineCount {
		if lines > maxLines && author != pr.Author {
			maxLines = lines
			topAuthor = author
		}
	}
	
	if topAuthor == "" {
		return nil
	}
	
	// Check if author has write access
	hasAccess, association := rf.checkUserWriteAccess(ctx, pr.Owner, pr.Repository, topAuthor)
	if !hasAccess {
		log.Printf("Top author %s does not have write access (association: %s)", topAuthor, association)
		return nil
	}
	
	candidate := ReviewerCandidate{
		Username:          topAuthor,
		ContextScore:      maxLines,
		SelectionMethod:   "fallback-line-author",
		AuthorAssociation: association,
		LastActivity:      time.Now().Add(-48 * time.Hour),
	}
	
	log.Printf("Line author fallback candidate: %s (lines: %d, association: %s, method: %s)", 
		topAuthor, maxLines, association, candidate.SelectionMethod)
	
	return []ReviewerCandidate{candidate}
}

// findLineAuthorsWithWriteAccess finds authors of changed lines who have write access
func (rf *ReviewerFinder) findLineAuthorsWithWriteAccess(ctx context.Context, pr *PullRequest, files []string) []ReviewerCandidate {
	log.Printf("Finding line authors with write access for fallback")
	
	authorLineCount := make(map[string]int)
	
	for _, filename := range files {
		blameData, err := rf.getBlameData(ctx, pr.Owner, pr.Repository, filename, pr.Number)
		if err != nil {
			log.Printf("Failed to get blame data for %s: %v", filename, err)
			continue
		}
		
		changedLines, err := rf.getChangedLines(ctx, pr.Owner, pr.Repository, pr.Number, filename)
		if err != nil {
			log.Printf("Failed to get changed lines for %s: %v", filename, err)
			continue
		}
		
		// Count lines per author
		for _, lineNum := range changedLines {
			for _, blameLine := range blameData.Lines {
				if blameLine.LineNumber == lineNum && blameLine.Author != "" {
					authorLineCount[blameLine.Author]++
					break
				}
			}
		}
	}
	
	// Get top author and check write access
	var topAuthor string
	maxLines := 0
	for author, lines := range authorLineCount {
		if lines > maxLines && author != pr.Author {
			maxLines = lines
			topAuthor = author
		}
	}
	
	if topAuthor == "" {
		return nil
	}
	
	// Check if author has write access
	hasAccess, association := rf.checkUserWriteAccess(ctx, pr.Owner, pr.Repository, topAuthor)
	if !hasAccess {
		log.Printf("Top author %s does not have write access (association: %s)", topAuthor, association)
		return nil
	}
	
	candidate := ReviewerCandidate{
		Username:          topAuthor,
		ContextScore:      maxLines,
		SelectionMethod:   "fallback-line-author",
		AuthorAssociation: association,
		LastActivity:      time.Now().Add(-48 * time.Hour),
	}
	
	log.Printf("Line author fallback candidate: %s (lines: %d, association: %s, method: %s)", 
		topAuthor, maxLines, association, candidate.SelectionMethod)
	
	return []ReviewerCandidate{candidate}
}

// findRecentFileAuthorsWithWriteAccess finds recent authors of files with write access
func (rf *ReviewerFinder) findRecentFileAuthorsWithWriteAccess(ctx context.Context, pr *PullRequest, files []string) []ReviewerCandidate {
	log.Printf("Finding recent file authors with write access for fallback")
	
	var candidates []ReviewerCandidate
	
	for _, filename := range files {
		author, err := rf.getRecentPRAuthorForFile(ctx, pr.Owner, pr.Repository, filename)
		if err != nil || author == "" || author == pr.Author {
			continue
		}
		
		// Check write access
		hasAccess, association := rf.checkUserWriteAccess(ctx, pr.Owner, pr.Repository, author)
		if !hasAccess {
			continue
		}
		
		candidate := ReviewerCandidate{
			Username:          author,
			ContextScore:      3,
			SelectionMethod:   "fallback-file-author",
			AuthorAssociation: association,
			LastActivity:      time.Now().Add(-72 * time.Hour),
		}
		
		candidates = append(candidates, candidate)
		log.Printf("File author fallback candidate: %s (association: %s, method: %s)", 
			author, association, candidate.SelectionMethod)
	}
	
	return candidates
}

// findDirectoryBasedReviewers finds reviewers based on directory activity
func (rf *ReviewerFinder) findDirectoryBasedReviewers(ctx context.Context, pr *PullRequest, files []string) []ReviewerCandidate {
	log.Printf("Finding directory-based reviewers for fallback")
	
	// Get unique directories from files
	directories := make(map[string]bool)
	for _, file := range files {
		dir := path.Dir(file)
		if dir != "." {
			directories[dir] = true
		}
	}
	
	var candidates []ReviewerCandidate
	
	for dir := range directories {
		// Try to find reviewer of most recent PR to this directory
		dirReviewer, err := rf.getRecentPRReviewerForDirectory(ctx, pr.Owner, pr.Repository, dir)
		if err == nil && dirReviewer != "" && dirReviewer != pr.Author {
			candidate := ReviewerCandidate{
				Username:        dirReviewer,
				ActivityScore:   2,
				SelectionMethod: "fallback-directory-reviewer",
				LastActivity:    time.Now().Add(-96 * time.Hour),
			}
			candidates = append(candidates, candidate)
			log.Printf("Directory reviewer fallback candidate: %s (directory: %s, method: %s)", 
				dirReviewer, dir, candidate.SelectionMethod)
		}
		
		// Try to find author of most recent PR to this directory
		if len(candidates) < 2 {
			dirAuthor, err := rf.getRecentPRAuthorForDirectory(ctx, pr.Owner, pr.Repository, dir)
			if err == nil && dirAuthor != "" && dirAuthor != pr.Author {
				candidate := ReviewerCandidate{
					Username:        dirAuthor,
					ContextScore:    1,
					SelectionMethod: "fallback-directory-author",
					LastActivity:    time.Now().Add(-120 * time.Hour),
				}
				candidates = append(candidates, candidate)
				log.Printf("Directory author fallback candidate: %s (directory: %s, method: %s)", 
					dirAuthor, dir, candidate.SelectionMethod)
			}
		}
	}
	
	return candidates
}

// findProjectWideReviewers finds reviewers based on project-wide activity
func (rf *ReviewerFinder) findProjectWideReviewers(ctx context.Context, pr *PullRequest) []ReviewerCandidate {
	log.Printf("Finding project-wide reviewers for fallback")
	
	var candidates []ReviewerCandidate
	
	// Get most recent PR reviewer in the project
	projectReviewer, err := rf.getRecentPRApproverForRepo(ctx, pr.Owner, pr.Repository)
	if err == nil && projectReviewer != "" && projectReviewer != pr.Author {
		candidate := ReviewerCandidate{
			Username:        projectReviewer,
			ActivityScore:   1,
			SelectionMethod: "fallback-project-reviewer",
			LastActivity:    time.Now().Add(-168 * time.Hour),
		}
		candidates = append(candidates, candidate)
		log.Printf("Project reviewer fallback candidate: %s (method: %s)", 
			projectReviewer, candidate.SelectionMethod)
	}
	
	// Get most recent PR author in the project
	if len(candidates) < 2 {
		projectAuthor, err := rf.getRecentPRAuthorForRepo(ctx, pr.Owner, pr.Repository)
		if err == nil && projectAuthor != "" && projectAuthor != pr.Author {
			candidate := ReviewerCandidate{
				Username:        projectAuthor,
				ContextScore:    1,
				SelectionMethod: "fallback-project-author",
				LastActivity:    time.Now().Add(-240 * time.Hour),
			}
			candidates = append(candidates, candidate)
			log.Printf("Project author fallback candidate: %s (method: %s)", 
				projectAuthor, candidate.SelectionMethod)
		}
	}
	
	return candidates
}

// deduplicateCandidates removes duplicates and excludes the PR author
func (rf *ReviewerFinder) deduplicateCandidates(candidates []ReviewerCandidate, prAuthor string) []ReviewerCandidate {
	seen := make(map[string]bool)
	var result []ReviewerCandidate
	
	for _, candidate := range candidates {
		if candidate.Username == prAuthor {
			continue
		}
		if seen[candidate.Username] {
			continue
		}
		seen[candidate.Username] = true
		result = append(result, candidate)
	}
	
	return result
}

// assignReviewersV2 assigns exactly 2 reviewers to a PR
func (rf *ReviewerFinder) assignReviewersV2(ctx context.Context, pr *PullRequest, candidates []ReviewerCandidate) error {
	if len(candidates) == 0 {
		log.Printf("ERROR: No reviewer candidates found for PR %d - unable to find any suitable reviewers", pr.Number)
		return fmt.Errorf("no reviewer candidates found for PR %d", pr.Number)
	}

	// Check existing reviewers
	existingReviewers := make(map[string]bool)
	for _, reviewer := range pr.Reviewers {
		existingReviewers[reviewer] = true
	}

	// Select up to 2 reviewers, preferring one context and one activity reviewer
	var selectedReviewers []ReviewerCandidate
	var hasContext, hasActivity bool

	for _, candidate := range candidates {
		if len(selectedReviewers) >= 2 {
			break
		}

		// Skip if already a reviewer
		if existingReviewers[candidate.Username] {
			continue
		}

		// Try to get one of each type
		if strings.Contains(candidate.SelectionMethod, "context") && !hasContext {
			selectedReviewers = append(selectedReviewers, candidate)
			hasContext = true
		} else if strings.Contains(candidate.SelectionMethod, "activity") && !hasActivity {
			selectedReviewers = append(selectedReviewers, candidate)
			hasActivity = true
		} else if len(selectedReviewers) < 2 {
			selectedReviewers = append(selectedReviewers, candidate)
		}
	}

	// Ensure we have at least 2 reviewers
	for i, candidate := range candidates {
		if len(selectedReviewers) >= 2 {
			break
		}
		// Add any remaining candidates
		alreadySelected := false
		for _, selected := range selectedReviewers {
			if selected.Username == candidate.Username {
				alreadySelected = true
				break
			}
		}
		if !alreadySelected && !existingReviewers[candidate.Username] {
			selectedReviewers = append(selectedReviewers, candidates[i])
		}
	}

	if len(selectedReviewers) == 0 {
		log.Printf("No new reviewers to add for PR %d", pr.Number)
		return nil
	}

	// Warn if we couldn't find 2 reviewers
	if len(selectedReviewers) < 2 {
		log.Printf("WARNING: Only found %d reviewer(s) for PR %d (target is 2)", len(selectedReviewers), pr.Number)
	}

	// Log selected reviewers
	var reviewerNames []string
	for _, reviewer := range selectedReviewers {
		reviewerNames = append(reviewerNames, reviewer.Username)
		log.Printf("Selected reviewer: %s (method: %s, context score: %d, activity score: %d)", 
			reviewer.Username, reviewer.SelectionMethod, reviewer.ContextScore, reviewer.ActivityScore)
	}

	// Check if this is a draft PR
	if pr.Draft {
		log.Printf("PR %d is a draft - skipping reviewer assignment", pr.Number)
		log.Printf("Would have assigned reviewers %v to PR %d if it wasn't a draft", reviewerNames, pr.Number)
		return nil
	}

	if rf.dryRun {
		log.Printf("DRY RUN: Would add reviewers %v to PR %d", reviewerNames, pr.Number)
		return nil
	}

	// Actually add reviewers
	return rf.addReviewers(ctx, pr, reviewerNames)
}