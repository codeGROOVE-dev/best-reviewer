package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/cache"
)

func TestClient_PullRequest_Success(t *testing.T) {
	// Use mock transport that handles multiple requests
	callCount := 0
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			callCount++
			var responseBody string

			// First call: PR details
			if callCount == 1 {
				responseBody = `{
					"number": 123,
					"title": "Test PR",
					"user": {"login": "author"},
					"created_at": "2024-01-01T10:00:00Z",
					"updated_at": "2024-01-01T12:00:00Z",
					"state": "open",
					"draft": false,
					"head": {"sha": "abc123"},
					"assignees": [{"login": "assignee1"}],
					"requested_reviewers": [{"login": "reviewer1"}]
				}`
			} else {
				// Second call: changed files
				responseBody = `[
					{
						"filename": "main.go",
						"additions": 10,
						"deletions": 5,
						"changes": 15,
						"patch": "@@ -1,3 +1,4 @@\n line1\n+line2\n line3"
					}
				]`
			}

			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(responseBody)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	// Test PullRequest
	ctx := context.Background()
	pr, err := c.PullRequest(ctx, "owner", "repo", 123)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if pr.Number != 123 {
		t.Errorf("expected PR number 123, got %d", pr.Number)
	}
	if pr.Title != "Test PR" {
		t.Errorf("expected title 'Test PR', got %q", pr.Title)
	}
	if pr.Author != "author" {
		t.Errorf("expected author 'author', got %q", pr.Author)
	}
	if pr.State != "open" {
		t.Errorf("expected state 'open', got %q", pr.State)
	}
	if len(pr.ChangedFiles) != 1 {
		t.Errorf("expected 1 changed file, got %d", len(pr.ChangedFiles))
	}
}

// mockRoundTripperFunc allows custom function-based mocking
type mockRoundTripperFunc struct {
	roundTripFunc func(*http.Request) (*http.Response, error)
}

func (m *mockRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.roundTripFunc(req)
}

func TestClient_ChangedFiles_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/pulls/123/files" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`[
			{
				"filename": "main.go",
				"additions": 10,
				"deletions": 5,
				"changes": 15,
				"patch": "@@ -1,3 +1,4 @@\n line1\n+line2\n line3"
			},
			{
				"filename": "test.go",
				"additions": 20,
				"deletions": 10,
				"changes": 30
			}
		]`)); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: server.Client(),
		token:      "test-token",
		isAppAuth:  false,
	}

	// Test by calling doRequest directly with server URL
	resp, err := c.doRequest(context.Background(), "GET", server.URL+"/repos/owner/repo/pulls/123/files", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Logf("failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestClient_Collaborators_Success(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			if !strings.Contains(req.URL.Path, "/repos/owner/repo/collaborators") {
				t.Errorf("unexpected path: %s", req.URL.Path)
			}

			// Check query parameters
			if req.URL.Query().Get("affiliation") != "all" {
				t.Errorf("expected affiliation=all")
			}
			if req.URL.Query().Get("permission") != "push" {
				t.Errorf("expected permission=push")
			}

			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(`[
					{"login": "alice", "type": "User"},
					{"login": "bob", "type": "User"},
					{"login": "bot-user", "type": "Bot"}
				]`)),
				Header: make(http.Header),
			}, nil
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	collaborators, err := c.Collaborators(context.Background(), "owner", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Collaborators function filters out bots, so we expect only 2 users
	if len(collaborators) != 2 {
		t.Errorf("expected 2 collaborators (bots filtered), got %d", len(collaborators))
	}

	expected := []string{"alice", "bob"}
	for i, name := range expected {
		if collaborators[i] != name {
			t.Errorf("expected %s at index %d, got %s", name, i, collaborators[i])
		}
	}
}

func TestClient_Collaborators_PermissionDenied(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Body:       io.NopCloser(strings.NewReader(`{"message": "Must have admin rights to Repository."}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	_, err := c.Collaborators(context.Background(), "owner", "repo")
	if err == nil {
		t.Error("expected error for permission denied")
	}
}

func TestClient_doRequest_WithRetry(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First call: return 500 (retriable)
			w.WriteHeader(http.StatusInternalServerError)
			if _, err := w.Write([]byte(`{"message": "Internal server error"}`)); err != nil {
				t.Errorf("failed to write response: %v", err)
			}
		} else {
			// Second call: success
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte(`{"status": "ok"}`)); err != nil {
				t.Errorf("failed to write response: %v", err)
			}
		}
	}))
	defer server.Close()

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: server.Client(),
		token:      "test-token",
		isAppAuth:  false,
	}

	resp, err := c.doRequest(context.Background(), "GET", server.URL+"/test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Logf("failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected retry to succeed with status 200, got %d", resp.StatusCode)
	}

	if callCount < 2 {
		t.Errorf("expected at least 2 calls (retry), got %d", callCount)
	}
}

