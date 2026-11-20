package mineskin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
	"github.com/IvanNovR/mineskin-go-client/internal/poll"
)

func jobJSON(id, status string) string {
	if status == "completed" {
		return `{"success":true,"job":{"id":"` + id + `","status":"completed","result":"` + jobResultID + `"}}`
	}
	return `{"success":true,"job":{"id":"` + id + `","status":"` + status + `"}}`
}

func TestWaitForJobCompletes(t *testing.T) {
	var gets atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		status := []string{"waiting", "processing", "completed"}[min(int(gets.Add(1))-1, 2)]
		writeJSON(200, jobJSON("j1", status))(w, r)
	})
	c := newTestClient(t, srv.URL, fastPolling())

	got, err := c.WaitForJob(waitCtx(t), Job{ID: "j1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Job.Status != JobStatusCompleted || got.Job.Result.String() != jobResultID {
		t.Errorf("job = %+v", got.Job)
	}
	if gets.Load() != 3 {
		t.Errorf("GET count = %d, want 3", gets.Load())
	}
}

func TestWaitForJobFailed(t *testing.T) {
	srv := newTestServer(t, writeJSON(200, `{"success":true,"errors":[{"code":"bad_image","message":"Bad image"}],"job":{"id":"j1","status":"failed"}}`))
	c := newTestClient(t, srv.URL, fastPolling())

	got, err := c.WaitForJob(waitCtx(t), Job{ID: "j1"})
	var jfe *JobFailedError
	if !errors.As(err, &jfe) || got != nil {
		t.Fatalf("got = %+v err = %v, want *JobFailedError", got, err)
	}
	if jfe.Job.ID != "j1" || len(jfe.Errors) != 1 || jfe.Errors[0].Code != "bad_image" {
		t.Errorf("err = %+v", jfe)
	}
}

func TestWaitForJobMaxAttempts(t *testing.T) {
	var gets atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gets.Add(1)
		writeJSON(200, jobJSON("j1", "waiting"))(w, r)
	})
	c := newTestClient(t, srv.URL, WithJobPolling(PollOptions{
		InitialDelay: time.Millisecond, MaxAttempts: 3,
		Interval: PollIntervalFunc(func(int) time.Duration { return time.Millisecond }),
	}))

	_, err := c.WaitForJob(waitCtx(t), Job{ID: "j1"})
	if !errors.Is(err, ErrMaxAttempts) {
		t.Fatalf("err = %v, want ErrMaxAttempts", err)
	}
	if gets.Load() != 3 {
		t.Errorf("GET count = %d, want 3", gets.Load())
	}
}

func TestWaitForJobRejectsInvalidInputWithoutRequest(t *testing.T) {
	var hits atomic.Int32
	srv := newTestServer(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	c := newTestClient(t, srv.URL, fastPolling())
	cases := map[string]Job{
		"empty id": {},
		"dot":      {ID: "."},
		"dot dot":  {ID: ".."},
	}
	for name, job := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := c.WaitForJob(context.Background(), job)
			if !errors.Is(err, ErrInvalidInput) || got != nil {
				t.Fatalf("got = %+v err = %v, want ErrInvalidInput", got, err)
			}
		})
	}
	if hits.Load() != 0 {
		t.Errorf("%d requests sent", hits.Load())
	}
	if c.poller.Running() {
		t.Error("poller started for invalid input")
	}
}

func TestWaitForJobEscapesID(t *testing.T) {
	var uri atomic.Value
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		uri.Store(r.RequestURI)
		writeJSON(200, jobJSON("a/b?c#d e", "completed"))(w, r)
	})
	c := newTestClient(t, srv.URL, fastPolling())

	if _, err := c.WaitForJob(waitCtx(t), Job{ID: "a/b?c#d e"}); err != nil {
		t.Fatal(err)
	}
	if want := "/v2/queue/a%2Fb%3Fc%23d%20e"; uri.Load() != want {
		t.Errorf("request URI = %v, want %s", uri.Load(), want)
	}
}

