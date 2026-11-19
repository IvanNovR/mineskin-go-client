package mineskin

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// DefaultRateLimitRetries is the default number of automatic retries of a
// request rejected with HTTP 429 (see WithRateLimitRetries).
const DefaultRateLimitRetries = 2

// acceptAnyBody is a payload decoder that never fails, so handleResponse only
// reports envelope and API errors.
func acceptAnyBody(map[string]json.RawMessage, []byte) error { return nil }

// retryRateLimited reports whether raw, the attempt-th response of a request,
// is a 429 error that must be retried. Only a 429 that handleResponse turns
// into an error is retried. The retry waits in g's throttle for the deadline
// the 429 already pushed. When the earliest possible start (the later of the
// server deadline and the interval after the last start) lies after ctx's own
// deadline, no retry is made and the caller gets the original 429 error. A
// non-JSON 429 counts towards the auto-tuning penalty here, as it would if it
// were the final response.
func (c *Client) retryRateLimited(ctx context.Context, g *requestGroup, raw rawResponse, attempt int) bool {
	if raw.Status != http.StatusTooManyRequests || attempt > c.rateLimitRetries {
		return false
	}
	_, err := handleResponse(raw, acceptAnyBody)
	if err == nil {
		return false
	}
	notBefore := g.throttle.EarliestStart()
	if dl, ok := ctx.Deadline(); ok && dl.Before(notBefore) {
		return false
	}
	if g.tuner != nil && isRetryableUnexpected(err) {
		g.tuner.reportFailure()
	}
	c.logger.Debug("MineSkin retrying rate-limited request",
		"group", g.name, "attempt", attempt, "delay", max(0, notBefore.Sub(c.clock.Now())).Round(time.Millisecond))
	return true
}

// retryCancelledError is returned when the caller's ctx ends while a retry of
// a 429 is waiting or in flight. It matches both the ctx error (and so
// ErrClient, through its wrapping) and the 429 error.
type retryCancelledError struct {
	// ctxErr is the wrapped ctx error of the failed retry attempt.
	ctxErr error
	// rateLimited is the *APIError or *UnexpectedResponseError of the 429.
	rateLimited error
}

// newRetryCancelledError combines the ctx error of a failed retry attempt with
// the error of the 429 that caused the retry; without a 429 error it returns
// ctxErr alone.
func newRetryCancelledError(ctxErr error, last rawResponse) error {
	_, rateLimited := handleResponse(last, acceptAnyBody)
	if rateLimited == nil {
		return ctxErr
	}
	return &retryCancelledError{ctxErr: ctxErr, rateLimited: rateLimited}
}

// Error implements the error interface and names both causes.
func (e *retryCancelledError) Error() string {
	return e.ctxErr.Error() + " (while retrying after " + e.rateLimited.Error() + ")"
}

// Unwrap lets errors.Is and errors.As reach both causes.
func (e *retryCancelledError) Unwrap() []error { return []error{e.ctxErr, e.rateLimited} }
