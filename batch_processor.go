package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"
)

// RepoGroup holds PRs grouped by repository for batch processing.
type RepoGroup struct {
	Owner      string
	Repository string
	PRs        []*PullRequest
}

// processPRsBatch processes multiple PRs using batch optimization.
func (rf *ReviewerFinder) processPRsBatch(ctx context.Context, prs []*PullRequest) {
	// Filter out draft PRs early to save processing time
	var nonDraftPRs []*PullRequest
	draftCount := 0
	for _, pr := range prs {
		if pr.Draft {
			draftCount++
			continue
		}
		nonDraftPRs = append(nonDraftPRs, pr)
	}

	log.Printf("🔄 Batch processing %d PRs (%d drafts filtered out)", len(nonDraftPRs), draftCount)

	// Group PRs by repository
	repoGroups := rf.groupPRsByRepository(nonDraftPRs)
	log.Printf("📦 Grouped into %d repositories", len(repoGroups))

	// Sort repositories by PR count (process larger repos first for better cache utilization)
	sort.Slice(repoGroups, func(i, j int) bool {
		return len(repoGroups[i].PRs) > len(repoGroups[j].PRs)
	})

	// Process each repository
	totalProcessed := 0
	totalAssigned := 0
	totalSkipped := 0

	for i, group := range repoGroups {
		log.Printf("\n📂 [%d/%d] Processing repository %s/%s with %d PRs",
			i+1, len(repoGroups), group.Owner, group.Repository, len(group.PRs))

		// Pre-fetch repository-wide data for better cache utilization
		rf.prefetchRepositoryData(ctx, group.Owner, group.Repository)

		// Process all PRs in this repository
		for j, pr := range group.PRs {
			log.Printf("\n  PR %d/%d: #%d", j+1, len(group.PRs), pr.Number)

			totalProcessed++
			wasAssigned, err := rf.processPR(ctx, pr)

			switch {
			case err != nil:
				log.Printf("    ❌ Error: %v", err)
			case wasAssigned:
				totalAssigned++
			default:
				totalSkipped++
			}

			// Brief pause between PRs to avoid rate limiting
			if j < len(group.PRs)-1 {
				time.Sleep(100 * time.Millisecond)
			}
		}

		// Log repository summary
		log.Printf("  ✅ Repository %s/%s complete", group.Owner, group.Repository)
	}

	// Print overall summary
	log.Printf("\n" + rf.output.formatSummary(totalProcessed, totalAssigned, totalSkipped))
}

// groupPRsByRepository groups PRs by their repository.
func (*ReviewerFinder) groupPRsByRepository(prs []*PullRequest) []RepoGroup {
	groupMap := make(map[string]*RepoGroup)

	for _, pr := range prs {
		key := fmt.Sprintf("%s/%s", pr.Owner, pr.Repository)
		if group, exists := groupMap[key]; exists {
			group.PRs = append(group.PRs, pr)
		} else {
			groupMap[key] = &RepoGroup{
				Owner:      pr.Owner,
				Repository: pr.Repository,
				PRs:        []*PullRequest{pr},
			}
		}
	}

	// Convert map to slice
	groups := make([]RepoGroup, 0, len(groupMap))
	for _, group := range groupMap {
		// Sort PRs within each repository by number (oldest first)
		sort.Slice(group.PRs, func(i, j int) bool {
			return group.PRs[i].Number < group.PRs[j].Number
		})
		groups = append(groups, *group)
	}

	return groups
}

// prefetchRepositoryData pre-fetches and caches repository-wide data.
func (rf *ReviewerFinder) prefetchRepositoryData(ctx context.Context, owner, repo string) {
	log.Printf("  🔍 Pre-fetching repository data for %s/%s", owner, repo)

	// Start all fetches in parallel
	done := make(chan bool, 3)

	// Fetch user activity (includes bot detection)
	go func() {
		activities := rf.fetchRepoUserActivity(ctx, owner, repo)
		log.Printf("    ✓ User activity: %d users found", len(activities))
		done <- true
	}()

	// Fetch repository contributors
	go func() {
		contributors := rf.topContributors(ctx, owner, repo)
		log.Printf("    ✓ Contributors: %d top contributors identified", len(contributors))
		done <- true
	}()

	// Placeholder for future repository statistics
	go func() {
		// This could fetch repository-wide statistics in the future
		log.Print("    ✓ Repository context loaded")
		done <- true
	}()

	// Wait for all fetches to complete
	for range 3 {
		<-done
	}

	log.Print("  ✅ Repository data pre-fetched and cached")
}