func TestWaitForJobUsesETA(t *testing.T) {
	const step = 10 * time.Millisecond
	run := func(useETA bool) (time.Duration, error) {
		fake := clock.NewFake(pollerT0)
		var firstGet atomic.Int64
		srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			firstGet.CompareAndSwap(0, fake.Now().UnixNano())
			writeJSON(200, jobJSON("j1", "completed"))(w, r)
		})
		c := newTestClient(t, srv.URL, withClock(fake), WithJobPolling(PollOptions{
			InitialDelay: 600 * time.Millisecond, UseETA: useETA,
		}))
		start := fake.Now()
		res := goCall(func() error {
			_, err := c.WaitForJob(waitCtx(t), Job{ID: "j1", ETA: start.Add(50 * time.Millisecond)})
			return err
		})
		eventually(t, "job registered", func() bool { return c.poller.Pending() == 1 })
		err := driveClock(t, fake, step, func() error { return <-res })
		return time.Unix(0, firstGet.Load()).Sub(start), err
	}

	withETA, err := run(true)
	if err != nil {
		t.Fatal(err)
	}
	withoutETA, err := run(false)
	if err != nil {
		t.Fatal(err)
	}
	if withETA < 50*time.Millisecond || withETA >= 100*time.Millisecond {
		t.Errorf("first GET with ETA after %v, want about the 50ms ETA", withETA)
	}
	if withoutETA < 600*time.Millisecond {
		t.Errorf("first GET without ETA after %v, want about InitialDelay (600ms)", withoutETA)
	}
}

func TestWaitForJobDedupSharesResult(t *testing.T) {
	var gets atomic.Int32
	release := make(chan struct{})
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if gets.Add(1) >= 3 {
			<-release
			writeJSON(200, jobJSON("j1", "completed"))(w, r)
			return
		}
		writeJSON(200, jobJSON("j1", "waiting"))(w, r)
	})
	c := newTestClient(t, srv.URL, fastPolling())

	var wg sync.WaitGroup
	results := make([]*JobResponse, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = c.WaitForJob(waitCtx(t), Job{ID: "j1"})
		}()
	}
	eventually(t, "both waiters registered", func() bool {
		return c.poller.Waiters("j1") == 2
	})
	close(release)
	wg.Wait()

	for i := range results {
		if errs[i] != nil {
			t.Fatalf("waiter %d: %v", i, errs[i])
		}
	}
	if results[0] == results[1] {
		t.Error("waiters share one *JobResponse, want separate copies")
	}
	if results[0].Job != results[1].Job {
		t.Errorf("results differ: %+v vs %+v", results[0].Job, results[1].Job)
	}
	if gets.Load() != 3 {
		t.Errorf("GET count = %d, want 3 (one stream of polls)", gets.Load())
	}
	results[0].Job.ID = "mutated"
	if results[1].Job.ID != "j1" {
		t.Error("mutating one result changed the other")
	}
}

func TestWaitForJobContextCancel(t *testing.T) {
	var gets atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gets.Add(1)
		writeJSON(200, jobJSON("j1", "waiting"))(w, r)
	})
	c := newTestClient(t, srv.URL, fastPolling())
	ctx, cancel := context.WithCancel(waitCtx(t))
	errCh := make(chan error, 1)
	go func() {
		_, err := c.WaitForJob(ctx, Job{ID: "j1"})
		errCh <- err
	}()
	eventually(t, "first poll", func() bool { return gets.Load() >= 1 })

	cancel()
	err := <-errCh

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if c.poller.Pending() != 0 {
		t.Error("job still pending after cancel")
	}
	eventually(t, "poller exit", func() bool { return !c.poller.Running() })
	after := gets.Load()
	time.Sleep(50 * time.Millisecond)
	if gets.Load() != after {
		t.Error("requests continued after cancel")
	}
}

