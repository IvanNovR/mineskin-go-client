package mineskin

import (
	"context"
	"errors"
)

// errCheckSkipped is returned by a request whose start gate declined it. The
// poller treats it as "nothing to do", never as a job failure.
var errCheckSkipped = errors.New("mineskin: request skipped by its start gate")

// startGateKey is the context key of a request's start gate.
type startGateKey struct{}

// withStartGate returns a context whose request, once it holds a throttle
// slot, calls proceed and is abandoned with errCheckSkipped when it returns
// false. The poller uses it so a job whose waiters left while its check was
// queued in the get throttle is not fetched.
func withStartGate(ctx context.Context, proceed func() bool) context.Context {
	return context.WithValue(ctx, startGateKey{}, proceed)
}

// passStartGate reports nil when the request may start: no gate is set, or the
// gate allows it. Otherwise it returns errCheckSkipped.
func passStartGate(ctx context.Context) error {
	if proceed, ok := ctx.Value(startGateKey{}).(func() bool); ok && !proceed() {
		return errCheckSkipped
	}
	return nil
}
