package poll

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPollerSkippedCheckKeepsJobPending(t *testing.T) {
	f := &fakeFetcher{getFn: func(id string) (*fakeResp, error) { return nil, ErrSkipped }}
	p := newTestPoller(t, f, Options{})
	w := p.add("a", time.Time{})

	p.tick(pollerT0.Add(time.Second))

	if isFinished(w) {
		t.Errorf("job finished with %v, want a skipped check to be ignored", w.err)
	}
	if pendingCount(p) != 1 {
		t.Error("job left the pending set")
	}
}

func TestPollerStartGateDeclinesJobThatLeft(t *testing.T) {
	queued := make(chan struct{})
	f := &fakeFetcher{slot: &sync.Mutex{}, queued: func() { close(queued) }}
	f.getFn = func(id string) (*fakeResp, error) { return waiting(id), nil }
	f.slot.Lock() // the check queues behind this slot
	p := newTestPoller(t, f, Options{})
	w := p.add("a", time.Time{})
	ticked := make(chan struct{})
	go func() {
		defer close(ticked)
		p.tick(pollerT0.Add(time.Second))
	}()
	<-queued
	p.Leave(w) // the only waiter leaves while the check is queued
	f.slot.Unlock()
	<-ticked

	if gets, _ := f.counts(); len(gets) != 0 {
		t.Errorf("Get calls = %v, want none: the gate must see the job gone", gets)
	}
}

func TestErrSkippedIsAComparableSentinel(t *testing.T) {
	if !errors.Is(ErrSkipped, ErrSkipped) || ErrSkipped.Error() == "" {
		t.Errorf("ErrSkipped = %q", ErrSkipped)
	}
}