func TestWaitForJobContextDeadline(t *testing.T) {
	srv := newTestServer(t, writeJSON(200, jobJSON("j1", "waiting")))
	c := newTestClient(t, srv.URL, fastPolling())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := c.WaitForJob(ctx, Job{ID: "j1"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestWaitForJobCancelKeepsJobForOtherWaiter(t *testing.T) {
	release := make(chan struct{})
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
			writeJSON(200, jobJSON("j1", "completed"))(w, r)
		default:
			writeJSON(200, jobJSON("j1", "waiting"))(w, r)
		}
	})
	c := newTestClient(t, srv.URL, fastPolling())
	cancelCtx, cancel := context.WithCancel(waitCtx(t))
	cancelled := make(chan error, 1)
	go func() {
		_, err := c.WaitForJob(cancelCtx, Job{ID: "j1"})
		cancelled <- err
	}()
	other := make(chan error, 1)
	var got atomic.Pointer[JobResponse]
	go func() {
		r, err := c.WaitForJob(waitCtx(t), Job{ID: "j1"})
		got.Store(r)
		other <- err
	}()
	eventually(t, "two waiters", func() bool {
		return c.poller.Waiters("j1") == 2
	})

	cancel()
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter err = %v", err)
	}
	if c.poller.Pending() != 1 {
		t.Fatal("job removed while another waiter remains")
	}
	close(release)
	if err := <-other; err != nil {
		t.Fatalf("other waiter err = %v", err)
	}
	if r := got.Load(); r == nil || r.Job.Status != JobStatusCompleted {
		t.Errorf("other waiter result = %+v", r)
	}
}

func TestWaitForJobPollerExitsWhenIdle(t *testing.T) {
	srv := newTestServer(t, writeJSON(200, jobJSON("j1", "completed")))
	c := newTestClient(t, srv.URL, fastPolling())
	if c.poller.Running() {
		t.Fatal("poller running before any wait")
	}

	if _, err := c.WaitForJob(waitCtx(t), Job{ID: "j1"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "poller exit", func() bool { return !c.poller.Running() })
}

func TestWaitForJobRestartsPollerAfterIdle(t *testing.T) {
	srv := newTestServer(t, writeJSON(200, jobJSON("j1", "completed")))
	c := newTestClient(t, srv.URL, fastPolling())

	for i := range 3 {
		if _, err := c.WaitForJob(waitCtx(t), Job{ID: "j1"}); err != nil {
			t.Fatalf("wait %d: %v", i, err)
		}
		eventually(t, "poller exit", func() bool { return !c.poller.Running() })
	}
}

func TestWaitForJobBatchedOverHTTP(t *testing.T) {
	const n = 6
	var lists atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/queue" {
			lists.Add(1)
			var sb strings.Builder
			for i := range n {
				if i > 0 {
					sb.WriteString(",")
				}
				fmt.Fprintf(&sb, `{"id":"j%d","status":"completed","result":"%s"}`, i, jobResultID)
			}
			writeJSON(200, `{"success":true,"jobs":[`+sb.String()+`]}`)(w, r)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v2/queue/")
		writeJSON(200, jobJSON(id, "completed"))(w, r)
	})
	fake := clock.NewFake(pollerT0)
	c := newTestClient(t, srv.URL, withClock(fake), WithJobPolling(PollOptions{
		InitialDelay: 300 * time.Millisecond,
		Interval:     PollIntervalFunc(func(int) time.Duration { return time.Millisecond }),
	}))

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = c.WaitForJob(waitCtx(t), Job{ID: fmt.Sprintf("j%d", i)})
		}()
	}
	eventually(t, "all jobs registered", func() bool { return c.poller.Pending() == n })
	allDone := make(chan error, 1)
	go func() { wg.Wait(); allDone <- nil }()
	_ = driveClock(t, fake, 50*time.Millisecond, func() error { return <-allDone })

	for i, err := range errs {
		if err != nil {
			t.Errorf("job j%d: %v", i, err)
		}
	}
	if lists.Load() < 1 {
		t.Error("no GET /v2/queue list request was sent")
	}
}

