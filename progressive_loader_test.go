package main

import (
	"context"
	"encoding/base64"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProgressiveLoaderCodeOwnersPriority(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(handleTestRequest))
	defer server.Close()

	// Create a GitHub client that will use our test server
	// We'll override the URL in makeRequest by using a wrapper
	originalClient := &http.Client{Timeout: 10 * time.Second}
	testClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &testTransport{
			baseURL: server.URL,
			rt:      originalClient.Transport,
		},
	}

	client := &GitHubClient{
		httpClient: testClient,
		cache:      &cache{entries: make(map[string]cacheEntry)},
	}

	// Create a ProgressiveLoader
	loader := &ProgressiveLoader{
		client: client,
	}

	// Create a ReviewerFinder
	rf := &ReviewerFinder{
		client: client,
	}

	ctx := context.Background()

	// Test 1: Verify CODEOWNERS is fetched and parsed correctly
	t.Run("FetchAndParseCodeOwners", func(t *testing.T) {
		owners := loader.fetchCodeOwners(ctx, "testowner", "testrepo")
		if owners == nil {
			t.Fatal("Expected CODEOWNERS to be fetched")
		}

		// Check specific patterns
		if defaultOwners, ok := owners["*"]; !ok || len(defaultOwners) != 1 || defaultOwners[0] != "defaultowner" {
			t.Errorf("Expected default owner, got %v", defaultOwners)
		}

		if jsOwners, ok := owners["*.js"]; !ok || len(jsOwners) != 2 {
			t.Errorf("Expected 2 JS owners, got %v", jsOwners)
		}

		if apiOwners, ok := owners["/api/"]; !ok || len(apiOwners) != 2 {
			t.Errorf("Expected 2 API owners, got %v", apiOwners)
		}
	})

	// Test 2: Verify CODEOWNERS takes priority in LoadReviewers
	t.Run("CodeOwnersPriority", func(t *testing.T) {
		// Create a PR that modifies JavaScript files
		pr := &PullRequest{
			Owner:      "testowner",
			Repository: "testrepo",
			Number:     1,
			ChangedFiles: []ChangedFile{
				{Filename: "src/app.js"},
				{Filename: "src/components/button.js"},
			},
		}

		candidates := loader.checkCodeOwners(ctx, rf, pr)

		// CODEOWNERS should return results immediately without making additional API calls
		if len(candidates) == 0 {
			t.Fatal("Expected candidates from CODEOWNERS")
		}

		expectedReviewers := []string{"frontend-team", "jsexpert", "componentlead"}
		verifyReviewers(t, candidates, expectedReviewers)
	})

	// Test 3: Verify Go files get correct owners
	t.Run("GoFileOwners", func(t *testing.T) {
		pr := &PullRequest{
			Owner:      "testowner",
			Repository: "testrepo",
			Number:     2,
			ChangedFiles: []ChangedFile{
				{Filename: "main.go"},
				{Filename: "api/handler.go"},
			},
		}

		candidates := loader.checkCodeOwners(ctx, rf, pr)

		expectedReviewers := []string{"backend-team", "goexpert", "api-team", "apimaster"}
		verifyReviewers(t, candidates, expectedReviewers)
	})

	// Test 4: Verify caching works
	t.Run("CodeOwnersCaching", func(t *testing.T) {
		// First call should fetch from API
		owners1 := loader.fetchCodeOwners(ctx, "testowner", "testrepo")
		if owners1 == nil {
			t.Fatal("Expected CODEOWNERS to be fetched")
		}

		// Second call should use cache (we can't easily verify this without instrumenting the code,
		// but we can at least verify it returns the same result)
		pr := &PullRequest{
			Owner:      "testowner",
			Repository: "testrepo",
			Number:     3,
			ChangedFiles: []ChangedFile{
				{Filename: "README.md"},
			},
		}

		candidates := loader.checkCodeOwners(ctx, rf, pr)

		expectedReviewers := []string{"docs-team"}
		verifyReviewers(t, candidates, expectedReviewers)
	})
}

