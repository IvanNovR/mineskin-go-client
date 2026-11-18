package mineskin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

var pollerT0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

var pollerResult = UUID{1, 2, 3}

// fakeFetcher is a jobFetcher that counts calls and delegates to functions.
type fakeFetcher struct {
	mu        sync.Mutex
	getIDs    []string
	listCalls int
	getFn     func(id string) (*JobResponse, error)
	listFn    func() (*JobListResponse, error)
}

func (f *fakeFetcher) GetJob(_ context.Context, id string) (*JobResponse, error) {
	f.mu.Lock()
	f.getIDs = append(f.getIDs, id)
	fn := f.getFn
	f.mu.Unlock()
	if fn == nil {
		return nil, errors.New("unexpected GetJob")
	}
	return fn(id)
}

func (f *fakeFetcher) ListJobs(context.Context) (*JobListResponse, error) {
	f.mu.Lock()
	f.listCalls++
	fn := f.listFn
	f.mu.Unlock()
	if fn == nil {
		return nil, errors.New("unexpected ListJobs")
	}
	return fn()
}

func (f *fakeFetcher) counts() (gets []string, lists int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.getIDs...), f.listCalls
}

// newTestPoller builds a poller with a 1 s initial delay, a constant 1 s
// interval unless opts sets one, and a fixed clock.
func newTestPoller(t *testing.T, f jobFetcher, opts PollOptions) *jobPoller {
	t.Helper()
	if opts.InitialDelay == 0 {
		opts.InitialDelay = time.Second
	}
	opts, err := opts.withDefaults()
	if err != nil {
		t.Fatalf("withDefaults() error = %v", err)
	}
	p := newJobPoller(f, opts, nil)
	p.now = func() time.Time { return pollerT0 }
	return p
}

// addJobs adds jobs "j0".."j{n-1"} and returns their pending entries.
func addJobs(p *jobPoller, n int) []*pendingJob {
	out := make([]*pendingJob, n)
	for i := range out {
		out[i] = p.add(Job{ID: fmt.Sprintf("j%d", i), Status: JobStatusWaiting})
	}
	return out
}

func jobResp(id string, status JobStatus, result UUID) *JobResponse {
	return &JobResponse{Job: Job{ID: id, Status: status, Result: result}}
}

func isFinished(pj *pendingJob) bool {
	select {
	case <-pj.done:
		return true
	default:
		return false
	}
}

func pendingCount(p *jobPoller) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pending)
}

func TestPollerBatchUsesListAboveThreshold(t *testing.T) {
	f := &fakeFetcher{}
	f.listFn = func() (*JobListResponse, error) {
		return &JobListResponse{Jobs: []Job{
			{ID: "j0", Status: JobStatusCompleted, Result: pollerResult},
			{ID: "j1", Status: JobStatusFailed},
			{ID: "j2", Status: JobStatusWaiting},
			{ID: "j3", Status: JobStatusProcessing},
			{ID: "j4", Status: JobStatusProcessing},
		}}, nil
	}
	f.getFn = func(id string) (*JobResponse, error) {
		if id == "j1" {
			return jobResp(id, JobStatusFailed, UUID{}), nil
		}
		return jobResp(id, JobStatusCompleted, pollerResult), nil
	}
	p := newTestPoller(t, f, PollOptions{})
	pjs := addJobs(p, 5)

	p.tick(pollerT0.Add(time.Second))

	gets, lists := f.counts()
	if lists != 1 {
		t.Errorf("list calls = %d, want 1", lists)
	}
	if len(gets) != 2 || gets[0] == gets[1] {
		t.Errorf("GetJob calls = %v, want one each for j0 and j1", gets)
	}
	for i, pj := range pjs {
		if got, want := isFinished(pj), i < 2; got != want {
			t.Errorf("j%d finished = %v, want %v", i, got, want)
		}
	}
}

func TestPollerIndividualAtOrBelowThreshold(t *testing.T) {
	f := &fakeFetcher{getFn: func(id string) (*JobResponse, error) {
		return jobResp(id, JobStatusWaiting, UUID{}), nil
	}}
	p := newTestPoller(t, f, PollOptions{})
	addJobs(p, batchThreshold)

	p.tick(pollerT0.Add(time.Second))

	gets, lists := f.counts()
	if len(gets) != 4 || lists != 0 {
		t.Errorf("gets = %v, lists = %d; want 4 gets and no list", gets, lists)
	}
}

