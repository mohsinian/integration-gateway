package resilience

import (
	"sync"
	"time"
)

// CBState represents the current state of a circuit breaker.
type CBState string

const (
	CBClosed   CBState = "closed"   // Normal — requests flow through.
	CBOpen     CBState = "open"     // Tripped — requests are rejected.
	CBHalfOpen CBState = "half-open" // Probing — allowing one request through.
)

// CircuitBreaker prevents cascading failures by stopping calls to a
// service that has repeatedly failed. One instance per external service.
type CircuitBreaker struct {
	name        string
	maxFailures int           // Consecutive failures required to trip.
	cooldown    time.Duration // How long to stay open before half-open.

	mu          sync.Mutex
	state       CBState
	failures    int
	lastFailure time.Time
	openedAt    time.Time
}

// NewCircuitBreaker creates a breaker in the Closed state.
func NewCircuitBreaker(name string, maxFailures int, cooldown time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		name:        name,
		maxFailures: maxFailures,
		cooldown:    cooldown,
		state:       CBClosed,
	}
}

// Allow returns true if the caller should proceed with the request.
// In the Open state it returns false until cooldown elapses, at which
// point it transitions to Half-Open and returns true (one probe request).
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CBClosed:
		return true

	case CBOpen:
		if time.Since(cb.openedAt) >= cb.cooldown {
			cb.state = CBHalfOpen
			return true // allow one probe
		}
		return false

	case CBHalfOpen:
		// Only one probe at a time — reject concurrent callers while
		// the probe is in flight.
		return false

	default:
		return false
	}
}

// RecordSuccess resets the consecutive-failure count and closes the circuit.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures = 0
	cb.state = CBClosed
}

// RecordFailure increments the failure counter. If the threshold is reached
// the circuit opens. In half-open state a single failure re-opens the circuit.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	cb.lastFailure = time.Now()

	switch cb.state {
	case CBClosed:
		if cb.failures >= cb.maxFailures {
			cb.state = CBOpen
			cb.openedAt = time.Now()
		}
	case CBHalfOpen:
		cb.state = CBOpen
		cb.openedAt = time.Now()
	}
}

// State returns the current circuit-breaker state (for health endpoint).
func (cb *CircuitBreaker) State() CBState {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	// Lazily transition open → half-open so State() is always accurate.
	if cb.state == CBOpen && time.Since(cb.openedAt) >= cb.cooldown {
		cb.state = CBHalfOpen
	}
	return cb.state
}

// Name returns the circuit-breaker name.
func (cb *CircuitBreaker) Name() string {
	return cb.name
}

// Failures returns the current consecutive-failure count.
func (cb *CircuitBreaker) Failures() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.failures
}