func handleTestRequest(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	switch path {
	case "/repos/testowner/testrepo/contents/.github/CODEOWNERS":
		handleCodeOwnersRequest(w)
	case "/repos/testowner/testrepo/contents/CODEOWNERS",
		"/repos/testowner/testrepo/contents/docs/CODEOWNERS":
		w.WriteHeader(http.StatusNotFound)
	default:
		switch {
		case strings.HasPrefix(path, "/users/"):
			handleUserRequest(w, path)
		case strings.HasPrefix(path, "/repos/testowner/testrepo/collaborators/"):
			handleCollaboratorRequest(w, path)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func handleCodeOwnersRequest(w http.ResponseWriter) {
	content := getCodeOwnersContent()
	encodedContent := base64.StdEncoding.EncodeToString([]byte(content))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(`{"content":"` + encodedContent + `"}`)); err != nil {
		log.Printf("Failed to write response: %v", err)
	}
}

func handleUserRequest(w http.ResponseWriter, path string) {
	validUsers := map[string]bool{
		"/users/defaultowner":  true,
		"/users/frontend-team": true,
		"/users/jsexpert":      true,
		"/users/componentlead": true,
		"/users/backend-team":  true,
		"/users/goexpert":      true,
		"/users/api-team":      true,
		"/users/apimaster":     true,
		"/users/docs-team":     true,
		"/users/docslead":      true,
	}

	if validUsers[path] {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"type":"User","login":"` + strings.TrimPrefix(path, "/users/") + `"}`)); err != nil {
			log.Printf("Failed to write response: %v", err)
		}
	} else {
		w.WriteHeader(http.StatusNotFound)
	}
}

func handleCollaboratorRequest(w http.ResponseWriter, path string) {
	// Extract username from path like "/repos/testowner/testrepo/collaborators/username"
	parts := strings.Split(path, "/")

	if len(parts) < 6 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	username := parts[5] // Index 5 because of leading slash: ["", "repos", "testowner", "testrepo", "collaborators", "username"]

	// All test users have write access for testing purposes
	collaboratorsWithAccess := map[string]bool{
		"defaultowner":  true,
		"frontend-team": true,
		"jsexpert":      true,
		"componentlead": true,
		"backend-team":  true,
		"goexpert":      true,
		"api-team":      true,
		"apimaster":     true,
		"docs-team":     true,
		"docslead":      true,
	}

	if collaboratorsWithAccess[username] {
		// GitHub returns 204 No Content for valid collaborators
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusNotFound)
	}
}

func getCodeOwnersContent() string {
	return `# This is a CODEOWNERS file
# These are the default owners for everything
* @defaultowner

# Frontend files
*.js @frontend-team @jsexpert
*.css @frontend-team
/src/components/ @frontend-team @componentlead

# Backend files
*.go @backend-team @goexpert
/api/ @api-team @apimaster

# Documentation
*.md @docs-team
/docs/ @docs-team @docslead
`
}

func verifyReviewers(t *testing.T, candidates []ReviewerCandidate, expected []string) {
	t.Helper()
	found := make(map[string]bool)
	for _, candidate := range candidates {
		found[candidate.Username] = true
	}

	for _, expectedReviewer := range expected {
		if !found[expectedReviewer] {
			t.Errorf("Expected %s to be suggested", expectedReviewer)
		}
	}
}

// testTransport rewrites GitHub API URLs to point to our test server
type testTransport struct {
	rt      http.RoundTripper
	baseURL string
}

func (t *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite the URL to point to our test server
	if strings.HasPrefix(req.URL.String(), "https://api.github.com") {
		newURL := strings.Replace(req.URL.String(), "https://api.github.com", t.baseURL, 1)
		newReq, err := http.NewRequest(req.Method, newURL, req.Body)
		if err != nil {
			return nil, err
		}
		newReq.Header = req.Header
		req = newReq
	}

	if t.rt == nil {
		return http.DefaultTransport.RoundTrip(req)
	}
	return t.rt.RoundTrip(req)
}
