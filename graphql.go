package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// makeGraphQLRequest makes a GraphQL request to GitHub API.
func (c *GitHubClient) makeGraphQLRequest(ctx context.Context, query string, variables map[string]interface{}) (map[string]interface{}, error) {
	payload := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GraphQL request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.github.com/graphql", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create GraphQL request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	// Retry logic for GraphQL requests
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			time.Sleep(time.Duration(retryDelay) * time.Second)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			lastErr = fmt.Errorf("failed to decode GraphQL response: %w", err)
			continue
		}

		// Check for GraphQL errors
		if errors, ok := result["errors"]; ok {
			lastErr = fmt.Errorf("GraphQL errors: %v", errors)
			continue
		}

		return result, nil
	}

	return nil, fmt.Errorf("GraphQL request failed after %d retries: %w", maxRetries, lastErr)
}

// getRecentPRsInDirectory finds recent PRs that modified files in a directory.
func (rf *ReviewerFinder) getRecentPRsInDirectory(ctx context.Context, owner, repo, directory string) ([]PRInfo, error) {
	// Check cache first
	cacheKey := fmt.Sprintf("prs-dir:%s/%s:%s", owner, repo, directory)
	if cached, found := rf.client.cache.get(cacheKey); found {
		return cached.([]PRInfo), nil
	}
	query := `
	query($owner: String!, $repo: String!, $path: String!) {
		repository(owner: $owner, name: $repo) {
			defaultBranchRef {
				target {
					... on Commit {
						history(first: 100, path: $path) {
							nodes {
								associatedPullRequests(first: 5) {
									nodes {
										number
										merged
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
						}
					}
				}
			}
		}
	}`

	variables := map[string]interface{}{
		"owner": owner,
		"repo":  repo,
		"path":  directory,
	}

	result, err := rf.client.makeGraphQLRequest(ctx, query, variables)
	if err != nil {
		return nil, fmt.Errorf("GraphQL request failed: %w", err)
	}


	prs, err := rf.parsePRsFromGraphQL(result)
	if err == nil && len(prs) > 0 {
		rf.client.cache.set(cacheKey, prs)
	}
	return prs, err
}

