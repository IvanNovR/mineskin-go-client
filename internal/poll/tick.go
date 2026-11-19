package poll

import (
	"context"
	"errors"
	"sync"
	"time"
)

// tick checks every job that is due at now. Attempts are counted and the next
// check is scheduled before any request is sent, so a concurrent tick never
// counts the same attempt twice. The checks of one tick run in parallel, each
// through the fetcher, and tick returns when all of them are complete.
func (p *Poller[R]) tick(now time.Time) {
	due, batched := p.collectDue(now)
	if len(due) == 0 {
		return
	}
	ctx := p.ctx
	if batched {
		p.checkViaList(ctx, due)
		return
	}
	p.checkIndividually(ctx, due)
}

// collectDue returns the jobs due at now and whether to check them with a list
// request (more than batchThreshold jobs pending). For each due job it
// increments attempts and sets nextCheckAt. A job past MaxAttempts, or whose
// interval is invalid or panics, is finished with an error instead and not
// returned.
//
// The PollInterval callback runs without mu held: attempts are counted and
// due jobs snapshotted under the lock, the callbacks run unlocked, and the
// schedule is applied under the lock again. A job that is no longer pending by
// then is dropped. Only the run goroutine ticks, so no other tick sees a
// job's old nextCheckAt in between.
func (p *Poller[R]) collectDue(now time.Time) (due []*Waiter[R], batched bool) {
	var zero R
	candidates, attempts := p.snapshotDue(now)
	waits := make([]time.Duration, len(candidates))
	errs := make([]error, len(candidates))
	for i := range candidates {
		waits[i], errs[i] = p.opts.interval(attempts[i])
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, w := range candidates {
		if p.pending[w.id] != w {
			continue
		}
		if errs[i] != nil {
			p.finishLocked(w, zero, State{}, errs[i])
			continue
		}
		w.nextCheckAt = now.Add(waits[i])
		w.wait = waits[i]
		due = append(due, w)
	}
	return due, len(p.pending) > batchThreshold
}

// snapshotDue counts an attempt for every job due at now and returns those
// still within MaxAttempts with their attempt numbers. Jobs past MaxAttempts
// are finished with an *ExhaustedError.
func (p *Poller[R]) snapshotDue(now time.Time) (jobs []*Waiter[R], attempts []int) {
	var zero R
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, w := range p.pending {
		if w.nextCheckAt.After(now) {
			continue
		}
		w.attempts++
		if w.attempts > p.opts.MaxAttempts {
			p.finishLocked(w, zero, State{}, &ExhaustedError{ID: w.id, Attempts: p.opts.MaxAttempts})
			continue
		}
		jobs = append(jobs, w)
		attempts = append(attempts, w.attempts)
	}
	return jobs, attempts
}

// attemptOf returns the current attempt number of w.
func (p *Poller[R]) attemptOf(w *Waiter[R]) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return w.attempts
}

// isPending reports whether w is still the pending entry for its id.
func (p *Poller[R]) isPending(w *Waiter[R]) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pending[w.id] == w
}

// fetchJob gets the job of w and finishes it on error or when
// the response is done (or force is set). Otherwise it records the latest job
// state. A job that is no longer pending (all waiters left or it finished) is
// skipped without a request.
func (p *Poller[R]) fetchJob(ctx context.Context, w *Waiter[R], force bool) {
	if !p.isPending(w) {
		return
	}
	p.logger.Debug("polling job", "job", w.id, "attempt", p.attemptOf(w))
	// The check may queue in the fetcher; re-check once it holds a slot.
	resp, st, err := p.fetch.Get(ctx, w.id, func() bool { return p.isPending(w) })
	if errors.Is(err, ErrSkipped) {
		return
	}
	if err != nil {
		var zero R
		p.finish(w, zero, State{}, err)
		return
	}
	p.observeState(w, st)
	if force || st.Done {
		p.finish(w, resp, st, nil)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending[w.id] == w {
		w.state = st
		// The next check is due a full interval after this one completed,
		// however long the request waited in the throttle.
		w.nextCheckAt = later(w.nextCheckAt, p.now().Add(w.wait))
	}
}

// later returns the later of a and b.
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// observeState calls onJobFailure the first time st is seen failed for w. The
// hook runs without mu held.
func (p *Poller[R]) observeState(w *Waiter[R], st State) {
	p.mu.Lock()
	notify := p.noteFailureLocked(w, st)
	p.mu.Unlock()
	if notify {
		p.onJobFailure()
	}
}

// noteFailureLocked records that w was seen FAILED and reports whether this
// is the first time. The caller must call onJobFailure after unlocking mu.
func (p *Poller[R]) noteFailureLocked(w *Waiter[R], st State) bool {
	if !st.Failed || w.failureSeen {
		return false
	}
	w.failureSeen = true
	return true
}

// checkIndividually sends one GET per due job, all at once; the fetcher
// bounds how many are in flight. A request error fails only that job. It
// returns when every check is complete.
func (p *Poller[R]) checkIndividually(ctx context.Context, due []*Waiter[R]) {
	p.fetchAll(ctx, due, false)
}

// fetchAll runs fetchJob for every job in parallel and waits for all of them.
func (p *Poller[R]) fetchAll(ctx context.Context, jobs []*Waiter[R], force bool) {
	var wg sync.WaitGroup
	for _, w := range jobs {
		wg.Go(func() { p.fetchJob(ctx, w, force) })
	}
	wg.Wait()
}

// checkViaList sends one list request for all due jobs. A list error fails the
// due jobs only. Every pending job found in the list gets its info updated;
// jobs missing from the list keep polling. Jobs that are done get a full GET,
// whose response (or error) completes just that job.
func (p *Poller[R]) checkViaList(ctx context.Context, due []*Waiter[R]) {
	p.logger.Debug("polling jobs via list", "jobs", len(due))
	states, err := p.fetch.List(ctx)
	if err != nil {
		var zero R
		for _, w := range due {
			p.finish(w, zero, State{}, err)
		}
		return
	}
	p.fetchAll(ctx, p.applyList(states), true)
}

// applyList stores the listed state of every pending job and returns the ones
// that are done.
func (p *Poller[R]) applyList(states []State) (done []*Waiter[R]) {
	failures := 0
	p.mu.Lock()
	for _, st := range states {
		w, ok := p.pending[st.ID]
		if !ok {
			continue
		}
		w.state = st
		if p.noteFailureLocked(w, st) {
			failures++
		}
		if st.Done {
			done = append(done, w)
		}
	}
	p.mu.Unlock()
	for range failures {
		p.onJobFailure()
	}
	return done
}
