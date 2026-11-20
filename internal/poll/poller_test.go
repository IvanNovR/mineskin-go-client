package poll

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

var pollerT0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// fakeResp is the response type the tests poll for.
type fakeResp struct{ state State }

func waiting(id string) *fakeResp   { return &fakeResp{State{ID: id}} }
func completed(id string) *fakeResp { return &fakeResp{State{ID: id, Done: true}} }
func failedResp(id string) *fakeResp {
	return &fakeResp{State{ID: id, Done: true, Failed: true}}
}

// fakeFetcher is a Fetcher[*fakeResp] that counts calls and delegates to
// functions.
type fakeFetcher struct {
	mu        sync.Mutex
	getIDs    []string
	getCtxs   []context.Context
	listCalls int
	getFn     func(id string) (*fakeResp, error)
	// slot, when set, emulates a get throttle of concurrency 1: Get holds it
	// from the start gate to the end of getFn.
	slot *sync.Mutex
	// queued, when set, is called just before Get waits for slot.
	queued func()
	listFn func() ([]State, error)
}

var _ Fetcher[*fakeResp] = (*fakeFetcher)(nil)

func (f *fakeFetcher) Get(ctx context.Context, id string, proceed func() bool) (*fakeResp, State, error) {
	if f.slot != nil {
		if f.queued != nil {
			f.queued()
		}
		f.slot.Lock()
		defer f.slot.Unlock()
		if proceed != nil && !proceed() {
			return nil, State{}, ErrSkipped
		}
	}
	f.mu.Lock()
	f.getIDs = append(f.getIDs, id)
	f.getCtxs = append(f.getCtxs, ctx)
	fn := f.getFn
	f.mu.Unlock()
	if fn == nil {
		return nil, State{}, errors.New("unexpected Get")
	}
	r, err := fn(id)
	if err != nil {
		return nil, State{}, err
	}
	return r, r.state, nil
}

func (f *fakeFetcher) List(context.Context) ([]State, error) {
	f.mu.Lock()
	f.listCalls++
	fn := f.listFn
	f.mu.Unlock()
	if fn == nil {
		return nil, errors.New("unexpected List")
	}
	return fn()
}

func (f *fakeFetcher) counts() (gets []string, lists int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.getIDs...), f.listCalls
}

// newTestPoller builds a poller with a 1 s initial delay, a constant 1 s
// interval and 10 attempts unless opts sets them, and a fake clock.
func newTestPoller(t *testing.T, f Fetcher[*fakeResp], opts Options) *Poller[*fakeResp] {
	t.Helper()
	if opts.InitialDelay == 0 {
		opts.InitialDelay = time.Second
	}
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = 10
	}
	if opts.Interval == nil {
		opts.Interval = func(int) time.Duration { return time.Second }
	}
	return New(Config[*fakeResp]{
		Ctx: context.Background(), Clock: clock.NewFake(pollerT0), Fetcher: f, Options: opts,
	})
}

// addJobs adds jobs "j0".."j{n-1"} and returns their waiters.
func addJobs(p *Poller[*fakeResp], n int) []*Waiter[*fakeResp] {
	out := make([]*Waiter[*fakeResp], n)
	for i := range out {
		out[i] = p.add(fmt.Sprintf("j%d", i), time.Time{})
	}
	return out
}

func isFinished(w *Waiter[*fakeResp]) bool {
	select {
	case <-w.Done():
		return true
	default:
		return false
	}
}

func pendingCount(p *Poller[*fakeResp]) int { return p.Pending() }

func TestPoller_BatchUsesListAboveThreshold(t *testing.T) {
	f := &fakeFetcher{}
	f.listFn = func() ([]State, error) {
		return []State{
			{ID: "j0", Done: true},
			{ID: "j1", Done: true, Failed: true},
			{ID: "j2"},
			{ID: "j3"},
			{ID: "j4"},
		}, nil
	}
	f.getFn = func(id string) (*fakeResp, error) {
		if id == "j1" {
			return failedResp(id), nil
		}
		return completed(id), nil
	}
	p := newTestPoller(t, f, Options{})
	ws := addJobs(p, 5)

	p.tick(pollerT0.Add(time.Second))

	gets, lists := f.counts()
	if lists != 1 {
		t.Errorf("list calls = %d, want 1", lists)
	}
	if len(gets) != 2 || gets[0] == gets[1] {
		t.Errorf("Get calls = %v, want one each for j0 and j1", gets)
	}
	for i, w := range ws {
		if got, want := isFinished(w), i < 2; got != want {
			t.Errorf("j%d finished = %v, want %v", i, got, want)
		}
	}
}

