package mineskin

import (
	"math"
	"time"
)

const (
	defaultPollInitialDelay = 2 * time.Second
	defaultPollInterval     = time.Second
	defaultPollMaxAttempts  = 10
)

// PollInterval decides how long to wait between status checks of a job.
// Wait returns the wait before the given check attempt (1-based). A result of
// zero or less fails the wait with ErrInvalidInput, and a panic in Wait fails
// it with an error. Wait is called without any client lock held but must not
// call back into the client.
//
// Use ConstantInterval, Backoff or PollIntervalFunc.
type PollInterval interface {
	Wait(attempt int) time.Duration
}

// PollIntervalFunc adapts a function to a PollInterval.
type PollIntervalFunc func(attempt int) time.Duration

// Wait calls f(attempt).
func (f PollIntervalFunc) Wait(attempt int) time.Duration { return f(attempt) }

// intervalValidator is implemented by the built-in intervals so that
// NewClient rejects their bad values up front.
type intervalValidator interface{ validate() error }

// constantInterval is the PollInterval returned by ConstantInterval.
type constantInterval time.Duration

func (c constantInterval) Wait(int) time.Duration { return time.Duration(c) }

func (c constantInterval) validate() error {
	if c <= 0 {
		return invalidInput("constant interval must be greater than zero")
	}
	return nil
}

// ConstantInterval returns a PollInterval that always waits d. d must be
// greater than zero; NewClient returns an error matching ErrInvalidInput
// otherwise.
func ConstantInterval(d time.Duration) PollInterval { return constantInterval(d) }

// Backoff is a PollInterval that waits Initial for the first FreeAttempts
// attempts and then grows by Multiplier per attempt, capped at Max:
// min(Initial*Multiplier^(attempt-FreeAttempts), Max).
//
// Initial must be positive, Max not below Initial, Multiplier finite and at
// least 1, and FreeAttempts not negative; NewClient returns an error matching
// ErrInvalidInput otherwise. The zero value is invalid.
//
// The values used by the Java client are 200 ms, 2 s, 2.0 and 3:
//
//	backoff := mineskin.Backoff{
//		Initial: 200 * time.Millisecond, Max: 2 * time.Second,
//		Multiplier: 2.0, FreeAttempts: 3,
//	}
//	client, err := mineskin.NewClient(mineskin.WithJobPolling(mineskin.PollOptions{Interval: backoff}))
type Backoff struct {
	// Initial is the wait of the free attempts and the base of the growth.
	Initial time.Duration
	// Max is the largest wait.
	Max time.Duration
	// Multiplier is the growth factor per attempt after the free ones.
	Multiplier float64
	// FreeAttempts is the number of leading attempts that wait Initial.
	FreeAttempts int
}

// Wait implements PollInterval.
func (b Backoff) Wait(attempt int) time.Duration {
	if attempt <= b.FreeAttempts {
		return b.Initial
	}
	d := float64(b.Initial) * math.Pow(b.Multiplier, float64(attempt-b.FreeAttempts))
	if math.IsNaN(d) || d >= float64(b.Max) {
		return b.Max
	}
	return time.Duration(d)
}

func (b Backoff) validate() error {
	switch {
	case b.Initial <= 0:
		return invalidInput("initial interval must be greater than zero")
	case b.Max < b.Initial:
		return invalidInput("max interval must not be below the initial interval")
	case math.IsNaN(b.Multiplier) || math.IsInf(b.Multiplier, 0) || b.Multiplier < 1:
		return invalidInput("multiplier must be a finite number of at least 1")
	case b.FreeAttempts < 0:
		return invalidInput("free attempts must not be negative")
	}
	return nil
}

// PollOptions configures how Client waits for jobs. A zero field selects its
// default: an InitialDelay of 2 s, a constant Interval of 1 s, 10 MaxAttempts
// and UseETA false.
type PollOptions struct {
	// InitialDelay is the wait before the first status check.
	InitialDelay time.Duration
	// Interval gives the wait between later checks: ConstantInterval,
	// Backoff or PollIntervalFunc. Bad values of the built-ins fail
	// NewClient; a custom Interval must return a positive duration, or the
	// wait fails with ErrInvalidInput, and a panic in it is recovered and
	// fails the wait with an error. nil selects ConstantInterval(1s).
	Interval PollInterval
	// MaxAttempts is the number of status checks before giving up.
	MaxAttempts int
	// UseETA makes the first check wait until the job's ETA when it lies
	// in the future.
	UseETA bool
}

// withDefaults returns p with zero fields replaced by their defaults.
// Negative values are kept for validate to reject.
func (p PollOptions) withDefaults() PollOptions {
	if p.InitialDelay == 0 {
		p.InitialDelay = defaultPollInitialDelay
	}
	if p.MaxAttempts == 0 {
		p.MaxAttempts = defaultPollMaxAttempts
	}
	if p.Interval == nil {
		p.Interval = ConstantInterval(defaultPollInterval)
	}
	return p
}

// validate returns an error matching ErrInvalidInput if InitialDelay or
// MaxAttempts is negative, or if a built-in Interval has invalid values.
func (p PollOptions) validate() error {
	if p.InitialDelay < 0 {
		return invalidInput("poll initial delay must not be negative")
	}
	if p.MaxAttempts < 0 {
		return invalidInput("poll max attempts must not be negative")
	}
	if v, ok := p.Interval.(intervalValidator); ok {
		return v.validate()
	}
	return nil
}
