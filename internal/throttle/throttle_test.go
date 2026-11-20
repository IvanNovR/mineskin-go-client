package throttle

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

// boundedWait caps every wait on another goroutine so a bug fails the test
// instead of hanging it.
const boundedWait = 5 * time.Second

const churnCalls = 500

// limitsBox is a mutable LimitFunc source safe to read under Throttle.mu.
type limitsBox struct {
	mu sync.Mutex
	l  Limits
}

func (b *limitsBox) get(time.Time) Limits {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.l
}

func (b *limitsBox) set(l Limits) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.l = l
}

func newTestThrottle(l Limits) (*Throttle, *clock.Fake, *limitsBox) {
	fake := clock.NewFake(t0)
	box := &limitsBox{l: l}
	return New(fake, box.get), fake, box
}

type result struct {
	release func()
	err     error
}

func acquireAsync(th *Throttle, ctx context.Context) <-chan result {
	out := make(chan result, 1)
	go func() {
		rel, err := th.Acquire(ctx)
		out <- result{rel, err}
	}()
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(boundedWait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		pause()
	}
}

func waitWaiting(t *testing.T, th *Throttle, n int) {
	t.Helper()
	waitFor(t, "waiters to queue", func() bool { return th.Stats().Waiting == n })
}

func recv(t *testing.T, ch <-chan result) result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(boundedWait):
		t.Fatal("timed out waiting for Acquire to return")
		return result{}
	}
}

func mustGrant(t *testing.T, ch <-chan result) func() {
	t.Helper()
	r := recv(t, ch)
	if r.err != nil {
		t.Fatalf("Acquire error = %v, want a grant", r.err)
	}
	return r.release
}

func mustAcquire(t *testing.T, th *Throttle) func() {
	t.Helper()
	rel, err := th.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire error = %v", err)
	}
	return rel
}

func assertStats(t *testing.T, th *Throttle, inFlight, waiting int) {
	t.Helper()
	s := th.Stats()
	if s.InFlight != inFlight || s.Waiting != waiting {
		t.Fatalf("Stats = InFlight %d Waiting %d, want InFlight %d Waiting %d",
			s.InFlight, s.Waiting, inFlight, waiting)
	}
}

func TestAcquireImmediateWhenIdle(t *testing.T) {
	th, _, _ := newTestThrottle(Limits{Interval: 0, Concurrency: 2})

	r1 := mustAcquire(t, th)
	r2 := mustAcquire(t, th)
	assertStats(t, th, 2, 0)
	r1()
	assertStats(t, th, 1, 0)
	r1() // exactly once: must not free a second slot
	assertStats(t, th, 1, 0)
	r2()
	assertStats(t, th, 0, 0)
}

func TestAcquireRecordsLastStart(t *testing.T) {
	th, fake, _ := newTestThrottle(Limits{Concurrency: 1})
	if !th.Stats().LastStart.IsZero() {
		t.Fatal("LastStart set before any start")
	}

	fake.Advance(3 * time.Second)
	mustAcquire(t, th)

	if got, want := th.Stats().LastStart, t0.Add(3*time.Second); !got.Equal(want) {
		t.Fatalf("LastStart = %v, want %v", got, want)
	}
}

func TestAcquireRespectsInterval(t *testing.T) {
	th, fake, _ := newTestThrottle(Limits{Interval: time.Second, Concurrency: 5})
	mustAcquire(t, th)

	second := acquireAsync(th, context.Background())
	waitWaiting(t, th, 1)
	fake.Advance(999 * time.Millisecond)
	assertStats(t, th, 1, 1)
	fake.Advance(time.Millisecond)

	mustGrant(t, second)
	assertStats(t, th, 2, 0)
}

