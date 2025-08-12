package main

import (
	"context"
	"log"

	"github.com/codeGROOVE-dev/retry"
)

// retryWithBackoff executes a function with exponential backoff using the codeGROOVE retry library.
func retryWithBackoff(ctx context.Context, operation string, fn func() error) error {
	// Configure retry with exponential backoff and jitter
	return retry.Do(
		func() error {
			return fn()
		},
		retry.Context(ctx),
		retry.Attempts(uint(maxRetryAttempts)),
		retry.Delay(initialRetryDelay),
		retry.MaxDelay(maxRetryDelay),
		retry.DelayType(retry.BackOffDelay),
		retry.MaxJitter(initialRetryDelay/4),
		retry.OnRetry(func(n uint, err error) {
			log.Printf("[RETRY] %s: attempt %d/%d failed: %v", operation, n+1, maxRetryAttempts, err)
		}),
		retry.LastErrorOnly(true),
		retry.RetryIf(func(err error) bool {
			// Retry on temporary errors, rate limits, and server errors
			if err == nil {
				return false
			}
			errStr := err.Error()
			// Retry on rate limiting, server errors, and network issues
			return contains(errStr, "rate limited") ||
				contains(errStr, "server error") ||
				contains(errStr, "connection refused") ||
				contains(errStr, "timeout") ||
				contains(errStr, "temporary failure") ||
				contains(errStr, "EOF")
		}),
	)
}

// contains checks if a string contains a substring.
func contains(s, substr string) bool {
	if s == "" || substr == "" {
		return false
	}
	if s == substr {
		return true
	}
	if len(s) > len(substr) {
		if s[:len(substr)] == substr || s[len(s)-len(substr):] == substr {
			return true
		}
		return containsMiddle(s, substr)
	}
	return false
}

func containsMiddle(s, substr string) bool {
	if len(s) <= len(substr) {
		return false
	}
	for i := 1; i < len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