// getRecentPRsInProject finds recent merged PRs in the project.
func (rf *ReviewerFinder) getRecentPRsInProject(ctx context.Context, owner, repo string) ([]PRInfo, error) {
	// Check cache first
	cacheKey := fmt.Sprintf("prs-project:%s/%s", owner, repo)
	if cached, found := rf.client.cache.get(cacheKey); found {
		return cached.([]PRInfo), nil
	}
	query := `
	query($owner: String!, $repo: String!) {
		repository(owner: $owner, name: $repo) {
			pullRequests(first: 20, states: MERGED, orderBy: {field: UPDATED_AT, direction: DESC}) {
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

	variables := map[string]interface{}{
		"owner": owner,
		"repo":  repo,
	}

	result, err := rf.client.makeGraphQLRequest(ctx, query, variables)
	if err != nil {
		return nil, fmt.Errorf("GraphQL request failed: %w", err)
	}

	prs, err := rf.parseProjectPRsFromGraphQL(result)
	if err == nil && len(prs) > 0 {
		rf.client.cache.set(cacheKey, prs)
	}
	return prs, err
}

// getHistoricalPRsForFile finds merged PRs that modified a specific file.
func (rf *ReviewerFinder) getHistoricalPRsForFile(ctx context.Context, owner, repo, filepath string, limit int) ([]PRInfo, error) {
	// Check cache first
	cacheKey := fmt.Sprintf("prs-file:%s/%s:%s:%d", owner, repo, filepath, limit)
	if cached, found := rf.client.cache.get(cacheKey); found {
		return cached.([]PRInfo), nil
	}
	
	query := `
	query($owner: String!, $repo: String!, $path: String!) {
		repository(owner: $owner, name: $repo) {
			defaultBranchRef {
				target {
					... on Commit {
						history(first: 100, path: $path) {
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
										reviews(first: 10, states: APPROVED) {
											nodes {
												author {
													login
												}
											}
										}
										files(first: 100) {
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

	variables := map[string]interface{}{
		"owner": owner,
		"repo":  repo,
		"path":  filepath,
	}

	result, err := rf.client.makeGraphQLRequest(ctx, query, variables)
	if err != nil {
		return nil, fmt.Errorf("GraphQL request failed: %w", err)
	}

	prs, err := rf.parseHistoricalPRsFromGraphQL(result, filepath)
	if err == nil && len(prs) > 0 {
		rf.client.cache.set(cacheKey, prs)
	}
	return prs, err
}

// parsePRsFromGraphQL extracts PR info from GraphQL response.
func (rf *ReviewerFinder) parsePRsFromGraphQL(result map[string]interface{}) ([]PRInfo, error) {
	var prs []PRInfo
	
	// Navigate through the GraphQL response structure
	data, ok := getMap(result, "data")
	if !ok {
		return nil, fmt.Errorf("invalid GraphQL response format")
	}

	repository, ok := getMap(data, "repository")
	if !ok {
		return nil, fmt.Errorf("repository not found in response")
	}

	defaultBranchRef, ok := getMap(repository, "defaultBranchRef")
	if !ok {
		return nil, fmt.Errorf("defaultBranchRef not found")
	}

	target, ok := getMap(defaultBranchRef, "target")
	if !ok {
		return nil, fmt.Errorf("target not found")
	}

	history, ok := getMap(target, "history")
	if !ok {
		return nil, fmt.Errorf("history not found")
	}

	nodes, ok := getSlice(history, "nodes")
	if !ok {
		return nil, fmt.Errorf("nodes not found")
	}

	// Process commits and their associated PRs
	seen := make(map[int]bool)
	for _, node := range nodes {
		commit, ok := node.(map[string]interface{})
		if !ok {
			continue
		}

		associatedPRs, ok := getMap(commit, "associatedPullRequests")
		if !ok {
			continue
		}

		prNodes, ok := getSlice(associatedPRs, "nodes")
		if !ok {
			continue
		}

		for _, prNode := range prNodes {
			pr, ok := prNode.(map[string]interface{})
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
func (rf *ReviewerFinder) parseProjectPRsFromGraphQL(result map[string]interface{}) ([]PRInfo, error) {
	var prs []PRInfo
	
	data, ok := getMap(result, "data")
	if !ok {
		return nil, fmt.Errorf("invalid GraphQL response format")
	}

	repository, ok := getMap(data, "repository")
	if !ok {
		return nil, fmt.Errorf("repository not found in response")
	}

	pullRequests, ok := getMap(repository, "pullRequests")
	if !ok {
		return nil, fmt.Errorf("pullRequests not found")
	}

	nodes, ok := getSlice(pullRequests, "nodes")
	if !ok {
		return nil, fmt.Errorf("nodes not found")
	}

	for _, node := range nodes {
		pr, ok := node.(map[string]interface{})
		if !ok {
			continue
		}

		prInfo := parsePRNode(pr)
		if prInfo != nil {
			prs = append(prs, *prInfo)
			
			// Short-circuit if we reach old PRs
		}
	}

	return prs, nil
}

// parseHistoricalPRsFromGraphQL extracts PR info from historical file query.
func (rf *ReviewerFinder) parseHistoricalPRsFromGraphQL(result map[string]interface{}, targetFile string) ([]PRInfo, error) {
	var prs []PRInfo
	seen := make(map[int]bool)
	
	// Navigate through response (similar structure as parsePRsFromGraphQL)
	data, ok := getMap(result, "data")
	if !ok {
		return nil, fmt.Errorf("invalid GraphQL response format")
	}

	repository, ok := getMap(data, "repository")
	if !ok {
		return nil, fmt.Errorf("repository not found in response")
	}

	defaultBranchRef, ok := getMap(repository, "defaultBranchRef")
	if !ok {
		return nil, fmt.Errorf("defaultBranchRef not found")
	}

	target, ok := getMap(defaultBranchRef, "target")
	if !ok {
		return nil, fmt.Errorf("target not found")
	}

	history, ok := getMap(target, "history")
	if !ok {
		return nil, fmt.Errorf("history not found")
	}

	nodes, ok := getSlice(history, "nodes")
	if !ok {
		return nil, fmt.Errorf("nodes not found")
	}

	// Process commits
	for _, node := range nodes {
		commit, ok := node.(map[string]interface{})
		if !ok {
			continue
		}

		associatedPRs, ok := getMap(commit, "associatedPullRequests")
		if !ok {
			continue
		}

		prNodes, ok := getSlice(associatedPRs, "nodes")
		if !ok || len(prNodes) == 0 {
			continue
		}

		for _, prNode := range prNodes {
			pr, ok := prNode.(map[string]interface{})
			if !ok {
				continue
			}

			// Verify this PR actually touched our target file
			files, ok := getMap(pr, "files")
			if !ok {
				continue
			}

			fileNodes, ok := getSlice(files, "nodes")
			if !ok {
				continue
			}

			fileFound := false
			for _, fileNode := range fileNodes {
				file, ok := fileNode.(map[string]interface{})
				if !ok {
					continue
				}
				if path, ok := getString(file, "path"); ok && path == targetFile {
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

// Helper functions for navigating GraphQL responses

func getMap(data map[string]interface{}, key string) (map[string]interface{}, bool) {
	val, ok := data[key]
	if !ok {
		return nil, false
	}
	m, ok := val.(map[string]interface{})
	return m, ok
}

func getSlice(data map[string]interface{}, key string) ([]interface{}, bool) {
	val, ok := data[key]
	if !ok {
		return nil, false
	}
	s, ok := val.([]interface{})
	return s, ok
}

func getString(data map[string]interface{}, key string) (string, bool) {
	val, ok := data[key]
	if !ok {
		return "", false
	}
	s, ok := val.(string)
	return s, ok
}

func getFloat(data map[string]interface{}, key string) (float64, bool) {
	val, ok := data[key]
	if !ok {
		return 0, false
	}
	f, ok := val.(float64)
	return f, ok
}

func parsePRNode(pr map[string]interface{}) *PRInfo {
	// Skip if not merged
	merged, ok := pr["merged"].(bool)
	if !ok || !merged {
		return nil
	}

	number, ok := getFloat(pr, "number")
	if !ok {
		return nil
	}

	prInfo := &PRInfo{Number: int(number)}

	// Get author
	if author, ok := getMap(pr, "author"); ok {
		if login, ok := getString(author, "login"); ok {
			prInfo.Author = login
		}
	}

	// Get merged time
	if mergedAt, ok := getString(pr, "mergedAt"); ok {
		if t, err := time.Parse(time.RFC3339, mergedAt); err == nil {
			prInfo.MergedAt = t
		}
	}

	// Get reviewers
	if reviews, ok := getMap(pr, "reviews"); ok {
		if reviewNodes, ok := getSlice(reviews, "nodes"); ok {
			for _, reviewNode := range reviewNodes {
				if review, ok := reviewNode.(map[string]interface{}); ok {
					if author, ok := getMap(review, "author"); ok {
						if login, ok := getString(author, "login"); ok {
							// Avoid duplicates
							found := false
							for _, r := range prInfo.Reviewers {
								if r == login {
									found = true
									break
								}
							}
							if !found {
								prInfo.Reviewers = append(prInfo.Reviewers, login)
							}
						}
					}
				}
			}
		}
	}

	return prInfo
}