package reviewer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// blameForLines uses GitHub's blame API to find who last touched specific lines in a file.
// Returns two lists: overlapping PRs (touched exact lines), and file PRs (touched file within last year).
func (f *Finder) blameForLines(ctx context.Context, owner, repo, filepath string, lineRanges [][2]int) ([]types.PRInfo, []types.PRInfo, error) {
	slog.InfoContext(ctx, "Using blame API to find line authors", "file", filepath, "line_ranges", len(lineRanges))

	if len(lineRanges) == 0 {
		return nil, nil, nil
	}

	// GitHub blame API gives us commits - we need to map commits to PRs
	query := `
	query($owner: String!, $repo: String!, $path: String!) {
		repository(owner: $owner, name: $repo) {
			defaultBranchRef {
				target {
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
											merged
											mergedAt
											author {
												login
											}
											mergedBy {
												login
											}
											reviews(first: 10, states: APPROVED) {
												nodes {
													author {
														login
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
	}`

	variables := map[string]any{
		"owner": owner,
		"repo":  repo,
		"path":  filepath,
	}

	result, err := f.client.MakeGraphQLRequest(ctx, query, variables)
	if err != nil {
		return nil, nil, fmt.Errorf("GraphQL blame request failed: %w", err)
	}

	// Check for GraphQL errors in response
	if gqlErrors, ok := result["errors"]; ok {
		slog.WarnContext(ctx, "GraphQL blame query returned errors", "errors", gqlErrors)
	}

	// Parse blame results - get both overlapping and all PRs
	overlappingPRs, filePRs := f.parseBlameResults(result, lineRanges)
	slog.InfoContext(ctx, "Blame API found PRs", "file", filepath, "overlapping_count", len(overlappingPRs), "file_contributors_count", len(filePRs))

	return overlappingPRs, filePRs, nil
}

