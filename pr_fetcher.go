package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
)

// prFromURL fetches a single PR from a URL.
func (rf *ReviewerFinder) prFromURL(ctx context.Context, prURL string) (*PullRequest, error) {
	log.Printf("[PR] Fetching PR from URL: %s", prURL)
	parts, err := parsePRURL(prURL)
	if err != nil {
		return nil, err
	}

	return rf.client.pullRequest(ctx, parts.Owner, parts.Repo, parts.Number)
}

// prsForProject fetches all open PRs for a project.
func (rf *ReviewerFinder) prsForProject(ctx context.Context, project string) ([]*PullRequest, error) {
	log.Printf("[PROJECT] Fetching PRs for project: %s", project)
	parts := strings.Split(project, "/")
	if len(parts) != 2 {
		return nil, errors.New("invalid project format, expected owner/repo")
	}

	owner, repo := parts[0], parts[1]
	prs, err := rf.client.openPullRequests(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	log.Printf("[PROJECT] Found %d open PRs for %s", len(prs), project)
	return prs, nil
}

// prsForOrg fetches all open PRs for an organization using GitHub's search API.
// This is the original method kept for backward compatibility and fallback.
func (rf *ReviewerFinder) prsForOrg(ctx context.Context, org string) ([]*PullRequest, error) {
	log.Printf("[ORG] Fetching open PRs for organization: %s", org)

	var allPRs []*PullRequest
	page := 1

	for {
		// Use GitHub search API to get all open PRs for the org in one query
		log.Printf("[API] Searching for open PRs across entire organization %s (page %d) to find all PRs needing reviewer assignment", org, page)
		url := fmt.Sprintf("https://api.github.com/search/issues?q=org:%s+type:pr+state:open&per_page=100&page=%d", org, page)

		// Extract API call to avoid defer in loop
		searchResults, shouldBreak, err := func() ([]struct {
			PullRequest struct {
				URL string `json:"url"`
			} `json:"pull_request"`
			RepositoryURL string `json:"repository_url"`
			Number        int    `json:"number"`
		}, bool, error,
		) {
			resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
			if err != nil {
				return nil, false, fmt.Errorf("failed to search org PRs: %w", err)
			}
			defer func() {
				if err := resp.Body.Close(); err != nil {
					log.Printf("[WARN] Failed to close response body: %v", err)
				}
			}()

			var searchResponse struct {
				Items []struct {
					PullRequest struct {
						URL string `json:"url"`
					} `json:"pull_request"`
					RepositoryURL string `json:"repository_url"`
					Number        int    `json:"number"`
				} `json:"items"`
			}

			if err := json.NewDecoder(resp.Body).Decode(&searchResponse); err != nil {
				return nil, false, fmt.Errorf("failed to decode search results: %w", err)
			}

			return searchResponse.Items, len(searchResponse.Items) < perPageLimit, nil
		}()
		if err != nil {
			return nil, err
		}

		if len(searchResults) == 0 {
			break
		}

		// Process each PR found in search results
		for _, item := range searchResults {
			// Extract owner and repo from repository_url
			// repository_url format: https://api.github.com/repos/owner/repo
			repoURL := strings.TrimPrefix(item.RepositoryURL, "https://api.github.com/repos/")
			parts := strings.Split(repoURL, "/")
			if len(parts) != 2 {
				log.Printf("[WARN] Invalid repository URL format: %s", item.RepositoryURL)
				continue
			}
			owner, repo := parts[0], parts[1]

			pr, err := rf.client.pullRequest(ctx, owner, repo, item.Number)
			if err != nil {
				log.Printf("[ERROR] Failed to get PR %d details for %s/%s: %v (skipping)", item.Number, owner, repo, err)
				continue
			}

			allPRs = append(allPRs, pr)
		}

		page++
		if shouldBreak {
			break
		}
	}

	log.Printf("[ORG] Found %d open PRs for organization %s", len(allPRs), org)
	return allPRs, nil
}

// prURLParts holds the parsed components of a PR URL.
type prURLParts struct {
	Owner  string
	Repo   string
	Number int
}

// parsePRURL parses a PR URL in various formats.
func parsePRURL(prURL string) (prURLParts, error) {
	// Handle GitHub URL format: https://github.com/owner/repo/pull/123
	if strings.HasPrefix(prURL, "https://github.com/") {
		parts := strings.Split(strings.TrimPrefix(prURL, "https://github.com/"), "/")
		if len(parts) >= minURLParts && parts[2] == "pull" {
			number, err := strconv.Atoi(parts[3])
			if err != nil {
				return prURLParts{}, fmt.Errorf("invalid PR number: %s", parts[3])
			}
			return prURLParts{
				Owner:  parts[0],
				Repo:   parts[1],
				Number: number,
			}, nil
		}
	}

	// Handle shorthand format: owner/repo#123
	if strings.Contains(prURL, "#") {
		parts := strings.Split(prURL, "#")
		if len(parts) == 2 {
			repoParts := strings.Split(parts[0], "/")
			if len(repoParts) == 2 {
				number, err := strconv.Atoi(parts[1])
				if err != nil {
					return prURLParts{}, fmt.Errorf("invalid PR number: %s", parts[1])
				}
				return prURLParts{
					Owner:  repoParts[0],
					Repo:   repoParts[1],
					Number: number,
				}, nil
			}
		}
	}

	return prURLParts{}, fmt.Errorf("invalid PR URL format: %s", prURL)
}