func TestAcquireRespectsConcurrency(t *testing.T) {
	th, fake, _ := newTestThrottle(Limits{Interval: time.Nanosecond, Concurrency: 2})
	r1 := mustAcquire(t, th)
	second := acquireAsync(th, context.Background())
	waitWaiting(t, th, 1)
	fake.Advance(time.Nanosecond)
	mustGrant(t, second)

	third := acquireAsync(th, context.Background())
	waitWaiting(t, th, 1)
	fake.Advance(time.Hour) // time alone must never grant (JB-16)
	assertStats(t, th, 2, 1)
	r1()

	mustGrant(t, third)
	assertStats(t, th, 2, 0)
}

func TestAcquireRespectsDeadline(t *testing.T) {
	th, fake, _ := newTestThrottle(Limits{Concurrency: 5})
	th.Delay(t0.Add(5 * time.Second))

	w := acquireAsync(th, context.Background())
	waitWaiting(t, th, 1)
	fake.Advance(4999 * time.Millisecond)
	assertStats(t, th, 0, 1)
	fake.Advance(time.Millisecond)

	mustGrant(t, w)
}

func TestAcquireEarlyTimerFireRearmsWhenDeadlineExtended(t *testing.T) {
	th, fake, _ := newTestThrottle(Limits{Concurrency: 5})
	th.Delay(t0.Add(5 * time.Second))
	w := acquireAsync(th, context.Background())
	waitWaiting(t, th, 1)

	th.deadline.Extend(t0.Add(8 * time.Second)) // no Kick: the armed timer is now early
	fake.Advance(5 * time.Second)

	assertStats(t, th, 0, 1)
	if fake.Timers() != 1 {
		t.Fatalf("Timers() = %d, want 1 re-armed timer", fake.Timers())
	}
	fake.Advance(3 * time.Second)
	mustGrant(t, w)
}

func TestDelayWithKickMovesArmedTimer(t *testing.T) {
	th, fake, _ := newTestThrottle(Limits{Concurrency: 5})
	th.Delay(t0.Add(2 * time.Second))
	w := acquireAsync(th, context.Background())
	waitWaiting(t, th, 1)

	th.Delay(t0.Add(6 * time.Second))
	fake.Advance(2 * time.Second)
	assertStats(t, th, 0, 1)
	if fake.Timers() != 1 {
		t.Fatalf("Timers() = %d, want exactly 1 armed timer", fake.Timers())
	}
	fake.Advance(4 * time.Second)

	mustGrant(t, w)
}

func TestDelayEarlierDoesNotShortenDeadline(t *testing.T) {
	th, fake, _ := newTestThrottle(Limits{Concurrency: 5})
	th.Delay(t0.Add(5 * time.Second))
	th.Delay(t0.Add(time.Second))

	if got, want := th.Stats().NotBefore, t0.Add(5*time.Second); !got.Equal(want) {
		t.Fatalf("NotBefore = %v, want %v", got, want)
	}
	w := acquireAsync(th, context.Background())
	waitWaiting(t, th, 1)
	fake.Advance(5 * time.Second)
	mustGrant(t, w)
}

func TestAcquireAllThreeConditions(t *testing.T) {
	tests := []struct {
		name       string
		limits     Limits
		setup      func(t *testing.T, th *Throttle) (held int, unblock func())
		wantTimers int
	}{
		{
			name:   "only the in-flight slot blocks",
			limits: Limits{Concurrency: 1},
			setup: func(t *testing.T, th *Throttle) (int, func()) {
				rel := mustAcquire(t, th)
				return 1, rel
			},
			wantTimers: 0,
		},
		{
			name:   "only the interval blocks",
			limits: Limits{Interval: time.Second, Concurrency: 5},
			setup: func(t *testing.T, th *Throttle) (int, func()) {
				mustAcquire(t, th)()
				return 0, nil
			},
			wantTimers: 1,
		},
		{
			name:   "only the deadline blocks",
			limits: Limits{Concurrency: 5},
			setup: func(t *testing.T, th *Throttle) (int, func()) {
				th.Delay(t0.Add(3 * time.Second))
				return 0, nil
			},
			wantTimers: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			th, fake, _ := newTestThrottle(tc.limits)
			held, unblock := tc.setup(t, th)

			w := acquireAsync(th, context.Background())
			waitWaiting(t, th, 1)
			assertStats(t, th, held, 1)
			if fake.Timers() != tc.wantTimers {
				t.Fatalf("Timers() = %d, want %d", fake.Timers(), tc.wantTimers)
			}
			if unblock != nil {
				unblock()
			} else {
				fake.Advance(3 * time.Second)
			}

			mustGrant(t, w)
		})
	}
}