func TestPoller_ChecksIndividuallyAtOrBelowThreshold(t *testing.T) {
	f := &fakeFetcher{getFn: func(id string) (*fakeResp, error) { return waiting(id), nil }}
	p := newTestPoller(t, f, Options{})
	addJobs(p, batchThreshold)

	p.tick(pollerT0.Add(time.Second))

	gets, lists := f.counts()
	if len(gets) != 4 || lists != 0 {
		t.Errorf("gets = %v, lists = %d; want 4 gets and no list", gets, lists)
	}
}

func TestPoller_NoDoubleCountingDuringListCall(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	f := &fakeFetcher{}
	f.listFn = func() ([]State, error) {
		close(entered)
		<-release
		return nil, nil
	}
	p := newTestPoller(t, f, Options{MaxAttempts: 3})
	ws := addJobs(p, 5)
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
	for _, w := range ws {
		if w.attempts != 1 {
			t.Errorf("%s attempts = %d, want 1", w.id, w.attempts)
		}
	}
}

func TestPoller_DoneStateFinishesJob(t *testing.T) {
	tests := []struct {
		name     string
		state    State
		wantDone bool
	}{
		{"done", State{Done: true}, true},
		{"done and failed", State{Done: true, Failed: true}, true},
		{"not done", State{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeFetcher{getFn: func(id string) (*fakeResp, error) {
				return &fakeResp{State{ID: id, Done: tt.state.Done, Failed: tt.state.Failed}}, nil
			}}
			p := newTestPoller(t, f, Options{})
			w := p.add("a", time.Time{})

			p.tick(pollerT0.Add(time.Second))

			if got := isFinished(w); got != tt.wantDone {
				t.Errorf("finished = %v, want %v", got, tt.wantDone)
			}
			if !tt.wantDone && pendingCount(p) != 1 {
				t.Errorf("job left the pending set")
			}
		})
	}
}

func TestPoller_ListMissingJobsKeepPolling(t *testing.T) {
	f := &fakeFetcher{listFn: func() ([]State, error) {
		return []State{{ID: "j0"}}, nil
	}}
	p := newTestPoller(t, f, Options{})
	ws := addJobs(p, 5)

	p.tick(pollerT0.Add(time.Second))

	if gets, _ := f.counts(); len(gets) != 0 {
		t.Errorf("Get calls = %v, want none", gets)
	}
	if pendingCount(p) != 5 {
		t.Errorf("pending = %d, want 5", pendingCount(p))
	}
	for _, w := range ws {
		if isFinished(w) {
			t.Errorf("%s finished, want still polling", w.id)
		}
	}
}

// delayJobs pushes nextCheckAt of the given entries far into the future.
func delayJobs(p *Poller[*fakeResp], ws ...*Waiter[*fakeResp]) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, w := range ws {
		w.nextCheckAt = pollerT0.Add(time.Hour)
	}
}

func TestPoller_ListUpdatesAllPendingInfo(t *testing.T) {
	eta := pollerT0.Add(5 * time.Second)
	f := &fakeFetcher{listFn: func() ([]State, error) {
		states := make([]State, 5)
		for i := range states {
			states[i] = State{ID: fmt.Sprintf("j%d", i), ETA: eta}
		}
		return states, nil
	}}
	p := newTestPoller(t, f, Options{})
	ws := addJobs(p, 5)
	delayJobs(p, ws[3], ws[4]) // not due, still updated

	p.tick(pollerT0.Add(time.Second))

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, w := range ws {
		if !w.state.ETA.Equal(eta) {
			t.Errorf("%s state = %+v, want the updated ETA", w.id, w.state)
		}
	}
}

