package poll

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

func TestPoller_SkippedCheckKeepsJobPending(t *testing.T) {
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

func TestPoller_StartGateDeclinesJobThatLeft(t *testing.T) {
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

// barrierGet returns a getFn that blocks until parties calls are inside it at
// once, then completes each job. completed counts the calls that returned.
func barrierGet(t *testing.T, parties int32) (fn func(string) (*fakeResp, error), nDone *atomic.Int32) {
	t.Helper()
	var arrived atomic.Int32
	var done atomic.Int32
	all := make(chan struct{})
	fn = func(id string) (*fakeResp, error) {
		if arrived.Add(1) == parties {
			close(all)
		}
		select {
		case <-all:
		case <-time.After(waitTimeout(t)):
			t.Errorf("only %d of %d checks ran at once", arrived.Load(), parties)
		}
		done.Add(1)
		return completed(id), nil
	}
	return fn, &done
}

func TestPoller_ChecksRunInParallel(t *testing.T) {
	f := &fakeFetcher{}
	var nDone *atomic.Int32
	f.getFn, nDone = barrierGet(t, 4)
	p := newTestPoller(t, f, Options{})
	ws := addJobs(p, 4)

	p.tick(pollerT0.Add(time.Hour))

	if got := nDone.Load(); got != 4 {
		t.Errorf("checks completed when tick returned = %d, want 4", got)
	}
	for _, w := range ws {
		if !isFinished(w) {
			t.Errorf("job %s not finished", w.id)
		}
	}
}

func TestPoller_ListForcedGetsRunInParallel(t *testing.T) {
	f := &fakeFetcher{}
	var nDone *atomic.Int32
	f.getFn, nDone = barrierGet(t, 3)
	f.listFn = func() ([]State, error) {
		return []State{{ID: "j0", Done: true}, {ID: "j1", Done: true}, {ID: "j2", Done: true}, {ID: "j3"}}, nil
	}
	p := newTestPoller(t, f, Options{})
	addJobs(p, 6)

	p.tick(pollerT0.Add(time.Hour))

	if got := nDone.Load(); got != 3 {
		t.Errorf("forced GETs completed = %d, want 3", got)
	}
	if got, lists := f.counts(); len(got) != 3 || lists != 1 {
		t.Errorf("GETs = %v, lists = %d, want 3 GETs and 1 list", got, lists)
	}
}

func TestPoller_ReschedulesFromCompletion(t *testing.T) {
	f := &fakeFetcher{}
	p := newTestPoller(t, f, Options{Interval: func(int) time.Duration { return time.Second }})
	fake := p.clock.(*clock.Fake)
	f.getFn = func(id string) (*fakeResp, error) {
		fake.Advance(5 * time.Second) // the check takes 5 s
		return waiting(id), nil
	}
	w := p.add("j0", time.Time{})

	p.tick(pollerT0.Add(time.Second))

	p.mu.Lock()
	defer p.mu.Unlock()
	if want := pollerT0.Add(6 * time.Second); !w.nextCheckAt.Equal(want) {
		t.Errorf("nextCheckAt = %v, want %v", w.nextCheckAt, want)
	}
}

// hookCounter counts onJobFailure calls.
func hookCounter(p *Poller[*fakeResp]) *atomic.Int32 {
	var n atomic.Int32
	p.onJobFailure = func() { n.Add(1) }
	return &n
}

func TestPoller_JobFailureHookOncePerJob(t *testing.T) {
	failed := func(id string) (*fakeResp, error) { return failedResp(id), nil }
	tests := []struct {
		name string
		jobs int
		get  func(string) (*fakeResp, error)
		list func() ([]State, error)
		dup  bool
		want int32
	}{
		{name: "individual GET failed", jobs: 1, get: failed, want: 1},
		{name: "completed never calls", jobs: 1, want: 0,
			get: func(id string) (*fakeResp, error) { return completed(id), nil }},
		{name: "two waiters on one job", jobs: 1, get: failed, dup: true, want: 1},
		{name: "list failed then forced GET failed", jobs: 5, get: failed, want: 1,
			list: func() ([]State, error) {
				return []State{{ID: "j0", Done: true, Failed: true}}, nil
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeFetcher{getFn: tt.get, listFn: tt.list}
			p := newTestPoller(t, f, Options{})
			hook := hookCounter(p)
			addJobs(p, tt.jobs)
			if tt.dup {
				p.add("j0", time.Time{})
			}
			p.tick(pollerT0.Add(time.Hour))
			if got := hook.Load(); got != tt.want {
				t.Errorf("hook calls = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPoller_JobFailureHookRunsWithoutLock(t *testing.T) {
	f := &fakeFetcher{getFn: func(id string) (*fakeResp, error) { return failedResp(id), nil }}
	p := newTestPoller(t, f, Options{})
	p.onJobFailure = func() {
		if !p.mu.TryLock() {
			t.Error("onJobFailure called with p.mu held")
			return
		}
		p.mu.Unlock()
	}
	addJobs(p, 1)
	p.tick(pollerT0.Add(time.Hour))
}
