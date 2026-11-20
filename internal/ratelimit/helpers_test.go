package ratelimit

import (
	"bytes"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

type syncWG = sync.WaitGroup

const waitTimeout = 5 * time.Second

func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// eventually polls cond until it holds or the test times out.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// advanceUntil advances the fake clock in 250 ms steps until done delivers.
func advanceUntil(t *testing.T, fake *clock.Fake, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("call error = %v", err)
			}
			return
		default:
			fake.Advance(250 * time.Millisecond)
			time.Sleep(time.Millisecond)
		}
	}
	t.Fatal("call did not finish")
}
