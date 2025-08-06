package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// TimelineEvent represents a GitHub timeline event.
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

// reviewerRequestedTimes returns when each reviewer was requested for a PR.
func (c *GitHubClient) reviewerRequestedTimes(ctx context.Context, owner, repo string, prNumber int) (map[string]time.Time, error) {
	log.Printf("[TIMELINE] Fetching reviewer request times for PR %s/%s#%d", owner, repo, prNumber)
	log.Printf("[API] Fetching timeline events for PR %s/%s#%d to determine reviewer request times for staleness detection", owner, repo, prNumber)
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d/timeline", owner, repo, prNumber)

	resp, err := c.makeRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get timeline: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[WARN] Failed to close response body: %v", err)
		}
	}()

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
				log.Printf("[TIMELINE] Reviewer %s was requested at %v", event.RequestedReviewer.Login, event.CreatedAt)
			}
		}
	}

	log.Printf("[TIMELINE] Found %d reviewer request times", len(reviewerTimes))
	return reviewerTimes, nil
}

// staleReviewers returns reviewers who were requested over X days ago.
func (c *GitHubClient) staleReviewers(ctx context.Context, pr *PullRequest, staleDuration time.Duration) ([]string, error) {
	if len(pr.Reviewers) == 0 {
		return nil, nil
	}

	reviewerTimes, err := c.reviewerRequestedTimes(ctx, pr.Owner, pr.Repository, pr.Number)
	if err != nil {
		return nil, fmt.Errorf("failed to get reviewer requested times: %w", err)
	}

	var staleReviewers []string
	cutoffTime := time.Now().Add(-staleDuration)

	for _, reviewer := range pr.Reviewers {
		if requestedTime, exists := reviewerTimes[reviewer]; exists {
			if requestedTime.Before(cutoffTime) {
				staleReviewers = append(staleReviewers, reviewer)
				log.Printf("[STALE] Reviewer %s is stale (requested %v ago)", reviewer, time.Since(requestedTime))
			}
		}
	}

	if len(staleReviewers) > 0 {
		log.Printf("[STALE] Found %d stale reviewers for PR %s/%s#%d", len(staleReviewers), pr.Owner, pr.Repository, pr.Number)
	}
	return staleReviewers, nil
}
