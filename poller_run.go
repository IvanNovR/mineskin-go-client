package mineskin

import "time"

// register adds a waiter for job, starts the run goroutine if it is not
// running, and wakes it so a sooner check time is picked up.
func (p *jobPoller) register(job Job) *pendingJob {
	pj := p.add(job)
	p.mu.Lock()
	if !p.running && !p.closed {
		p.running = true
		p.wg.Add(1)
		go p.run()
	}
	p.mu.Unlock()
	p.wake()
	return pj
}

// wake asks the run loop to recompute its schedule. It never blocks; a wake
// already queued is enough.
func (p *jobPoller) wake() {
	select {
	case p.wakeCh <- struct{}{}:
	default:
	}
}

// isRunning reports whether the run goroutine is alive.
func (p *jobPoller) isRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

// soonest returns the earliest nextCheckAt of the pending jobs. If none are
// pending it marks the poller as not running and returns false. Checking for
// work and clearing running happen under one lock hold, so register either
// sees running == false and starts a new goroutine, or its job is seen here.
func (p *jobPoller) soonest() (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var next time.Time
	found := false
	for _, pj := range p.pending {
		if !found || pj.nextCheckAt.Before(next) {
			next, found = pj.nextCheckAt, true
		}
	}
	if !found || p.closed {
		p.running = false
		return next, false
	}
	return next, found
}

// run is the poller loop. It sleeps until the soonest check time or a wake,
// ticks, and exits when no job is pending. It is started only by register.
func (p *jobPoller) run() {
	defer p.wg.Done()
	// A single timer is reused. Since Go 1.23, Reset and Stop discard a stale
	// unreceived value, so the timer needs no draining before Reset.
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		next, ok := p.soonest()
		if !ok {
			return
		}
		timer.Reset(max(next.Sub(p.now()), 0))
		select {
		case <-timer.C:
			p.tick(p.now())
		case <-p.wakeCh:
		}
	}
}

// close fails every pending job with err, makes later registrations fail with
// it too, wakes the run loop and waits for it to exit. Requests in flight end
// by themselves because the client cancels the base context first. It is safe
// to call more than once.
func (p *jobPoller) close(err error) {
	p.mu.Lock()
	if !p.closed {
		p.closed, p.closeErr = true, err
		for _, pj := range p.pending {
			p.finishLocked(pj, nil, err)
		}
	}
	p.mu.Unlock()
	p.wake()
	p.wg.Wait()
}
