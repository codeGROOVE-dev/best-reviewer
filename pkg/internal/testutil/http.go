package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// MockHTTPDoer implements github.HTTPDoer for testing.
// It's programmable - you can configure responses for specific requests.
type MockHTTPDoer struct {
	responses map[string]*http.Response
	errors    map[string]error
	calls     []HTTPCall
	mu        sync.RWMutex
}

// HTTPCall records a single HTTP call.
type HTTPCall struct {
	Method string
	URL    string
	Body   []byte
}

// NewMockHTTPDoer creates a new MockHTTPDoer.
func NewMockHTTPDoer() *MockHTTPDoer {
	return &MockHTTPDoer{
		responses: make(map[string]*http.Response),
		errors:    make(map[string]error),
		calls:     []HTTPCall{},
	}
}

// Do executes the HTTP request and returns the configured response.
func (m *MockHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Record the call
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			// If we can't read the body, return an error response
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader(`{"error":"failed to read request body"}`)),
				Header:     make(http.Header),
			}, nil
		}
		req.Body = io.NopCloser(bytes.NewReader(body)) // Restore body
	}
	m.calls = append(m.calls, HTTPCall{
		Method: req.Method,
		URL:    req.URL.String(),
		Body:   body,
	})

	key := m.makeKey(req.Method, req.URL.String())

	// Check for configured error
	if err, ok := m.errors[key]; ok {
		return nil, err
	}

	// Check for configured response
	if resp, ok := m.responses[key]; ok {
		return resp, nil
	}

	// Default 404 response
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Status:     "404 Not Found",
		Body:       io.NopCloser(strings.NewReader(`{"message":"not found"}`)),
		Header:     make(http.Header),
	}, nil
}

// SetResponse configures a response for a specific method and URL.
func (m *MockHTTPDoer) SetResponse(method, url string, statusCode int, body any) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			panic(fmt.Sprintf("failed to marshal response body: %v", err))
		}
	}

	key := m.makeKey(method, url)
	m.responses[key] = &http.Response{
		StatusCode: statusCode,
		Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		Body:       io.NopCloser(bytes.NewReader(bodyBytes)),
		Header:     make(http.Header),
	}
}

// SetError configures an error for a specific method and URL.
func (m *MockHTTPDoer) SetError(method, url string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := m.makeKey(method, url)
	m.errors[key] = err
}

// Calls returns all recorded HTTP calls.
func (m *MockHTTPDoer) Calls() []HTTPCall {
	m.mu.RLock()
	defer m.mu.RUnlock()

	calls := make([]HTTPCall, len(m.calls))
	copy(calls, m.calls)
	return calls
}

// Reset clears all configured responses and recorded calls.
func (m *MockHTTPDoer) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.responses = make(map[string]*http.Response)
	m.errors = make(map[string]error)
	m.calls = []HTTPCall{}
}

func (*MockHTTPDoer) makeKey(method, url string) string {
	return method + ":" + url
}
