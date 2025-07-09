package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
)

// getPRFromURL fetches a single PR from a URL.
func (rf *ReviewerFinder) getPRFromURL(ctx context.Context, prURL string) (*PullRequest, error) {
	owner, repo, number, err := parsePRURL(prURL)
	if err != nil {
		return nil, err
	}
	
	return rf.client.getPullRequest(ctx, owner, repo, number)
}

// getPRsForProject fetches all open PRs for a project.
func (rf *ReviewerFinder) getPRsForProject(ctx context.Context, project string) ([]*PullRequest, error) {
	parts := strings.Split(project, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid project format, expected owner/repo")
	}
	
	owner, repo := parts[0], parts[1]
	return rf.client.getOpenPullRequests(ctx, owner, repo)
}

// getPRsForOrg fetches all open PRs for an organization.
func (rf *ReviewerFinder) getPRsForOrg(ctx context.Context, org string) ([]*PullRequest, error) {
	log.Printf("Fetching repositories for organization: %s", org)
	
	var allPRs []*PullRequest
	page := 1
	
	for {
		url := fmt.Sprintf("https://api.github.com/orgs/%s/repos?per_page=100&page=%d", org, page)
		resp, err := rf.client.makeRequest(ctx, "GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch org repos: %w", err)
		}
		defer resp.Body.Close()
		
		var repos []struct {
			Name string `json:"name"`
		}
		
		if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
			return nil, fmt.Errorf("failed to decode repos: %w", err)
		}
		
		if len(repos) == 0 {
			break
		}
		
		// Fetch PRs for each repo
		for _, repo := range repos {
			prs, err := rf.client.getOpenPullRequests(ctx, org, repo.Name)
			if err != nil {
				log.Printf("Failed to get PRs for %s/%s: %v", org, repo.Name, err)
				continue
			}
			allPRs = append(allPRs, prs...)
		}
		
		page++
		if len(repos) < 100 {
			break
		}
	}
	
	return allPRs, nil
}

// parsePRURL parses a PR URL in various formats.
func parsePRURL(prURL string) (owner string, repo string, number int, err error) {
	// Handle GitHub URL format: https://github.com/owner/repo/pull/123
	if strings.HasPrefix(prURL, "https://github.com/") {
		parts := strings.Split(strings.TrimPrefix(prURL, "https://github.com/"), "/")
		if len(parts) >= 4 && parts[2] == "pull" {
			owner = parts[0]
			repo = parts[1]
			number, err = strconv.Atoi(parts[3])
			if err != nil {
				return "", "", 0, fmt.Errorf("invalid PR number: %s", parts[3])
			}
			return owner, repo, number, nil
		}
	}
	
	// Handle shorthand format: owner/repo#123
	if strings.Contains(prURL, "#") {
		parts := strings.Split(prURL, "#")
		if len(parts) == 2 {
			repoParts := strings.Split(parts[0], "/")
			if len(repoParts) == 2 {
				owner = repoParts[0]
				repo = repoParts[1]
				number, err = strconv.Atoi(parts[1])
				if err != nil {
					return "", "", 0, fmt.Errorf("invalid PR number: %s", parts[1])
				}
				return owner, repo, number, nil
			}
		}
	}
	
	return "", "", 0, fmt.Errorf("invalid PR URL format: %s", prURL)
}