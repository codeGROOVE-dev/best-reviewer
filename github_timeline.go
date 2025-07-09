package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// TimelineEvent represents a GitHub timeline event
type TimelineEvent struct {
	Event     string    `json:"event"`
	CreatedAt time.Time `json:"created_at"`
	Actor     struct {
		Login string `json:"login"`
	} `json:"actor"`
	RequestedReviewer struct {
		Login string `json:"login"`
	} `json:"requested_reviewer"`
}

// getReviewerRequestedTimes gets when each reviewer was requested for a PR
func (c *GitHubClient) getReviewerRequestedTimes(ctx context.Context, owner, repo string, prNumber int) (map[string]time.Time, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d/timeline", owner, repo, prNumber)
	
	resp, err := c.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get timeline: %w", err)
	}
	defer resp.Body.Close()

	var events []TimelineEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, fmt.Errorf("failed to decode timeline: %w", err)
	}

	reviewerTimes := make(map[string]time.Time)
	
	for _, event := range events {
		if event.Event == "review_requested" && event.RequestedReviewer.Login != "" {
			// Store the earliest request time for each reviewer
			if existingTime, exists := reviewerTimes[event.RequestedReviewer.Login]; !exists || event.CreatedAt.Before(existingTime) {
				reviewerTimes[event.RequestedReviewer.Login] = event.CreatedAt
			}
			log.Printf("Found review request for %s at %s", event.RequestedReviewer.Login, event.CreatedAt.Format(time.RFC3339))
		}
	}
	
	return reviewerTimes, nil
}

// getStaleReviewers checks which reviewers were requested over X days ago
func (c *GitHubClient) getStaleReviewers(ctx context.Context, pr *PullRequest, staleDuration time.Duration) ([]string, error) {
	if len(pr.Reviewers) == 0 {
		return nil, nil
	}

	reviewerTimes, err := c.getReviewerRequestedTimes(ctx, pr.Owner, pr.Repository, pr.Number)
	if err != nil {
		return nil, fmt.Errorf("failed to get reviewer requested times: %w", err)
	}

	var staleReviewers []string
	cutoffTime := time.Now().Add(-staleDuration)
	
	for _, reviewer := range pr.Reviewers {
		if requestedTime, exists := reviewerTimes[reviewer]; exists {
			if requestedTime.Before(cutoffTime) {
				log.Printf("Reviewer %s is stale (requested %s ago)", reviewer, time.Since(requestedTime).Round(time.Hour))
				staleReviewers = append(staleReviewers, reviewer)
			}
		}
	}
	
	return staleReviewers, nil
}

// removeReviewers removes specific reviewers from a PR
func (c *GitHubClient) removeReviewers(ctx context.Context, owner, repo string, prNumber int, reviewers []string) error {
	if len(reviewers) == 0 {
		return nil
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/requested_reviewers", owner, repo, prNumber)
	
	payload := map[string]interface{}{
		"reviewers": reviewers,
	}
	
	resp, err := c.makeRequest(ctx, "DELETE", url, payload)
	if err != nil {
		return fmt.Errorf("failed to remove reviewers: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		return fmt.Errorf("failed to remove reviewers (status %d)", resp.StatusCode)
	}
	
	log.Printf("Successfully removed reviewers %v from PR %d", reviewers, prNumber)
	return nil
}