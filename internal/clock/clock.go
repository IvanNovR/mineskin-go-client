// Package clock is a tiny time seam: production code asks a Clock for the
// current time and for delayed callbacks, so tests can substitute a Fake and
// control time without real sleeps.
package clock

import "time"

// Clock supplies the current time and delayed callbacks.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// AfterFunc runs f in its own goroutine after d has elapsed and returns
	// a Timer that can cancel it.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a pending callback created by Clock.AfterFunc.
type Timer interface {
	// Stop prevents the callback from running. It returns true if the call
	// stopped the timer and false if it had already fired or been stopped.
	Stop() bool
}

// realClock is the production Clock backed by the time package.
type realClock struct{}

// Real returns a Clock that wraps time.Now and time.AfterFunc.
func Real() Clock { return realClock{} }

// Now returns time.Now().
func (realClock) Now() time.Time { return time.Now() }

// AfterFunc wraps time.AfterFunc.
func (realClock) AfterFunc(d time.Duration, f func()) Timer {
	return time.AfterFunc(d, f)
}
