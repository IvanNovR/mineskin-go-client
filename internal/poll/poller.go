package poll

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

// batchThreshold is the number of pending jobs above which a tick checks jobs
// with one list request instead of one request per due job (Java parity).
const batchThreshold = 4

// Waiter is a job being polled. All fields except id and done are guarded by
// Poller.mu. resp, state and err are final once done is closed; waiters may
// read them without the lock after receiving from done. Every Register of the
// same pending id returns the same Waiter.
type Waiter[R any] struct {
	id          string
	state       State // latest known state
	attempts    int
	waiters     int
	nextCheckAt time.Time
	wait        time.Duration // the interval chosen for the current check
	failureSeen bool          // OnJobFailure already called for this job
	done        chan struct{}
	resp        R
	err         error
}

// Done is closed when the job is finished.
func (w *Waiter[R]) Done() <-chan struct{} { return w.done }

// Result returns the outcome of a finished job: the response and its state,
// or an error (the zero R and State). It must be called only after Done is
// closed.
func (w *Waiter[R]) Result() (R, State, error) { return w.resp, w.state, w.err }

// Config configures a Poller.
type Config[R any] struct {
	// Ctx is the client base context; every check request runs on it.
	Ctx context.Context
	// Clock drives the poller's time and its wait timer.
	Clock clock.Clock
	// Fetcher supplies the responses.
	Fetcher Fetcher[R]
	// Options is the schedule; it must be complete.
	Options Options
	// Logger receives debug logs; nil discards them.
	Logger *slog.Logger
	// OnJobFailure is called the first time a job is seen failed; nil does
	// nothing.
	OnJobFailure func()
	// GetLogAttrs and ListLogAttrs are opaque slog key/value pairs appended
	// to the debug logs of a single-job check and of a list check. The
	// poller does not interpret them.
	GetLogAttrs, ListLogAttrs []any
}

// Poller polls pending jobs, sharing one list request between jobs when more
// than batchThreshold are pending.
//
// Locking invariant: mu guards the pending map and every Waiter field except
// id and done. mu is never held during I/O (Get/List) or while a user
// callback (Options.Interval) runs.
type Poller[R any] struct {
	fetch  Fetcher[R]
	opts   Options
	logger *slog.Logger
	clock  clock.Clock
	// ctx is the client base context; every check request runs on it.
	ctx context.Context
	// onJobFailure is called when a job is seen failed; never nil.
	onJobFailure func()
	// getLogAttrs and listLogAttrs are extra debug log attributes.
	getLogAttrs, listLogAttrs []any

	// wakeCh has capacity 1; a send means "recompute the schedule".
	wakeCh chan struct{}

	mu      sync.Mutex
	pending map[string]*Waiter[R]
	running bool // whether the run goroutine is alive; guarded by mu
	closed  bool // set by Close; guarded by mu
	// closeErr is the error pending and later waiters finish with once closed.
	closeErr error
	// wg tracks the run goroutine; Add happens under mu while !closed.
	wg sync.WaitGroup
}

// New returns a Poller for cfg.
func New[R any](cfg Config[R]) *Poller[R] {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	onJobFailure := cfg.OnJobFailure
	if onJobFailure == nil {
		onJobFailure = func() {}
	}
	return &Poller[R]{
		ctx:          cfg.Ctx,
		onJobFailure: onJobFailure,
		getLogAttrs:  cfg.GetLogAttrs,
		listLogAttrs: cfg.ListLogAttrs,
		fetch:        cfg.Fetcher,
		opts:         cfg.Options,
		logger:       logger,
		clock:        cfg.Clock,
		pending:      make(map[string]*Waiter[R]),
		wakeCh:       make(chan struct{}, 1),
	}
}

// now returns the poller clock's current time.
func (p *Poller[R]) now() time.Time { return p.clock.Now() }

// Pending returns the number of jobs being polled. It exists for
// observability and tests.
func (p *Poller[R]) Pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pending)
}

// Waiters returns the number of waiters on the pending job id, or 0 when it
// is not pending. It exists for observability and tests.
func (p *Poller[R]) Waiters(id string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if w, ok := p.pending[id]; ok {
		return w.waiters
	}
	return 0
}

// add registers a waiter for job id. A job with an id that is already pending
// shares the existing entry. The first check is scheduled by
// Options.firstDelay.
func (p *Poller[R]) add(id string, eta time.Time) *Waiter[R] {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		w := &Waiter[R]{id: id, state: State{ID: id, ETA: eta}, done: make(chan struct{}), err: p.closeErr}
		close(w.done)
		return w
	}
	if w, ok := p.pending[id]; ok {
		w.waiters++
		return w
	}
	now := p.now()
	w := &Waiter[R]{
		id:          id,
		state:       State{ID: id, ETA: eta},
		waiters:     1,
		nextCheckAt: now.Add(p.opts.firstDelay(eta, now)),
		done:        make(chan struct{}),
	}
	p.pending[id] = w
	return w
}

// Leave removes a waiter. The entry is dropped when the last waiter leaves
// while the job is still pending.
func (p *Poller[R]) Leave(w *Waiter[R]) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w.waiters--
	if w.waiters <= 0 && p.pending[w.id] == w {
		delete(p.pending, w.id)
		p.wake() // let an idle run loop notice and exit
	}
}

// finish completes w with r and st, or with err, at most once: it does
// nothing unless w is still the pending entry for its id.
func (p *Poller[R]) finish(w *Waiter[R], r R, st State, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.finishLocked(w, r, st, err)
}

// finishLocked is finish for callers holding p.mu.
func (p *Poller[R]) finishLocked(w *Waiter[R], r R, st State, err error) {
	if p.pending[w.id] != w {
		return
	}
	delete(p.pending, w.id)
	if err != nil {
		var zero R
		r, st = zero, State{}
	}
	w.resp, w.state, w.err = r, st, err
	close(w.done)
}
