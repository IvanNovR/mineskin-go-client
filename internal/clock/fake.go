package clock

import (
	"sync"
	"time"
)

// Fake is a manually advanced Clock for tests. Time only moves when Advance
// is called. It is safe for concurrent use.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	seq    uint64
	timers []*fakeTimer
}

// fakeTimer is a pending callback owned by a Fake.
type fakeTimer struct {
	fake *Fake
	when time.Time
	seq  uint64
	f    func()
}

// NewFake returns a Fake whose clock starts at start.
func NewFake(start time.Time) *Fake {
	return &Fake{now: start}
}

// Now returns the fake's current time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// AfterFunc schedules fn to run synchronously inside a later call to Advance
// once d has elapsed. A non-positive d makes the timer due at the current
// time, so it fires on the next Advance (even Advance(0)).
func (f *Fake) AfterFunc(d time.Duration, fn func()) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	d = max(d, 0)
	f.seq++
	t := &fakeTimer{fake: f, when: f.now.Add(d), seq: f.seq, f: fn}
	f.timers = append(f.timers, t)
	return t
}

// Advance moves the clock forward by d and runs every callback that falls due,
// in due-time order (creation order for ties). While a callback runs, Now
// reports that callback's due time, and the fake's lock is not held, so
// callbacks may call back into the Fake. Timers scheduled by callbacks fire
// within the same Advance if they fall due before the target time.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	target := f.now.Add(max(d, 0))
	f.mu.Unlock()

	for {
		fn, ok := f.popDue(target)
		if !ok {
			return
		}
		fn()
	}
}

// popDue removes the earliest timer due at or before target and returns its
// callback with the clock set to its due time. When none is due it sets the
// clock to target and reports false.
func (f *Fake) popDue(target time.Time) (func(), bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	best := -1
	for i, t := range f.timers {
		if t.when.After(target) {
			continue
		}
		if best < 0 || t.earlierThan(f.timers[best]) {
			best = i
		}
	}
	if best < 0 {
		f.now = target
		return nil, false
	}
	t := f.timers[best]
	f.timers = append(f.timers[:best], f.timers[best+1:]...)
	if t.when.After(f.now) {
		f.now = t.when
	}
	return t.f, true
}

// earlierThan orders timers by due time, then by creation order.
func (t *fakeTimer) earlierThan(o *fakeTimer) bool {
	if t.when.Equal(o.when) {
		return t.seq < o.seq
	}
	return t.when.Before(o.when)
}

// Timers returns the number of pending (unfired, unstopped) timers.
func (f *Fake) Timers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}

// Stop cancels the timer. It returns false if it already fired or was stopped.
func (t *fakeTimer) Stop() bool {
	f := t.fake
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.timers {
		if p == t {
			f.timers = append(f.timers[:i], f.timers[i+1:]...)
			return true
		}
	}
	return false
}
