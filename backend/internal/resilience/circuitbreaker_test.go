package resilience

import (
	"testing"
	"time"
)

func TestCircuitBreaker_StartsClosed(t *testing.T) {
	cb := NewCircuitBreaker("test", 3, 1*time.Second)
	if cb.State() != CBClosed {
		t.Fatalf("expected closed, got %s", cb.State())
	}
	if !cb.Allow() {
		t.Fatal("closed circuit should allow requests")
	}
}

func TestCircuitBreaker_OpensAfterMaxFailures(t *testing.T) {
	cb := NewCircuitBreaker("test", 3, 1*time.Second)

	for i := 0; i < 3; i++ {
		cb.Allow()
		cb.RecordFailure()
	}

	if cb.State() != CBOpen {
		t.Fatalf("expected open after 3 failures, got %s", cb.State())
	}
	if cb.Allow() {
		t.Fatal("open circuit should reject requests")
	}
}

func TestCircuitBreaker_SuccessResetsFailures(t *testing.T) {
	cb := NewCircuitBreaker("test", 3, 1*time.Second)

	cb.RecordFailure()
	cb.RecordFailure()
	if cb.Failures() != 2 {
		t.Fatalf("expected 2 failures, got %d", cb.Failures())
	}

	cb.RecordSuccess()
	if cb.Failures() != 0 {
		t.Fatalf("expected 0 failures after success, got %d", cb.Failures())
	}
	if cb.State() != CBClosed {
		t.Fatalf("expected closed after success, got %s", cb.State())
	}
}

func TestCircuitBreaker_HalfOpenAfterCooldown(t *testing.T) {
	cb := NewCircuitBreaker("test", 2, 50*time.Millisecond)

	// Trip the circuit open.
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != CBOpen {
		t.Fatalf("expected open, got %s", cb.State())
	}

	// Wait for cooldown.
	time.Sleep(60 * time.Millisecond)

	// Allow() transitions open → half-open and grants the probe slot.
	if !cb.Allow() {
		t.Fatal("half-open should allow one probe")
	}
	if cb.Allow() {
		t.Fatal("half-open should reject second concurrent call")
	}
}

func TestCircuitBreaker_HalfOpenProbeSuccess(t *testing.T) {
	cb := NewCircuitBreaker("test", 2, 50*time.Millisecond)

	cb.RecordFailure()
	cb.RecordFailure()
	time.Sleep(60 * time.Millisecond)

	cb.Allow()        // consume the probe slot
	cb.RecordSuccess()

	if cb.State() != CBClosed {
		t.Fatalf("expected closed after successful probe, got %s", cb.State())
	}
	if !cb.Allow() {
		t.Fatal("closed circuit should allow requests after successful probe")
	}
}

func TestCircuitBreaker_HalfOpenProbeFailure(t *testing.T) {
	cb := NewCircuitBreaker("test", 2, 50*time.Millisecond)

	cb.RecordFailure()
	cb.RecordFailure()
	time.Sleep(60 * time.Millisecond)

	cb.Allow()        // consume probe
	cb.RecordFailure()

	if cb.State() != CBOpen {
		t.Fatalf("expected open again after failed probe, got %s", cb.State())
	}
	if cb.Allow() {
		t.Fatal("re-opened circuit should reject requests")
	}
}

func TestCircuitBreaker_Name(t *testing.T) {
	cb := NewCircuitBreaker("property", 5, 30*time.Second)
	if cb.Name() != "property" {
		t.Fatalf("expected 'property', got %q", cb.Name())
	}
}
