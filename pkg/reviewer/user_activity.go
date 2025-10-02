package reviewer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/cache"
	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// fetchRepoUserActivity fetches all user activity for a repository in a single batch.
func (f *Finder) fetchRepoUserActivity(ctx context.Context, owner, repo string) map[string]types.UserActivity {
	cacheKey := fmt.Sprintf("repo-user-activity:%s/%s", owner, repo)
	if cached, found := f.cache.Get(cacheKey); found {
		if activities, ok := cached.(map[string]types.UserActivity); ok {
			return activities
		}
	}

	slog.InfoContext(ctx, "Fetching repository-wide user activity", "owner", owner, "repo", repo)

	activities := make(map[string]types.UserActivity)

	query := `
	query($owner: String!, $repo: String!) {
		repository(owner: $owner, name: $repo) {
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
		}
	}`

	variables := map[string]any{
		"owner": owner,
		"repo":  repo,
	}

	result, err := f.client.MakeGraphQLRequest(ctx, query, variables)
	if err != nil {
		slog.WarnContext(ctx, "Failed to fetch user activity", "error", err)
		return activities
	}

	f.parseUserActivity(result, activities)

	activeCount := 0
	for _, activity := range activities {
		daysSince := int(time.Since(activity.LastActivity).Hours() / 24)
		if daysSince <= 30 {
			activeCount++
		}
	}
	slog.InfoContext(ctx, "User activity fetched", "total_users", len(activities), "active_last_30d", activeCount)

	f.cache.SetWithTTL(cacheKey, activities, repoContributorsCacheTTL)

	return activities
}

// parseUserActivity parses GraphQL result into user activities.
func (f *Finder) parseUserActivity(result map[string]any, activities map[string]types.UserActivity) {
	data, ok := result["data"].(map[string]any)
	if !ok {
		return
	}

	repository, ok := data["repository"].(map[string]any)
	if !ok {
		return
	}

	if pullRequests, ok := repository["pullRequests"].(map[string]any); ok {
		if nodes, ok := pullRequests["nodes"].([]any); ok {
			for _, node := range nodes {
				f.parsePRNodeActivity(node, activities)
			}
		}
	}
}

// parsePRNodeActivity parses a single PR node for user activity.
func (f *Finder) parsePRNodeActivity(node any, activities map[string]types.UserActivity) {
	pr, ok := node.(map[string]any)
	if !ok {
		return
	}

	prDate := f.extractPRDate(pr)

	f.trackPRAuthor(pr, activities, prDate)
	f.trackPRReviewers(pr, activities)
	f.trackPRParticipants(pr, activities, prDate)
}

// extractPRDate extracts the relevant date from a PR.
func (*Finder) extractPRDate(pr map[string]any) time.Time {
	if mergedAtStr, ok := pr["mergedAt"].(string); ok && mergedAtStr != "" {
		if t, err := time.Parse(time.RFC3339, mergedAtStr); err == nil {
			return t
		}
	}

	if updatedAtStr, ok := pr["updatedAt"].(string); ok {
		if t, err := time.Parse(time.RFC3339, updatedAtStr); err == nil {
			return t
		}
	}

	return time.Time{}
}

// trackPRAuthor tracks the PR author's activity.
func (f *Finder) trackPRAuthor(pr map[string]any, activities map[string]types.UserActivity, prDate time.Time) {
	author, ok := pr["author"].(map[string]any)
	if !ok {
		return
	}

	login, ok := author["login"].(string)
	if !ok || login == "" {
		return
	}

	if typeName, ok := author["__typename"].(string); ok {
		f.client.CacheUserTypeFromGraphQL(login, typeName)
		if typeName == "Bot" {
			return
		}
	}

	if cache.IsLikelyBot(login) {
		f.client.CacheUserTypeFromGraphQL(login, "Bot")
		return
	}

	f.updateUserActivity(activities, login, prDate, "pr_author")
}

// trackPRReviewers tracks all reviewers' activity from a PR.
func (f *Finder) trackPRReviewers(pr map[string]any, activities map[string]types.UserActivity) {
	reviews, ok := pr["reviews"].(map[string]any)
	if !ok {
		return
	}

	reviewNodes, ok := reviews["nodes"].([]any)
	if !ok {
		return
	}

	for _, reviewNode := range reviewNodes {
		f.trackSingleReviewer(reviewNode, activities)
	}
}

// trackSingleReviewer tracks a single reviewer's activity.
func (f *Finder) trackSingleReviewer(reviewNode any, activities map[string]types.UserActivity) {
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

	if f.isBot(author, login) {
		return
	}

	reviewDate := f.extractReviewDate(review)
	f.updateUserActivity(activities, login, reviewDate, "pr_reviewer")
}

// extractReviewDate extracts the date from a review.
func (*Finder) extractReviewDate(review map[string]any) time.Time {
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
func (f *Finder) trackPRParticipants(pr map[string]any, activities map[string]types.UserActivity, prDate time.Time) {
	participants, ok := pr["participants"].(map[string]any)
	if !ok {
		return
	}

	participantNodes, ok := participants["nodes"].([]any)
	if !ok {
		return
	}

	for _, participantNode := range participantNodes {
		f.trackSingleParticipant(participantNode, activities, prDate)
	}
}

// trackSingleParticipant tracks a single participant's activity.
func (f *Finder) trackSingleParticipant(participantNode any, activities map[string]types.UserActivity, prDate time.Time) {
	participant, ok := participantNode.(map[string]any)
	if !ok {
		return
	}

	login, ok := participant["login"].(string)
	if !ok || login == "" {
		return
	}

	if f.isBot(participant, login) {
		return
	}

	f.updateUserActivity(activities, login, prDate, "participant")
}

// isBot checks if a user is a bot based on type and username patterns.
func (*Finder) isBot(userNode map[string]any, login string) bool {
	if typeName, ok := userNode["__typename"].(string); ok && typeName == "Bot" {
		return true
	}

	return cache.IsLikelyBot(login)
}

// updateUserActivity updates or creates a user activity record.
func (*Finder) updateUserActivity(activities map[string]types.UserActivity, username string, date time.Time, source string) {
	if username == "" || date.IsZero() {
		return
	}

	if existing, exists := activities[username]; exists {
		if date.After(existing.LastActivity) {
			activities[username] = types.UserActivity{
				Username:     username,
				LastActivity: date,
				Source:       source,
			}
		}
	} else {
		activities[username] = types.UserActivity{
			Username:     username,
			LastActivity: date,
			Source:       source,
		}
	}
}