// parseBlameResults extracts PR info from blame GraphQL response for specific line ranges.
// Returns two lists: PRs that overlap with changed lines, and all PRs in the file.
func (f *Finder) parseBlameResults(result map[string]any, lineRanges [][2]int) ([]types.PRInfo, []types.PRInfo) {
	var overlappingPRs []types.PRInfo
	var allPRs []types.PRInfo
	seenOverlapping := make(map[int]bool)
	seenAll := make(map[int]bool)
	seenOverlappingCommits := make(map[string]bool)
	seenAllCommits := make(map[string]bool)

	data, ok := mapValue(result, "data")
	if !ok {
		slog.Debug("No data field in blame response")
		return overlappingPRs, allPRs
	}

	repository, ok := mapValue(data, "repository")
	if !ok {
		slog.Debug("No repository field in blame response")
		return overlappingPRs, allPRs
	}

	defaultBranchRef, ok := mapValue(repository, "defaultBranchRef")
	if !ok {
		slog.Debug("No defaultBranchRef field in blame response")
		return overlappingPRs, allPRs
	}

	target, ok := mapValue(defaultBranchRef, "target")
	if !ok {
		slog.Debug("No target field in blame response")
		return overlappingPRs, allPRs
	}

	blame, ok := mapValue(target, "blame")
	if !ok {
		slog.Debug("No blame field in blame response")
		return overlappingPRs, allPRs
	}

	ranges, ok := blame["ranges"].([]any)
	if !ok {
		slog.Debug("No ranges field in blame response")
		return overlappingPRs, allPRs
	}

	slog.Debug("Parsing blame ranges", "range_count", len(ranges), "looking_for_lines", lineRanges)
	oneYearAgo := time.Now().AddDate(-1, 0, 0)

	slog.Debug("Processing blame ranges", "total_ranges", len(ranges), "changed_line_ranges", lineRanges)
	for _, r := range ranges {
		rangeMap, ok := r.(map[string]any)
		if !ok {
			continue
		}

		startLine, ok := rangeMap["startingLine"].(float64)
		if !ok {
			continue
		}
		endLine, ok := rangeMap["endingLine"].(float64)
		if !ok {
			continue
		}

		// Check if this blame range overlaps with any of our changed line ranges
		overlaps := false
		for _, lineRange := range lineRanges {
			if int(startLine) <= lineRange[1] && int(endLine) >= lineRange[0] {
				overlaps = true
				break
			}
		}

		commit, ok := mapValue(rangeMap, "commit")
		if !ok {
			continue
		}

		// Extract commit author (fallback for direct commits without PRs)
		var commitAuthor string
		if author, ok := mapValue(commit, "author"); ok {
			if user, ok := mapValue(author, "user"); ok {
				if login, ok := user["login"].(string); ok {
					commitAuthor = login
				}
			}
		}

		associatedPRs, ok := mapValue(commit, "associatedPullRequests")
		if !ok {
			continue
		}

		nodes, ok := associatedPRs["nodes"].([]any)
		if !ok || len(nodes) == 0 {
			// No PR associated - use commit author directly if available
			if commitAuthor == "" {
				slog.Debug("Skipping commit with no PR and no author")
				continue
			}

			// Create a pseudo-PR entry for this direct commit
			pr := types.PRInfo{
				Number:    0, // Use 0 to indicate direct commit
				Author:    commitAuthor,
				LineCount: int(endLine) - int(startLine) + 1,
			}

			if overlaps {
				// Overlapping lines - always include
				if !seenOverlappingCommits[commitAuthor] {
					seenOverlappingCommits[commitAuthor] = true
					overlappingPRs = append(overlappingPRs, pr)
					slog.Debug("Added overlapping commit author", "author", commitAuthor, "line_count", pr.LineCount)
				}
			} else {
				// Non-overlapping file contributor from direct commit
				if !seenAllCommits[commitAuthor] {
					seenAllCommits[commitAuthor] = true
					allPRs = append(allPRs, pr)
					slog.Debug("Added non-overlapping commit author", "author", commitAuthor)
				}
			}
			continue
		}

		// Take the first associated PR
		prNode, ok := nodes[0].(map[string]any)
		if !ok {
			continue
		}

		prNumber, ok := prNode["number"].(float64)
		if !ok {
			continue
		}
		merged, ok := prNode["merged"].(bool)
		if !ok {
			continue
		}
		if !merged {
			continue // Only consider merged PRs
		}

		// Extract mergedAt for recency check
		var mergedAt time.Time
		if mergedAtStr, ok := prNode["mergedAt"].(string); ok {
			if t, err := time.Parse(time.RFC3339, mergedAtStr); err == nil {
				mergedAt = t
			}
		}

		// Build PR info
		pr := types.PRInfo{
			Number:    int(prNumber),
			Merged:    merged,
			MergedAt:  mergedAt,
			LineCount: int(endLine) - int(startLine) + 1,
		}

		// Extract author
		if author, ok := mapValue(prNode, "author"); ok {
			if login, ok := author["login"].(string); ok {
				pr.Author = login
			}
		}

		// Extract mergedBy
		if mergedBy, ok := mapValue(prNode, "mergedBy"); ok {
			if login, ok := mergedBy["login"].(string); ok {
				pr.MergedBy = login
			}
		}

		// Extract reviewers
		if reviews, ok := mapValue(prNode, "reviews"); ok {
			if nodes, ok := reviews["nodes"].([]any); ok {
				for _, node := range nodes {
					if reviewNode, ok := node.(map[string]any); ok {
						if author, ok := mapValue(reviewNode, "author"); ok {
							if login, ok := author["login"].(string); ok {
								pr.Reviewers = append(pr.Reviewers, login)
							}
						}
					}
				}
			}
		}

		// Add to appropriate list(s)
		if overlaps {
			// Overlapping lines - always include with full weight
			if !seenOverlapping[int(prNumber)] {
				seenOverlapping[int(prNumber)] = true
				overlappingPRs = append(overlappingPRs, pr)
				slog.Debug("Added overlapping PR", "pr_number", int(prNumber), "author", pr.Author, "mergedBy", pr.MergedBy, "reviewers", pr.Reviewers, "line_count", pr.LineCount)
			}
		} else if mergedAt.IsZero() {
			// Non-overlapping - skip if no mergedAt
			slog.Debug("Skipping non-overlapping PR (no mergedAt)", "pr_number", int(prNumber), "author", pr.Author)
		} else if !mergedAt.After(oneYearAgo) {
			// Non-overlapping - skip if too old
			slog.Debug("Skipping non-overlapping PR (too old)", "pr_number", int(prNumber), "author", pr.Author, "merged_at", mergedAt)
		} else if !seenAll[int(prNumber)] {
			// Non-overlapping - include if within last year and not seen
			seenAll[int(prNumber)] = true
			allPRs = append(allPRs, pr)
			slog.Debug("Added non-overlapping file contributor", "pr_number", int(prNumber), "author", pr.Author, "mergedBy", pr.MergedBy, "reviewers", pr.Reviewers, "merged_at", mergedAt)
		}
	}

	return overlappingPRs, allPRs
}

