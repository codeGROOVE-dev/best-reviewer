package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"time"
)

// retryWithBackoff executes a function with exponential backoff.
// Simple, direct implementation without external dependencies.
func retryWithBackoff(ctx context.Context, operation string, fn func() error) error {
	delay := initialRetryDelay

	for attempt := range maxRetryAttempts {
		err := fn()
		if err == nil {
			return nil
		}

		// Check if context is cancelled
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s cancelled: %w", operation, ctx.Err())
		default:
		}

		// Last attempt, return the error
		if attempt == maxRetryAttempts-1 {
			return err
		}

		log.Printf("[RETRY] %s: attempt %d/%d failed: %v", operation, attempt+1, maxRetryAttempts, err)

		// Calculate next delay with jitter using crypto/rand for security
		jitterMax := big.NewInt(int64(delay / 4))
		jitterBig, err := rand.Int(rand.Reader, jitterMax)
		if err != nil {
			jitterBig = big.NewInt(0) // Fall back to no jitter on error
		}
		jitter := time.Duration(jitterBig.Int64())
		sleepTime := delay + jitter
		if sleepTime > maxRetryDelay {
			sleepTime = maxRetryDelay
		}

		// Sleep with context cancellation
		timer := time.NewTimer(sleepTime)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%s cancelled during retry: %w", operation, ctx.Err())
		case <-timer.C:
		}

		// Exponential backoff
		delay *= 2
		if delay > maxRetryDelay {
			delay = maxRetryDelay
		}
	}

	// Should never reach here, but be safe
	return fmt.Errorf("%s failed after %d attempts", operation, maxRetryAttempts)
}
