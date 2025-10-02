package reviewer

import (
	"context"
	"fmt"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// TODO: These methods require GraphQL support in pkg/github/client.go
// Once github.Client has a MakeGraphQLRequest(ctx, query, variables) method,
// these can be fully implemented.

// recentPRsInDirectory fetches recent PRs that modified files in a directory.
func (f *Finder) recentPRsInDirectory(ctx context.Context, owner, repo, directory string) ([]types.PRInfo, error) {
	// Check cache first
	cacheKey := makeCacheKey("recent-prs-dir", owner, repo, directory)
	if cached, found := f.cache.Get(cacheKey); found {
		if prs, ok := cached.([]types.PRInfo); ok {
			return prs, nil
		}
	}

	// TODO: Implement using GraphQL query
	// query {
	//   repository(owner: $owner, name: $repo) {
	//     pullRequests(states: MERGED, first: 20) {
	//       nodes {
	//         number
	//         author { login }
	//         mergedAt
	//         files(first: 50) {
	//           nodes { path }
	//         }
	//         reviews(first: 10) {
	//           nodes { author { login } }
	//         }
	//       }
	//     }
	//   }
	// }

	return nil, fmt.Errorf("recentPRsInDirectory requires GraphQL support in github.Client")
}

// recentPRsInProject fetches recent PRs in the entire project.
func (f *Finder) recentPRsInProject(ctx context.Context, owner, repo string) ([]types.PRInfo, error) {
	// Check cache first
	cacheKey := makeCacheKey("recent-prs-project", owner, repo)
	if cached, found := f.cache.Get(cacheKey); found {
		if prs, ok := cached.([]types.PRInfo); ok {
			return prs, nil
		}
	}

	// TODO: Implement using GraphQL query similar to recentPRsInDirectory
	// but without file filtering

	return nil, fmt.Errorf("recentPRsInProject requires GraphQL support in github.Client")
}

// historicalPRsForFile fetches historical PRs that modified a specific file.
func (f *Finder) historicalPRsForFile(ctx context.Context, owner, repo, filepath string, limit int) ([]types.PRInfo, error) {
	// Check cache first
	cacheKey := makeCacheKey("historical-prs-file", owner, repo, filepath, limit)
	if cached, found := f.cache.Get(cacheKey); found {
		if prs, ok := cached.([]types.PRInfo); ok {
			return prs, nil
		}
	}

	// TODO: Implement using GraphQL query
	// This is more complex as it needs to filter PRs by file path

	return nil, fmt.Errorf("historicalPRsForFile requires GraphQL support in github.Client")
}

// UserActivity represents a user's activity in a repository.
type UserActivity struct {
	LastActivity time.Time
	Source       string
	Username     string
	PRCount      int
}

// fetchRepoUserActivity fetches all user activity in a repository.
func (f *Finder) fetchRepoUserActivity(ctx context.Context, owner, repo string) map[string]UserActivity {
	// Check cache first
	cacheKey := makeCacheKey("repo-user-activity", owner, repo)
	if cached, found := f.cache.Get(cacheKey); found {
		if activity, ok := cached.(map[string]UserActivity); ok {
			return activity
		}
	}

	// TODO: Implement using GraphQL query to fetch all recent PR activity
	// This is a critical optimization that fetches all user activity in one query
	// query {
	//   repository(owner: $owner, name: $repo) {
	//     pullRequests(states: [MERGED, OPEN], first: 100, orderBy: {field: UPDATED_AT, direction: DESC}) {
	//       nodes {
	//         number
	//         author { login }
	//         createdAt
	//         updatedAt
	//         reviews(first: 50) {
	//           nodes {
	//             author { login }
	//             createdAt
	//           }
	//         }
	//         participants(first: 50) {
	//           nodes {
	//             login
	//           }
	//         }
	//       }
	//     }
	//   }
	// }

	return make(map[string]UserActivity)
}
