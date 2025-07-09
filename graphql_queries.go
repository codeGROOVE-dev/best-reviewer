package main

import (
	"context"
	"fmt"
	"log"
	"time"
)

// getRecentPRsInDirectory uses GraphQL to find recent PRs in a directory more efficiently
func (rf *ReviewerFinder) getRecentPRsInDirectory(ctx context.Context, owner, repo, directory string) ([]PRInfo, error) {
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

	return rf.parsePRsFromGraphQL(result)
}

// getRecentPRsInProject uses GraphQL to find recent PRs in the project
func (rf *ReviewerFinder) getRecentPRsInProject(ctx context.Context, owner, repo string) ([]PRInfo, error) {
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

	return rf.parseProjectPRsFromGraphQL(result)
}

// PRInfo holds basic PR information
type PRInfo struct {
	Number    int
	Author    string
	Reviewers []string
	MergedAt  time.Time
}

// parsePRsFromGraphQL extracts PR info from GraphQL response
func (rf *ReviewerFinder) parsePRsFromGraphQL(result map[string]interface{}) ([]PRInfo, error) {
	var prs []PRInfo
	oneYearAgo := time.Now().AddDate(-1, 0, 0)
	
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid GraphQL response format")
	}

	repository, ok := data["repository"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("repository not found in response")
	}

	defaultBranchRef, ok := repository["defaultBranchRef"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("defaultBranchRef not found")
	}

	target, ok := defaultBranchRef["target"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("target not found")
	}

	history, ok := target["history"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("history not found")
	}

	nodes, ok := history["nodes"].([]interface{})
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

		associatedPRs, ok := commit["associatedPullRequests"].(map[string]interface{})
		if !ok {
			continue
		}

		prNodes, ok := associatedPRs["nodes"].([]interface{})
		if !ok {
			continue
		}

		for _, prNode := range prNodes {
			pr, ok := prNode.(map[string]interface{})
			if !ok {
				continue
			}

			number, ok := pr["number"].(float64)
			if !ok {
				continue
			}

			prNum := int(number)
			if seen[prNum] {
				continue
			}
			seen[prNum] = true

			// Skip if not merged
			merged, ok := pr["merged"].(bool)
			if !ok || !merged {
				continue
			}

			prInfo := PRInfo{Number: prNum}

			// Get author
			if author, ok := pr["author"].(map[string]interface{}); ok {
				if login, ok := author["login"].(string); ok {
					prInfo.Author = login
				}
			}

			// Get merged time
			if mergedAt, ok := pr["mergedAt"].(string); ok {
				if t, err := time.Parse(time.RFC3339, mergedAt); err == nil {
					prInfo.MergedAt = t
					// Skip PRs older than one year
					if t.Before(oneYearAgo) {
						log.Printf("Skipping PR #%d - merged more than 1 year ago (%s)", prNum, t.Format("2006-01-02"))
						continue
					}
				}
			}

			// Get reviewers
			if reviews, ok := pr["reviews"].(map[string]interface{}); ok {
				if reviewNodes, ok := reviews["nodes"].([]interface{}); ok {
					for _, reviewNode := range reviewNodes {
						if review, ok := reviewNode.(map[string]interface{}); ok {
							if author, ok := review["author"].(map[string]interface{}); ok {
								if login, ok := author["login"].(string); ok {
									prInfo.Reviewers = append(prInfo.Reviewers, login)
								}
							}
						}
					}
				}
			}

			prs = append(prs, prInfo)
		}
	}

	return prs, nil
}

// parseProjectPRsFromGraphQL extracts PR info from project-wide GraphQL response
func (rf *ReviewerFinder) parseProjectPRsFromGraphQL(result map[string]interface{}) ([]PRInfo, error) {
	var prs []PRInfo
	oneYearAgo := time.Now().AddDate(-1, 0, 0)
	
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid GraphQL response format")
	}

	repository, ok := data["repository"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("repository not found in response")
	}

	pullRequests, ok := repository["pullRequests"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("pullRequests not found")
	}

	nodes, ok := pullRequests["nodes"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("nodes not found")
	}

	for _, node := range nodes {
		pr, ok := node.(map[string]interface{})
		if !ok {
			continue
		}

		number, ok := pr["number"].(float64)
		if !ok {
			continue
		}

		prInfo := PRInfo{Number: int(number)}

		// Get author
		if author, ok := pr["author"].(map[string]interface{}); ok {
			if login, ok := author["login"].(string); ok {
				prInfo.Author = login
			}
		}

		// Get merged time
		if mergedAt, ok := pr["mergedAt"].(string); ok {
			if t, err := time.Parse(time.RFC3339, mergedAt); err == nil {
				prInfo.MergedAt = t
				// Skip PRs older than one year and short-circuit
				if t.Before(oneYearAgo) {
					log.Printf("Reached PR #%d from %s (>1 year old) - stopping search", prInfo.Number, t.Format("2006-01-02"))
					break // Short-circuit since PRs are in reverse chronological order
				}
			}
		}

		// Get reviewers
		if reviews, ok := pr["reviews"].(map[string]interface{}); ok {
			if reviewNodes, ok := reviews["nodes"].([]interface{}); ok {
				for _, reviewNode := range reviewNodes {
					if review, ok := reviewNode.(map[string]interface{}); ok {
						if author, ok := review["author"].(map[string]interface{}); ok {
							if login, ok := author["login"].(string); ok {
								prInfo.Reviewers = append(prInfo.Reviewers, login)
							}
						}
					}
				}
			}
		}

		prs = append(prs, prInfo)
	}

	return prs, nil
}

