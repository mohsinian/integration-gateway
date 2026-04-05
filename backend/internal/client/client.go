package client

import (
	"context"
	"errors"
)

// ---------------------------------------------------------------------------
// Shared error types — the retry / circuit-breaker layer inspects these to
// decide whether a failed call should be retried.
// ---------------------------------------------------------------------------

// PermanentError wraps an error to signal that the caller should NOT retry.
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

var (
	// ErrNotFound is returned on a 404 — the resource simply does not exist.
	ErrNotFound = &PermanentError{Err: errors.New("not found")}

	// ErrServiceUnavailable is returned on a 503 — transient, retryable.
	ErrServiceUnavailable = errors.New("service unavailable")

	// ErrTimeout is returned when the HTTP request exceeds its deadline.
	ErrTimeout = errors.New("request timed out")

	// ErrRateLimited is returned when the court service responds with 429.
	ErrRateLimited = errors.New("rate limited")

	// ErrMalformedResponse is returned when the court service returns invalid XML.
	ErrMalformedResponse = errors.New("malformed xml response")

	// ErrSearchFailed is returned when the SCRA service reports that the
	// search permanently failed (status "error" in poll response).
	ErrSearchFailed = errors.New("scra search failed")
)

// IsPermanent returns true if err wraps a PermanentError and should not be retried.
func IsPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}

// isTimeout returns true when the error is caused by a deadline or timeout
// (either the caller's context or the http.Client timeout).
func isTimeout(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	// net/http wraps its own timeouts in *url.Error which implements Timeout() bool.
	type timeout interface{ Timeout() bool }
	var te timeout
	if errors.As(err, &te) {
		return te.Timeout()
	}
	return false
}
