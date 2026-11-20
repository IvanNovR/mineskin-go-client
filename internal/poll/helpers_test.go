package poll

import (
	"runtime"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

const (
	// fallbackWaitTimeout bounds every blocking wait of a test.
	fallbackWaitTimeout = 20 * time.Second
	// deadlineMargin is kept free before the test binary's own deadline so a
	// stuck wait fails with a goroutine dump instead of a bare panic.
	deadlineMargin = 2 * time.Second
	// pollEvery is how often eventually and driveClock re-check.
	pollEvery = time.Millisecond
)

// waitTimeout is the single bound for blocking waits: fallbackWaitTimeout,
// shortened to fit before the test deadline when there is one.
func waitTimeout(t *testing.T) time.Duration {
	t.Helper()
	limit := fallbackWaitTimeout
	if dl, ok := t.Deadline(); ok {
		limit = min(limit, time.Until(dl)-deadlineMargin)
	}
	return max(limit, time.Millisecond)
}

// failTimeout fails the test and dumps every goroutine to ease diagnosis.
func failTimeout(t *testing.T, what string) {
	t.Helper()
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	t.Fatalf("timed out waiting for %s\n%s", what, buf)
}

// eventually polls cond until it holds or waitTimeout passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout(t))
	for !cond() {
		if time.Now().After(deadline) {
			failTimeout(t, what)
		}
		pause()
	}
}

// driveClock runs fn in a goroutine and advances fake by step until fn
// returns, then returns fn's error.
func driveClock(t *testing.T, fake *clock.Fake, step time.Duration, fn func() error) error {
	t.Helper()
	res := make(chan error, 1)
	go func() { res <- fn() }()
	deadline := time.Now().Add(waitTimeout(t))
	for time.Now().Before(deadline) {
		select {
		case err := <-res:
			return err
		default:
		}
		fake.Advance(step)
		pause()
	}
	failTimeout(t, "the call to finish while advancing the fake clock")
	return nil
}

// pause waits one poll tick between two checks of shared state. It is the only
// real-time wait the polling helpers use; no assertion depends on its length.
func pause() { time.Sleep(pollEvery) }