// Enhanced prsForOrg that returns PRs without individual fetching.
func (rf *ReviewerFinder) prsForOrgBatched(ctx context.Context, org string) ([]*PullRequest, error) {
	log.Printf("[ORG] Fetching open PRs for organization: %s (batch mode)", org)

	// Try different batch sizes if queries fail
	batchSizes := []struct{ repos, prs int }{
		{defaultBatchSize, defaultBatchSize}, // Default optimized size
		{smallBatchSize, smallBatchSize},     // Smaller if first fails
		{minBatchSize, minBatchSize},         // Even smaller
	}

	var lastErr error
	for i, size := range batchSizes {
		if i > 0 {
			log.Printf("[RETRY] Trying smaller batch size: %d repos, %d PRs per repo", size.repos, size.prs)
		}

		prs, err := rf.prsForOrgWithBatchSize(ctx, org, size.repos, size.prs)
		if err == nil {
			return prs, nil
		}
		lastErr = err
		log.Printf("[WARN] Batch size %d/%d failed: %v", size.repos, size.prs, err)
	}

	// If all GraphQL attempts fail, fall back to REST API
	log.Printf("[WARN] All GraphQL batch attempts failed, falling back to REST API: %v", lastErr)
	return rf.prsForOrg(ctx, org)
}

func (rf *ReviewerFinder) prsForOrgWithBatchSize(ctx context.Context, org string, repoLimit, prLimit int) ([]*PullRequest, error) {
	// Determine if this is a user or organization account
	isUser := rf.client.isUserAccount(org)

	// Use different GraphQL query based on account type
	var query string
	if isUser {
		// Use 'user' query for user accounts - only fetch repositories owned by the user
		query = fmt.Sprintf(`
		query($org: String!, $cursor: String) {
			user(login: $org) {
				repositories(first: %d, after: $cursor, ownerAffiliations: OWNER, orderBy: {field: PUSHED_AT, direction: DESC}) {
					pageInfo {
						hasNextPage
						endCursor
					}
					nodes {
						name
						owner { login }
						pullRequests(states: OPEN, first: %d, orderBy: {field: UPDATED_AT, direction: DESC}) {
							nodes {
								number
								title
								author { login }
								createdAt
								updatedAt
								isDraft
								assignees(first: 3) {
									nodes { login }
								}
								reviewRequests(first: 5) {
									nodes {
										requestedReviewer {
											... on User { login }
										}
									}
								}
								commits(last: 1) {
									nodes {
										commit {
											committedDate
										}
									}
								}
							}
						}
					}
				}
			}
		}`, repoLimit, prLimit)
	} else {
		// Use 'organization' query for organization accounts
		query = fmt.Sprintf(`
		query($org: String!, $cursor: String) {
			organization(login: $org) {
				repositories(first: %d, after: $cursor, orderBy: {field: PUSHED_AT, direction: DESC}) {
					pageInfo {
						hasNextPage
						endCursor
					}
					nodes {
						name
						owner { login }
						pullRequests(states: OPEN, first: %d, orderBy: {field: UPDATED_AT, direction: DESC}) {
							nodes {
								number
								title
								author { login }
								createdAt
								updatedAt
								isDraft
								assignees(first: 3) {
									nodes { login }
								}
								reviewRequests(first: 5) {
									nodes {
										requestedReviewer {
											... on User { login }
										}
									}
								}
								commits(last: 1) {
									nodes {
										commit {
											committedDate
										}
									}
								}
							}
						}
					}
				}
			}
		}`, repoLimit, prLimit)
	}

	var allPRs []*PullRequest
	cursor := ""

	for {
		variables := map[string]any{
			"org":    org,
			"cursor": nil,
		}
		if cursor != "" {
			variables["cursor"] = cursor
		}

		result, err := rf.client.makeGraphQLRequest(ctx, query, variables)
		if err != nil {
			return nil, fmt.Errorf("GraphQL request failed: %w", err)
		}

		// Parse the GraphQL response
		prs, hasNextPage, nextCursor := rf.parseOrgPRsFromGraphQL(result)
		allPRs = append(allPRs, prs...)

		if !hasNextPage {
			break
		}
		cursor = nextCursor
	}

	log.Printf("[ORG] Found %d open PRs across organization %s", len(allPRs), org)
	return allPRs, nil
}

// parseOrgPRsFromGraphQL parses PRs from GraphQL response.
func (rf *ReviewerFinder) parseOrgPRsFromGraphQL(result map[string]any) (prs []*PullRequest, hasNextPage bool, cursor string) {
	repos := rf.extractOrgRepositories(result)
	if repos == nil {
		return prs, false, ""
	}

	hasNextPage, cursor = rf.extractPaginationInfo(repos)
	prs = rf.extractPRsFromRepos(repos)

	return prs, hasNextPage, cursor
}

// extractOrgRepositories extracts the repositories object from org or user GraphQL response.
func (*ReviewerFinder) extractOrgRepositories(result map[string]any) map[string]any {
	data, ok := result["data"].(map[string]any)
	if !ok {
		return nil
	}

	// Try organization first
	if org, ok := data["organization"].(map[string]any); ok {
		if repos, ok := org["repositories"].(map[string]any); ok {
			return repos
		}
	}

	// Try user if organization doesn't exist
	if user, ok := data["user"].(map[string]any); ok {
		if repos, ok := user["repositories"].(map[string]any); ok {
			return repos
		}
	}

	return nil
}

