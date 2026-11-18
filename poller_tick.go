package mineskin

import (
	"context"
	"fmt"
	"time"
)

// tick checks every job that is due at now. Attempts are counted and the next
// check is scheduled before any request is sent, so a concurrent tick never
// counts the same attempt twice. Checks run one after another.
func (p *jobPoller) tick(now time.Time) {
	due, batched := p.collectDue(now)
	if len(due) == 0 {
		return
	}
	ctx := context.Background()
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
	resp, err := p.fetch.GetJob(ctx, pj.id)
	if err != nil {
		p.finish(pj, nil, err)
		return
	}
	if force || isPollDone(resp.Job) {
		p.finish(pj, resp, nil)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending[pj.id] == pj {
		pj.job = resp.Job
	}
}

// checkIndividually sends one GET per due job. A request error fails only
// that job.
func (p *jobPoller) checkIndividually(ctx context.Context, due []*pendingJob) {
	for _, pj := range due {
		p.fetchJob(ctx, pj, false)
	}
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
	for _, pj := range p.applyList(list.Jobs) {
		p.fetchJob(ctx, pj, true)
	}
}

// applyList stores the listed state of every pending job and returns the ones
// that are done.
func (p *jobPoller) applyList(jobs []Job) (done []*pendingJob) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, j := range jobs {
		pj, ok := p.pending[j.ID]
		if !ok {
			continue
		}
		pj.job = j
		if isPollDone(j) {
			done = append(done, pj)
		}
	}
	return done
}
