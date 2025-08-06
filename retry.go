package main

import (
	"context"
	"log"

	"github.com/codeGROOVE-dev/retry"
)

// retryWithBackoff executes a function with exponential backoff and jitter using the recommended library.
func retryWithBackoff(ctx context.Context, operation string, fn func() error) error {
	return retry.Do(
		fn,
		retry.Context(ctx),
		retry.Attempts(maxRetryAttempts),
		retry.DelayType(retry.BackOffDelay),
		retry.Delay(initialRetryDelay),
		retry.MaxDelay(maxRetryDelay),
		retry.OnRetry(func(n uint, err error) {
			log.Printf("[RETRY] %s: attempt %d/%d failed: %v", operation, n+1, maxRetryAttempts, err)
		}),
		retry.LastErrorOnly(true),
	)
}