// recentCommitsInDirectory finds recent commits in a directory and their associated PRs.
func (f *Finder) recentCommitsInDirectory(ctx context.Context, owner, repo, dirPath string, limit int) ([]types.PRInfo, error) {
	slog.InfoContext(ctx, "Querying recent commits in directory", "owner", owner, "repo", repo, "dir", dirPath, "limit", limit)

	cacheKey := fmt.Sprintf("commits-dir:%s/%s:%s:%d", owner, repo, dirPath, limit)
	if cached, found := f.cache.Get(cacheKey); found {
		slog.DebugContext(ctx, "Cache hit", "key", cacheKey)
		if prs, ok := cached.([]types.PRInfo); ok {
			return prs, nil
		}
		slog.WarnContext(ctx, "Cache type assertion failed", "key", cacheKey)
	}

	// GraphQL query to get recent commits in a directory
	// Try both main and master branches
	query := `
	query($owner: String!, $repo: String!, $path: String!, $limit: Int!) {
		repository(owner: $owner, name: $repo) {
			defaultBranchRef {
				name
				target {
					... on Commit {
						history(first: $limit, path: $path) {
							nodes {
								oid
								author {
									user {
										login
									}
								}
								associatedPullRequests(first: 1) {
									nodes {
										number
										merged
										mergedAt
										author {
											login
										}
										mergedBy {
											login
										}
										reviews(first: 10, states: APPROVED) {
											nodes {
												author {
													login
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
	}`

	variables := map[string]any{
		"owner": owner,
		"repo":  repo,
		"path":  dirPath,
		"limit": limit,
	}

	result, err := f.client.MakeGraphQLRequest(ctx, query, variables)
	if err != nil {
		return nil, fmt.Errorf("GraphQL request failed: %w", err)
	}

	prs := f.parseDirectoryCommitsFromGraphQL(result)
	slog.InfoContext(ctx, "Parsed directory commits", "unique_prs", len(prs), "from_commits", limit)

	f.cache.Set(cacheKey, prs)
	return prs, nil
}

// parseDirectoryCommitsFromGraphQL extracts PR info from directory commit history.
func (*Finder) parseDirectoryCommitsFromGraphQL(result map[string]any) []types.PRInfo {
	var prs []types.PRInfo
	seenPRs := make(map[int]bool)
	seenCommitAuthors := make(map[string]bool)

	data, ok := mapValue(result, "data")
	if !ok {
		return prs
	}

	repository, ok := mapValue(data, "repository")
	if !ok {
		return prs
	}

	defaultBranchRef, ok := mapValue(repository, "defaultBranchRef")
	if !ok {
		slog.Debug("No defaultBranchRef field")
		return prs
	}

	target, ok := mapValue(defaultBranchRef, "target")
	if !ok {
		return prs
	}

	history, ok := mapValue(target, "history")
	if !ok {
		return prs
	}

	nodes, ok := sliceNodes(history)
	if !ok {
		return prs
	}

	for _, node := range nodes {
		commitNode, ok := node.(map[string]any)
		if !ok {
			continue
		}

		// Extract commit author for direct commits without PRs
		var commitAuthor string
		if author, ok := mapValue(commitNode, "author"); ok {
			if user, ok := mapValue(author, "user"); ok {
				if login, ok := user["login"].(string); ok {
					commitAuthor = login
				}
			}
		}

		associatedPRs, ok := mapValue(commitNode, "associatedPullRequests")
		if !ok {
			continue
		}

		prNodes, ok := sliceNodes(associatedPRs)
		if !ok || len(prNodes) == 0 {
			// No PR - use commit author if available
			if commitAuthor != "" && !seenCommitAuthors[commitAuthor] {
				seenCommitAuthors[commitAuthor] = true
				prs = append(prs, types.PRInfo{
					Number: 0,
					Author: commitAuthor,
				})
			}
			continue
		}

		// Take first associated PR
		prInfo := parsePRNode(prNodes[0].(map[string]any))
		if prInfo != nil && !seenPRs[prInfo.Number] {
			seenPRs[prInfo.Number] = true
			prs = append(prs, *prInfo)
		}
	}

	return prs
}