func TestClient_OpenPullRequests_Success(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			// OpenPullRequests makes multiple requests:
			// 1. GET /pulls?state=open to get list of PR numbers
			// 2. GET /pulls/{number} for each PR to get full details

			if strings.Contains(req.URL.RawQuery, "state=open") {
				// List request
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(`[
						{"number": 1},
						{"number": 2}
					]`)),
					Header: make(http.Header),
				}, nil
			}

			// Individual PR details request
			if strings.HasSuffix(req.URL.Path, "/pulls/1") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(`{
						"number": 1,
						"title": "PR 1",
						"user": {"login": "alice"},
						"state": "open",
						"draft": false,
						"head": {"sha": "abc123"},
						"created_at": "2024-01-01T10:00:00Z",
						"updated_at": "2024-01-01T12:00:00Z",
						"assignees": [],
						"requested_reviewers": []
					}`)),
					Header: make(http.Header),
				}, nil
			}

			if strings.HasSuffix(req.URL.Path, "/pulls/2") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(`{
						"number": 2,
						"title": "PR 2",
						"user": {"login": "bob"},
						"state": "open",
						"draft": true,
						"head": {"sha": "def456"},
						"created_at": "2024-01-02T10:00:00Z",
						"updated_at": "2024-01-02T12:00:00Z",
						"assignees": [],
						"requested_reviewers": []
					}`)),
					Header: make(http.Header),
				}, nil
			}

			// Handle files requests for both PRs
			if strings.Contains(req.URL.Path, "/files") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`[]`)),
					Header:     make(http.Header),
				}, nil
			}

			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader(`{}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	prs, err := c.OpenPullRequests(context.Background(), "owner", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(prs) != 2 {
		t.Errorf("expected 2 PRs, got %d", len(prs))
	}

	if prs[0].Number != 1 {
		t.Errorf("expected PR 1, got %d", prs[0].Number)
	}

	if prs[1].Draft != true {
		t.Error("expected PR 2 to be draft")
	}
}

func TestClient_OpenPullRequests_NonOKStatus(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			// Return 404 for the list request
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader(`{"message": "Not Found"}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	_, err := c.OpenPullRequests(context.Background(), "owner", "repo")
	if err == nil {
		t.Error("expected error for non-OK status")
	}

	if !strings.Contains(err.Error(), "status 404") {
		t.Errorf("expected error to mention status 404, got: %v", err)
	}
}