func TestPoller_MaxAttemptsExhaustsJob(t *testing.T) {
	f := &fakeFetcher{getFn: func(id string) (*fakeResp, error) { return waiting(id), nil }}
	p := newTestPoller(t, f, Options{MaxAttempts: 2})
	w := p.add("a", time.Time{})

	p.tick(pollerT0.Add(1 * time.Second))
	p.tick(pollerT0.Add(2 * time.Second))
	if gets, _ := f.counts(); len(gets) != 2 || isFinished(w) {
		t.Fatalf("after 2 ticks gets = %d finished = %v, want 2 and false", len(gets), isFinished(w))
	}
	p.tick(pollerT0.Add(3 * time.Second))

	if gets, _ := f.counts(); len(gets) != 2 {
		t.Errorf("Get calls = %d, want 2 (no request after max attempts)", len(gets))
	}
	var ex *ExhaustedError
	if !isFinished(w) || !errors.As(w.err, &ex) || ex.ID != "a" || ex.Attempts != 2 {
		t.Errorf("err = %v, want *ExhaustedError{a, 2}", w.err)
	}
	if pendingCount(p) != 0 {
		t.Error("job still pending")
	}
}

func TestPoller_IntervalPerAttempt(t *testing.T) {
	var attempts []int
	interval := func(attempt int) time.Duration {
		attempts = append(attempts, attempt)
		return time.Duration(attempt) * time.Second
	}
	f := &fakeFetcher{getFn: func(id string) (*fakeResp, error) { return waiting(id), nil }}
	p := newTestPoller(t, f, Options{Interval: interval})
	w := p.add("a", time.Time{})

	now := pollerT0.Add(time.Second)
	for attempt := 1; attempt <= 3; attempt++ {
		p.tick(now)
		p.mu.Lock()
		want := now.Add(time.Duration(attempt) * time.Second)
		if !w.nextCheckAt.Equal(want) {
			t.Errorf("attempt %d nextCheckAt = %v, want %v", attempt, w.nextCheckAt, want)
		}
		now = w.nextCheckAt
		p.mu.Unlock()
	}
	if fmt.Sprint(attempts) != "[1 2 3]" {
		t.Errorf("interval attempts = %v, want [1 2 3]", attempts)
	}
}

func TestPoller_DedupSharesPending(t *testing.T) {
	p := newTestPoller(t, &fakeFetcher{}, Options{})

	first := p.add("a", time.Time{})
	second := p.add("a", time.Time{})

	if first != second {
		t.Fatal("add returned different entries for the same id")
	}
	if pendingCount(p) != 1 || first.waiters != 2 || p.Waiters("a") != 2 {
		t.Errorf("pending = %d waiters = %d, want 1 and 2", pendingCount(p), first.waiters)
	}
}

func TestPoller_WaitersOfUnknownID(t *testing.T) {
	p := newTestPoller(t, &fakeFetcher{}, Options{})
	if got := p.Waiters("nope"); got != 0 {
		t.Errorf("Waiters(unknown) = %d, want 0", got)
	}
}

func TestPoller_LeaveRemovesWhenLastWaiterGone(t *testing.T) {
	p := newTestPoller(t, &fakeFetcher{}, Options{})
	w := p.add("a", time.Time{})
	p.add("a", time.Time{})

	p.Leave(w)
	if pendingCount(p) != 1 {
		t.Fatal("job removed while a waiter remains")
	}
	p.Leave(w)
	if pendingCount(p) != 0 {
		t.Error("job kept after last waiter left")
	}
}

func TestPoller_FirstDelayUsesETA(t *testing.T) {
	p := newTestPoller(t, &fakeFetcher{}, Options{UseETA: true, InitialDelay: 10 * time.Second})
	w := p.add("a", pollerT0.Add(50*time.Millisecond))
	if want := pollerT0.Add(50 * time.Millisecond); !w.nextCheckAt.Equal(want) {
		t.Errorf("nextCheckAt = %v, want %v", w.nextCheckAt, want)
	}
}

func TestPoller_FailedStateIsReportedInResult(t *testing.T) {
	f := &fakeFetcher{getFn: func(id string) (*fakeResp, error) { return failedResp(id), nil }}
	p := newTestPoller(t, f, Options{})
	w := p.add("a", time.Time{})

	p.tick(pollerT0.Add(time.Second))

	r, st, err := w.Result()
	if err != nil {
		t.Fatalf("err = %v, want nil: the poller leaves failed jobs to its caller", err)
	}
	if r == nil || r.state.ID != "a" || !st.Failed || !st.Done {
		t.Errorf("Result() = %v, %+v; want the failed response and its state", r, st)
	}
}

