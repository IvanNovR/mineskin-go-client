package mineskin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// tick checks every job that is due at now. Attempts are counted and the next
// check is scheduled before any request is sent, so a concurrent tick never
// counts the same attempt twice. The checks of one tick run in parallel, each
// through the get throttle, and tick returns when all of them are complete.
func (p *jobPoller) tick(now time.Time) {
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
func (p *jobPoller) collectDue(now time.Time) (due []*pendingJob, batched bool) {
	candidates, attempts := p.snapshotDue(now)
	waits := make([]time.Duration, len(candidates))
	errs := make([]error, len(candidates))
	for i := range candidates {
		waits[i], errs[i] = p.opts.interval(attempts[i])
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, pj := range candidates {
		if p.pending[pj.id] != pj {
			continue
		}
		if errs[i] != nil {
			p.finishLocked(pj, nil, errs[i])
			continue
		}
		pj.nextCheckAt = now.Add(waits[i])
		pj.wait = waits[i]
		due = append(due, pj)
	}
	return due, len(p.pending) > batchThreshold
}

// snapshotDue counts an attempt for every job due at now and returns those
// still within MaxAttempts with their attempt numbers. Jobs past MaxAttempts
// are finished with ErrMaxAttempts.
func (p *jobPoller) snapshotDue(now time.Time) (jobs []*pendingJob, attempts []int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pj := range p.pending {
		if pj.nextCheckAt.After(now) {
			continue
		}
		pj.attempts++
		if pj.attempts > p.opts.MaxAttempts {
			p.finishLocked(pj, nil, fmt.Errorf("%w: job %s after %d attempts",
				ErrMaxAttempts, pj.id, p.opts.MaxAttempts))
			continue
		}
		jobs = append(jobs, pj)
		attempts = append(attempts, pj.attempts)
	}
	return jobs, attempts
}

// attemptOf returns the current attempt number of pj.
func (p *jobPoller) attemptOf(pj *pendingJob) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return pj.attempts
}

// isPending reports whether pj is still the pending entry for its id.
func (p *jobPoller) isPending(pj *pendingJob) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pending[pj.id] == pj
}

// fetchJob sends GET /v2/queue/{id} for pj and finishes it on error or when
// the response is done (or force is set). Otherwise it records the latest job
// state. A job that is no longer pending (all waiters left or it finished) is
// skipped without a request.
func (p *jobPoller) fetchJob(ctx context.Context, pj *pendingJob, force bool) {
	if !p.isPending(pj) {
		return
	}
	p.logger.Debug("polling job", "job", pj.id, "attempt", p.attemptOf(pj), "path", queuePath+"/{id}")
	// The check may queue in the get throttle; re-check once it holds a slot.
	resp, err := p.fetch.getJobGated(ctx, pj.id, func() bool { return p.isPending(pj) })
	if errors.Is(err, errCheckSkipped) {
		return
	}
	if err != nil {
		p.finish(pj, nil, err)
		return
	}
	p.observeJob(pj, resp.Job)
	if force || isPollDone(resp.Job) {
		p.finish(pj, resp, nil)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending[pj.id] == pj {
		pj.job = resp.Job
		// The next check is due a full interval after this one completed,
		// however long the request waited in the throttle.
		pj.nextCheckAt = later(pj.nextCheckAt, p.now().Add(pj.wait))
	}
}

// later returns the later of a and b.
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// observeJob calls onJobFailure the first time j is seen FAILED for pj. The
// hook runs without mu held.
func (p *jobPoller) observeJob(pj *pendingJob, j Job) {
	p.mu.Lock()
	notify := p.noteFailureLocked(pj, j)
	p.mu.Unlock()
	if notify {
		p.onJobFailure()
	}
}

// noteFailureLocked records that pj was seen FAILED and reports whether this
// is the first time. The caller must call onJobFailure after unlocking mu.
func (p *jobPoller) noteFailureLocked(pj *pendingJob, j Job) bool {
	if j.Status != JobStatusFailed || pj.failureSeen {
		return false
	}
	pj.failureSeen = true
	return true
}

// checkIndividually sends one GET per due job, all at once; the get throttle
// bounds how many are in flight. A request error fails only that job. It
// returns when every check is complete.
func (p *jobPoller) checkIndividually(ctx context.Context, due []*pendingJob) {
	p.fetchAll(ctx, due, false)
}

// fetchAll runs fetchJob for every job in parallel and waits for all of them.
func (p *jobPoller) fetchAll(ctx context.Context, jobs []*pendingJob, force bool) {
	var wg sync.WaitGroup
	for _, pj := range jobs {
		wg.Go(func() { p.fetchJob(ctx, pj, force) })
	}
	wg.Wait()
}

// checkViaList sends one list request for all due jobs. A list error fails the
// due jobs only. Every pending job found in the list gets its info updated;
// jobs missing from the list keep polling. Jobs that are done get a full GET,
// whose response (or error) completes just that job.
func (p *jobPoller) checkViaList(ctx context.Context, due []*pendingJob) {
	p.logger.Debug("polling jobs via list", "jobs", len(due), "path", queuePath)
	list, err := p.fetch.ListJobs(ctx)
	if err != nil {
		for _, pj := range due {
			p.finish(pj, nil, err)
		}
		return
	}
	p.fetchAll(ctx, p.applyList(list.Jobs), true)
}

// applyList stores the listed state of every pending job and returns the ones
// that are done.
func (p *jobPoller) applyList(jobs []Job) (done []*pendingJob) {
	failures := 0
	p.mu.Lock()
	for _, j := range jobs {
		pj, ok := p.pending[j.ID]
		if !ok {
			continue
		}
		pj.job = j
		if p.noteFailureLocked(pj, j) {
			failures++
		}
		if isPollDone(j) {
			done = append(done, pj)
		}
	}
	p.mu.Unlock()
	for range failures {
		p.onJobFailure()
	}
	return done
}
