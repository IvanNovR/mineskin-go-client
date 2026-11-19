package mineskin

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// batchThreshold is the number of pending jobs above which a tick checks jobs
// with one list request instead of one request per due job (Java parity).
const batchThreshold = 4

// jobFetcher is the part of Client the poller needs. *Client satisfies it.
type jobFetcher interface {
	getJobGated(ctx context.Context, id string, proceed func() bool) (*JobResponse, error)
	ListJobs(ctx context.Context) (*JobListResponse, error)
}

// pendingJob is a job being polled. All fields except id and done are guarded
// by jobPoller.mu. resp and err are final once done is closed; waiters may
// read them without the lock after receiving from done.
type pendingJob struct {
	id          string
	job         Job // latest known state
	attempts    int
	waiters     int
	nextCheckAt time.Time
	wait        time.Duration // the interval chosen for the current check
	failureSeen bool          // onJobFailure already called for this job
	done        chan struct{}
	resp        *JobResponse
	err         error
}

// jobPoller polls pending jobs, sharing one list request between jobs when
// more than batchThreshold are pending.
//
// Locking invariant: mu guards the pending map and every pendingJob field
// except id and done. mu is never held during I/O (GetJob/ListJobs) or while
// a user callback (PollInterval) runs.
type jobPoller struct {
	fetch  jobFetcher
	opts   PollOptions
	logger *slog.Logger
	now    func() time.Time
	// ctx is the client base context; every check request runs on it.
	ctx context.Context
	// onJobFailure is called when a job is seen FAILED; never nil.
	onJobFailure func()

	// wakeCh has capacity 1; a send means "recompute the schedule".
	wakeCh chan struct{}

	mu      sync.Mutex
	pending map[string]*pendingJob
	running bool // whether the run goroutine is alive; guarded by mu
	closed  bool // set by close; guarded by mu
	// closeErr is the error pending and later waiters finish with once closed.
	closeErr error
	// wg tracks the run goroutine; Add happens under mu while !closed.
	wg sync.WaitGroup
}

// newJobPoller returns a poller using f and opts, which must already have
// defaults applied. Requests run on ctx, the client base context. A nil
// logger discards logs; a nil onJobFailure does nothing.
func newJobPoller(ctx context.Context, f jobFetcher, opts PollOptions, logger *slog.Logger, onJobFailure func()) *jobPoller {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if onJobFailure == nil {
		onJobFailure = func() {}
	}
	return &jobPoller{
		ctx:          ctx,
		onJobFailure: onJobFailure,
		fetch:        f,
		opts:         opts,
		logger:       logger,
		now:          time.Now,
		pending:      make(map[string]*pendingJob),
		wakeCh:       make(chan struct{}, 1),
	}
}

// add registers a waiter for job. A job with an id that is already pending
// shares the existing entry. The first check is scheduled by
// PollOptions.firstDelay.
func (p *jobPoller) add(job Job) *pendingJob {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		pj := &pendingJob{id: job.ID, job: job, done: make(chan struct{}), err: p.closeErr}
		close(pj.done)
		return pj
	}
	if pj, ok := p.pending[job.ID]; ok {
		pj.waiters++
		return pj
	}
	now := p.now()
	pj := &pendingJob{
		id:          job.ID,
		job:         job,
		waiters:     1,
		nextCheckAt: now.Add(p.opts.firstDelay(job.ETA, now)),
		done:        make(chan struct{}),
	}
	p.pending[job.ID] = pj
	return pj
}

// leave removes a waiter. The entry is dropped when the last waiter leaves
// while the job is still pending.
func (p *jobPoller) leave(pj *pendingJob) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pj.waiters--
	if pj.waiters <= 0 && p.pending[pj.id] == pj {
		delete(p.pending, pj.id)
		p.wake() // let an idle run loop notice and exit
	}
}

// finish completes pj with r or err, at most once: it does nothing unless pj
// is still the pending entry for its id. A FAILED response becomes a
// *JobFailedError.
func (p *jobPoller) finish(pj *pendingJob, r *JobResponse, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.finishLocked(pj, r, err)
}

// finishLocked is finish for callers holding p.mu.
func (p *jobPoller) finishLocked(pj *pendingJob, r *JobResponse, err error) {
	if p.pending[pj.id] != pj {
		return
	}
	delete(p.pending, pj.id)
	if err == nil && r != nil && r.Job.Status == JobStatusFailed {
		err = newJobFailedError(r)
	}
	if err != nil {
		r = nil
	}
	pj.resp, pj.err = r, err
	close(pj.done)
}