func TestAcquireFIFOOrder(t *testing.T) {
	const n = 10
	th, fake, _ := newTestThrottle(Limits{Interval: time.Second, Concurrency: n + 1})
	mustAcquire(t, th)
	granted := make(chan int, n)
	for i := range n {
		go func() {
			if _, err := th.Acquire(context.Background()); err == nil {
				granted <- i
			}
		}()
		waitWaiting(t, th, i+1) // pins arrival order
	}

	var order []int
	for range n {
		fake.Advance(time.Second) // one interval releases exactly one waiter
		select {
		case i := <-granted:
			order = append(order, i)
		case <-time.After(boundedWait):
			t.Fatalf("no grant after an interval; order so far %v", order)
		}
	}

	if want := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}; !slices.Equal(order, want) {
		t.Fatalf("grant order = %v, want %v", order, want)
	}
}

func TestAcquireNewArrivalQueuesBehindWaiters(t *testing.T) {
	th, _, box := newTestThrottle(Limits{Concurrency: 1})
	mustAcquire(t, th)
	first := acquireAsync(th, context.Background())
	waitWaiting(t, th, 1)

	box.set(Limits{Concurrency: 2}) // a slot is now free, but first is still queued
	second := acquireAsync(th, context.Background())

	mustGrant(t, first)
	waitWaiting(t, th, 1)
	assertStats(t, th, 2, 1)
	select {
	case <-second:
		t.Fatal("new arrival jumped ahead of an earlier waiter")
	default:
	}
}

func TestAcquireCtxCancelLeavesQueue(t *testing.T) {
	th, _, _ := newTestThrottle(Limits{Concurrency: 1})
	holder := mustAcquire(t, th)
	w1 := acquireAsync(th, context.Background())
	waitWaiting(t, th, 1)
	ctx, cancel := context.WithCancel(context.Background())
	w2 := acquireAsync(th, ctx)
	waitWaiting(t, th, 2)
	w3 := acquireAsync(th, context.Background())
	waitWaiting(t, th, 3)

	cancel()

	r := recv(t, w2)
	if !errors.Is(r.err, context.Canceled) || r.release != nil {
		t.Fatalf("cancelled waiter = (%v, %v), want (nil, context.Canceled)", r.release != nil, r.err)
	}
	assertStats(t, th, 1, 2)
	holder()
	rel1 := mustGrant(t, w1)
	assertStats(t, th, 1, 1)
	rel1()
	mustGrant(t, w3)
}

func TestAcquireCtxAlreadyDone(t *testing.T) {
	th, _, _ := newTestThrottle(Limits{Concurrency: 1})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rel, err := th.Acquire(ctx)

	if !errors.Is(err, context.Canceled) || rel != nil {
		t.Fatalf("Acquire = (%v, %v), want (nil, context.Canceled)", rel != nil, err)
	}
	assertStats(t, th, 0, 0)
}

func TestAcquireCtxDeadlineExceededIsReturned(t *testing.T) {
	th, _, _ := newTestThrottle(Limits{Concurrency: 1})
	mustAcquire(t, th)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	_, err := th.Acquire(ctx)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	assertStats(t, th, 1, 0)
}

func TestAbandonAfterGrantReturnsSlotAndDispatchesNext(t *testing.T) {
	th, _, _ := newTestThrottle(Limits{Concurrency: 1})
	holder := mustAcquire(t, th)
	ctx, cancel := context.WithCancel(context.Background())
	w, err := th.enqueue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := acquireAsync(th, context.Background())
	waitWaiting(t, th, 2)
	holder() // grants w
	cancel()

	got := th.abandon(w, ctx.Err()) // ctx "wins" after the grant

	if !errors.Is(got, context.Canceled) {
		t.Fatalf("abandon = %v, want context.Canceled", got)
	}
	mustGrant(t, next) // the returned slot went to the next waiter
	assertStats(t, th, 1, 0)
}