func TestPoller_FinishExactlyOnce(t *testing.T) {
	p := newTestPoller(t, &fakeFetcher{}, Options{})
	w := p.add("a", time.Time{})
	resp := completed("a")

	p.finish(w, resp, resp.state, nil)
	p.finish(w, nil, State{}, errors.New("late")) // would panic on a double close

	if w.resp != resp || w.err != nil {
		t.Errorf("resp = %v err = %v, want the first outcome kept", w.resp, w.err)
	}
	if !isFinished(w) || pendingCount(p) != 0 {
		t.Error("job not finished and removed")
	}

	// A stale entry must not finish a newer entry with the same id.
	fresh := p.add("a", time.Time{})
	p.finish(w, nil, State{}, errors.New("stale"))
	if isFinished(fresh) {
		t.Error("stale finish completed the new entry")
	}
}

// jobOutcome is what a test expects of one job after a tick.
type jobOutcome int

const (
	outcomePending jobOutcome = iota
	outcomeFailed
	outcomeCompleted
)

func TestPoller_FailureIsolation(t *testing.T) {
	t.Parallel()
	listErr := errors.New("list boom")
	getErr := errors.New("get boom")
	tests := []struct {
		name string
		jobs int
		// delayed jobs are not due on the tick.
		delayed     []int
		listFn      func() ([]State, error)
		getFn       func(id string) (*fakeResp, error)
		want        map[int]jobOutcome // jobs not listed must be pending
		wantErr     map[int]error
		wantPending int
	}{
		{
			name:    "list error fails due jobs only",
			jobs:    5,
			delayed: []int{3, 4},
			listFn:  func() ([]State, error) { return nil, listErr },
			want:    map[int]jobOutcome{0: outcomeFailed, 1: outcomeFailed, 2: outcomeFailed},
			wantErr: map[int]error{0: listErr, 1: listErr, 2: listErr}, wantPending: 2,
		},
		{
			name: "fetch error of a done job from a list fails that job",
			jobs: 5,
			listFn: func() ([]State, error) {
				return []State{{ID: "j0", Done: true}, {ID: "j1", Done: true}}, nil
			},
			getFn: func(id string) (*fakeResp, error) {
				if id == "j0" {
					return nil, getErr
				}
				return completed(id), nil
			},
			want:    map[int]jobOutcome{0: outcomeFailed, 1: outcomeCompleted},
			wantErr: map[int]error{0: getErr}, wantPending: 3,
		},
		{
			name: "individual get error fails only that job",
			jobs: 3,
			getFn: func(id string) (*fakeResp, error) {
				if id == "j1" {
					return nil, getErr
				}
				return waiting(id), nil
			},
			want:    map[int]jobOutcome{1: outcomeFailed},
			wantErr: map[int]error{1: getErr}, wantPending: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newTestPoller(t, &fakeFetcher{listFn: tt.listFn, getFn: tt.getFn}, Options{})
			ws := addJobs(p, tt.jobs)
			for _, i := range tt.delayed {
				delayJobs(p, ws[i])
			}

			p.tick(pollerT0.Add(time.Second))

			for i, w := range ws {
				switch tt.want[i] {
				case outcomeFailed:
					if !isFinished(w) || !errors.Is(w.err, tt.wantErr[i]) {
						t.Errorf("j%d finished = %v, err = %v; want failed with %v", i, isFinished(w), w.err, tt.wantErr[i])
					}
				case outcomeCompleted:
					if !isFinished(w) || w.err != nil || w.resp == nil {
						t.Errorf("j%d err = %v resp = %v, want completed", i, w.err, w.resp)
					}
				case outcomePending:
					if isFinished(w) {
						t.Errorf("j%d finished with %v, want still pending", i, w.err)
					}
				}
			}
			if got := pendingCount(p); got != tt.wantPending {
				t.Errorf("pending = %d, want %d", got, tt.wantPending)
			}
		})
	}
}