// extractPaginationInfo extracts pagination info from repositories response.
func (*ReviewerFinder) extractPaginationInfo(repos map[string]any) (hasNextPage bool, cursor string) {
	pageInfo, ok := repos["pageInfo"].(map[string]any)
	if !ok {
		return false, ""
	}

	if next, ok := pageInfo["hasNextPage"].(bool); ok {
		hasNextPage = next
	}

	if endCursor, ok := pageInfo["endCursor"].(string); ok {
		cursor = endCursor
	}

	return hasNextPage, cursor
}

// extractPRsFromRepos extracts PRs from repository nodes.
func (rf *ReviewerFinder) extractPRsFromRepos(repos map[string]any) []*PullRequest {
	var prs []*PullRequest

	nodes, ok := repos["nodes"].([]any)
	if !ok {
		return prs
	}

	for _, node := range nodes {
		repo, ok := node.(map[string]any)
		if !ok {
			continue
		}
		prs = append(prs, rf.parsePRsFromRepo(repo)...)
	}

	return prs
}

// parsePRsFromRepo parses PRs from a repository node.
func (rf *ReviewerFinder) parsePRsFromRepo(repo map[string]any) []*PullRequest {
	var prs []*PullRequest

	repoName := ""
	if name, ok := repo["name"].(string); ok {
		repoName = name
	}
	var ownerLogin string
	if owner, ok := repo["owner"].(map[string]any); ok {
		if login, ok := owner["login"].(string); ok {
			ownerLogin = login
		}
	}

	if pullRequests, ok := repo["pullRequests"].(map[string]any); ok {
		if nodes, ok := pullRequests["nodes"].([]any); ok {
			for _, node := range nodes {
				if prData, ok := node.(map[string]any); ok {
					pr := rf.parsePRFromGraphQL(prData, ownerLogin, repoName)
					if pr != nil {
						prs = append(prs, pr)
					}
				}
			}
		}
	}

	return prs
}

// parsePRFromGraphQL converts GraphQL PR data to PullRequest struct.
func (rf *ReviewerFinder) parsePRFromGraphQL(prData map[string]any, owner, repo string) *PullRequest {
	pr := &PullRequest{
		Owner:      owner,
		Repository: repo,
	}

	// Parse basic fields directly
	if number, ok := prData["number"].(float64); ok {
		pr.Number = int(number)
	}
	if title, ok := prData["title"].(string); ok {
		pr.Title = title
	}
	if isDraft, ok := prData["isDraft"].(bool); ok {
		pr.Draft = isDraft
	}

	// Parse author
	if author, ok := prData["author"].(map[string]any); ok {
		if login, ok := author["login"].(string); ok {
			pr.Author = login
		}
	}

	// Parse dates
	if createdAt, ok := prData["createdAt"].(string); ok {
		if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
			pr.CreatedAt = t
		}
	}
	if updatedAt, ok := prData["updatedAt"].(string); ok {
		if t, err := time.Parse(time.RFC3339, updatedAt); err == nil {
			pr.UpdatedAt = t
		}
	}

	rf.parsePRAssignees(prData, pr)
	rf.parsePRReviewers(prData, pr)
	rf.parsePRLastCommit(prData, pr)

	return pr
}

// parsePRAssignees parses the list of PR assignees.
func (*ReviewerFinder) parsePRAssignees(prData map[string]any, pr *PullRequest) {
	assignees, ok := prData["assignees"].(map[string]any)
	if !ok {
		return
	}

	nodes, ok := assignees["nodes"].([]any)
	if !ok {
		return
	}

	for _, node := range nodes {
		assignee, ok := node.(map[string]any)
		if !ok {
			continue
		}

		if login, ok := assignee["login"].(string); ok {
			pr.Assignees = append(pr.Assignees, login)
		}
	}
}

// parsePRReviewers parses the list of requested reviewers.
func (*ReviewerFinder) parsePRReviewers(prData map[string]any, pr *PullRequest) {
	reviewRequests, ok := prData["reviewRequests"].(map[string]any)
	if !ok {
		return
	}

	nodes, ok := reviewRequests["nodes"].([]any)
	if !ok {
		return
	}

	for _, node := range nodes {
		request, ok := node.(map[string]any)
		if !ok {
			continue
		}

		reviewer, ok := request["requestedReviewer"].(map[string]any)
		if !ok {
			continue
		}

		if login, ok := reviewer["login"].(string); ok {
			pr.Reviewers = append(pr.Reviewers, login)
		}
	}
}

// parsePRLastCommit parses the last commit date from the PR.
func (*ReviewerFinder) parsePRLastCommit(prData map[string]any, pr *PullRequest) {
	commits, ok := prData["commits"].(map[string]any)
	if !ok {
		return
	}

	nodes, ok := commits["nodes"].([]any)
	if !ok || len(nodes) == 0 {
		return
	}

	commit, ok := nodes[0].(map[string]any)
	if !ok {
		return
	}

	commitData, ok := commit["commit"].(map[string]any)
	if !ok {
		return
	}

	if committedDate, ok := commitData["committedDate"].(string); ok {
		if t, err := time.Parse(time.RFC3339, committedDate); err == nil {
			pr.LastCommit = t
		}
	}
}