func TestAcquireCancelRacingGrantIsExactlyOneOutcome(t *testing.T) {
	const rounds = 200
	for range rounds {
		th, _, _ := newTestThrottle(Limits{Concurrency: 1})
		holder := mustAcquire(t, th)
		ctx, cancel := context.WithCancel(context.Background())
		w := acquireAsync(th, ctx)
		waitWaiting(t, th, 1)

		var wg sync.WaitGroup
		wg.Go(cancel)
		wg.Go(holder)
		wg.Wait()
		r := recv(t, w)

		if (r.release == nil) == (r.err == nil) {
			t.Fatalf("Acquire = (release %v, err %v), want exactly one", r.release != nil, r.err)
		}
		if r.err != nil {
			if !errors.Is(r.err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", r.err)
			}
			assertStats(t, th, 0, 0) // the slot is back
			continue
		}
		r.release()
		assertStats(t, th, 0, 0)
	}
}

func TestLimitsReadOnEveryDispatch(t *testing.T) {
	t.Run("interval change re-arms on Kick", func(t *testing.T) {
		th, fake, box := newTestThrottle(Limits{Interval: time.Second, Concurrency: 5})
		mustAcquire(t, th)
		w := acquireAsync(th, context.Background())
		waitWaiting(t, th, 1)

		box.set(Limits{Interval: 100 * time.Millisecond, Concurrency: 5})
		th.Kick()
		fake.Advance(100 * time.Millisecond)

		mustGrant(t, w)
	})
	t.Run("raised concurrency dispatches on Kick", func(t *testing.T) {
		th, _, box := newTestThrottle(Limits{Concurrency: 1})
		mustAcquire(t, th)
		w := acquireAsync(th, context.Background())
		waitWaiting(t, th, 1)

		box.set(Limits{Concurrency: 2})
		th.Kick()

		mustGrant(t, w)
	})
	t.Run("lowered concurrency blocks until enough releases", func(t *testing.T) {
		th, _, box := newTestThrottle(Limits{Concurrency: 3})
		rels := []func(){mustAcquire(t, th), mustAcquire(t, th), mustAcquire(t, th)}
		box.set(Limits{Concurrency: 1})
		th.Kick()
		w := acquireAsync(th, context.Background())
		waitWaiting(t, th, 1)

		rels[0]()
		rels[1]()
		assertStats(t, th, 1, 1) // one in flight still fills the cap of 1
		rels[2]()

		mustGrant(t, w)
	})
}

func TestLimitsAreNormalized(t *testing.T) {
	th, _, _ := newTestThrottle(Limits{Interval: -time.Hour, Concurrency: -4})

	r := mustAcquire(t, th) // Concurrency < 1 acts as 1, negative Interval as 0
	w := acquireAsync(th, context.Background())
	waitWaiting(t, th, 1)
	assertStats(t, th, 1, 1)
	r()

	mustGrant(t, w)
}

func TestCloseFailsWaitersAndFutureAcquires(t *testing.T) {
	th, fake, _ := newTestThrottle(Limits{Interval: time.Second, Concurrency: 1})
	rel := mustAcquire(t, th)
	w1 := acquireAsync(th, context.Background())
	w2 := acquireAsync(th, context.Background())
	waitWaiting(t, th, 2)

	th.Close()
	th.Close() // idempotent

	for i, w := range []<-chan result{w1, w2} {
		r := recv(t, w)
		if !errors.Is(r.err, ErrClosed) || r.release != nil {
			t.Fatalf("waiter %d = (%v, %v), want (nil, ErrClosed)", i, r.release != nil, r.err)
		}
	}
	if _, err := th.Acquire(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Acquire after Close = %v, want ErrClosed", err)
	}
	rel() // must not panic
	rel()
	if fake.Timers() != 0 {
		t.Fatalf("Timers() = %d after Close, want 0", fake.Timers())
	}
	assertStats(t, th, 0, 0)
	th.Delay(t0.Add(time.Hour)) // no-ops after Close
	th.Kick()
	if fake.Timers() != 0 {
		t.Fatalf("Timers() = %d after Delay on a closed throttle, want 0", fake.Timers())
	}
}

