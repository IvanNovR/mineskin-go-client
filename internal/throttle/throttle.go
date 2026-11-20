package throttle

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

// ErrClosed is returned by Acquire for waiters queued when the Throttle is
// closed, and for every Acquire after Close.
var ErrClosed = errors.New("throttle: closed")

// Limits are the pacing rules in force at one dispatch.
type Limits struct {
	// Interval is the minimum time between two starts. Negative acts as 0.
	Interval time.Duration
	// Concurrency is the maximum number of requests in flight. Values below
	// 1 act as 1.
	Concurrency int
}

// LimitFunc reports the current Limits. It is called with the Throttle's
// internal mutex held, so it must be quick, must not block, and must never
// call into the Throttle.
type LimitFunc func(now time.Time) Limits

// Stats is a snapshot of a Throttle's state.
type Stats struct {
	// InFlight is the number of granted, unreleased requests.
	InFlight int
	// Waiting is the number of queued Acquire calls.
	Waiting int
	// LastStart is when the most recent request started; zero if none has.
	LastStart time.Time
	// NotBefore is the server-imposed deadline; zero if unset.
	NotBefore time.Time
}

// waiterState tracks one queued Acquire. It is guarded by Throttle.mu.
type waiterState int

const (
	waiterQueued waiterState = iota
	waiterGranted
	waiterFailed
)

// waiter is one queued Acquire call.
type waiter struct {
	ready chan struct{} // closed once the waiter is granted or failed
	state waiterState
	err   error
	elem  *list.Element
}

// Throttle admits requests in FIFO order. A request starts only when all
// three hold: a free in-flight slot, the minimum interval since the last
// start, and the "not before" deadline passed. It is safe for concurrent use.
type Throttle struct {
	clock    clock.Clock
	limits   LimitFunc
	deadline Deadline

	mu        sync.Mutex
	queue     *list.List // of *waiter, oldest first
	inFlight  int
	started   bool
	lastStart time.Time
	timer     clock.Timer
	closed    bool
}

// New returns a Throttle that reads its limits from limits on every dispatch
// and takes time and timers from c.
//
// Lock order is Throttle.mu then the caller's locks: limits runs under
// Throttle.mu, so callers must never call into the Throttle (Acquire, Delay,
// Kick, Close, Stats or a release func) while holding a lock that limits
// also takes.
func New(c clock.Clock, limits LimitFunc) *Throttle {
	return &Throttle{clock: c, limits: limits, queue: list.New()}
}

// Acquire blocks until the request may start and returns a release func that
// the caller must call exactly once when the request ends; further calls are
// no-ops. It returns the ctx error if ctx is done first (the waiter leaves the
// queue at once) and ErrClosed if the Throttle is closed. It never returns
// both a release func and an error.
func (t *Throttle) Acquire(ctx context.Context) (release func(), err error) {
	w, err := t.enqueue(ctx)
	if err != nil {
		return nil, err
	}
	return t.wait(ctx, w)
}

// enqueue queues a new waiter behind all existing ones and dispatches.
func (t *Throttle) enqueue(ctx context.Context) (*waiter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, ErrClosed
	}
	w := &waiter{ready: make(chan struct{})}
	w.elem = t.queue.PushBack(w)
	t.dispatchLocked(t.clock.Now())
	return w, nil
}

// wait blocks until w is granted or failed, or ctx is done.
func (t *Throttle) wait(ctx context.Context, w *waiter) (func(), error) {
	select {
	case <-w.ready:
		return t.outcome(w)
	case <-ctx.Done():
		return nil, t.abandon(w, ctx.Err())
	}
}

// outcome converts a settled waiter into Acquire's result.
func (t *Throttle) outcome(w *waiter) (func(), error) {
	t.mu.Lock()
	state, err := w.state, w.err
	t.mu.Unlock()
	if state == waiterFailed {
		return nil, err
	}
	return t.releaser(), nil
}

// releaser returns a once-only func that frees one in-flight slot.
func (t *Throttle) releaser() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.inFlight--
			t.dispatchLocked(t.clock.Now())
		})
	}
}

