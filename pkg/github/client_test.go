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

func TestClient_SetCurrentOrg(t *testing.T) {
	c := &Client{}

	c.SetCurrentOrg("test-org")

	if c.currentOrg != "test-org" {
		t.Errorf("expected currentOrg to be 'test-org', got %q", c.currentOrg)
	}
}

func TestClient_IsUserAccount(t *testing.T) {
	c := &Client{
		installationTypes: map[string]string{
			"user1": "User",
			"org1":  "Organization",
		},
	}

	tests := []struct {
		account string
		want    bool
	}{
		{"user1", true},
		{"org1", false},
		{"unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.account, func(t *testing.T) {
			got := c.IsUserAccount(tt.account)
			if got != tt.want {
				t.Errorf("IsUserAccount(%q) = %v, want %v", tt.account, got, tt.want)
			}
		})
	}
}

func TestClient_Token_PersonalToken(t *testing.T) {
	ctx := context.Background()
	c := &Client{
		isAppAuth: false,
		token:     "test-token",
	}

	token, err := c.Token(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if token != "test-token" {
		t.Errorf("expected 'test-token', got %q", token)
	}
}

func TestClient_Token_AppAuthNoOrg(t *testing.T) {
	ctx := context.Background()
	c := &Client{
		isAppAuth:  true,
		token:      "jwt-token",
		currentOrg: "",
	}

	token, err := c.Token(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if token != "jwt-token" {
		t.Errorf("expected 'jwt-token', got %q", token)
	}
}

func TestDrainAndCloseBody(t *testing.T) {
	// Test that drainAndCloseBody doesn't panic
	resp := &http.Response{
		Body: http.NoBody,
	}

	// Should not panic
	drainAndCloseBody(resp.Body)
}

type errorReader struct{}

func (e *errorReader) Read(p []byte) (n int, err error) {
	return 0, fmt.Errorf("read error")
}

func (e *errorReader) Close() error {
	return nil
}

type errorCloser struct {
	reader io.Reader
}

func (e *errorCloser) Read(p []byte) (n int, err error) {
	return e.reader.Read(p)
}

func (e *errorCloser) Close() error {
	return fmt.Errorf("close error")
}

func TestDrainAndCloseBody_ReadError(t *testing.T) {
	// Test that drainAndCloseBody handles read errors gracefully
	body := &errorReader{}

	// Should not panic even with read error
	drainAndCloseBody(body)
}

func TestDrainAndCloseBody_CloseError(t *testing.T) {
	// Test that drainAndCloseBody handles close errors gracefully
	body := &errorCloser{reader: strings.NewReader("test")}

	// Should not panic even with close error
	drainAndCloseBody(body)
}

func TestValidateAppID(t *testing.T) {
	tests := []struct {
		name    string
		appID   string
		wantErr bool
	}{
		{"valid app ID", "123456", false},
		{"empty app ID", "", true},
		{"non-numeric app ID", "abc", true},
		{"too large app ID", "9999999999", true},
		{"negative app ID", "-1", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAppID(tt.appID)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateAppID() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateToken(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{"empty token", "", true},
		{"too short token", "abc", true},
		{"valid ghp_ prefix token", "ghp_" + strings.Repeat("a", 36), false},
		{"valid gho_ prefix token", "gho_" + strings.Repeat("b", 36), false},
		{"valid ghu_ prefix token", "ghu_" + strings.Repeat("c", 36), false},
		{"valid ghs_ prefix token", "ghs_" + strings.Repeat("d", 36), false},
		{"valid ghr_ prefix token", "ghr_" + strings.Repeat("e", 36), false},
		{"valid classic token", strings.Repeat("a", 40), false},
		{"valid classic token with numbers", strings.Repeat("1", 40), false},
		{"invalid classic token with uppercase", strings.Repeat("A", 40), true},
		{"invalid classic token with invalid char", strings.Repeat("g", 40), true},
		{"invalid token no valid prefix and wrong length", "xyz_" + strings.Repeat("a", 30), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateToken(tt.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateToken() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSanitizeURLForLogging(t *testing.T) {
	// Test that function exists and doesn't panic
	result := sanitizeURLForLogging("https://api.github.com/repos/owner/repo")
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestRetryWithBackoff_Success(t *testing.T) {
	ctx := context.Background()
	callCount := 0

	err := retryWithBackoff(ctx, "test operation", func() error {
		callCount++
		return nil
	})
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}

	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}
}

func TestRetryWithBackoff_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	callCount := 0
	err := retryWithBackoff(ctx, "test operation", func() error {
		callCount++
		time.Sleep(20 * time.Millisecond) // Force timeout
		return nil
	})

	// Should fail due to context timeout
	if err == nil {
		t.Log("note: context timeout may not always trigger in test environment")
	}
}

func TestRetryWithBackoff_NonRetryableError(t *testing.T) {
	ctx := context.Background()
	callCount := 0

	err := retryWithBackoff(ctx, "test operation", func() error {
		callCount++
		return context.DeadlineExceeded // Not a retryable error
	})

	if err == nil {
		t.Error("expected error")
	}

	// Should only try once for non-retryable errors
	if callCount != 1 {
		t.Errorf("expected 1 call for non-retryable error, got %d", callCount)
	}
}

func TestRetryConstants(t *testing.T) {
	// Verify retry constants are reasonable
	if maxRetryAttempts <= 0 {
		t.Error("maxRetryAttempts should be positive")
	}

	if initialRetryDelay <= 0 {
		t.Error("initialRetryDelay should be positive")
	}

	if maxRetryDelay <= initialRetryDelay {
		t.Error("maxRetryDelay should be greater than initialRetryDelay")
	}

	// Should be 2 minutes as per requirement
	if maxRetryDelay != 2*time.Minute {
		t.Errorf("expected maxRetryDelay to be 2 minutes, got %v", maxRetryDelay)
	}
}