func TestPollerNoDoubleCountingDuringListCall(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	f := &fakeFetcher{}
	f.listFn = func() (*JobListResponse, error) {
		close(entered)
		<-release
		return &JobListResponse{}, nil
	}
	p := newTestPoller(t, f, PollOptions{MaxAttempts: 3})
	pjs := addJobs(p, 5)
	now := pollerT0.Add(time.Second)

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		p.tick(now)
	}()
	<-entered
	p.tick(now) // must find nothing due while the list call is in flight
	close(release)
	<-finished

	if _, lists := f.counts(); lists != 1 {
		t.Errorf("list calls = %d, want 1", lists)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pj := range pjs {
		if pj.attempts != 1 {
			t.Errorf("%s attempts = %d, want 1", pj.id, pj.attempts)
		}
	}
}

func TestPollerDoneCondition(t *testing.T) {
	tests := []struct {
		name     string
		status   JobStatus
		result   UUID
		wantDone bool
	}{
		{"failed", JobStatusFailed, UUID{}, true},
		{"completed with result", JobStatusCompleted, pollerResult, true},
		{"completed without result", JobStatusCompleted, UUID{}, false},
		{"unknown", JobStatusUnknown, UUID{}, false},
		{"weird", JobStatus("weird"), UUID{}, false},
		{"waiting", JobStatusWaiting, UUID{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeFetcher{getFn: func(id string) (*JobResponse, error) {
				return jobResp(id, tt.status, tt.result), nil
			}}
			p := newTestPoller(t, f, PollOptions{})
			pj := p.add(Job{ID: "a"})

			p.tick(pollerT0.Add(time.Second))

			if got := isFinished(pj); got != tt.wantDone {
				t.Errorf("finished = %v, want %v", got, tt.wantDone)
			}
			if !tt.wantDone && pendingCount(p) != 1 {
				t.Errorf("job left the pending set")
			}
		})
	}
}

func TestPollerListMissingJobsKeepPolling(t *testing.T) {
	f := &fakeFetcher{listFn: func() (*JobListResponse, error) {
		return &JobListResponse{Jobs: []Job{{ID: "j0", Status: JobStatusWaiting}}}, nil
	}}
	p := newTestPoller(t, f, PollOptions{})
	pjs := addJobs(p, 5)

	p.tick(pollerT0.Add(time.Second))

	if gets, _ := f.counts(); len(gets) != 0 {
		t.Errorf("GetJob calls = %v, want none", gets)
	}
	if pendingCount(p) != 5 {
		t.Errorf("pending = %d, want 5", pendingCount(p))
	}
	for _, pj := range pjs {
		if isFinished(pj) {
			t.Errorf("%s finished, want still polling", pj.id)
		}
	}
}

// delayJobs pushes nextCheckAt of the given entries far into the future.
func delayJobs(p *jobPoller, pjs ...*pendingJob) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pj := range pjs {
		pj.nextCheckAt = pollerT0.Add(time.Hour)
	}
}

func TestPollerListUpdatesAllPendingInfo(t *testing.T) {
	eta := pollerT0.Add(5 * time.Second)
	f := &fakeFetcher{listFn: func() (*JobListResponse, error) {
		jobs := make([]Job, 5)
		for i := range jobs {
			jobs[i] = Job{ID: fmt.Sprintf("j%d", i), Status: JobStatusProcessing, ETA: eta}
		}
		return &JobListResponse{Jobs: jobs}, nil
	}}
	p := newTestPoller(t, f, PollOptions{})
	pjs := addJobs(p, 5)
	delayJobs(p, pjs[3], pjs[4]) // not due, still updated

	p.tick(pollerT0.Add(time.Second))

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pj := range pjs {
		if pj.job.Status != JobStatusProcessing || !pj.job.ETA.Equal(eta) {
			t.Errorf("%s job = %+v, want processing with updated ETA", pj.id, pj.job)
		}
	}
}

func TestPollerListErrorFailsDueJobsOnly(t *testing.T) {
	listErr := errors.New("list boom")
	f := &fakeFetcher{listFn: func() (*JobListResponse, error) { return nil, listErr }}
	p := newTestPoller(t, f, PollOptions{})
	pjs := addJobs(p, 5)
	delayJobs(p, pjs[3], pjs[4])

	p.tick(pollerT0.Add(time.Second))

	for i, pj := range pjs {
		if i < 3 {
			if !isFinished(pj) || !errors.Is(pj.err, listErr) {
				t.Errorf("j%d finished = %v, err = %v; want failed with list error", i, isFinished(pj), pj.err)
			}
		} else if isFinished(pj) {
			t.Errorf("j%d finished, want still pending", i)
		}
	}
	if pendingCount(p) != 2 {
		t.Errorf("pending = %d, want 2", pendingCount(p))
	}
}

