package poll

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

var errClosed = errors.New("closed")

func TestPollerUsesBaseContext(t *testing.T) {
	type key struct{}
	base := context.WithValue(context.Background(), key{}, "base")
	f := &fakeFetcher{getFn: func(id string) (*fakeResp, error) { return completed(id), nil }}
	p := New(Config[*fakeResp]{
		Ctx: base, Clock: clock.NewFake(pollerT0), Fetcher: f,
		Options: Options{InitialDelay: time.Second, Interval: func(int) time.Duration { return time.Second }, MaxAttempts: 10},
	})
	p.add("j0", time.Time{})
	p.tick(pollerT0.Add(time.Hour))

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.getCtxs) != 1 || f.getCtxs[0] != base {
		t.Errorf("fetcher ctxs = %v, want exactly the base context", f.getCtxs)
	}
}

func TestPollerRegisterAfterCloseIsFinished(t *testing.T) {
	p := newTestPoller(t, &fakeFetcher{}, Options{})
	p.Close(errClosed)
	w := p.Register("j0", time.Time{})
	if !isFinished(w) || !errors.Is(w.err, errClosed) {
		t.Errorf("register after close: finished=%v err=%v, want finished with the close error", isFinished(w), w.err)
	}
	if p.Running() {
		t.Error("run goroutine started after close")
	}
}

func TestPollerCloseFailsPending(t *testing.T) {
	p := newTestPoller(t, &fakeFetcher{}, Options{})
	ws := addJobs(p, 3)
	p.Close(errClosed)
	for _, w := range ws {
		r, st, err := w.Result()
		if !isFinished(w) || !errors.Is(err, errClosed) || r != nil || st != (State{}) {
			t.Errorf("job %s: result = %v %+v %v, want the close error only", w.id, r, st, err)
		}
	}
	if got := pendingCount(p); got != 0 {
		t.Errorf("pending = %d, want 0", got)
	}
}

func TestPollerCloseIsIdempotent(t *testing.T) {
	p := newTestPoller(t, &fakeFetcher{}, Options{})
	w := p.Register("a", time.Time{})
	p.Close(errClosed)
	p.Close(errors.New("second")) // keeps the first error
	if _, _, err := w.Result(); !errors.Is(err, errClosed) {
		t.Errorf("err = %v, want the first close error", err)
	}
}

func TestPollerRunLoopPollsUntilDoneThenStops(t *testing.T) {
	fake := clock.NewFake(pollerT0)
	var calls int
	f := &fakeFetcher{getFn: func(id string) (*fakeResp, error) {
		calls++
		if calls < 3 {
			return waiting(id), nil
		}
		return completed(id), nil
	}}
	p := New(Config[*fakeResp]{
		Ctx: context.Background(), Clock: fake, Fetcher: f,
		Options: Options{InitialDelay: time.Second, Interval: func(int) time.Duration { return time.Second }, MaxAttempts: 10},
	})
	t.Cleanup(func() { p.Close(errClosed) })

	w := p.Register("a", time.Time{})
	if !p.Running() {
		t.Fatal("Register did not start the run loop")
	}
	deadline := time.Now().Add(waitTimeout)
	for !isFinished(w) {
		if time.Now().After(deadline) {
			t.Fatal("job not finished")
		}
		fake.Advance(time.Second)
		time.Sleep(time.Millisecond)
	}

	r, st, err := w.Result()
	if err != nil || r == nil || !st.Done {
		t.Errorf("Result() = %v %+v %v, want the completed response", r, st, err)
	}
	eventuallyTrue(t, func() bool { return !p.Running() })
}

func TestPollerLeaveStopsIdleRunLoop(t *testing.T) {
	p := newTestPoller(t, &fakeFetcher{}, Options{InitialDelay: time.Hour})
	t.Cleanup(func() { p.Close(errClosed) })
	w := p.Register("a", time.Time{})

	p.Leave(w)

	eventuallyTrue(t, func() bool { return !p.Running() })
}

// eventuallyTrue polls cond until it holds or waitTimeout passes.
func eventuallyTrue(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPollerCloseWaitsForRunLoopAndInFlightGet(t *testing.T) {
	fake := clock.NewFake(pollerT0)
	entered := make(chan struct{})
	f := &fakeFetcher{}
	p := New(Config[*fakeResp]{
		Ctx: context.Background(), Clock: fake, Fetcher: f,
		Options: Options{InitialDelay: time.Second, Interval: func(int) time.Duration { return time.Second }, MaxAttempts: 10},
	})
	release := make(chan struct{})
	f.getFn = func(id string) (*fakeResp, error) {
		close(entered)
		<-release
		return waiting(id), nil
	}
	w := p.Register("a", time.Time{})
	go func() {
		for {
			select {
			case <-entered:
				return
			default:
				fake.Advance(time.Second)
				time.Sleep(time.Millisecond)
			}
		}
	}()
	<-entered

	closed := make(chan struct{})
	go func() { p.Close(errClosed); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned while a check was in flight")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(waitTimeout):
		t.Fatal("Close did not return")
	}
	if _, _, err := w.Result(); !errors.Is(err, errClosed) {
		t.Errorf("err = %v, want the close error", err)
	}
	if p.Running() {
		t.Error("run loop alive after Close returned")
	}
}
