package reviewer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// recentPRsInDirectory finds recent PRs that modified files in a directory.
func (f *Finder) recentPRsInDirectory(ctx context.Context, owner, repo, directory string) ([]types.PRInfo, error) {
	slog.InfoContext(ctx, "Querying historical PRs in directory", "directory", directory, "owner", owner, "repo", repo)

	cacheKey := fmt.Sprintf("prs-dir:%s/%s:%s", owner, repo, directory)
	if cached, found := f.cache.Get(cacheKey); found {
		slog.DebugContext(ctx, "Cache hit", "key", cacheKey)
		if prs, ok := cached.([]types.PRInfo); ok {
			return prs, nil
		}
		slog.WarnContext(ctx, "Cache type assertion failed", "key", cacheKey)
	}

	query := `
	query($owner: String!, $repo: String!, $path: String!) {
		repository(owner: $owner, name: $repo) {
			defaultBranchRef {
				target {
					... on Commit {
						history(first: 10, path: $path) {
							nodes {
								associatedPullRequests(first: 3) {
									nodes {
										number
										merged
										author {
											login
										}
										mergedAt
										reviews(first: 5, states: APPROVED) {
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
		"path":  directory,
	}

	result, err := f.client.MakeGraphQLRequest(ctx, query, variables)
	if err != nil {
		return nil, fmt.Errorf("GraphQL request failed: %w", err)
	}

	prs, err := f.parsePRsFromGraphQL(result)
	if err == nil && len(prs) > 0 {
		f.cache.Set(cacheKey, prs)
	}
	return prs, err
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

	query := `
	query($owner: String!, $repo: String!) {
		repository(owner: $owner, name: $repo) {
			pullRequests(first: 3, states: MERGED, orderBy: {field: UPDATED_AT, direction: DESC}) {
				nodes {
					number
					author {
						login
					}
					mergedAt
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

	result, err := f.client.MakeGraphQLRequest(ctx, query, variables)
	if err != nil {
		return nil, fmt.Errorf("GraphQL request failed: %w", err)
	}

	prs, err := f.parseProjectPRsFromGraphQL(result)
	if err == nil && len(prs) > 0 {
		f.cache.Set(cacheKey, prs)
	}
	return prs, err
}

// historicalPRsForFile finds merged PRs that modified a specific file.
func (f *Finder) historicalPRsForFile(ctx context.Context, owner, repo, filepath string, limit int) ([]types.PRInfo, error) {
	slog.InfoContext(ctx, "Querying historical PRs for file", "file", filepath, "owner", owner, "repo", repo, "limit", limit)

	cacheKey := fmt.Sprintf("prs-file:%s/%s:%s:%d", owner, repo, filepath, limit)
	if cached, found := f.cache.Get(cacheKey); found {
		slog.DebugContext(ctx, "Cache hit", "key", cacheKey)
		if prs, ok := cached.([]types.PRInfo); ok {
			return prs, nil
		}
		slog.WarnContext(ctx, "Cache type assertion failed", "key", cacheKey)
	}

	query := `
	query($owner: String!, $repo: String!, $path: String!) {
		repository(owner: $owner, name: $repo) {
			defaultBranchRef {
				target {
					... on Commit {
						history(first: 10, path: $path) {
							nodes {
								oid
								author {
									user {
										login
									}
								}
								associatedPullRequests(first: 1, orderBy: {field: UPDATED_AT, direction: DESC}) {
									nodes {
										number
										state
										merged
										author {
											login
										}
										mergedAt
										reviews(first: 5, states: APPROVED) {
											nodes {
												author {
													login
												}
											}
										}
										files(first: 50) {
											nodes {
												path
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
		return nil, fmt.Errorf("GraphQL request failed: %w", err)
	}

	prs, err := f.parseHistoricalPRsFromGraphQL(result, filepath)
	if err == nil && len(prs) > 0 {
		f.cache.Set(cacheKey, prs)
	}
	return prs, err
}

// parsePRsFromGraphQL extracts PR info from GraphQL response.
func (*Finder) parsePRsFromGraphQL(result map[string]any) ([]types.PRInfo, error) {
	var prs []types.PRInfo

	data, ok := mapValue(result, "data")
	if !ok {
		return nil, errors.New("invalid GraphQL response format")
	}

	repository, ok := mapValue(data, "repository")
	if !ok {
		return nil, errors.New("repository not found in response")
	}

	defaultBranchRef, ok := mapValue(repository, "defaultBranchRef")
	if !ok {
		return nil, errors.New("defaultBranchRef not found")
	}

	target, ok := mapValue(defaultBranchRef, "target")
	if !ok {
		return nil, errors.New("target not found")
	}

	history, ok := mapValue(target, "history")
	if !ok {
		return nil, errors.New("history not found")
	}

	nodes, ok := sliceNodes(history)
	if !ok {
		return nil, errors.New("nodes not found")
	}

	seen := make(map[int]bool)
	for _, node := range nodes {
		commit, ok := node.(map[string]any)
		if !ok {
			continue
		}

		associatedPRs, ok := mapValue(commit, "associatedPullRequests")
		if !ok {
			continue
		}

		prNodes, ok := sliceNodes(associatedPRs)
		if !ok {
			continue
		}

		for _, prNode := range prNodes {
			pr, ok := prNode.(map[string]any)
			if !ok {
				continue
			}

			prInfo := parsePRNode(pr)
			if prInfo != nil && !seen[prInfo.Number] {
				seen[prInfo.Number] = true
				prs = append(prs, *prInfo)
			}
		}
	}

	return prs, nil
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

// parseHistoricalPRsFromGraphQL extracts PR info from historical file query.
func (*Finder) parseHistoricalPRsFromGraphQL(result map[string]any, targetFile string) ([]types.PRInfo, error) {
	var prs []types.PRInfo
	seen := make(map[int]bool)

	data, ok := mapValue(result, "data")
	if !ok {
		return nil, errors.New("invalid GraphQL response format")
	}

	repository, ok := mapValue(data, "repository")
	if !ok {
		return nil, errors.New("repository not found in response")
	}

	defaultBranchRef, ok := mapValue(repository, "defaultBranchRef")
	if !ok {
		return nil, errors.New("defaultBranchRef not found")
	}

	target, ok := mapValue(defaultBranchRef, "target")
	if !ok {
		return nil, errors.New("target not found")
	}

	history, ok := mapValue(target, "history")
	if !ok {
		return nil, errors.New("history not found")
	}

	nodes, ok := sliceNodes(history)
	if !ok {
		return nil, errors.New("nodes not found")
	}

	for _, node := range nodes {
		commit, ok := node.(map[string]any)
		if !ok {
			continue
		}

		associatedPRs, ok := mapValue(commit, "associatedPullRequests")
		if !ok {
			continue
		}

		prNodes, ok := sliceNodes(associatedPRs)
		if !ok || len(prNodes) == 0 {
			continue
		}

		for _, prNode := range prNodes {
			pr, ok := prNode.(map[string]any)
			if !ok {
				continue
			}

			files, ok := mapValue(pr, "files")
			if !ok {
				continue
			}

			fileNodes, ok := sliceNodes(files)
			if !ok {
				continue
			}

			fileFound := false
			for _, fileNode := range fileNodes {
				file, ok := fileNode.(map[string]any)
				if !ok {
					continue
				}
				if path, ok := stringValue(file, "path"); ok && path == targetFile {
					fileFound = true
					break
				}
			}

			if !fileFound {
				continue
			}

			prInfo := parsePRNode(pr)
			if prInfo != nil && !seen[prInfo.Number] {
				seen[prInfo.Number] = true
				prs = append(prs, *prInfo)
			}
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
	val, ok := data[graphQLNodes]
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

	prInfo.Reviewers = extractReviewers(pr)

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