// abandon handles ctx finishing while w waits. If w is still queued it leaves
// the queue; if w was granted in the meantime the slot is returned and the
// next waiter dispatched; if w failed, that failure is reported. It returns
// the error Acquire must report.
func (t *Throttle) abandon(w *waiter, ctxErr error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch w.state {
	case waiterFailed:
		return w.err
	case waiterGranted:
		t.inFlight--
	case waiterQueued:
		t.queue.Remove(w.elem)
	}
	t.dispatchLocked(t.clock.Now())
	return ctxErr
}

// Delay moves the "not before" deadline to until if that is later than the
// current one, then re-runs dispatch. The deadline never moves earlier.
func (t *Throttle) Delay(until time.Time) {
	t.deadline.Extend(until)
	t.Kick()
}

// Kick re-runs dispatch, so changed limits take effect for queued waiters.
func (t *Throttle) Kick() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dispatchLocked(t.clock.Now())
}

// Close fails every queued waiter with ErrClosed, cancels the pending timer
// and makes all later Acquire calls fail with ErrClosed. It is idempotent.
// Releases of requests already in flight remain safe.
func (t *Throttle) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	t.stopTimerLocked()
	for e := t.queue.Front(); e != nil; e = e.Next() {
		w := e.Value.(*waiter)
		w.state, w.err = waiterFailed, ErrClosed
		close(w.ready)
	}
	t.queue.Init()
}

// Stats returns a snapshot of the Throttle's state. It is for diagnostics and
// tests only: the values may be stale as soon as it returns, so never base a
// decision on them.
func (t *Throttle) Stats() Stats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Stats{
		InFlight:  t.inFlight,
		Waiting:   t.queue.Len(),
		LastStart: t.lastStart,
		NotBefore: t.deadline.Load(),
	}
}

// EarliestStart returns the earliest time at which the time conditions (the
// server deadline and the interval since the last start) allow a start. It
// ignores free slots and queued waiters, so it is a lower bound for a request
// queued now. It is zero when nothing holds a start back.
func (t *Throttle) EarliestStart() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.nextStartLocked(normalizeLimits(t.limits(t.clock.Now())).Interval)
}

// normalizeLimits clamps limits to their documented floors.
func normalizeLimits(l Limits) Limits {
	return Limits{Interval: max(l.Interval, 0), Concurrency: max(l.Concurrency, 1)}
}

// nextStartLocked is the earliest time the time conditions allow a start.
func (t *Throttle) nextStartLocked(interval time.Duration) time.Time {
	next := t.deadline.Load()
	if t.started {
		if due := t.lastStart.Add(interval); due.After(next) {
			next = due
		}
	}
	return next
}

// dispatchLocked grants waiters from the head of the queue while a slot is
// free and both time conditions hold, then arms one timer if only time is
// blocking. t.mu must be held.
func (t *Throttle) dispatchLocked(now time.Time) {
	if t.closed {
		return
	}
	t.stopTimerLocked()
	lim := normalizeLimits(t.limits(now))
	for t.queue.Len() > 0 && t.inFlight < lim.Concurrency {
		if t.nextStartLocked(lim.Interval).After(now) {
			t.armLocked(t.nextStartLocked(lim.Interval).Sub(now))
			return
		}
		t.grantHeadLocked(now)
	}
}

// grantHeadLocked starts the oldest waiter.
func (t *Throttle) grantHeadLocked(now time.Time) {
	w := t.queue.Remove(t.queue.Front()).(*waiter)
	w.state = waiterGranted
	t.inFlight++
	t.started, t.lastStart = true, now
	close(w.ready)
}

// armLocked arms the single wake-up timer for d from now.
func (t *Throttle) armLocked(d time.Duration) {
	t.timer = t.clock.AfterFunc(d, t.Kick)
}

// stopTimerLocked cancels the pending wake-up timer, if any.
func (t *Throttle) stopTimerLocked() {
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
}
