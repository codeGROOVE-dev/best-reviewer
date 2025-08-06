package main

import (
	"context"
	"log"
	"time"

	"github.com/codeGROOVE-dev/retry"
)

// retryWithBackoff executes a function with exponential backoff and jitter using the recommended library.
func retryWithBackoff(ctx context.Context, operation string, fn func() error) error {
	return retry.Do(
		fn,
		retry.Context(ctx),
		retry.Attempts(5),
		retry.DelayType(retry.BackOffDelay),
		retry.Delay(time.Second),
		retry.MaxDelay(2*time.Minute),
		retry.OnRetry(func(n uint, err error) {
			log.Printf("[RETRY] %s: attempt %d/5 failed: %v", operation, n+1, err)
		}),
		retry.LastErrorOnly(true),
	)
}
