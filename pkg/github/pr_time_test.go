package github

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClient_lastCommitTime_Success(t *testing.T) {
	mockTransport := &mockRoundTripper{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{
				"commit": {
					"author": {
						"date": "2024-01-01T12:00:00Z"
					}
				}
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

	lastCommit, err := c.lastCommitTime(context.Background(), "owner", "repo", "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if lastCommit.IsZero() {
		t.Error("expected non-zero commit time")
	}

	expected := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	if !lastCommit.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, lastCommit)
	}
}

func TestClient_lastReviewTime_Success(t *testing.T) {
	mockTransport := &mockRoundTripper{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`[
				{
					"submitted_at": "2024-01-01T10:00:00Z",
					"state": "COMMENTED"
				},
				{
					"submitted_at": "2024-01-02T12:00:00Z",
					"state": "APPROVED"
				}
			]`)),
			Header: make(http.Header),
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	lastReview, err := c.lastReviewTime(context.Background(), "owner", "repo", 123)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if lastReview.IsZero() {
		t.Error("expected non-zero review time")
	}

	// Should return the latest review time
	expected := time.Date(2024, 1, 2, 12, 0, 0, 0, time.UTC)
	if !lastReview.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, lastReview)
	}
}

func TestClient_lastReviewTime_NoReviews(t *testing.T) {
	mockTransport := &mockRoundTripper{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`[]`)),
			Header:     make(http.Header),
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	lastReview, err := c.lastReviewTime(context.Background(), "owner", "repo", 123)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !lastReview.IsZero() {
		t.Errorf("expected zero time for no reviews, got %v", lastReview)
	}
}

func TestClient_lastCommitTime_InvalidJSON(t *testing.T) {
	mockTransport := &mockRoundTripper{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`invalid json`)),
			Header:     make(http.Header),
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	_, err := c.lastCommitTime(context.Background(), "owner", "repo", "abc123")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestClient_lastCommitTime_InvalidDateFormat(t *testing.T) {
	mockTransport := &mockRoundTripper{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{
				"commit": {
					"author": {
						"date": "invalid-date"
					}
				}
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

	_, err := c.lastCommitTime(context.Background(), "owner", "repo", "abc123")
	if err == nil {
		t.Error("expected error for invalid date format")
	}
}

func TestClient_lastReviewTime_InvalidJSON(t *testing.T) {
	mockTransport := &mockRoundTripper{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`invalid json`)),
			Header:     make(http.Header),
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	_, err := c.lastReviewTime(context.Background(), "owner", "repo", 123)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestClient_lastReviewTime_InvalidDateInReview(t *testing.T) {
	mockTransport := &mockRoundTripper{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`[
				{
					"submitted_at": "invalid-date",
					"state": "APPROVED"
				}
			]`)),
			Header: make(http.Header),
		},
	}

	c := &Client{
		cache:      mustNewDiskCache(t),
		httpClient: &http.Client{Transport: mockTransport},
		token:      "test-token",
		isAppAuth:  false,
	}

	// Should not error, but should return zero time (invalid dates are skipped)
	lastReview, err := c.lastReviewTime(context.Background(), "owner", "repo", 123)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !lastReview.IsZero() {
		t.Errorf("expected zero time when all dates are invalid, got %v", lastReview)
	}
}

func TestClient_MakeCacheKey(t *testing.T) {
	key := makeCacheKey("type", "owner", "repo")
	if key == "" {
		t.Error("expected non-empty cache key")
	}

	// Keys should be different for different inputs
	key2 := makeCacheKey("type2", "owner", "repo")
	if key == key2 {
		t.Error("expected different cache keys for different inputs")
	}
}
