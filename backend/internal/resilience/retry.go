package resilience

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"time"

	"github.com/mohsinian/integration-gateway/internal/client"
)

// RetryConfig controls the behaviour of the exponential-backoff retry loop.
type RetryConfig struct {
	MaxAttempts  int           // Maximum number of attempts (>= 1).
	InitialDelay time.Duration // Delay before the first retry.
	MaxDelay     time.Duration // Upper bound on any single delay.
	Multiplier   float64       // Factor by which delay grows each attempt (e.g. 2.0).
}

// DefaultRetryConfig returns sensible defaults used throughout the project.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:  5,
		InitialDelay: 1 * time.Second,
		MaxDelay:     30 * time.Second,
		Multiplier:   2.0,
	}
}

// Do executes fn up to cfg.MaxAttempts times with exponential backoff + jitter.
//
// Permanent errors (client.PermanentError) cause an immediate abort — no retry.
// On exhaustion of all attempts the last error from fn is returned.
func Do(ctx context.Context, cfg RetryConfig, fn func() error) error {
	var lastErr error

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		// Respect cancellation between attempts.
		if ctx.Err() != nil {
			return ctx.Err()
		}

		lastErr = fn()
		if lastErr == nil {
			return nil
		}

		// Do not retry permanent errors.
		if client.IsPermanent(lastErr) {
			return lastErr
		}

		// If this was the last allowed attempt, stop — no need to sleep.
		if attempt == cfg.MaxAttempts {
			break
		}

		// Calculate exponential backoff with jitter.
		delay := backoff(cfg, attempt)
		delay = addJitter(delay, 500*time.Millisecond)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
			// continue to next attempt
		}
	}

	return lastErr
}

// backoff computes the base delay for the given 1-based attempt number.
func backoff(cfg RetryConfig, attempt int) time.Duration {
	// delay = InitialDelay * Multiplier^(attempt-1)
	d := float64(cfg.InitialDelay) * math.Pow(cfg.Multiplier, float64(attempt-1))
	if d > float64(cfg.MaxDelay) {
		return cfg.MaxDelay
	}
	return time.Duration(d)
}

// addJitter adds a random duration in [0, jitter) to the base delay.
func addJitter(base, jitter time.Duration) time.Duration {
	if jitter <= 0 {
		return base
	}
	return base + time.Duration(rand.Int64N(int64(jitter)))
}

// DoWithResult is a generic variant of Do that returns a value alongside the
// error. It follows the same retry rules as Do.
func DoWithResult[T any](ctx context.Context, cfg RetryConfig, fn func() (T, error)) (T, error) {
	var zero T
	var lastErr error

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}

		val, err := fn()
		if err == nil {
			return val, nil
		}

		lastErr = err

		if client.IsPermanent(lastErr) {
			return zero, lastErr
		}

		if attempt == cfg.MaxAttempts {
			break
		}

		delay := backoff(cfg, attempt)
		delay = addJitter(delay, 500*time.Millisecond)

		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(delay):
		}
	}

	return zero, lastErr
}

// IsPermanentError is a convenience re-export for callers that import only
// the resilience package.
func IsPermanentError(err error) bool {
	return errors.As(err, new(*client.PermanentError))
}
