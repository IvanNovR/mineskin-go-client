package mineskin

import (
	"context"
	"sync"
	"time"
)

// grantsLoader fetches the current grants. The client wires it to GetUser, so
// the request goes through the get throttle (D8).
type grantsLoader func(ctx context.Context) (Grants, error)

// grantsLoad is the load state of a tuner; its fields are guarded by the
// tuner's mutex.
type grantsLoad struct {
	load    grantsLoader
	baseCtx context.Context
	bg      *sync.WaitGroup

	// loading is true while a load goroutine runs; done is closed when it ends.
	loading bool
	done    chan struct{}
	// settled is true once a load attempt has finished, successfully or not.
	settled bool
	// attemptAt is the start of the latest attempt (D21).
	attemptAt time.Time
	// closed stops new loads; set by shutdown before Close waits on bg.
	closed bool
}

// ensureLoaded makes sure one grants load attempt has finished. The first
// caller starts the load; concurrent callers share it. It returns early with
// ctx's error when ctx ends, and ErrClientClosed after shutdown. After the
// first attempt it returns nil at once, even if that attempt failed.
func (t *tuner) ensureLoaded(ctx context.Context) error {
	t.mu.Lock()
	if t.settled {
		t.mu.Unlock()
		return nil
	}
	if t.closed {
		t.mu.Unlock()
		return ErrClientClosed
	}
	var done <-chan struct{} = t.done
	if !t.loading {
		done = t.startLoadLocked()
	}
	t.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// maybeRefresh starts one background refresh when the last attempt is older
// than grantsRefreshAge. It never waits.
func (t *tuner) maybeRefresh() {
	now := t.clock.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.settled || t.loading || t.closed || now.Sub(t.attemptAt) < grantsRefreshAge {
		return
	}
	t.startLoadLocked()
}

// startLoadLocked starts the load goroutine on the client base context and
// returns the channel closed when it ends. The caller holds mu and has checked
// that no load runs and the tuner is open.
func (t *tuner) startLoadLocked() <-chan struct{} {
	done := make(chan struct{})
	t.loading, t.done, t.attemptAt = true, done, t.clock.Now()
	refresh := t.settled
	t.bg.Add(1)
	go func() {
		defer t.bg.Done()
		g, err := t.load(t.baseCtx)
		t.finishLoad(g, err, refresh)
		close(done)
	}()
	return done
}

// finishLoad records the result of a load. A failure keeps the last-known
// limits and logs a warning; a failure caused by Close is not logged.
func (t *tuner) finishLoad(g Grants, err error, refresh bool) {
	if err == nil {
		t.applyGrants(g)
	} else if t.baseCtx.Err() == nil {
		msg := "Loading MineSkin grants failed, using the baseline limits"
		if refresh {
			msg = "Refreshing MineSkin grants failed, keeping the last-known limits"
		}
		// The error may carry server-controlled text (APIError message and
		// code), so control characters are stripped before it is logged.
		t.logger.Warn(msg, "error", stripControl(err.Error()))
	}
	t.mu.Lock()
	t.loading, t.settled = false, true
	t.mu.Unlock()
}

// shutdown stops new loads. Close calls it before waiting on the load group,
// so no load can be added after the wait began.
func (t *tuner) shutdown() {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
}
