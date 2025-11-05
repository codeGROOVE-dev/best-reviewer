// Package testutil provides mock implementations and testing utilities for the best-reviewer project.
package testutil

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/github"
	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// MockGitHubClient implements github.API for testing.
// It's a smart, programmable mock that allows configuring responses.
type MockGitHubClient struct {
	writeAccess       map[string]bool
	openPRCounts      map[string]int
	changedFiles      map[string][]types.ChangedFile
	filePatches       map[string]string
	collaborators     map[string][]string
	botUsers          map[string]bool
	pullRequests      map[string]*types.PullRequest
	errors            map[string]error
	isUserAccount     map[string]bool
	graphQLResponses  map[string]map[string]any
	batchPRCounts     map[string]map[string]int
	currentOrg        string
	addReviewersCalls []AddReviewersCall
	installations     []string
	mu                sync.RWMutex
}

// AddReviewersCall records a call to AddReviewers.
type AddReviewersCall struct {
	Owner     string
	Repo      string
	Reviewers []string
	PRNumber  int
}

// NewMockGitHubClient creates a new MockGitHubClient.
func NewMockGitHubClient() *MockGitHubClient {
	return &MockGitHubClient{
		pullRequests:      make(map[string]*types.PullRequest),
		changedFiles:      make(map[string][]types.ChangedFile),
		filePatches:       make(map[string]string),
		collaborators:     make(map[string][]string),
		botUsers:          make(map[string]bool),
		writeAccess:       make(map[string]bool),
		openPRCounts:      make(map[string]int),
		batchPRCounts:     make(map[string]map[string]int),
		graphQLResponses:  make(map[string]map[string]any),
		isUserAccount:     make(map[string]bool),
		addReviewersCalls: []AddReviewersCall{},
		errors:            make(map[string]error),
	}
}

// SetCurrentOrg sets the current organization.
func (m *MockGitHubClient) SetCurrentOrg(org string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentOrg = org
}

// SetPrxClient sets the prx client (no-op for mock).
func (*MockGitHubClient) SetPrxClient(_ github.PrxClient) {
	// No-op for mock
}

// IsUserAccount checks if an account is a user account.
func (m *MockGitHubClient) IsUserAccount(account string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.isUserAccount[account]
}

// Token returns a mock token.
func (*MockGitHubClient) Token(ctx context.Context) (string, error) {
	return "mock-token", nil
}

// PullRequest returns a configured pull request.
func (m *MockGitHubClient) PullRequest(ctx context.Context, owner, repo string, number int) (*types.PullRequest, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s/%s/%d", owner, repo, number)
	if err := m.errors[fmt.Sprintf("PullRequest:%s", key)]; err != nil {
		return nil, err
	}

	pr, ok := m.pullRequests[key]
	if !ok {
		return nil, fmt.Errorf("PR not found: %s", key)
	}
	return pr, nil
}

// OpenPullRequestsForOrg returns configured open PRs for an org.
func (m *MockGitHubClient) OpenPullRequestsForOrg(ctx context.Context, org string) ([]*types.PullRequest, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if err := m.errors[fmt.Sprintf("OpenPullRequestsForOrg:%s", org)]; err != nil {
		return nil, err
	}

	var prs []*types.PullRequest
	for key, pr := range m.pullRequests {
		if pr.Owner == org {
			prs = append(prs, pr)
		}
		_ = key // Used implicitly
	}
	return prs, nil
}

// OpenPullRequests returns configured open PRs for a repo.
func (m *MockGitHubClient) OpenPullRequests(ctx context.Context, owner, repo string) ([]*types.PullRequest, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s/%s", owner, repo)
	if err := m.errors[fmt.Sprintf("OpenPullRequests:%s", key)]; err != nil {
		return nil, err
	}

	var prs []*types.PullRequest
	for _, pr := range m.pullRequests {
		if pr.Owner == owner && pr.Repository == repo {
			prs = append(prs, pr)
		}
	}
	return prs, nil
}

// ChangedFiles returns configured changed files.
func (m *MockGitHubClient) ChangedFiles(ctx context.Context, owner, repo string, prNumber int) ([]types.ChangedFile, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s/%s/%d", owner, repo, prNumber)
	if err := m.errors[fmt.Sprintf("ChangedFiles:%s", key)]; err != nil {
		return nil, err
	}

	files, ok := m.changedFiles[key]
	if !ok {
		return []types.ChangedFile{}, nil
	}
	return files, nil
}