func TestPollerListDoneFetchErrorFailsThatJob(t *testing.T) {
	getErr := errors.New("get boom")
	f := &fakeFetcher{}
	f.listFn = func() (*JobListResponse, error) {
		return &JobListResponse{Jobs: []Job{
			{ID: "j0", Status: JobStatusCompleted, Result: pollerResult},
			{ID: "j1", Status: JobStatusCompleted, Result: pollerResult},
		}}, nil
	}
	f.getFn = func(id string) (*JobResponse, error) {
		if id == "j0" {
			return nil, getErr
		}
		return jobResp(id, JobStatusCompleted, pollerResult), nil
	}
	p := newTestPoller(t, f, PollOptions{})
	pjs := addJobs(p, 5)

	p.tick(pollerT0.Add(time.Second))

	if !isFinished(pjs[0]) || !errors.Is(pjs[0].err, getErr) {
		t.Errorf("j0 err = %v, want get error", pjs[0].err)
	}
	if !isFinished(pjs[1]) || pjs[1].err != nil || pjs[1].resp == nil {
		t.Errorf("j1 err = %v resp = %v, want completed", pjs[1].err, pjs[1].resp)
	}
	if pendingCount(p) != 3 {
		t.Errorf("pending = %d, want 3", pendingCount(p))
	}
}

func TestPollerIndividualErrorFailsOnlyThatJob(t *testing.T) {
	getErr := errors.New("get boom")
	f := &fakeFetcher{getFn: func(id string) (*JobResponse, error) {
		if id == "j1" {
			return nil, getErr
		}
		return jobResp(id, JobStatusWaiting, UUID{}), nil
	}}
	p := newTestPoller(t, f, PollOptions{})
	pjs := addJobs(p, 3)

	p.tick(pollerT0.Add(time.Second))

	if !isFinished(pjs[1]) || !errors.Is(pjs[1].err, getErr) {
		t.Errorf("j1 err = %v, want get error", pjs[1].err)
	}
	if isFinished(pjs[0]) || isFinished(pjs[2]) {
		t.Error("other jobs finished, want still pending")
	}
}

func TestPollerMaxAttempts(t *testing.T) {
	f := &fakeFetcher{getFn: func(id string) (*JobResponse, error) {
		return jobResp(id, JobStatusWaiting, UUID{}), nil
	}}
	p := newTestPoller(t, f, PollOptions{MaxAttempts: 2})
	pj := p.add(Job{ID: "a"})

	p.tick(pollerT0.Add(1 * time.Second))
	p.tick(pollerT0.Add(2 * time.Second))
	if gets, _ := f.counts(); len(gets) != 2 || isFinished(pj) {
		t.Fatalf("after 2 ticks gets = %d finished = %v, want 2 and false", len(gets), isFinished(pj))
	}
	p.tick(pollerT0.Add(3 * time.Second))

	if gets, _ := f.counts(); len(gets) != 2 {
		t.Errorf("GetJob calls = %d, want 2 (no request after max attempts)", len(gets))
	}
	if !isFinished(pj) || !errors.Is(pj.err, ErrMaxAttempts) || !errors.Is(pj.err, ErrClient) {
		t.Errorf("err = %v, want ErrMaxAttempts and ErrClient", pj.err)
	}
	if pendingCount(p) != 0 {
		t.Error("job still pending")
	}
}

func TestPollerIntervalPerAttempt(t *testing.T) {
	var attempts []int
	interval := func(attempt int) time.Duration {
		attempts = append(attempts, attempt)
		return time.Duration(attempt) * time.Second
	}
	f := &fakeFetcher{getFn: func(id string) (*JobResponse, error) {
		return jobResp(id, JobStatusWaiting, UUID{}), nil
	}}
	p := newTestPoller(t, f, PollOptions{Interval: interval})
	pj := p.add(Job{ID: "a"})

	now := pollerT0.Add(time.Second)
	for attempt := 1; attempt <= 3; attempt++ {
		p.tick(now)
		p.mu.Lock()
		want := now.Add(time.Duration(attempt) * time.Second)
		if !pj.nextCheckAt.Equal(want) {
			t.Errorf("attempt %d nextCheckAt = %v, want %v", attempt, pj.nextCheckAt, want)
		}
		now = pj.nextCheckAt
		p.mu.Unlock()
	}
	if fmt.Sprint(attempts) != "[1 2 3]" {
		t.Errorf("interval attempts = %v, want [1 2 3]", attempts)
	}
}