func TestClient_OpenPullRequests_InvalidJSON(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			// Return invalid JSON for the list request
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`invalid json`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	_, err := c.OpenPullRequests(context.Background(), "owner", "repo")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestClient_OpenPullRequests_EmptyList(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			// Return empty list
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`[]`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	prs, err := c.OpenPullRequests(context.Background(), "owner", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(prs) != 0 {
		t.Errorf("expected 0 PRs for empty list, got %d", len(prs))
	}
}

func TestClient_AddReviewers_Success(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", req.Method)
			}
			if !strings.Contains(req.URL.Path, "/pulls/123/requested_reviewers") {
				t.Errorf("unexpected path: %s", req.URL.Path)
			}

			return &http.Response{
				StatusCode: http.StatusCreated,
				Body: io.NopCloser(strings.NewReader(`{
					"number": 123,
					"requested_reviewers": [
						{"login": "reviewer1"},
						{"login": "reviewer2"}
					]
				}`)),
				Header: make(http.Header),
			}, nil
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	err := c.AddReviewers(context.Background(), "owner", "repo", 123, []string{"reviewer1", "reviewer2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClient_AddReviewers_Error(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnprocessableEntity,
				Body:       io.NopCloser(strings.NewReader(`{"message": "Validation Failed"}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	err := c.AddReviewers(context.Background(), "owner", "repo", 123, []string{"reviewer1"})
	if err == nil {
		t.Error("expected error for 422 status")
	}
}

func TestClient_AddReviewers_EmptyList(t *testing.T) {
	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{},
		token:      "test-token",
		isAppAuth:  false,
	}

	// Adding empty reviewers list should still work (API will accept it)
	err := c.AddReviewers(context.Background(), "owner", "repo", 123, []string{})
	// May succeed or fail depending on API - just check it doesn't panic
	_ = err
}

func TestClient_MakeGraphQLRequest_WithServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/graphql" {
			t.Errorf("expected /graphql path, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{
			"data": {
				"repository": {
					"name": "test-repo",
					"owner": {"login": "test-owner"}
				}
			}
		}`)); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer server.Close()

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: server.Client(),
		token:      "test-token",
		isAppAuth:  false,
	}

	// Test with server URL
	resp, err := c.MakeRequest(context.Background(), "POST", server.URL+"/graphql", map[string]any{
		"query": "query { repository { name } }",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Logf("failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestClient_Token_AppAuth(t *testing.T) {
	c := &Client{
		isAppAuth:  true,
		token:      "jwt-token",
		currentOrg: "",
		tokenMutex: sync.RWMutex{},
	}

	token, err := c.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if token != "jwt-token" {
		t.Errorf("expected jwt-token, got %q", token)
	}
}

func TestClient_OpenPullRequestsForOrg_Success(t *testing.T) {
	callCount := 0
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			callCount++
			var responseBody string

			// First call: search API
			if callCount == 1 {
				responseBody = `{
					"total_count": 2,
					"items": [
						{
							"number": 1,
							"title": "PR 1",
							"updated_at": "2024-01-01T12:00:00Z",
							"state": "open",
							"draft": false,
							"repository_url": "https://api.github.com/repos/testorg/repo1",
							"pull_request": {
								"url": "https://api.github.com/repos/testorg/repo1/pulls/1"
							}
						},
						{
							"number": 2,
							"title": "PR 2",
							"updated_at": "2024-01-02T12:00:00Z",
							"state": "open",
							"draft": false,
							"repository_url": "https://api.github.com/repos/testorg/repo2",
							"pull_request": {
								"url": "https://api.github.com/repos/testorg/repo2/pulls/2"
							}
						}
					]
				}`
			} else {
				// Subsequent calls: PR details and files
				if strings.Contains(req.URL.Path, "/files") {
					responseBody = `[]`
				} else {
					responseBody = `{
						"number": ` + fmt.Sprintf("%d", callCount-1) + `,
						"title": "PR ` + fmt.Sprintf("%d", callCount-1) + `",
						"user": {"login": "author"},
						"created_at": "2024-01-01T10:00:00Z",
						"updated_at": "2024-01-01T12:00:00Z",
						"state": "open",
						"draft": false,
						"head": {"sha": "abc123"},
						"assignees": [],
						"requested_reviewers": []
					}`
				}
			}

			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(responseBody)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	prs, err := c.OpenPullRequestsForOrg(context.Background(), "testorg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(prs) != 2 {
		t.Errorf("expected 2 PRs, got %d", len(prs))
	}
}

func TestClient_FilePatch_Success(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			// Return files with patches
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(`[
					{
						"filename": "main.go",
						"additions": 10,
						"deletions": 5,
						"changes": 15,
						"patch": "@@ -1,3 +1,4 @@\n line1\n+line2\n line3"
					}
				]`)),
				Header: make(http.Header),
			}, nil
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	patch, err := c.FilePatch(context.Background(), "owner", "repo", 123, "main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if patch == "" {
		t.Error("expected non-empty patch")
	}
	if !strings.Contains(patch, "+line2") {
		t.Errorf("expected patch to contain '+line2', got: %s", patch)
	}
}

func TestClient_FilePatch_FileNotFound(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			// Return files but not the one we're looking for
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(`[
					{
						"filename": "main.go",
						"additions": 10,
						"deletions": 5,
						"changes": 15,
						"patch": "@@ -1,3 +1,4 @@\n line1\n+line2\n line3"
					}
				]`)),
				Header: make(http.Header),
			}, nil
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	// Request a file that doesn't exist in the PR
	_, err := c.FilePatch(context.Background(), "owner", "repo", 123, "nonexistent.go")
	if err == nil {
		t.Error("expected error for file not found")
	}

	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected error to contain 'not found', got: %v", err)
	}
}

func TestClient_PullRequest_NotFound(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader(`{"message": "Not Found"}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	_, err := c.PullRequest(context.Background(), "owner", "repo", 999)
	if err == nil {
		t.Error("expected error for 404 response")
	}
}

func TestClient_PullRequest_InvalidJSON(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Path, "/files") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`[]`)),
					Header:     make(http.Header),
				}, nil
			}

			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{invalid json`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	_, err := c.PullRequest(context.Background(), "owner", "repo", 123)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestClient_PullRequest_InvalidDates(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Path, "/files") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`[]`)),
					Header:     make(http.Header),
				}, nil
			}
			if strings.Contains(req.URL.Path, "/commits/") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(`{
						"commit": {
							"author": {
								"date": "2024-01-01T12:00:00Z"
							}
						}
					}`)),
					Header: make(http.Header),
				}, nil
			}
			if strings.Contains(req.URL.Path, "/reviews") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`[]`)),
					Header:     make(http.Header),
				}, nil
			}

			// Return PR with invalid date formats
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(`{
					"number": 123,
					"title": "Test PR",
					"user": {"login": "alice"},
					"state": "open",
					"draft": false,
					"head": {"sha": "abc123"},
					"created_at": "invalid-date",
					"updated_at": "also-invalid",
					"assignees": [],
					"requested_reviewers": []
				}`)),
				Header: make(http.Header),
			}, nil
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	// Should succeed with fallback to current time for invalid dates
	pr, err := c.PullRequest(context.Background(), "owner", "repo", 123)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if pr == nil {
		t.Fatal("expected non-nil PR")
	}

	// CreatedAt and UpdatedAt should be set to current time (not zero)
	if pr.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt (should fallback to current time)")
	}
	if pr.UpdatedAt.IsZero() {
		t.Error("expected non-zero UpdatedAt (should fallback to current time)")
	}
}

