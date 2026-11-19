package poll

import "time"

// Register adds a waiter for job id, whose ETA is eta (zero when unknown),
// starts the run goroutine if it is not running, and wakes it so a sooner
// check time is picked up.
func (p *Poller[R]) Register(id string, eta time.Time) *Waiter[R] {
	w := p.add(id, eta)
	p.mu.Lock()
	if !p.running && !p.closed {
		p.running = true
		p.wg.Add(1)
		go p.run()
	}
	p.mu.Unlock()
	p.wake()
	return w
}

// wake asks the run loop to recompute its schedule. It never blocks; a wake
// already queued is enough.
func (p *Poller[R]) wake() {
	select {
	case p.wakeCh <- struct{}{}:
	default:
	}
}

// Running reports whether the run goroutine is alive.
func (p *Poller[R]) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

// soonest returns the earliest nextCheckAt of the pending jobs. If none are
// pending it marks the poller as not running and returns false. Checking for
// work and clearing running happen under one lock hold, so Register either
// sees running == false and starts a new goroutine, or its job is seen here.
func (p *Poller[R]) soonest() (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var next time.Time
	found := false
	for _, w := range p.pending {
		if !found || w.nextCheckAt.Before(next) {
			next, found = w.nextCheckAt, true
		}
	}
	if !found || p.closed {
		p.running = false
		return next, false
	}
	return next, found
}

// run is the poller loop. It sleeps until the soonest check time or a wake,
// ticks, and exits when no job is pending. It is started only by Register.
func (p *Poller[R]) run() {
	defer p.wg.Done()
	// fire has capacity 1; the timer callback never blocks. A stale value from
	// a timer that fired just as a wake arrived only causes a harmless tick
	// that finds nothing due.
	fire := make(chan struct{}, 1)
	for {
		next, ok := p.soonest()
		if !ok {
			return
		}
		timer := p.clock.AfterFunc(max(next.Sub(p.now()), 0), func() {
			select {
			case fire <- struct{}{}:
			default:
			}
		})
		select {
		case <-fire:
			p.tick(p.now())
		case <-p.wakeCh:
			timer.Stop()
		}
	}
}

// Close fails every pending job with err, makes later registrations fail with
// it too, wakes the run loop and waits for it to exit. Requests in flight end
// by themselves because the client cancels the base context first. It is safe
// to call more than once.
func (p *Poller[R]) Close(err error) {
	p.mu.Lock()
	if !p.closed {
		p.closed, p.closeErr = true, err
		var zero R
		for _, w := range p.pending {
			p.finishLocked(w, zero, State{}, err)
		}
	}
	p.mu.Unlock()
	p.wake()
	p.wg.Wait()
}
