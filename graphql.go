package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// makeGraphQLRequest makes a GraphQL request to GitHub API.
func (c *GitHubClient) makeGraphQLRequest(ctx context.Context, query string, variables map[string]any) (map[string]any, error) {
	// Extract query type for better debugging
	queryType := extractGraphQLQueryType(query)
	querySize := len(query)

	log.Printf("[API] Executing GraphQL query: %s (size: %d chars)", queryType, querySize)
	if len(variables) > 0 {
		log.Printf("[GRAPHQL] Variables: %+v", variables)
	}
	payload := map[string]any{
		"query":     query,
		"variables": variables,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GraphQL request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/graphql", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create GraphQL request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	log.Printf("[GRAPHQL] Executing %s query", queryType)
	start := time.Now()

	var result map[string]any
	err = retryWithBackoff(ctx, fmt.Sprintf("GraphQL %s query", queryType), func() error {
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("graphql request failed: %w", err)
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				log.Printf("[WARN] Failed to close response body: %v", err)
			}
		}()

		// Read the response body
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}

		// Check for HTTP errors
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("graphql request failed with status %d: %s", resp.StatusCode, string(body))
		}

		// Parse the response
		if err := json.Unmarshal(body, &result); err != nil {
			return fmt.Errorf("failed to decode GraphQL response: %w", err)
		}

		// Check for GraphQL errors
		if errors, ok := result["errors"]; ok {
			return fmt.Errorf("graphql errors: %v", errors)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	duration := time.Since(start)
	log.Printf("[GRAPHQL] %s query completed successfully in %v", queryType, duration)
	return result, nil
}

