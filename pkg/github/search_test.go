package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClient_searchPRCount_Success(t *testing.T) {
	mockTransport := &mockRoundTripper{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{
				"total_count": 5,
				"items": []
			}`)),
			Header: make(http.Header),
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	count, err := c.searchPRCount(context.Background(), "is:pr author:alice org:testorg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if count != 5 {
		t.Errorf("expected count 5, got %d", count)
	}
}

func TestClient_searchPRCount_ZeroResults(t *testing.T) {
	mockTransport := &mockRoundTripper{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{
				"total_count": 0,
				"items": []
			}`)),
			Header: make(http.Header),
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	count, err := c.searchPRCount(context.Background(), "is:pr author:nobody org:testorg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}
}

func TestClient_OpenPRCount_WithCache(t *testing.T) {
	callCount := 0
	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			callCount++
			// OpenPRCount makes 2 search API calls: assigned and review-requested
			// First call returns 2 assigned PRs, second call returns 1 review-requested PR
			var count int
			if strings.Contains(req.URL.RawQuery, "assignee") {
				count = 2
			} else if strings.Contains(req.URL.RawQuery, "review-requested") {
				count = 1
			}

			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{
					"total_count": %d,
					"items": []
				}`, count))),
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

	// First call - should hit API (makes 2 HTTP requests internally)
	cacheTTL := time.Hour // Use non-zero TTL for caching
	count1, err := c.OpenPRCount(context.Background(), "testorg", "alice", cacheTTL)
	if err != nil {
		t.Fatalf("unexpected error on first call: %v", err)
	}
	if count1 != 3 {
		t.Errorf("expected count 3 (2 assigned + 1 review), got %d", count1)
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls, got %d", callCount)
	}

	// Second call with same params - should use cache (won't hit mock again)
	callCount = 0
	count2, err := c.OpenPRCount(context.Background(), "testorg", "alice", cacheTTL)
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if count2 != 3 {
		t.Errorf("expected cached count 3, got %d", count2)
	}
	if callCount != 0 {
		t.Errorf("expected cache hit (0 API calls), got %d calls", callCount)
	}
}

func TestClient_BatchOpenPRCount_MultipleUsers(t *testing.T) {
	expectedCounts := map[string]int{
		"alice":   5,
		"bob":     3,
		"charlie": 1,
	}

	mockTransport := &mockRoundTripperFunc{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			// BatchOpenPRCount uses GraphQL
			// Return GraphQL response with assigned/review counts for each user
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(`{
					"data": {
						"assigned0": {"issueCount": 5},
						"review0": {"issueCount": 0},
						"assigned1": {"issueCount": 3},
						"review1": {"issueCount": 0},
						"assigned2": {"issueCount": 1},
						"review2": {"issueCount": 0}
					}
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

	users := []string{"alice", "bob", "charlie"}
	counts, err := c.BatchOpenPRCount(context.Background(), "testorg", users, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(counts) != 3 {
		t.Errorf("expected 3 results, got %d", len(counts))
	}

	for _, user := range users {
		if counts[user] != expectedCounts[user] {
			t.Errorf("expected count %d for %s, got %d", expectedCounts[user], user, counts[user])
		}
	}
}