// FilePatch returns a configured file patch.
func (m *MockGitHubClient) FilePatch(ctx context.Context, owner, repo string, prNumber int, filename string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s/%s/%d/%s", owner, repo, prNumber, filename)
	if err := m.errors[fmt.Sprintf("FilePatch:%s", key)]; err != nil {
		return "", err
	}

	patch, ok := m.filePatches[key]
	if !ok {
		return "", fmt.Errorf("patch not found: %s", key)
	}
	return patch, nil
}

// AddReviewers records the call and returns success.
func (m *MockGitHubClient) AddReviewers(ctx context.Context, owner, repo string, prNumber int, reviewers []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s/%s/%d", owner, repo, prNumber)
	if err := m.errors[fmt.Sprintf("AddReviewers:%s", key)]; err != nil {
		return err
	}

	m.addReviewersCalls = append(m.addReviewersCalls, AddReviewersCall{
		Owner:     owner,
		Repo:      repo,
		PRNumber:  prNumber,
		Reviewers: reviewers,
	})
	return nil
}

// IsUserBot checks if a user is configured as a bot.
func (m *MockGitHubClient) IsUserBot(ctx context.Context, username string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.botUsers[username]
}

// HasWriteAccess checks if a user has write access.
func (m *MockGitHubClient) HasWriteAccess(ctx context.Context, owner, repo, username string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s/%s/%s", owner, repo, username)
	return m.writeAccess[key]
}

// OpenPRCount returns the configured open PR count for a user in an org.
func (m *MockGitHubClient) OpenPRCount(ctx context.Context, org, user string, _ time.Duration) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s:%s", org, user)
	if err := m.errors[fmt.Sprintf("OpenPRCount:%s", key)]; err != nil {
		return 0, err
	}

	count, ok := m.openPRCounts[key]
	if !ok {
		return 0, nil
	}
	return count, nil
}

// BatchOpenPRCount returns configured PR counts for multiple users in an org.
func (m *MockGitHubClient) BatchOpenPRCount(ctx context.Context, org string, users []string, _ time.Duration) (map[string]int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if err := m.errors[fmt.Sprintf("BatchOpenPRCount:%s", org)]; err != nil {
		return nil, err
	}

	// If we have batch counts configured for this org, use them
	if orgCounts, ok := m.batchPRCounts[org]; ok {
		result := make(map[string]int)
		for _, user := range users {
			if count, exists := orgCounts[user]; exists {
				result[user] = count
			} else {
				result[user] = 0
			}
		}
		return result, nil
	}

	// Fall back to individual counts
	result := make(map[string]int)
	for _, user := range users {
		key := fmt.Sprintf("%s:%s", org, user)
		if count, ok := m.openPRCounts[key]; ok {
			result[user] = count
		} else {
			result[user] = 0
		}
	}
	return result, nil
}

// Collaborators returns configured collaborators for a repo.
func (m *MockGitHubClient) Collaborators(ctx context.Context, owner, repo string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s/%s", owner, repo)
	if err := m.errors[fmt.Sprintf("Collaborators:%s", key)]; err != nil {
		return nil, err
	}

	collabs, ok := m.collaborators[key]
	if !ok {
		return []string{}, nil
	}
	return collabs, nil
}

// MakeGraphQLRequest returns a configured GraphQL response.
func (m *MockGitHubClient) MakeGraphQLRequest(ctx context.Context, query string, _ map[string]any) (map[string]any, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := query
	if err := m.errors[fmt.Sprintf("MakeGraphQLRequest:%s", key)]; err != nil {
		return nil, err
	}

	resp, ok := m.graphQLResponses[key]
	if !ok {
		return map[string]any{}, nil
	}
	return resp, nil
}

// MakeRequest makes a mock HTTP request.
func (m *MockGitHubClient) MakeRequest(ctx context.Context, method, apiURL string, _ any) (*http.Response, error) {
	key := fmt.Sprintf("%s:%s", method, apiURL)
	if err := m.errors[key]; err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
	}, nil
}

