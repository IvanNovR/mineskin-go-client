// Package poll polls pending jobs until they are done. It is generic over the
// response type R and knows nothing about HTTP, throttles or the public API:
// a Fetcher supplies responses and the neutral State derived from them.
package poll

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrSkipped is returned by Fetcher.Get when the start gate declined the
// check. The poller treats it as "nothing to do", never as a job failure.
var ErrSkipped = errors.New("poll: check skipped")

// State is what the poller needs to know about a job.
type State struct {
	ID string
	// ETA is when the job is expected to finish; zero when unknown.
	ETA time.Time
	// Done reports that polling can stop.
	Done bool
	// Failed reports that the job failed. The poller only counts it (through
	// Config.OnJobFailure); turning a failed result into an error is up to the
	// caller of Waiter.Result.
	Failed bool
}

// Fetcher supplies job responses.
type Fetcher[R any] interface {
	// Get fetches one job. A non-nil proceed is the start gate: Get calls it
	// once it may send, and returns ErrSkipped without sending when it
	// reports false.
	Get(ctx context.Context, id string, proceed func() bool) (R, State, error)
	// List fetches the states of the jobs of the account in one request.
	List(ctx context.Context) ([]State, error)
}

// Options controls the schedule of the checks. Zero values are not replaced
// by defaults: the caller supplies a complete Options.
type Options struct {
	// InitialDelay is the wait before the first check.
	InitialDelay time.Duration
	// Interval gives the wait before the given attempt (1-based). It must
	// return a positive duration and must not be nil.
	Interval func(attempt int) time.Duration
	// MaxAttempts is the number of checks before giving up.
	MaxAttempts int
	// UseETA makes the first check wait until the job's ETA when it lies in
	// the future.
	UseETA bool
}

// maxETADelay caps the first-check wait taken from a server ETA, so a hostile
// or broken ETA cannot park a waiter for years. It matches the one-hour cap on
// other server-controlled delays; internal/poll may not import ratelimit, and
// a package constant is simpler than plumbing the cap through Options.
const maxETADelay = time.Hour

// firstDelay returns the wait before the first check of a job with the given
// ETA: eta-now (at most maxETADelay) when UseETA is set and the ETA is in the
// future, else InitialDelay.
func (o Options) firstDelay(eta, now time.Time) time.Duration {
	if o.UseETA && !eta.IsZero() {
		if d := eta.Sub(now); d > 0 {
			return min(d, maxETADelay)
		}
	}
	return o.InitialDelay
}

// interval returns the wait before the given attempt, or an *IntervalError
// if Interval returns a value of zero or less or panics.
func (o Options) interval(attempt int) (d time.Duration, err error) {
	defer func() {
		if r := recover(); r != nil {
			d, err = 0, &IntervalError{Panic: r}
		}
	}()
	d = o.Interval(attempt)
	if d <= 0 {
		return 0, &IntervalError{Value: d}
	}
	return d, nil
}

// ExhaustedError is the outcome of a job that was still unfinished after
// MaxAttempts checks.
type ExhaustedError struct {
	ID       string
	Attempts int
}

// Error implements the error interface.
func (e *ExhaustedError) Error() string {
	return fmt.Sprintf("job %s not finished after %d attempts", e.ID, e.Attempts)
}

// IntervalError is the outcome of a job whose Interval function returned a
// value of zero or less (Value) or panicked (Panic is the recovered value).
type IntervalError struct {
	Value time.Duration
	Panic any
}

// Error implements the error interface.
func (e *IntervalError) Error() string {
	if e.Panic != nil {
		return fmt.Sprintf("poll interval panicked: %v", e.Panic)
	}
	return fmt.Sprintf("poll interval %v is not greater than zero", e.Value)
}
