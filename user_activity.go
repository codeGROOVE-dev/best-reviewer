package main

import (
	"context"
	"fmt"
	"log"
	"time"
)

// UserActivity tracks a user's last activity in a repository.
type UserActivity struct {
	Username     string
	LastActivity time.Time
	Source       string // "commit", "pr_author", "pr_reviewer"
}

// fetchRepoUserActivity fetches all user activity for a repository in a single batch.
func (rf *ReviewerFinder) fetchRepoUserActivity(ctx context.Context, owner, repo string) map[string]UserActivity {
	// Check cache first
	cacheKey := fmt.Sprintf("repo-user-activity:%s/%s", owner, repo)
	if cached, found := rf.client.cache.value(cacheKey); found {
		if activities, ok := cached.(map[string]UserActivity); ok {
			return activities
		}
	}

	log.Print("  📊 Fetching repository-wide user activity (single batch query)")

	activities := make(map[string]UserActivity)

	// Use GraphQL to get all recent activity in one query
	query := `
	query($owner: String!, $repo: String!) {
		repository(owner: $owner, name: $repo) {
			# Recent PRs (both open and merged)
			pullRequests(first: 100, orderBy: {field: UPDATED_AT, direction: DESC}) {
				nodes {
					number
					author { 
						login
						__typename
					}
					updatedAt
					mergedAt
					createdAt
					participants(first: 50) {
						nodes { 
							login
							__typename
						}
					}
					reviews(first: 50) {
						nodes {
							author { 
								login
								__typename
							}
							submittedAt
						}
					}
					timelineItems(first: 100, itemTypes: [PULL_REQUEST_COMMIT, PULL_REQUEST_REVIEW, ISSUE_COMMENT]) {
						nodes {
							__typename
							... on PullRequestCommit {
								commit {
									author { 
										user { 
											login
											__typename
										}
									}
									committedDate
								}
							}
							... on PullRequestReview {
								author { 
									login
									__typename
								}
								submittedAt
							}
							... on IssueComment {
								author { 
									login
									__typename
								}
								createdAt
							}
						}
					}
				}
			}
			
			# Recent issues for additional activity
			issues(first: 50, orderBy: {field: UPDATED_AT, direction: DESC}) {
				nodes {
					author { 
						login
						__typename
					}
					updatedAt
					participants(first: 20) {
						nodes { 
							login
							__typename
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
		log.Printf("  ⚠️  Failed to fetch user activity: %v", err)
		return activities
	}

	// Parse the result
	rf.parseUserActivity(result, activities)

	// Log summary
	activeCount := 0
	for _, activity := range activities {
		daysSince := int(time.Since(activity.LastActivity).Hours() / 24)
		if daysSince <= 30 {
			activeCount++
		}
	}
	log.Printf("  ✅ Found %d total users, %d active in last 30 days", len(activities), activeCount)

	// Cache for 4 hours (same as contributors cache)
	rf.client.cache.setWithTTL(cacheKey, activities, repoContributorsCacheTTL)

	return activities
}

// parseUserActivity parses GraphQL result into user activities.
func (rf *ReviewerFinder) parseUserActivity(result map[string]any, activities map[string]UserActivity) {
	data, ok := result["data"].(map[string]any)
	if !ok {
		return
	}

	repository, ok := data["repository"].(map[string]any)
	if !ok {
		return
	}

	// Parse pull requests
	if pullRequests, ok := repository["pullRequests"].(map[string]any); ok {
		if nodes, ok := pullRequests["nodes"].([]any); ok {
			for _, node := range nodes {
				rf.parsePRNode(node, activities)
			}
		}
	}

	// Parse issues
	if issues, ok := repository["issues"].(map[string]any); ok {
		if nodes, ok := issues["nodes"].([]any); ok {
			for _, node := range nodes {
				rf.parseIssueNode(node, activities)
			}
		}
	}
}

// parsePRNode parses a single PR node for user activity.
// parsePRNode parses a single PR node for user activity.
func (rf *ReviewerFinder) parsePRNode(node any, activities map[string]UserActivity) {
	pr, ok := node.(map[string]any)
	if !ok {
		return
	}

	prDate := rf.extractPRDate(pr)

	rf.trackPRAuthor(pr, activities, prDate)
	rf.trackPRReviewers(pr, activities)
	rf.trackPRParticipants(pr, activities, prDate)
}

// extractPRDate extracts the relevant date from a PR.
func (*ReviewerFinder) extractPRDate(pr map[string]any) time.Time {
	// Try merged date first
	if mergedAtStr, ok := pr["mergedAt"].(string); ok && mergedAtStr != "" {
		if t, err := time.Parse(time.RFC3339, mergedAtStr); err == nil {
			return t
		}
	}

	// Fall back to updated date
	if updatedAtStr, ok := pr["updatedAt"].(string); ok {
		if t, err := time.Parse(time.RFC3339, updatedAtStr); err == nil {
			return t
		}
	}

	return time.Time{}
}

// trackPRAuthor tracks the PR author's activity.
func (rf *ReviewerFinder) trackPRAuthor(pr map[string]any, activities map[string]UserActivity, prDate time.Time) {
	author, ok := pr["author"].(map[string]any)
	if !ok {
		return
	}

	login, ok := author["login"].(string)
	if !ok || login == "" {
		return
	}

	// Check and cache user type
	if typeName, ok := author["__typename"].(string); ok {
		rf.client.cacheUserTypeFromGraphQL(login, typeName)
		if typeName == "Bot" {
			return
		}
	}

	// Check username patterns for bots
	if isLikelyBot(login) {
		rf.client.cacheUserTypeFromGraphQL(login, "Bot")
		return
	}

	rf.updateUserActivity(activities, login, prDate, "pr_author")
}

// trackPRReviewers tracks all reviewers' activity from a PR.
func (rf *ReviewerFinder) trackPRReviewers(pr map[string]any, activities map[string]UserActivity) {
	reviews, ok := pr["reviews"].(map[string]any)
	if !ok {
		return
	}

	reviewNodes, ok := reviews["nodes"].([]any)
	if !ok {
		return
	}

	for _, reviewNode := range reviewNodes {
		rf.trackSingleReviewer(reviewNode, activities)
	}
}

// trackSingleReviewer tracks a single reviewer's activity.
func (rf *ReviewerFinder) trackSingleReviewer(reviewNode any, activities map[string]UserActivity) {
	review, ok := reviewNode.(map[string]any)
	if !ok {
		return
	}

	author, ok := review["author"].(map[string]any)
	if !ok {
		return
	}

	login, ok := author["login"].(string)
	if !ok || login == "" {
		return
	}

	// Skip bots
	if rf.isBot(author, login) {
		return
	}

	reviewDate := rf.extractReviewDate(review)
	rf.updateUserActivity(activities, login, reviewDate, "pr_reviewer")
}

// extractReviewDate extracts the date from a review.
func (*ReviewerFinder) extractReviewDate(review map[string]any) time.Time {
	submittedAtStr, ok := review["submittedAt"].(string)
	if !ok {
		return time.Time{}
	}

	t, err := time.Parse(time.RFC3339, submittedAtStr)
	if err != nil {
		return time.Time{}
	}

	return t
}

// trackPRParticipants tracks all participants' activity from a PR.
func (rf *ReviewerFinder) trackPRParticipants(pr map[string]any, activities map[string]UserActivity, prDate time.Time) {
	participants, ok := pr["participants"].(map[string]any)
	if !ok {
		return
	}

	participantNodes, ok := participants["nodes"].([]any)
	if !ok {
		return
	}

	for _, participantNode := range participantNodes {
		rf.trackSingleParticipant(participantNode, activities, prDate)
	}
}

// trackSingleParticipant tracks a single participant's activity.
func (rf *ReviewerFinder) trackSingleParticipant(participantNode any, activities map[string]UserActivity, prDate time.Time) {
	participant, ok := participantNode.(map[string]any)
	if !ok {
		return
	}

	login, ok := participant["login"].(string)
	if !ok || login == "" {
		return
	}

	// Skip bots
	if rf.isBot(participant, login) {
		return
	}

	rf.updateUserActivity(activities, login, prDate, "participant")
}

// isBot checks if a user is a bot based on type and username patterns.
func (*ReviewerFinder) isBot(userNode map[string]any, login string) bool {
	// Check __typename field
	if typeName, ok := userNode["__typename"].(string); ok && typeName == "Bot" {
		return true
	}

	// Check username patterns
	return isLikelyBot(login)
}

// parseIssueNode parses a single issue node for user activity.
func (rf *ReviewerFinder) parseIssueNode(node any, activities map[string]UserActivity) {
	issue, ok := node.(map[string]any)
	if !ok {
		return
	}

	var issueDate time.Time
	if updatedAtStr, ok := issue["updatedAt"].(string); ok {
		if t, err := time.Parse(time.RFC3339, updatedAtStr); err == nil {
			issueDate = t
		}
	}

	// Track issue author
	if author, ok := issue["author"].(map[string]any); ok {
		if login, ok := author["login"].(string); ok && login != "" {
			rf.updateUserActivity(activities, login, issueDate, "issue_author")
		}
	}

	// Track participants
	if participants, ok := issue["participants"].(map[string]any); ok {
		if participantNodes, ok := participants["nodes"].([]any); ok {
			for _, participantNode := range participantNodes {
				if participant, ok := participantNode.(map[string]any); ok {
					if login, ok := participant["login"].(string); ok && login != "" {
						rf.updateUserActivity(activities, login, issueDate, "participant")
					}
				}
			}
		}
	}
}

// updateUserActivity updates or creates a user activity record.
func (*ReviewerFinder) updateUserActivity(activities map[string]UserActivity, username string, date time.Time, source string) {
	if username == "" || date.IsZero() {
		return
	}

	if existing, exists := activities[username]; exists {
		// Update if this activity is more recent
		if date.After(existing.LastActivity) {
			activities[username] = UserActivity{
				Username:     username,
				LastActivity: date,
				Source:       source,
			}
		}
	} else {
		activities[username] = UserActivity{
			Username:     username,
			LastActivity: date,
			Source:       source,
		}
	}
}