// recentPRsInDirectory finds recent PRs that modified files in a directory.
func (rf *ReviewerFinder) recentPRsInDirectory(ctx context.Context, owner, repo, directory string) ([]PRInfo, error) {
	log.Printf("[API] Querying historical PRs that modified directory '%s' in %s/%s to find expert reviewers", directory, owner, repo)
	// Check cache first
	cacheKey := fmt.Sprintf("prs-dir:%s/%s:%s", owner, repo, directory)
	if cached, found := rf.client.cache.value(cacheKey); found {
		log.Printf("[CACHE] Hit for key: %s", cacheKey)
		if prs, ok := cached.([]PRInfo); ok {
			return prs, nil
		}
		log.Printf("[WARN] Cache type assertion failed for key: %s", cacheKey)
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

// recentPRsInProject finds recent merged PRs in the project.
func (rf *ReviewerFinder) recentPRsInProject(ctx context.Context, owner, repo string) ([]PRInfo, error) {
	log.Printf("[API] Querying recent merged PRs across entire project %s/%s to build reviewer activity and expertise profiles", owner, repo)
	// Check cache first
	cacheKey := fmt.Sprintf("prs-project:%s/%s", owner, repo)
	if cached, found := rf.client.cache.value(cacheKey); found {
		log.Printf("[CACHE] Hit for key: %s", cacheKey)
		if prs, ok := cached.([]PRInfo); ok {
			return prs, nil
		}
		log.Printf("[WARN] Cache type assertion failed for key: %s", cacheKey)
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

// historicalPRsForFile finds merged PRs that modified a specific file.
func (rf *ReviewerFinder) historicalPRsForFile(ctx context.Context, owner, repo, filepath string, limit int) ([]PRInfo, error) {
	log.Printf("[API] Querying historical PRs that modified file '%s' in %s/%s to identify expert reviewers", filepath, owner, repo)
	// Check cache first
	cacheKey := fmt.Sprintf("prs-file:%s/%s:%s:%d", owner, repo, filepath, limit)
	if cached, found := rf.client.cache.value(cacheKey); found {
		log.Printf("[CACHE] Hit for key: %s", cacheKey)
		if prs, ok := cached.([]PRInfo); ok {
			return prs, nil
		}
		log.Printf("[WARN] Cache type assertion failed for key: %s", cacheKey)
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
func (*ReviewerFinder) parsePRsFromGraphQL(result map[string]any) ([]PRInfo, error) {
	var prs []PRInfo

	// Navigate through the GraphQL response structure
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

	nodes, ok := nodesValue(history)
	if !ok {
		return nil, errors.New("nodes not found")
	}

	// Process commits and their associated PRs
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

		prNodes, ok := nodesValue(associatedPRs)
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
func (*ReviewerFinder) parseProjectPRsFromGraphQL(result map[string]any) ([]PRInfo, error) {
	var prs []PRInfo

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

	nodes, ok := nodesValue(pullRequests)
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

			// Short-circuit if we reach old PRs
		}
	}

	return prs, nil
}

// parseHistoricalPRsFromGraphQL extracts PR info from historical file query.
func (*ReviewerFinder) parseHistoricalPRsFromGraphQL(result map[string]any, targetFile string) ([]PRInfo, error) {
	var prs []PRInfo
	seen := make(map[int]bool)

	// Navigate through response (similar structure as parsePRsFromGraphQL)
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

	nodes, ok := nodesValue(history)
	if !ok {
		return nil, errors.New("nodes not found")
	}

	// Process commits
	for _, node := range nodes {
		commit, ok := node.(map[string]any)
		if !ok {
			continue
		}

		associatedPRs, ok := mapValue(commit, "associatedPullRequests")
		if !ok {
			continue
		}

		prNodes, ok := nodesValue(associatedPRs)
		if !ok || len(prNodes) == 0 {
			continue
		}

		for _, prNode := range prNodes {
			pr, ok := prNode.(map[string]any)
			if !ok {
				continue
			}

			// Verify this PR actually touched our target file
			files, ok := mapValue(pr, "files")
			if !ok {
				continue
			}

			fileNodes, ok := nodesValue(files)
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

func sliceValue(data map[string]any, key string) ([]any, bool) {
	val, ok := data[key]
	if !ok {
		return nil, false
	}
	s, ok := val.([]any)
	return s, ok
}

func nodesValue(data map[string]any) ([]any, bool) {
	return sliceValue(data, graphQLNodes)
}

func stringValue(data map[string]any, key string) (string, bool) {
	val, ok := data[key]
	if !ok {
		return "", false
	}
	s, ok := val.(string)
	return s, ok
}

func floatValue(data map[string]any, key string) (float64, bool) {
	val, ok := data[key]
	if !ok {
		return 0, false
	}
	f, ok := val.(float64)
	return f, ok
}

func parsePRNode(pr map[string]any) *PRInfo {
	// Skip if not merged
	merged, ok := pr["merged"].(bool)
	if !ok || !merged {
		return nil
	}

	number, ok := floatValue(pr, "number")
	if !ok {
		return nil
	}

	prInfo := &PRInfo{Number: int(number)}

	// Get author
	if author, ok := mapValue(pr, "author"); ok {
		if login, ok := stringValue(author, "login"); ok {
			prInfo.Author = login
		}
	}

	// Get merged time
	if mergedAt, ok := stringValue(pr, "mergedAt"); ok {
		if t, err := time.Parse(time.RFC3339, mergedAt); err == nil {
			prInfo.MergedAt = t
		}
	}

	// Get reviewers
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

	reviewNodes, ok := nodesValue(reviews)
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

// extractGraphQLQueryType extracts a descriptive query type from GraphQL query for debugging.
func extractGraphQLQueryType(query string) string {
	query = strings.TrimSpace(query)
	lines := strings.Split(query, "\n")

	// Look for the main query pattern
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "query(") {
			// Extract the first field being queried
			for i := 1; i < len(lines); i++ {
				fieldLine := strings.TrimSpace(lines[i])
				if fieldLine != "" && !strings.HasPrefix(fieldLine, "}") {
					// Remove common prefixes and extract main object
					if strings.Contains(fieldLine, "organization(") {
						return "organization-repositories"
					}
					if strings.Contains(fieldLine, "repository(") {
						if strings.Contains(query, "pullRequests") {
							if strings.Contains(query, "history") {
								return "repository-commit-history"
							}
							return "repository-pullrequests"
						}
						return "repository-query"
					}
					// Extract the main field name
					if idx := strings.Index(fieldLine, "("); idx != -1 {
						return strings.TrimSpace(fieldLine[:idx])
					}
					if idx := strings.Index(fieldLine, " "); idx != -1 {
						return strings.TrimSpace(fieldLine[:idx])
					}
					return fieldLine
				}
			}
		}
	}

	// Fallback to detecting by content
	if strings.Contains(query, "organization") && strings.Contains(query, "repositories") {
		return "org-batch-prs"
	}
	if strings.Contains(query, "repository") && strings.Contains(query, "pullRequests") {
		return "repo-recent-prs"
	}
	if strings.Contains(query, "history") {
		return "commit-history"
	}

	return "unknown-graphql"
}
