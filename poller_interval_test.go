package mineskin

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func waitingFetcher() *fakeFetcher {
	return &fakeFetcher{getFn: func(id string) (*JobResponse, error) {
		return jobResp(id, JobStatusWaiting, UUID{}), nil
	}}
}

func TestPollerZeroIntervalFailsJob(t *testing.T) {
	p := newTestPoller(t, waitingFetcher(), PollOptions{Interval: func(int) time.Duration { return 0 }})
	a := p.add(Job{ID: "a"})

	p.tick(pollerT0.Add(time.Second))

	if !isFinished(a) || !errors.Is(a.err, ErrInvalidInput) {
		t.Errorf("a err = %v, want ErrInvalidInput", a.err)
	}
	if gets, _ := p.fetch.(*fakeFetcher).counts(); len(gets) != 0 {
		t.Errorf("GetJob calls = %v, want none", gets)
	}
}

func TestPollerPanickingIntervalFailsOnlyThatJob(t *testing.T) {
	p := newTestPoller(t, waitingFetcher(), PollOptions{Interval: func(attempt int) time.Duration {
		if attempt == 2 {
			panic("boom")
		}
		return time.Second
	}})
	a := p.add(Job{ID: "a"})
	p.tick(pollerT0.Add(time.Second)) // a: attempt 1
	b := p.add(Job{ID: "b"})
	p.mu.Lock()
	b.nextCheckAt = pollerT0.Add(2 * time.Second)
	p.mu.Unlock()

	p.tick(pollerT0.Add(2 * time.Second)) // a: attempt 2 panics, b: attempt 1

	if !isFinished(a) || !errors.Is(a.err, ErrClient) {
		t.Errorf("a err = %v, want ErrClient", a.err)
	}
	if isFinished(b) {
		t.Error("b finished, want still pending")
	}
}

func TestPollerIntervalCallbackRunsOutsideLock(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	p := newTestPoller(t, waitingFetcher(), PollOptions{Interval: func(int) time.Duration {
		close(entered)
		<-release
		return time.Second
	}})
	p.add(Job{ID: "a"})
	ticked := make(chan struct{})
	go func() {
		defer close(ticked)
		p.tick(pollerT0.Add(time.Second))
	}()
	<-entered

	added := make(chan struct{})
	go func() {
		defer close(added)
		p.add(Job{ID: "b"})
	}()
	select {
	case <-added:
	case <-time.After(2 * time.Second):
		t.Error("add blocked while the interval callback ran: lock held")
	}
	close(release)
	<-ticked
}

func TestPollerIntervalCancelledJobIsNotRescheduled(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	p := newTestPoller(t, waitingFetcher(), PollOptions{Interval: func(int) time.Duration {
		close(entered)
		<-release
		return time.Second
	}})
	a := p.add(Job{ID: "a"})
	ticked := make(chan struct{})
	go func() {
		defer close(ticked)
		p.tick(pollerT0.Add(time.Second))
	}()
	<-entered
	p.leave(a)
	close(release)
	<-ticked

	if gets, _ := p.fetch.(*fakeFetcher).counts(); len(gets) != 0 {
		t.Errorf("GetJob calls = %v, want none for a job that left", gets)
	}
}

func TestPollerSkipsJobsThatLeftDuringTick(t *testing.T) {
	var p *jobPoller
	var pjs []*pendingJob
	f := &fakeFetcher{slot: &sync.Mutex{}}
	f.getFn = func(id string) (*JobResponse, error) {
		for _, pj := range pjs {
			if pj.id != id {
				p.leave(pj) // the only waiter of the other due job cancels
			}
		}
		return jobResp(id, JobStatusWaiting, UUID{}), nil
	}
	p = newTestPoller(t, f, PollOptions{})
	pjs = addJobs(p, 2)

	p.tick(pollerT0.Add(time.Second))

	if gets, _ := f.counts(); len(gets) != 1 {
		t.Errorf("GetJob calls = %v, want exactly 1 (job that left must be skipped)", gets)
	}
}
