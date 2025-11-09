package github

import (
	"context"
	"net/http"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// HTTPDoer provides an interface for making HTTP requests.
// This allows us to mock HTTP calls in tests.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// TimeProvider provides an interface for time operations.
// This allows us to control time in tests.
type TimeProvider interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	NewTicker(d time.Duration) Ticker
}

// Ticker represents a time.Ticker interface for testability.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// API defines operations for interacting with GitHub API.
//
//nolint:interfacebloat // GitHub API client legitimately requires many methods for different operations
type API interface {
	// Authentication and configuration
	SetCurrentOrg(org string)
	SetPrxClient(prxClient PrxClient)
	IsUserAccount(account string) bool
	Token(ctx context.Context) (string, error)

	// Pull request operations
	PullRequest(ctx context.Context, owner, repo string, number int) (*types.PullRequest, error)
	OpenPullRequestsForOrg(ctx context.Context, org string) ([]*types.PullRequest, error)
	OpenPullRequests(ctx context.Context, owner, repo string) ([]*types.PullRequest, error)
	ChangedFiles(ctx context.Context, owner, repo string, prNumber int) ([]types.ChangedFile, error)
	FilePatch(ctx context.Context, owner, repo string, prNumber int, filename string) (string, error)
	AddReviewers(ctx context.Context, owner, repo string, prNumber int, reviewers []string) error

	// User operations
	IsUserBot(ctx context.Context, username string) bool
	HasWriteAccess(ctx context.Context, owner, repo, username string) bool
	OpenPRCount(ctx context.Context, org, user string, cacheTTL time.Duration) (int, error)
	BatchOpenPRCount(ctx context.Context, org string, users []string, cacheTTL time.Duration) (map[string]int, error)
	Collaborators(ctx context.Context, owner, repo string) ([]string, error)

	// GraphQL operations
	MakeGraphQLRequest(ctx context.Context, query string, variables map[string]any) (map[string]any, error)

	// HTTP operations
	MakeRequest(ctx context.Context, method, apiURL string, body any) (*http.Response, error)

	// App installation operations
	ListAppInstallations(ctx context.Context) ([]string, error)
}

// PrxClient defines the interface for enhanced PR data fetching.
type PrxClient interface {
	PullRequestWithReferenceTime(ctx context.Context, owner, repo string, prNumber int, referenceTime time.Time) (any, error)
}
