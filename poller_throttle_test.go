package mineskin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPollerHTTPBoundedByGetConcurrency(t *testing.T) {
	release := make(chan struct{})
	handler, peak := trackConcurrency(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v2/queue/")
		writeJSON(200, jobJSON(id, "completed"))(w, r)
	})
	srv := newTestServer(t, handler)
	c := newTestClient(t, srv.URL,
		WithGetThrottle(Throttle{Interval: time.Nanosecond, Concurrency: 2}),
		WithJobPolling(PollOptions{InitialDelay: time.Millisecond}))
	t.Cleanup(func() { _ = c.Close() })

	var wg sync.WaitGroup
	for _, id := range []string{"a", "b", "c", "d"} {
		wg.Go(func() {
			if _, err := c.WaitForJob(waitCtx(t), Job{ID: id}); err != nil {
				t.Errorf("WaitForJob(%s) error = %v", id, err)
			}
		})
	}
	// Requests are held, so two in flight is reached for certain. The poller
	// runs one tick at a time and waits for its checks, so a third request
	// before release can only come from a throttle that ignores its limit; the
	// throttle's own count must still read two while the handlers hold them.
	eventually(t, "two GETs in flight", func() bool { return peak() >= 2 })
	if got := c.getGroup.Throttle.Stats().InFlight; got > 2 {
		t.Errorf("throttle InFlight = %d while requests are held, want <= 2", got)
	}
	close(release)
	wg.Wait()
	if got := c.getGroup.Throttle.Stats().InFlight; got != 0 {
		t.Errorf("throttle InFlight = %d after all jobs finished, want 0", got)
	}
	if got := peak(); got != 2 {
		t.Errorf("peak in-flight GETs = %d, want exactly 2", got)
	}
}

func TestPollerMaxAttemptsIgnoresThrottleWait(t *testing.T) {
	var mu sync.Mutex
	gets := map[string]int{}
	release := make(chan struct{})
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/v2/queue/")
		mu.Lock()
		gets[id]++
		mu.Unlock()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		writeJSON(200, jobJSON(id, "waiting"))(w, r)
	})
	c := newTestClient(t, srv.URL,
		WithGetThrottle(Throttle{Interval: time.Nanosecond, Concurrency: 1}),
		WithJobPolling(PollOptions{
			InitialDelay: time.Millisecond, MaxAttempts: 2,
			Interval: PollIntervalFunc(func(int) time.Duration { return time.Millisecond }),
		}))
	t.Cleanup(func() { _ = c.Close() })

	ids := []string{"a", "b", "c", "d"}
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Go(func() { _, errs[i] = c.WaitForJob(context.Background(), Job{ID: id}) })
	}
	// Hold the first request until others are queued behind it, so the
	// throttle wait is certain rather than a matter of timing.
	eventually(t, "checks queued in the get throttle", func() bool { return c.getGroup.Throttle.Stats().Waiting > 0 })
	close(release)
	wg.Wait()
	for i, id := range ids {
		if !errors.Is(errs[i], ErrMaxAttempts) {
			t.Errorf("%s error = %v, want ErrMaxAttempts", id, errs[i])
		}
		mu.Lock()
		if gets[id] != 2 {
			t.Errorf("%s GETs = %d, want exactly 2", id, gets[id])
		}
		mu.Unlock()
	}
}
