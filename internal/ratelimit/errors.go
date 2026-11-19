// Package ratelimit holds the throttled request groups of the client: the
// start slot, server-requested delays, automatic retries of HTTP 429 and the
// auto-tuned generate limits. It knows nothing about the public API types;
// the root package translates its errors and values.
package ratelimit

import (
	"errors"

	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

var (
	// ErrSkipped is returned by Group.Do when the start gate declined the
	// request; nothing was sent.
	ErrSkipped = errors.New("mineskin: request skipped by its start gate")
	// ErrClosed is returned when the tuner was shut down before its first
	// grants load finished.
	ErrClosed = errors.New("ratelimit: closed")
)

// RetryCancelledError is returned by Group.Do when the context ends while a
// retry of a 429 is waiting or in flight. Err is the error of the failed
// attempt and Last the 429 response that caused the retry.
type RetryCancelledError struct {
	Err  error
	Last transport.Response
}

// Error implements the error interface.
func (e *RetryCancelledError) Error() string {
	return "ratelimit: " + e.Err.Error() + " while retrying a rate-limited request"
}

// Unwrap returns the error of the failed attempt.
func (e *RetryCancelledError) Unwrap() error { return e.Err }