// recentPRsInProject finds recent merged PRs in the project.
func (f *Finder) recentPRsInProject(ctx context.Context, owner, repo string) ([]types.PRInfo, error) {
	slog.InfoContext(ctx, "Querying recent merged PRs", "owner", owner, "repo", repo)

	cacheKey := fmt.Sprintf("prs-project:%s/%s", owner, repo)
	if cached, found := f.cache.Get(cacheKey); found {
		slog.DebugContext(ctx, "Cache hit", "key", cacheKey)
		if prs, ok := cached.([]types.PRInfo); ok {
			return prs, nil
		}
		slog.WarnContext(ctx, "Cache type assertion failed", "key", cacheKey)
	}

	// Fetch in two batches of 100 (GitHub's max) to get 200 total
	var allPRs []types.PRInfo

	// First batch
	query1 := `
	query($owner: String!, $repo: String!) {
		repository(owner: $owner, name: $repo) {
			pullRequests(first: 100, states: MERGED, orderBy: {field: CREATED_AT, direction: DESC}) {
				pageInfo {
					endCursor
					hasNextPage
				}
				nodes {
					number
					merged
					author {
						login
					}
					mergedAt
					mergedBy {
						login
					}
					reviews(first: 10, states: APPROVED) {
						nodes {
							author {
								login
							}
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

	result, err := f.client.MakeGraphQLRequest(ctx, query1, variables)
	if err != nil {
		return nil, fmt.Errorf("GraphQL request failed: %w", err)
	}

	prs, err := f.parseProjectPRsFromGraphQL(result)
	if err != nil {
		return nil, err
	}
	allPRs = append(allPRs, prs...)

	if len(prs) > 0 {
		slog.InfoContext(ctx, "First batch PRs", "count", len(prs), "first_pr", prs[0].Number, "last_pr", prs[len(prs)-1].Number)
	}

	// Get cursor for second batch
	data, ok := mapValue(result, "data")
	if !ok {
		return allPRs, nil
	}
	repository, ok := mapValue(data, "repository")
	if !ok {
		return allPRs, nil
	}
	pullRequests, ok := mapValue(repository, "pullRequests")
	if !ok {
		return allPRs, nil
	}
	pageInfo, ok := mapValue(pullRequests, "pageInfo")
	if !ok {
		return allPRs, nil
	}
	hasNextPage, ok := pageInfo["hasNextPage"].(bool)
	if !ok || !hasNextPage {
		slog.InfoContext(ctx, "Fetched all recent PRs", "count", len(allPRs))
		f.cache.Set(cacheKey, allPRs)
		return allPRs, nil
	}

	endCursor, ok := pageInfo["endCursor"].(string)
	if !ok {
		slog.InfoContext(ctx, "Fetched first batch of PRs", "count", len(allPRs))
		f.cache.Set(cacheKey, allPRs)
		return allPRs, nil
	}

	// Second batch
	query2 := `
	query($owner: String!, $repo: String!, $after: String!) {
		repository(owner: $owner, name: $repo) {
			pullRequests(first: 100, after: $after, states: MERGED, orderBy: {field: CREATED_AT, direction: DESC}) {
				nodes {
					number
					merged
					author {
						login
					}
					mergedAt
					mergedBy {
						login
					}
					reviews(first: 10, states: APPROVED) {
						nodes {
							author {
								login
							}
						}
					}
				}
			}
		}
	}`

	variables["after"] = endCursor
	result2, err := f.client.MakeGraphQLRequest(ctx, query2, variables)
	if err != nil {
		slog.WarnContext(ctx, "Failed to fetch second batch of PRs", "error", err)
		f.cache.Set(cacheKey, allPRs)
		return allPRs, nil
	}

	prs2, err := f.parseProjectPRsFromGraphQL(result2)
	if err == nil {
		allPRs = append(allPRs, prs2...)
	}

	slog.InfoContext(ctx, "Fetched recent PRs with pagination", "total_count", len(allPRs))
	f.cache.Set(cacheKey, allPRs)
	return allPRs, nil
}

// parseProjectPRsFromGraphQL extracts PR info from project-wide GraphQL response.
func (*Finder) parseProjectPRsFromGraphQL(result map[string]any) ([]types.PRInfo, error) {
	var prs []types.PRInfo

	data, ok := mapValue(result, "data")
	if !ok {
		return nil, errors.New("invalid GraphQL response format")
	}

	repository, ok := mapValue(data, "repository")
	if !ok {
		return nil, errors.New("repository not found in response")
	}

	pullRequests, ok := mapValue(repository, "pullRequests")
	if !ok {
		return nil, errors.New("pullRequests not found")
	}

	nodes, ok := sliceNodes(pullRequests)
	if !ok {
		return nil, errors.New("nodes not found")
	}

	for _, node := range nodes {
		pr, ok := node.(map[string]any)
		if !ok {
			continue
		}

		prInfo := parsePRNode(pr)
		if prInfo != nil {
			prs = append(prs, *prInfo)
		}
	}

	return prs, nil
}

// Helper functions for navigating GraphQL responses.

func mapValue(data map[string]any, key string) (map[string]any, bool) {
	val, ok := data[key]
	if !ok {
		return nil, false
	}
	m, ok := val.(map[string]any)
	return m, ok
}

func sliceNodes(data map[string]any) ([]any, bool) {
	val, ok := data["nodes"]
	if !ok {
		return nil, false
	}
	s, ok := val.([]any)
	return s, ok
}

func stringValue(data map[string]any, key string) (string, bool) {
	val, ok := data[key]
	if !ok {
		return "", false
	}
	s, ok := val.(string)
	return s, ok
}

func parsePRNode(pr map[string]any) *types.PRInfo {
	merged, ok := pr["merged"].(bool)
	if !ok || !merged {
		return nil
	}

	val, ok := pr["number"]
	if !ok {
		return nil
	}
	number, ok := val.(float64)
	if !ok {
		return nil
	}

	prInfo := &types.PRInfo{Number: int(number)}

	if author, ok := mapValue(pr, "author"); ok {
		if login, ok := stringValue(author, "login"); ok {
			prInfo.Author = login
		}
	}

	if mergedAt, ok := stringValue(pr, "mergedAt"); ok {
		if t, err := time.Parse(time.RFC3339, mergedAt); err == nil {
			prInfo.MergedAt = t
		}
	}

	if mergedBy, ok := mapValue(pr, "mergedBy"); ok {
		if login, ok := stringValue(mergedBy, "login"); ok {
			prInfo.MergedBy = login
		}
	} else {
		slog.Debug("No mergedBy found in PR node", "pr", prInfo.Number, "raw_mergedBy", pr["mergedBy"])
	}

	prInfo.Reviewers = extractReviewers(pr)

	slog.Debug("Parsed PR node", "pr", prInfo.Number, "author", prInfo.Author, "mergedBy", prInfo.MergedBy, "reviewers", prInfo.Reviewers)

	return prInfo
}

// extractReviewers extracts unique reviewer logins from a PR node.
func extractReviewers(pr map[string]any) []string {
	var reviewers []string
	seen := make(map[string]bool)

	reviews, ok := mapValue(pr, "reviews")
	if !ok {
		return reviewers
	}

	reviewNodes, ok := sliceNodes(reviews)
	if !ok {
		return reviewers
	}

	for _, reviewNode := range reviewNodes {
		review, ok := reviewNode.(map[string]any)
		if !ok {
			continue
		}

		author, ok := mapValue(review, "author")
		if !ok {
			continue
		}

		login, ok := stringValue(author, "login")
		if !ok || seen[login] {
			continue
		}

		seen[login] = true
		reviewers = append(reviewers, login)
	}

	return reviewers
}
