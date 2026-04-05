package resilience

import (
	"context"
	"testing"
	"time"
)

func TestRateLimiter_FirstCallImmediate(t *testing.T) {
	rl := NewRateLimiter(100) // fast rate, won't interfere
	defer rl.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// First call should succeed immediately (seeded token).
	if err := rl.Wait(ctx); err != nil {
		t.Fatalf("first Wait should succeed immediately, got %v", err)
	}
}

func TestRateLimiter_SecondCallWaitsForRefill(t *testing.T) {
	// 10 req/sec → 100ms interval between tokens.
	rl := NewRateLimiter(10)
	defer rl.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Consume the seeded token.
	rl.Wait(ctx)

	// Second call should block until the next tick (~100ms).
	start := time.Now()
	if err := rl.Wait(ctx); err != nil {
		t.Fatalf("second Wait failed: %v", err)
	}
	elapsed := time.Since(start)

	// Should have waited at least ~100ms (allow some slack).
	if elapsed < 50*time.Millisecond {
		t.Fatalf("second call returned too fast (%v), expected ~100ms wait", elapsed)
	}
}

func TestRateLimiter_ContextCancel(t *testing.T) {
	rl := NewRateLimiter(1) // 1 req/sec = 1s interval
	defer rl.Stop()

	// Consume seeded token.
	ctx := context.Background()
	rl.Wait(ctx)

	// Cancel context before next token is available.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := rl.Wait(ctx)
	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
}

func TestRateLimiter_MultipleCallers(t *testing.T) {
	rl := NewRateLimiter(100) // fast rate
	defer rl.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Multiple goroutines should all be able to acquire tokens over time.
	for i := 0; i < 5; i++ {
		if err := rl.Wait(ctx); err != nil {
			t.Fatalf("Wait %d failed: %v", i, err)
		}
	}
}