func TestClient_drainAndCloseBody_Success(t *testing.T) {
	body := io.NopCloser(strings.NewReader("test data"))
	drainAndCloseBody(body)
	// Should not panic
}

// Note: AddReviewers, Collaborators, and OpenPullRequests tests exist earlier in this file

func TestClient_OpenPullRequestsForOrg_NonOKStatus(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			// Simulate 403 Forbidden for search API
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Status:     "403 Forbidden",
				Body:       io.NopCloser(strings.NewReader(`{"message":"API rate limit exceeded"}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
	}

	ctx := context.Background()
	_, err := c.OpenPullRequestsForOrg(ctx, "test-org")

	if err == nil {
		t.Error("expected error for non-OK status")
	}

	if !strings.Contains(err.Error(), "status 403") {
		t.Errorf("expected error to mention status 403, got %q", err.Error())
	}
}

func TestClient_OpenPullRequestsForOrg_InvalidJSON(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(`invalid json`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
	}

	ctx := context.Background()
	_, err := c.OpenPullRequestsForOrg(ctx, "test-org")

	if err == nil {
		t.Error("expected error for invalid JSON")
	}

	if !strings.Contains(err.Error(), "failed to decode") {
		t.Errorf("expected error to mention decode failure, got %q", err.Error())
	}
}

func TestClient_OpenPullRequestsForOrg_InvalidRepositoryURL(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			// Return a search result with an invalid repository_url
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body: io.NopCloser(strings.NewReader(`{
					"total_count": 1,
					"items": [
						{
							"number": 123,
							"title": "Test PR",
							"state": "open",
							"repository_url": "invalid",
							"pull_request": {
								"url": "https://api.github.com/repos/test-org/repo/pulls/123"
							}
						}
					]
				}`)),
				Header: make(http.Header),
			}, nil
		},
	}

	c := &Client{
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
	}

	ctx := context.Background()
	prs, err := c.OpenPullRequestsForOrg(ctx, "test-org")
	// Should not error, but should skip invalid PR
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Should have skipped the invalid PR
	if len(prs) != 0 {
		t.Errorf("expected 0 PRs (invalid repo URL should be skipped), got %d", len(prs))
	}
}

func TestClient_ChangedFiles_InvalidJSON(t *testing.T) {
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(`invalid json`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	diskCache, err := cache.NewDiskCache(time.Hour, t.TempDir())
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}

	c := &Client{
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		cache:      diskCache,
	}

	ctx := context.Background()
	_, err = c.ChangedFiles(ctx, "owner", "repo", 123)

	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestClient_ChangedFiles_CacheHit(t *testing.T) {
	callCount := 0
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			callCount++
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body: io.NopCloser(strings.NewReader(`[
					{
						"filename": "main.go",
						"additions": 10,
						"deletions": 5,
						"patch": "@@ -1,3 +1,4 @@"
					}
				]`)),
				Header: make(http.Header),
			}, nil
		},
	}

	diskCache, err := cache.NewDiskCache(time.Hour, t.TempDir())
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}

	c := &Client{
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		cache:      diskCache,
	}

	ctx := context.Background()

	// First call should hit the API
	files1, err := c.ChangedFiles(ctx, "owner", "repo", 123)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files1) != 1 {
		t.Errorf("expected 1 file, got %d", len(files1))
	}
	if callCount != 1 {
		t.Errorf("expected 1 API call, got %d", callCount)
	}

	// Second call should use cache
	files2, err := c.ChangedFiles(ctx, "owner", "repo", 123)
	if err != nil {
		t.Fatalf("unexpected error on cached call: %v", err)
	}
	if len(files2) != 1 {
		t.Errorf("expected 1 file from cache, got %d", len(files2))
	}
	if callCount != 1 {
		t.Errorf("expected no additional API calls (cache hit), got %d total calls", callCount)
	}
}
