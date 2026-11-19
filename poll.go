package mineskin

import (
	"math"
	"time"
)

const (
	// DefaultPollInitialDelay is the default wait before the first status check.
	DefaultPollInitialDelay = 2 * time.Second
	// DefaultPollInterval is the default constant wait between status checks.
	DefaultPollInterval = time.Second
	// DefaultPollMaxAttempts is the default number of status checks per job.
	DefaultPollMaxAttempts = 10
)

// PollInterval returns the wait before the given check attempt (1-based).
// A result of zero or less fails the wait with ErrInvalidInput, and a panic
// in the function fails it with an error matching ErrClient. The function is
// called without any client lock held but must not call back into the client.
type PollInterval func(attempt int) time.Duration

// ConstantInterval returns a PollInterval that always waits d. It returns an
// error matching ErrInvalidInput if d is not greater than zero.
func ConstantInterval(d time.Duration) (PollInterval, error) {
	if d <= 0 {
		return nil, newInvalidInputError("constant interval must be greater than zero")
	}
	return func(int) time.Duration { return d }, nil
}

// ExponentialInterval returns a PollInterval that waits initial for the first
// freeAttempts attempts and then grows by multiplier per attempt, capped at
// max: min(initial*multiplier^(attempt-freeAttempts), max).
//
// It returns an error matching ErrInvalidInput if initial is not positive, max
// is below initial, multiplier is below 1, NaN or infinite, or freeAttempts is
// negative.
//
// The values used by the Java client are 200 ms, 2 s, 2.0 and 3:
//
//	interval, err := mineskin.ExponentialInterval(200*time.Millisecond, 2*time.Second, 2.0, 3)
//	client, err := mineskin.NewClient(mineskin.WithJobPolling(mineskin.PollOptions{Interval: interval}))
func ExponentialInterval(initial, max time.Duration, multiplier float64, freeAttempts int) (PollInterval, error) {
	switch {
	case initial <= 0:
		return nil, newInvalidInputError("initial interval must be greater than zero")
	case max < initial:
		return nil, newInvalidInputError("max interval must not be below the initial interval")
	case math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier < 1:
		return nil, newInvalidInputError("multiplier must be a finite number of at least 1")
	case freeAttempts < 0:
		return nil, newInvalidInputError("free attempts must not be negative")
	}
	return func(attempt int) time.Duration {
		if attempt <= freeAttempts {
			return initial
		}
		d := float64(initial) * math.Pow(multiplier, float64(attempt-freeAttempts))
		if math.IsNaN(d) || d >= float64(max) {
			return max
		}
		return time.Duration(d)
	}, nil
}

// PollOptions configures how Client waits for jobs. A zero field selects its
// default: DefaultPollInitialDelay, a constant DefaultPollInterval,
// DefaultPollMaxAttempts and UseETA false.
type PollOptions struct {
	// InitialDelay is the wait before the first status check.
	InitialDelay time.Duration
	// Interval gives the wait between later checks. It must return a
	// positive duration: zero or less fails the wait with ErrInvalidInput. A
	// panic is recovered and fails the wait with an error matching ErrClient.
	// nil selects a constant DefaultPollInterval.
	Interval PollInterval
	// MaxAttempts is the number of status checks before giving up.
	MaxAttempts int
	// UseETA makes the first check wait until the job's ETA when it lies
	// in the future.
	UseETA bool
}

// withDefaults returns p with defaults filled in, or an error matching
// ErrInvalidInput if InitialDelay or MaxAttempts is negative.
func (p PollOptions) withDefaults() (PollOptions, error) {
	if p.InitialDelay < 0 {
		return PollOptions{}, newInvalidInputError("poll initial delay must not be negative")
	}
	if p.MaxAttempts < 0 {
		return PollOptions{}, newInvalidInputError("poll max attempts must not be negative")
	}
	if p.InitialDelay == 0 {
		p.InitialDelay = DefaultPollInitialDelay
	}
	if p.MaxAttempts == 0 {
		p.MaxAttempts = DefaultPollMaxAttempts
	}
	if p.Interval == nil {
		p.Interval = func(int) time.Duration { return DefaultPollInterval }
	}
	return p, nil
}
