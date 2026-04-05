package resilience

import (
	"context"
	"time"
)

// RateLimiter controls the rate at which calls are made to an external
// service. It uses a token-bucket approach backed by a time.Ticker.
//
// A singleton instance is shared across all lookup goroutines to enforce
// a global rate limit (e.g., 2 req/sec for Court Records).
type RateLimiter struct {
	ticker *time.Ticker
	tokens chan struct{}
	done   chan struct{}
}

// NewRateLimiter creates a RateLimiter that allows up to rate requests per
// second. A ticker fires at the corresponding interval and deposits one
// token into a buffered channel of size 1.
func NewRateLimiter(rate float64) *RateLimiter {
	interval := time.Duration(float64(time.Second) / rate)
	rl := &RateLimiter{
		ticker: time.NewTicker(interval),
		tokens: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}

	// Seed the bucket with one token so the first call doesn't wait.
	rl.tokens <- struct{}{}

	go rl.refill()
	return rl
}

// refill runs in the background, depositing a token every tick. If the
// channel already holds a token the tick is dropped (burst = 1).
func (rl *RateLimiter) refill() {
	for {
		select {
		case <-rl.done:
			return
		case <-rl.ticker.C:
			select {
			case rl.tokens <- struct{}{}:
			default: // bucket full, drop tick
			}
		}
	}
}

// Wait blocks until a token is available or the context is cancelled.
// Callers must invoke Wait before every outbound request.
func (rl *RateLimiter) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-rl.tokens:
		return nil
	}
}

// Stop shuts down the background refill goroutine and releases resources.
func (rl *RateLimiter) Stop() {
	rl.ticker.Stop()
	close(rl.done)
}