// ListAppInstallations returns configured installations.
func (m *MockGitHubClient) ListAppInstallations(ctx context.Context) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if err := m.errors["ListAppInstallations"]; err != nil {
		return nil, err
	}

	return m.installations, nil
}

// Configuration methods for testing.

// SetPullRequest configures a pull request response.
func (m *MockGitHubClient) SetPullRequest(owner, repo string, number int, pr *types.PullRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s/%s/%d", owner, repo, number)
	m.pullRequests[key] = pr
}

// SetChangedFiles configures changed files for a PR.
func (m *MockGitHubClient) SetChangedFiles(owner, repo string, prNumber int, files []types.ChangedFile) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s/%s/%d", owner, repo, prNumber)
	m.changedFiles[key] = files
}

// SetFilePatch configures a file patch.
func (m *MockGitHubClient) SetFilePatch(owner, repo string, prNumber int, filename, patch string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s/%s/%d/%s", owner, repo, prNumber, filename)
	m.filePatches[key] = patch
}

// SetCollaborators configures collaborators for a repo.
func (m *MockGitHubClient) SetCollaborators(owner, repo string, collaborators []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s/%s", owner, repo)
	m.collaborators[key] = collaborators
}

// SetBotUser configures a user as a bot.
func (m *MockGitHubClient) SetBotUser(username string, isBot bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.botUsers[username] = isBot
}

// SetWriteAccess configures write access for a user.
func (m *MockGitHubClient) SetWriteAccess(owner, repo, username string, hasAccess bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s/%s/%s", owner, repo, username)
	m.writeAccess[key] = hasAccess
}

// SetOpenPRCount configures the open PR count for a user in an org.
func (m *MockGitHubClient) SetOpenPRCount(org, username string, count int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s:%s", org, username)
	m.openPRCounts[key] = count
}

// SetBatchOpenPRCount configures PR counts for multiple users in an org.
func (m *MockGitHubClient) SetBatchOpenPRCount(org string, counts map[string]int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.batchPRCounts[org] = counts
}

// SetGraphQLResponse configures a GraphQL response.
func (m *MockGitHubClient) SetGraphQLResponse(query string, response map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.graphQLResponses[query] = response
}

// SetError configures an error for a specific method and parameters.
func (m *MockGitHubClient) SetError(methodWithParams string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.errors[methodWithParams] = err
}

// SetUserAccount configures whether an account is a user account.
func (m *MockGitHubClient) SetUserAccount(account string, isUser bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.isUserAccount[account] = isUser
}

// SetInstallations configures app installations.
func (m *MockGitHubClient) SetInstallations(installations []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.installations = installations
}

// AddReviewersCallsCount returns the number of times AddReviewers was called.
func (m *MockGitHubClient) AddReviewersCallsCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.addReviewersCalls)
}

// LastAddReviewersCall returns the last call to AddReviewers.
func (m *MockGitHubClient) LastAddReviewersCall() *AddReviewersCall {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.addReviewersCalls) == 0 {
		return nil
	}
	return &m.addReviewersCalls[len(m.addReviewersCalls)-1]
}

// MockPrxClient implements github.PrxClient for testing.
type MockPrxClient struct {
	responses map[string]any
	errors    map[string]error
	mu        sync.RWMutex
}

// NewMockPrxClient creates a new MockPrxClient.
func NewMockPrxClient() *MockPrxClient {
	return &MockPrxClient{
		responses: make(map[string]any),
		errors:    make(map[string]error),
	}
}

// PullRequestWithReferenceTime returns a configured response.
func (m *MockPrxClient) PullRequestWithReferenceTime(ctx context.Context, owner, repo string, prNumber int, _ time.Time) (any, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := fmt.Sprintf("%s/%s/%d", owner, repo, prNumber)
	if err := m.errors[key]; err != nil {
		return nil, err
	}

	resp, ok := m.responses[key]
	if !ok {
		return nil, fmt.Errorf("no configured response for %s", key)
	}
	return resp, nil
}

// SetResponse configures a response.
func (m *MockPrxClient) SetResponse(owner, repo string, prNumber int, response any) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s/%s/%d", owner, repo, prNumber)
	m.responses[key] = response
}

// SetError configures an error.
func (m *MockPrxClient) SetError(owner, repo string, prNumber int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s/%s/%d", owner, repo, prNumber)
	m.errors[key] = err
}
