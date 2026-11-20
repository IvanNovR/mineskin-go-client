package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// grantsLoad is the load state of a Tuner; its fields are guarded by the
// tuner's mutex. The loader is wired by the client to GetUser, so the request
// goes through the get throttle (D8).
type grantsLoad struct {
	load    func(ctx context.Context) (Grants, error)
	baseCtx context.Context
	bg      *sync.WaitGroup

	// loading is true while a load goroutine runs; done is closed when it ends.
	loading bool
	done    chan struct{}
	// settled is true once a load attempt has finished, successfully or not.
	settled bool
	// attemptAt is the start of the latest attempt (D21).
	attemptAt time.Time
	// closed stops new loads; set by Shutdown before Close waits on bg.
	closed bool
}

// ensureLoaded makes sure one grants load attempt has finished. The first
// caller starts the load; concurrent callers share it. It returns early with
// ctx's error when ctx ends, and ErrClosed after shutdown. After the
// first attempt it returns nil at once, even if that attempt failed.
func (t *Tuner) ensureLoaded(ctx context.Context) error {
	t.mu.Lock()
	if t.grants.settled {
		t.mu.Unlock()
		return nil
	}
	if t.grants.closed {
		t.mu.Unlock()
		return ErrClosed
	}
	var done <-chan struct{} = t.grants.done
	if !t.grants.loading {
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
func (t *Tuner) maybeRefresh() {
	now := t.clock.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.grants.settled || t.grants.loading || t.grants.closed || now.Sub(t.grants.attemptAt) < grantsRefreshAge {
		return
	}
	t.startLoadLocked()
}

// startLoadLocked starts the load goroutine on the client base context and
// returns the channel closed when it ends. The caller holds mu and has checked
// that no load runs and the tuner is open.
func (t *Tuner) startLoadLocked() <-chan struct{} {
	done := make(chan struct{})
	t.grants.loading, t.grants.done, t.grants.attemptAt = true, done, t.clock.Now()
	refresh := t.grants.settled
	t.grants.bg.Add(1)
	go func() {
		defer t.grants.bg.Done()
		g, err := t.grants.load(t.grants.baseCtx)
		t.finishLoad(g, err, refresh)
		close(done)
	}()
	return done
}

// finishLoad records the result of a load. A failure keeps the last-known
// limits and logs a warning; a failure caused by Close is not logged.
func (t *Tuner) finishLoad(g Grants, err error, refresh bool) {
	if err == nil {
		t.applyGrants(g)
	} else if t.grants.baseCtx.Err() == nil {
		msg := "Loading MineSkin grants failed, using the baseline limits"
		if refresh {
			msg = "Refreshing MineSkin grants failed, keeping the last-known limits"
		}
		// The error may carry server-controlled text (APIError message and
		// code), so control characters are stripped before it is logged.
		t.logger.Warn(msg, "error", transport.StripControl(err.Error()))
	}
	t.mu.Lock()
	t.grants.loading, t.grants.settled = false, true
	t.mu.Unlock()
}

// Shutdown stops new loads. Close calls it before waiting on the load group,
// so no load can be added after the wait began.
func (t *Tuner) Shutdown() {
	t.mu.Lock()
	t.grants.closed = true
	t.mu.Unlock()
}

// Loaded reports whether one grants load attempt has finished. It exists for
// observability and tests.
func (t *Tuner) Loaded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.grants.settled
}