func TestWaitForJobConcurrent(t *testing.T) {
	const ids, waiters = 20, 2
	tr := &http.Transport{}
	var gets sync.Map // ids that were fetched
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/v2/queue/")
		if r.URL.Path == "/v2/queue" {
			var jobs []string
			for i := range ids {
				jobs = append(jobs, fmt.Sprintf(`{"id":"j%d","status":"completed","result":"%s"}`, i, jobResultID))
			}
			writeJSON(200, `{"success":true,"jobs":[`+strings.Join(jobs, ",")+`]}`)(w, r)
			return
		}
		gets.Store(id, true)
		writeJSON(200, jobJSON(id, "completed"))(w, r)
	}))
	c := newTestClient(t, srv.URL, fastPolling(), WithTransport(tr))

	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := range ids {
		for range waiters {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r, err := c.WaitForJob(waitCtx(t), Job{ID: fmt.Sprintf("j%d", i)})
				if err != nil || r.Job.Status != JobStatusCompleted {
					failures.Add(1)
				}
			}()
		}
	}
	wg.Wait()

	if failures.Load() != 0 {
		t.Errorf("%d waiters failed", failures.Load())
	}
	eventually(t, "poller exit", func() bool { return !c.poller.Running() })
	if got := c.poller.Pending(); got != 0 {
		t.Errorf("pending jobs after all waiters returned = %d, want 0", got)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	// srv.Close blocks until every handler has returned.
	srv.Close()
	tr.CloseIdleConnections()
}

// TestPollerWakesForSoonerJob checks that a job with an earlier check time
// wakes a poller that is sleeping until a much later one.
func TestPollerWakesForSoonerJob(t *testing.T) {
	var soonGets atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/soon") {
			soonGets.Add(1)
		}
		id := strings.TrimPrefix(r.URL.Path, "/v2/queue/")
		writeJSON(200, jobJSON(id, "completed"))(w, r)
	})
	fake := clock.NewFake(pollerT0)
	c := newTestClient(t, srv.URL, withClock(fake), WithJobPolling(PollOptions{
		InitialDelay: time.Hour, UseETA: true,
	}))
	ctx, cancel := context.WithCancel(waitCtx(t))
	defer cancel()
	slowDone := make(chan struct{})
	go func() {
		defer close(slowDone)
		_, _ = c.WaitForJob(ctx, Job{ID: "slow"})
	}()
	eventually(t, "poller sleeping on the slow job", func() bool {
		return c.poller.Running() && c.poller.Pending() == 1
	})

	soon := goCall(func() error {
		_, err := c.WaitForJob(waitCtx(t), Job{ID: "soon", ETA: fake.Now().Add(30 * time.Millisecond)})
		return err
	})
	eventually(t, "sooner job registered", func() bool { return c.poller.Pending() == 2 })
	err := driveClock(t, fake, 10*time.Millisecond, func() error { return <-soon })

	if err != nil {
		t.Fatal(err)
	}
	if soonGets.Load() != 1 {
		t.Errorf("GETs for the sooner job = %d, want 1", soonGets.Load())
	}
	cancel()
	<-slowDone
}

// doneAfterIdle is a context whose Done channel is closed, but only once the
// poller has no pending jobs, so a finished job and a done ctx are both ready
// when WaitForJob selects.
type doneAfterIdle struct {
	context.Context
	p      *poll.Poller[*JobResponse]
	closed chan struct{}
	calls  atomic.Int32
}

func (c *doneAfterIdle) Err() error {
	if c.calls.Add(1) == 1 {
		return nil // the entry check passes
	}
	return context.Canceled
}

func (c *doneAfterIdle) Done() <-chan struct{} {
	for c.p.Pending() != 0 {
		time.Sleep(time.Millisecond)
	}
	return c.closed
}

func TestWaitForJobFinishedResultWinsOverDoneContext(t *testing.T) {
	srv := newTestServer(t, writeJSON(200, jobJSON("j1", "completed")))
	c := newTestClient(t, srv.URL, fastPolling())
	for i := 0; i < 30; i++ {
		closed := make(chan struct{})
		close(closed)
		ctx := &doneAfterIdle{Context: waitCtx(t), p: c.poller, closed: closed}

		got, err := c.WaitForJob(ctx, Job{ID: "j1"})

		if err != nil || got.Job.Status != JobStatusCompleted {
			t.Fatalf("iteration %d: got %v, err %v; want the finished result", i, got, err)
		}
	}
}