// getHistoricalPRsForFile uses GraphQL to find merged PRs that modified a specific file
func (rf *ReviewerFinder) getHistoricalPRsForFile(ctx context.Context, owner, repo, filepath string, limit int) ([]PRInfo, error) {
	log.Printf("Finding historical PRs that modified file: %s", filepath)
	
	query := `
	query($owner: String!, $repo: String!, $path: String!, $limit: Int!) {
		repository(owner: $owner, name: $repo) {
			defaultBranchRef {
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
		"limit": limit,
	}

	result, err := rf.client.makeGraphQLRequest(ctx, query, variables)
	if err != nil {
		return nil, fmt.Errorf("GraphQL request failed: %w", err)
	}

	return rf.parseHistoricalPRsFromGraphQL(result, filepath)
}

// parseHistoricalPRsFromGraphQL extracts PR info from historical file query
func (rf *ReviewerFinder) parseHistoricalPRsFromGraphQL(result map[string]interface{}, targetFile string) ([]PRInfo, error) {
	var prs []PRInfo
	seen := make(map[int]bool)
	oneYearAgo := time.Now().AddDate(-1, 0, 0)
	
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid GraphQL response format")
	}

	repository, ok := data["repository"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("repository not found in response")
	}

	defaultBranchRef, ok := repository["defaultBranchRef"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("defaultBranchRef not found")
	}

	target, ok := defaultBranchRef["target"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("target not found")
	}

	history, ok := target["history"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("history not found")
	}

	nodes, ok := history["nodes"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("nodes not found")
	}

	// Process commits and their associated PRs
	for _, node := range nodes {
		commit, ok := node.(map[string]interface{})
		if !ok {
			continue
		}

		associatedPRs, ok := commit["associatedPullRequests"].(map[string]interface{})
		if !ok {
			continue
		}

		prNodes, ok := associatedPRs["nodes"].([]interface{})
		if !ok || len(prNodes) == 0 {
			continue
		}

		for _, prNode := range prNodes {
			pr, ok := prNode.(map[string]interface{})
			if !ok {
				continue
			}

			// Skip if not merged
			merged, ok := pr["merged"].(bool)
			if !ok || !merged {
				continue
			}

			number, ok := pr["number"].(float64)
			if !ok {
				continue
			}

			prNum := int(number)
			if seen[prNum] {
				continue
			}
			seen[prNum] = true

			// Verify this PR actually touched our target file
			fileFound := false
			if files, ok := pr["files"].(map[string]interface{}); ok {
				if fileNodes, ok := files["nodes"].([]interface{}); ok {
					for _, fileNode := range fileNodes {
						if file, ok := fileNode.(map[string]interface{}); ok {
							if path, ok := file["path"].(string); ok && path == targetFile {
								fileFound = true
								break
							}
						}
					}
				}
			}

			if !fileFound {
				continue
			}

			prInfo := PRInfo{Number: prNum}

			// Get author
			if author, ok := pr["author"].(map[string]interface{}); ok {
				if login, ok := author["login"].(string); ok {
					prInfo.Author = login
				}
			}

			// Get merged time
			if mergedAt, ok := pr["mergedAt"].(string); ok {
				if t, err := time.Parse(time.RFC3339, mergedAt); err == nil {
					prInfo.MergedAt = t
					// Skip PRs older than one year
					if t.Before(oneYearAgo) {
						log.Printf("Skipping PR #%d - merged more than 1 year ago (%s)", prNum, t.Format("2006-01-02"))
						continue
					}
				}
			}

			// Get reviewers
			if reviews, ok := pr["reviews"].(map[string]interface{}); ok {
				if reviewNodes, ok := reviews["nodes"].([]interface{}); ok {
					for _, reviewNode := range reviewNodes {
						if review, ok := reviewNode.(map[string]interface{}); ok {
							if author, ok := review["author"].(map[string]interface{}); ok {
								if login, ok := author["login"].(string); ok {
									// Avoid duplicates
									hasDup := false
									for _, r := range prInfo.Reviewers {
										if r == login {
											hasDup = true
											break
										}
									}
									if !hasDup {
										prInfo.Reviewers = append(prInfo.Reviewers, login)
									}
								}
							}
						}
					}
				}
			}

			log.Printf("Found historical PR #%d by %s (reviewers: %v)", prInfo.Number, prInfo.Author, prInfo.Reviewers)
			prs = append(prs, prInfo)
		}
	}

	log.Printf("Found %d historical PRs that modified %s", len(prs), targetFile)
	return prs, nil
}