func TestPollerNegativeIntervalFailsJob(t *testing.T) {
	interval := func(attempt int) time.Duration {
		if attempt == 2 {
			return -time.Millisecond
		}
		return time.Second
	}
	f := &fakeFetcher{getFn: func(id string) (*JobResponse, error) {
		return jobResp(id, JobStatusWaiting, UUID{}), nil
	}}
	p := newTestPoller(t, f, PollOptions{Interval: interval})
	a := p.add(Job{ID: "a"})
	p.tick(pollerT0.Add(time.Second)) // a: attempt 1
	b := p.add(Job{ID: "b"})
	p.mu.Lock()
	b.nextCheckAt = pollerT0.Add(2 * time.Second)
	p.mu.Unlock()

	p.tick(pollerT0.Add(2 * time.Second)) // a: attempt 2 (negative), b: attempt 1

	if !isFinished(a) || !errors.Is(a.err, ErrInvalidInput) {
		t.Errorf("a err = %v, want ErrInvalidInput", a.err)
	}
	if isFinished(b) {
		t.Error("b finished, want still pending")
	}
	if gets, _ := f.counts(); len(gets) != 2 || gets[1] != "b" {
		t.Errorf("GetJob calls = %v, want [a b] (none for the failed a)", gets)
	}
}

func TestPollerDedupSharesPending(t *testing.T) {
	p := newTestPoller(t, &fakeFetcher{}, PollOptions{})

	first := p.add(Job{ID: "a"})
	second := p.add(Job{ID: "a"})

	if first != second {
		t.Fatal("add returned different entries for the same id")
	}
	if pendingCount(p) != 1 || first.waiters != 2 {
		t.Errorf("pending = %d waiters = %d, want 1 and 2", pendingCount(p), first.waiters)
	}
}

func TestPollerLeaveRemovesWhenLastWaiterGone(t *testing.T) {
	p := newTestPoller(t, &fakeFetcher{}, PollOptions{})
	pj := p.add(Job{ID: "a"})
	p.add(Job{ID: "a"})

	p.leave(pj)
	if pendingCount(p) != 1 {
		t.Fatal("job removed while a waiter remains")
	}
	p.leave(pj)
	if pendingCount(p) != 0 {
		t.Error("job kept after last waiter left")
	}
}

func TestPollerFirstDelayUsesETA(t *testing.T) {
	p := newTestPoller(t, &fakeFetcher{}, PollOptions{UseETA: true, InitialDelay: 10 * time.Second})
	pj := p.add(Job{ID: "a", ETA: pollerT0.Add(50 * time.Millisecond)})
	if want := pollerT0.Add(50 * time.Millisecond); !pj.nextCheckAt.Equal(want) {
		t.Errorf("nextCheckAt = %v, want %v", pj.nextCheckAt, want)
	}
}

func TestPollerFailedResultIsJobFailedError(t *testing.T) {
	f := &fakeFetcher{getFn: func(id string) (*JobResponse, error) {
		r := jobResp(id, JobStatusFailed, UUID{})
		r.Errors = []CodeAndMessage{{Code: "bad_image", Message: "Bad image"}}
		return r, nil
	}}
	p := newTestPoller(t, f, PollOptions{})
	pj := p.add(Job{ID: "a"})

	p.tick(pollerT0.Add(time.Second))

	var jfe *JobFailedError
	if !errors.As(pj.err, &jfe) || jfe.Code != "bad_image" || jfe.Job.ID != "a" {
		t.Fatalf("err = %v, want *JobFailedError bad_image for a", pj.err)
	}
	if !errors.Is(pj.err, ErrClient) {
		t.Error("error does not match ErrClient")
	}
}

func TestPollerFinishExactlyOnce(t *testing.T) {
	p := newTestPoller(t, &fakeFetcher{}, PollOptions{})
	pj := p.add(Job{ID: "a"})
	resp := jobResp("a", JobStatusCompleted, pollerResult)

	p.finish(pj, resp, nil)
	p.finish(pj, nil, errors.New("late")) // would panic on a double close

	if pj.resp != resp || pj.err != nil {
		t.Errorf("resp = %v err = %v, want the first outcome kept", pj.resp, pj.err)
	}
	if !isFinished(pj) || pendingCount(p) != 0 {
		t.Error("job not finished and removed")
	}

	// A stale entry must not finish a newer entry with the same id.
	stale := pj
	fresh := p.add(Job{ID: "a"})
	p.finish(stale, nil, errors.New("stale"))
	if isFinished(fresh) {
		t.Error("stale finish completed the new entry")
	}
}
