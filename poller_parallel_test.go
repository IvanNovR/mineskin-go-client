package mineskin

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

// barrierGet returns a getFn that blocks until parties calls are inside it at
// once, then completes each job. completed counts the calls that returned.
func barrierGet(t *testing.T, parties int32) (fn func(string) (*JobResponse, error), completed *atomic.Int32) {
	t.Helper()
	var arrived atomic.Int32
	var done atomic.Int32
	all := make(chan struct{})
	fn = func(id string) (*JobResponse, error) {
		if arrived.Add(1) == parties {
			close(all)
		}
		select {
		case <-all:
		case <-time.After(waitTimeout):
			t.Errorf("only %d of %d checks ran at once", arrived.Load(), parties)
		}
		done.Add(1)
		return jobResp(id, JobStatusCompleted, pollerResult), nil
	}
	return fn, &done
}

func TestPollerChecksRunInParallel(t *testing.T) {
	f := &fakeFetcher{}
	var completed *atomic.Int32
	f.getFn, completed = barrierGet(t, 4)
	p := newTestPoller(t, f, PollOptions{})
	pjs := addJobs(p, 4)

	p.tick(pollerT0.Add(time.Hour))

	if got := completed.Load(); got != 4 {
		t.Errorf("checks completed when tick returned = %d, want 4", got)
	}
	for _, pj := range pjs {
		if !isFinished(pj) {
			t.Errorf("job %s not finished", pj.id)
		}
	}
}

func TestPollerListForcedGetsRunInParallel(t *testing.T) {
	f := &fakeFetcher{}
	var completed *atomic.Int32
	f.getFn, completed = barrierGet(t, 3)
	f.listFn = func() (*JobListResponse, error) {
		return &JobListResponse{Jobs: []Job{
			{ID: "j0", Status: JobStatusCompleted, Result: pollerResult},
			{ID: "j1", Status: JobStatusCompleted, Result: pollerResult},
			{ID: "j2", Status: JobStatusCompleted, Result: pollerResult},
			{ID: "j3", Status: JobStatusWaiting},
		}}, nil
	}
	p := newTestPoller(t, f, PollOptions{})
	addJobs(p, 6)

	p.tick(pollerT0.Add(time.Hour))

	if got := completed.Load(); got != 3 {
		t.Errorf("forced GETs completed = %d, want 3", got)
	}
	if got, lists := f.counts(); len(got) != 3 || lists != 1 {
		t.Errorf("GETs = %v, lists = %d, want 3 GETs and 1 list", got, lists)
	}
}

func TestPollerReschedulesFromCompletion(t *testing.T) {
	f := &fakeFetcher{}
	p := newTestPoller(t, f, PollOptions{Interval: func(int) time.Duration { return time.Second }})
	fake := p.clock.(*clock.Fake)
	f.getFn = func(id string) (*JobResponse, error) {
		fake.Advance(5 * time.Second) // the check takes 5 s
		return jobResp(id, JobStatusProcessing, UUID{}), nil
	}
	pj := p.add(Job{ID: "j0", Status: JobStatusWaiting})

	p.tick(pollerT0.Add(time.Second))

	p.mu.Lock()
	defer p.mu.Unlock()
	if want := pollerT0.Add(6 * time.Second); !pj.nextCheckAt.Equal(want) {
		t.Errorf("nextCheckAt = %v, want %v", pj.nextCheckAt, want)
	}
}

// hookCounter counts onJobFailure calls.
func hookCounter(p *jobPoller) *atomic.Int32 {
	var n atomic.Int32
	p.onJobFailure = func() { n.Add(1) }
	return &n
}

func TestPollerJobFailureHookOncePerJob(t *testing.T) {
	failed := func(id string) (*JobResponse, error) { return jobResp(id, JobStatusFailed, UUID{}), nil }
	tests := []struct {
		name string
		jobs int
		get  func(string) (*JobResponse, error)
		list func() (*JobListResponse, error)
		dup  bool
		want int32
	}{
		{name: "individual GET failed", jobs: 1, get: failed, want: 1},
		{name: "completed never calls", jobs: 1, want: 0,
			get: func(id string) (*JobResponse, error) { return jobResp(id, JobStatusCompleted, pollerResult), nil }},
		{name: "two waiters on one job", jobs: 1, get: failed, dup: true, want: 1},
		{name: "list failed then forced GET failed", jobs: 5, get: failed, want: 1,
			list: func() (*JobListResponse, error) {
				return &JobListResponse{Jobs: []Job{{ID: "j0", Status: JobStatusFailed}}}, nil
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeFetcher{getFn: tt.get, listFn: tt.list}
			p := newTestPoller(t, f, PollOptions{})
			hook := hookCounter(p)
			addJobs(p, tt.jobs)
			if tt.dup {
				p.add(Job{ID: "j0"})
			}
			p.tick(pollerT0.Add(time.Hour))
			if got := hook.Load(); got != tt.want {
				t.Errorf("hook calls = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPollerJobFailureHookRunsWithoutLock(t *testing.T) {
	f := &fakeFetcher{getFn: func(id string) (*JobResponse, error) { return jobResp(id, JobStatusFailed, UUID{}), nil }}
	p := newTestPoller(t, f, PollOptions{})
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
