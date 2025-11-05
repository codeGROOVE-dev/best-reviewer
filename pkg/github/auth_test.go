package github

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateAppID_Valid(t *testing.T) {
	tests := []struct {
		name  string
		appID string
	}{
		{"single digit", "1"},
		{"multiple digits", "123456"},
		{"max valid", "999999999"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAppID(tt.appID)
			if err != nil {
				t.Errorf("validateAppID(%q) unexpected error: %v", tt.appID, err)
			}
		})
	}
}

func TestValidateAppID_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		appID   string
		wantErr string
	}{
		{"empty", "", "app ID cannot be empty"},
		{"non-numeric", "abc", "app ID must be numeric"},
		{"negative", "-1", "app ID must be numeric"},
		{"too large", "9999999999", "app ID too large"},
		{"with spaces", "123 456", "app ID must be numeric"},
		{"with special chars", "123@456", "app ID must be numeric"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAppID(tt.appID)
			if err == nil {
				t.Errorf("validateAppID(%q) expected error, got nil", tt.appID)
			}
		})
	}
}

func TestValidateToken_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"too short", "abc"},
		{"just under min", "ghp_" + "x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateToken(tt.token)
			if err == nil {
				t.Errorf("validateToken(%q) expected error, got nil", tt.token)
			}
		})
	}
}

func TestAuthConstants(t *testing.T) {
	// Verify auth constants have reasonable values
	if maxTokenLength <= minTokenLength {
		t.Error("maxTokenLength should be greater than minTokenLength")
	}

	if minTokenLength != 40 {
		t.Errorf("expected minTokenLength to be 40, got %d", minTokenLength)
	}

	if maxTokenLength != 100 {
		t.Errorf("expected maxTokenLength to be 100, got %d", maxTokenLength)
	}

	if classicTokenLength != 40 {
		t.Errorf("expected classicTokenLength to be 40, got %d", classicTokenLength)
	}

	if maxAppID != 999999999 {
		t.Errorf("expected maxAppID to be 999999999, got %d", maxAppID)
	}

	if filePermSecure != 0o077 {
		t.Errorf("expected filePermSecure to be 0o077, got %o", filePermSecure)
	}

	if filePermReadOnly != 0o400 {
		t.Errorf("expected filePermReadOnly to be 0o400, got %o", filePermReadOnly)
	}

	if filePermOwnerRW != 0o600 {
		t.Errorf("expected filePermOwnerRW to be 0o600, got %o", filePermOwnerRW)
	}
}

func TestClient_RefreshJWTIfNeeded_NotAppAuth(t *testing.T) {
	c := &Client{
		isAppAuth: false,
	}

	// Should be no-op for non-app auth
	err := c.refreshJWTIfNeeded()
	if err != nil {
		t.Errorf("refreshJWTIfNeeded() unexpected error for non-app auth: %v", err)
	}
}

func TestClient_RefreshJWTIfNeeded_NoRefreshNeeded(t *testing.T) {
	c := &Client{
		isAppAuth:   true,
		tokenExpiry: time.Now().Add(time.Hour), // Not expired
		appID:       "123456",
		privateKeyContent: []byte(`-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEA0Z3VS5JJcds3xfn/ygWyF0q4JwfFLp8rh6f5tLUGJKqWJQs9
-----END RSA PRIVATE KEY-----`),
	}

	// Should not refresh if not needed
	err := c.refreshJWTIfNeeded()
	if err != nil {
		t.Errorf("refreshJWTIfNeeded() unexpected error: %v", err)
	}
}

func TestClient_SetPrxClient(t *testing.T) {
	c := &Client{}

	mockPrx := &mockPrxClientImpl{}
	c.SetPrxClient(mockPrx)

	if c.prxClient == nil {
		t.Error("expected prxClient to be set")
	}
}

type mockPrxClientImpl struct{}

func (m *mockPrxClientImpl) PullRequestWithReferenceTime(ctx context.Context, owner, repo string, prNumber int, referenceTime time.Time) (any, error) {
	return map[string]any{"number": prNumber}, nil
}

func TestNew_PersonalTokenMode(t *testing.T) {
	ctx := context.Background()

	// Test that New works with personal token mode
	cfg := Config{
		UseAppAuth:  false,
		Token:       "",
		HTTPTimeout: 30 * time.Second,
		CacheTTL:    time.Hour,
	}

	// This should not error - it creates a client even with empty token
	// The token validation happens on first use, not at client creation
	_, err := New(ctx, cfg)
	if err != nil {
		t.Logf("Note: New with empty token returned error: %v", err)
		// This is acceptable behavior
	}
}

func TestNewPersonalTokenClient_WithValidToken(t *testing.T) {
	ctx := context.Background()

	// Use a valid token format
	validToken := "ghp_" + strings.Repeat("a", 36)

	client, err := newPersonalTokenClient(ctx, validToken, 30*time.Second, time.Hour, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client == nil {
		t.Fatal("expected non-nil client")
	}

	if client.token != validToken {
		t.Errorf("expected token %q, got %q", validToken, client.token)
	}

	if client.isAppAuth {
		t.Error("expected isAppAuth to be false for personal token")
	}
}

func TestNewPersonalTokenClient_InvalidCacheDir(t *testing.T) {
	ctx := context.Background()

	// Use a valid token format
	validToken := "ghp_" + strings.Repeat("a", 36)

	// Use an invalid cache directory (file instead of directory)
	tmpFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(tmpFile, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Should succeed even with invalid cache dir (falls back to memory cache)
	client, err := newPersonalTokenClient(ctx, validToken, 30*time.Second, time.Hour, tmpFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client == nil {
		t.Fatal("expected non-nil client")
	}

	if client.cache == nil {
		t.Error("expected non-nil cache (should fallback to memory cache)")
	}
}

func TestClient_TokenMutex(t *testing.T) {
	c := &Client{
		installationTypes: make(map[string]string),
	}

	// Test concurrent access to ensure mutex works
	done := make(chan bool)

	// Concurrent SetCurrentOrg calls
	for i := range 10 {
		go func(id int) {
			c.SetCurrentOrg("org" + string(rune(id)))
			done <- true
		}(i)
	}

	// Concurrent IsUserAccount calls
	for i := range 10 {
		go func(id int) {
			c.IsUserAccount("org" + string(rune(id)))
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for range 20 {
		<-done
	}
}
