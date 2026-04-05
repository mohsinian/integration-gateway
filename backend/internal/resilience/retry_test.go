package resilience

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mohsinian/integration-gateway/internal/client"
)

func TestDo_SucceedsImmediately(t *testing.T) {
	cfg := RetryConfig{MaxAttempts: 3, InitialDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond, Multiplier: 2.0}
	err := Do(context.Background(), cfg, func() error { return nil })
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestDo_SucceedsAfterRetries(t *testing.T) {
	cfg := RetryConfig{MaxAttempts: 5, InitialDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond, Multiplier: 2.0}
	var calls atomic.Int32
	err := Do(context.Background(), cfg, func() error {
		if calls.Add(1) < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 calls, got %d", got)
	}
}

func TestDo_StopsOnPermanentError(t *testing.T) {
	cfg := RetryConfig{MaxAttempts: 5, InitialDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond, Multiplier: 2.0}
	var calls atomic.Int32
	err := Do(context.Background(), cfg, func() error {
		calls.Add(1)
		return client.ErrNotFound
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("permanent error should not be retried, got %d calls", got)
	}
}

func TestDo_ExhaustsAllAttempts(t *testing.T) {
	cfg := RetryConfig{MaxAttempts: 3, InitialDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond, Multiplier: 2.0}
	var calls atomic.Int32
	err := Do(context.Background(), cfg, func() error {
		calls.Add(1)
		return errors.New("always fails")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
}

func TestDo_RespectsContextCancellation(t *testing.T) {
	cfg := RetryConfig{MaxAttempts: 10, InitialDelay: 50 * time.Millisecond, MaxDelay: 200 * time.Millisecond, Multiplier: 2.0}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	var calls atomic.Int32
	err := Do(ctx, cfg, func() error {
		calls.Add(1)
		return errors.New("fail")
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

func TestDoWithResult_Succeeds(t *testing.T) {
	cfg := RetryConfig{MaxAttempts: 3, InitialDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond, Multiplier: 2.0}
	val, err := DoWithResult(context.Background(), cfg, func() (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "ok" {
		t.Fatalf("expected 'ok', got %q", val)
	}
}

func TestDoWithResult_RetriesThenSucceeds(t *testing.T) {
	cfg := RetryConfig{MaxAttempts: 5, InitialDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond, Multiplier: 2.0}
	var calls atomic.Int32
	val, err := DoWithResult(context.Background(), cfg, func() (int, error) {
		n := calls.Add(1)
		if n < 3 {
			return 0, errors.New("transient")
		}
		return 42, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 42 {
		t.Fatalf("expected 42, got %d", val)
	}
}

func TestBackoff_RespectsMaxDelay(t *testing.T) {
	cfg := RetryConfig{MaxAttempts: 10, InitialDelay: 1 * time.Second, MaxDelay: 5 * time.Second, Multiplier: 2.0}
	// attempt 5 = 1s * 2^4 = 16s, but capped at 5s
	got := backoff(cfg, 5)
	if got != 5*time.Second {
		t.Fatalf("expected 5s cap, got %v", got)
	}
}
