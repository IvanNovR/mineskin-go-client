package mineskin

import "github.com/IvanNovR/mineskin-go-client/internal/clock"

// This file exposes the few unexported hooks the external contract tests need.

// WithClock replaces the client's clock; it exists only for tests.
func WithClock(c clock.Clock) Option { return withClock(c) }

// FastThrottles makes both groups practically unthrottled so tests that are
// not about throttling stay fast.
func FastThrottles() Option { return fastThrottles() }