func TestExactlyOnceUnderChurn(t *testing.T) {
	th, _, _ := newTestThrottle(Limits{Concurrency: 3})
	var returned, granted, cancelled, closed atomic.Int64
	var wg sync.WaitGroup
	for i := range churnCalls {
		if i == churnCalls/2 {
			th.Close()
		}
		ctx, cancel := context.WithCancel(context.Background())
		if i%3 == 0 {
			go cancel()
		}
		wg.Go(func() {
			defer cancel()
			rel, err := th.Acquire(ctx)
			returned.Add(1)
			switch {
			case err == nil:
				granted.Add(1)
				rel()
			case errors.Is(err, context.Canceled):
				cancelled.Add(1)
			case errors.Is(err, ErrClosed):
				closed.Add(1)
			default:
				t.Errorf("unexpected error %v", err)
			}
		})
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(boundedWait):
		t.Fatal("Acquire calls did not all return")
	}

	if returned.Load() != churnCalls {
		t.Fatalf("returned %d, want %d", returned.Load(), churnCalls)
	}
	if sum := granted.Load() + cancelled.Load() + closed.Load(); sum != churnCalls {
		t.Fatalf("granted+cancelled+closed = %d, want %d", sum, churnCalls)
	}
	assertStats(t, th, 0, 0)
}

func TestExactlyOnceUnderChurnBeforeClose(t *testing.T) {
	th, fake, _ := newTestThrottle(Limits{Interval: time.Millisecond, Concurrency: 3})
	var returned atomic.Int64
	var wg sync.WaitGroup
	stop := make(chan struct{})
	go func() { // keep the fake clock moving so interval waiters progress
		for {
			select {
			case <-stop:
				return
			default:
				fake.Advance(time.Millisecond)
			}
		}
	}()
	for i := range churnCalls {
		ctx, cancel := context.WithCancel(context.Background())
		if i%2 == 0 {
			go cancel()
		}
		wg.Go(func() {
			defer cancel()
			if rel, err := th.Acquire(ctx); err == nil {
				rel()
			}
			returned.Add(1)
		})
	}
	wg.Wait()
	close(stop)

	if returned.Load() != churnCalls {
		t.Fatalf("returned %d, want %d", returned.Load(), churnCalls)
	}
	assertStats(t, th, 0, 0)
}

func TestEarliestStartIsLaterOfIntervalAndDeadline(t *testing.T) {
	th, fake, box := newTestThrottle(Limits{Interval: 10 * time.Second, Concurrency: 5})
	if got := th.EarliestStart(); !got.IsZero() {
		t.Fatalf("EarliestStart() = %v before any start, want zero", got)
	}

	mustAcquire(t, th)
	if got, want := th.EarliestStart(), t0.Add(10*time.Second); !got.Equal(want) {
		t.Fatalf("EarliestStart() = %v, want last start + interval %v", got, want)
	}

	th.Delay(t0.Add(30 * time.Second))
	if got, want := th.EarliestStart(), t0.Add(30*time.Second); !got.Equal(want) {
		t.Fatalf("EarliestStart() = %v, want the later deadline %v", got, want)
	}

	box.set(Limits{Interval: time.Minute, Concurrency: 5})
	fake.Advance(time.Second)
	if got, want := th.EarliestStart(), t0.Add(time.Minute); !got.Equal(want) {
		t.Fatalf("EarliestStart() = %v, want %v after the interval grew", got, want)
	}
}

// pause waits one poll tick between two checks of shared state; no assertion
// depends on its length.
func pause() { time.Sleep(50 * time.Microsecond) }
