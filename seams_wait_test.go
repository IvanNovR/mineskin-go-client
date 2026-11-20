package mineskin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
	"github.com/IvanNovR/mineskin-go-client/internal/poll"
)

func TestWaitForJob_RejectsInvalidInputWithoutRequest(t *testing.T) {
	t.Parallel()
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

func TestWaitForJob_UsesETA(t *testing.T) {
	t.Parallel()
	const eta, initialDelay = 50 * time.Millisecond, 600 * time.Millisecond
	run := func(useETA bool) (time.Duration, error) {
		fake := clock.NewFake(pollerT0)
		var firstGet atomic.Int64
		srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			firstGet.CompareAndSwap(0, fake.Now().UnixNano())
			writeJSON(200, jobJSON("j1", "completed"))(w, r)
		})
		c := newTestClient(t, srv.URL, withClock(fake), WithJobPolling(PollOptions{
			InitialDelay: initialDelay, UseETA: useETA,
		}))
		start := fake.Now()
		res := goCall(func() error {
			_, err := c.WaitForJob(waitCtx(t), Job{ID: "j1", ETA: start.Add(eta)})
			return err
		})
		eventually(t, "poller timer armed", func() bool { return c.poller.Pending() == 1 && fake.Timers() == 1 })
		// The clock moves exactly to the ETA, then to the initial delay, so the
		// first GET time does not depend on how fast the machine is.
		fake.Advance(eta)
		if !useETA {
			if got := firstGet.Load(); got != 0 {
				t.Errorf("GET sent at %v without UseETA, before the initial delay", time.Unix(0, got).Sub(start))
			}
			fake.Advance(initialDelay - eta)
		}
		err := mustDone(t, "WaitForJob", res)
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

func TestWaitForJob_DedupSharesResult(t *testing.T) {
	t.Parallel()
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

func TestWaitForJob_CancelKeepsJobForOtherWaiter(t *testing.T) {
	t.Parallel()
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

func TestWaitForJob_PollerExitsWhenIdle(t *testing.T) {
	t.Parallel()
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

func TestWaitForJob_RestartsPollerAfterIdle(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, writeJSON(200, jobJSON("j1", "completed")))
	c := newTestClient(t, srv.URL, fastPolling())

	for i := range 3 {
		if _, err := c.WaitForJob(waitCtx(t), Job{ID: "j1"}); err != nil {
			t.Fatalf("wait %d: %v", i, err)
		}
		eventually(t, "poller exit", func() bool { return !c.poller.Running() })
	}
}

func TestWaitForJob_BatchedOverHTTP(t *testing.T) {
	t.Parallel()
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

func TestWaitForJob_Concurrent(t *testing.T) {
	t.Parallel()
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
func TestPoller_WakesForSoonerJob(t *testing.T) {
	t.Parallel()
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

func TestWaitForJob_FinishedResultWinsOverDoneContext(t *testing.T) {
	t.Parallel()
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

const (
	richCompleted = `{"success":true,` +
		`"job":{"id":"j1","status":"completed","result":"` + jobResultID + `"},` +
		`"skin":{"uuid":"` + jobResultID + `","name":"steve","variant":"slim","visibility":"public",` +
		`"texture":{"data":{"value":"val","signature":"sig"},"hash":{"skin":"hs","cape":"hc"},"url":{"skin":"us","cape":"uc"}}},` +
		`"warnings":[{"code":"w1","message":"warn one"},{"code":"w2","message":"warn two"}],` +
		`"messages":[{"code":"m1","message":"msg one"}],` +
		`"links":{"skin":"https://example.test/s"}}`
	richFailed = `{"success":true,` +
		`"job":{"id":"j1","status":"failed"},` +
		`"errors":[{"code":"e1","message":"err one"},{"code":"e2","message":"err two"}],` +
		`"warnings":[{"code":"w1","message":"warn one"}],` +
		`"messages":[{"code":"m1","message":"msg one"}],` +
		`"links":{"skin":"https://example.test/s"}}`
)

// waitTwice makes two waiters wait for the same job, answered with finalBody,
// and returns both outcomes.
func waitTwice(t *testing.T, finalBody string) (resp [2]*JobResponse, errs [2]error) {
	t.Helper()
	release := make(chan struct{})
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("MineSkin-Breadcrumb", "crumb")
		w.Header().Set("X-MineSkin-Extra", "extra")
		writeJSON(200, finalBody)(w, r)
	})
	c := newTestClient(t, srv.URL, fastPolling())
	var wg sync.WaitGroup
	for i := range resp {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp[i], errs[i] = c.WaitForJob(waitCtx(t), Job{ID: "j1"})
		}()
	}
	eventually(t, "both waiters registered", func() bool { return c.poller.Waiters("j1") == 2 })
	close(release)
	wg.Wait()
	return resp, errs
}

func TestPoller_HTTPBoundedByGetConcurrency(t *testing.T) {
	release := make(chan struct{})
	handler, peak := trackConcurrency(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v2/queue/")
		writeJSON(200, jobJSON(id, "completed"))(w, r)
	})
	srv := newTestServer(t, handler)
	c := newTestClient(t, srv.URL,
		WithGetThrottle(Throttle{Interval: time.Nanosecond, Concurrency: 2}),
		WithJobPolling(PollOptions{InitialDelay: time.Millisecond}))
	t.Cleanup(func() { _ = c.Close() })

	var wg sync.WaitGroup
	for _, id := range []string{"a", "b", "c", "d"} {
		wg.Go(func() {
			if _, err := c.WaitForJob(waitCtx(t), Job{ID: id}); err != nil {
				t.Errorf("WaitForJob(%s) error = %v", id, err)
			}
		})
	}
	// Requests are held, so two in flight is reached for certain. The poller
	// runs one tick at a time and waits for its checks, so a third request
	// before release can only come from a throttle that ignores its limit; the
	// throttle's own count must still read two while the handlers hold them.
	eventually(t, "two GETs in flight", func() bool { return peak() >= 2 })
	if got := c.getGroup.Throttle.Stats().InFlight; got > 2 {
		t.Errorf("throttle InFlight = %d while requests are held, want <= 2", got)
	}
	close(release)
	wg.Wait()
	if got := c.getGroup.Throttle.Stats().InFlight; got != 0 {
		t.Errorf("throttle InFlight = %d after all jobs finished, want 0", got)
	}
	if got := peak(); got != 2 {
		t.Errorf("peak in-flight GETs = %d, want exactly 2", got)
	}
}

func TestPoller_MaxAttemptsIgnoresThrottleWait(t *testing.T) {
	var mu sync.Mutex
	gets := map[string]int{}
	release := make(chan struct{})
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/v2/queue/")
		mu.Lock()
		gets[id]++
		mu.Unlock()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		writeJSON(200, jobJSON(id, "waiting"))(w, r)
	})
	c := newTestClient(t, srv.URL,
		WithGetThrottle(Throttle{Interval: time.Nanosecond, Concurrency: 1}),
		WithJobPolling(PollOptions{
			InitialDelay: time.Millisecond, MaxAttempts: 2,
			Interval: PollIntervalFunc(func(int) time.Duration { return time.Millisecond }),
		}))
	t.Cleanup(func() { _ = c.Close() })

	ids := []string{"a", "b", "c", "d"}
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Go(func() { _, errs[i] = c.WaitForJob(context.Background(), Job{ID: id}) })
	}
	// Hold the first request until others are queued behind it, so the
	// throttle wait is certain rather than a matter of timing.
	eventually(t, "checks queued in the get throttle", func() bool { return c.getGroup.Throttle.Stats().Waiting > 0 })
	close(release)
	wg.Wait()
	for i, id := range ids {
		if !errors.Is(errs[i], ErrMaxAttempts) {
			t.Errorf("%s error = %v, want ErrMaxAttempts", id, errs[i])
		}
		mu.Lock()
		if gets[id] != 2 {
			t.Errorf("%s GETs = %d, want exactly 2", id, gets[id])
		}
		mu.Unlock()
	}
}

func TestWaitForJob_ContextEnds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// newCtx derives the wait context; end, when non-nil, ends it after the
		// first poll was seen.
		newCtx  func(parent context.Context) (ctx context.Context, end func())
		wantErr error
	}{
		{
			name: "cancelled",
			newCtx: func(parent context.Context) (context.Context, func()) {
				ctx, cancel := context.WithCancel(parent)
				return ctx, cancel
			},
			wantErr: context.Canceled,
		},
		{
			name: "deadline exceeded",
			newCtx: func(parent context.Context) (context.Context, func()) {
				ctx, cancel := context.WithTimeout(parent, 30*time.Millisecond)
				t.Cleanup(cancel)
				return ctx, nil
			},
			wantErr: context.DeadlineExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var gets atomic.Int32
			srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				gets.Add(1)
				writeJSON(200, jobJSON("j1", "waiting"))(w, r)
			})
			fake := clock.NewFake(pollerT0)
			c := newTestClient(t, srv.URL, fastPolling(), withClock(fake))
			ctx, end := tt.newCtx(waitCtx(t))
			errCh := make(chan error, 1)
			go func() {
				_, err := c.WaitForJob(ctx, Job{ID: "j1"})
				errCh <- err
			}()
			if end != nil {
				eventually(t, "first poll", func() bool {
					fake.Advance(time.Millisecond)
					return gets.Load() >= 1
				})
				end()
			}

			err := <-errCh

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if c.poller.Pending() != 0 {
				t.Error("job still pending after the context ended")
			}
			eventually(t, "poller exit", func() bool { return !c.poller.Running() })
			// The poller is gone and holds no timer: stepping the clock far past
			// many poll intervals must not produce another request.
			after := gets.Load()
			if got := fake.Timers(); got != 0 {
				t.Errorf("pending fake timers after the context ended = %d, want 0", got)
			}
			for range 50 {
				fake.Advance(time.Second)
			}
			if gets.Load() != after {
				t.Error("requests continued after the context ended")
			}
		})
	}
}

func (c *doneAfterIdle) Err() error {
	if c.calls.Add(1) == 1 {
		return nil // the entry check passes
	}
	return context.Canceled
}

func (c *doneAfterIdle) Done() <-chan struct{} {
	for c.p.Pending() != 0 {
		runtime.Gosched()
	}
	return c.closed
}

// scribble overwrites everything reachable from m, in place.
func scribble(m *ResponseMeta) {
	for k := range m.Header {
		for i := range m.Header[k] {
			m.Header[k][i] = "mutated"
		}
		m.Header[k] = append(m.Header[k], "added")
	}
	m.Header.Set("Injected", "yes")
	for i := range m.Warnings {
		m.Warnings[i] = CodeAndMessage{Code: "mutated", Message: "mutated"}
	}
	for i := range m.Messages {
		m.Messages[i] = CodeAndMessage{Code: "mutated", Message: "mutated"}
	}
	for i := range m.Links {
		m.Links[i] = 'X'
	}
}

func TestWaitForJob_ResultsAreIndependentOnCompletion(t *testing.T) {
	t.Parallel()
	resp, errs := waitTwice(t, richCompleted)
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errors = %v, %v", errs[0], errs[1])
	}
	pristine := *resp[1]
	pristineSkin := *resp[1].Skin
	pristine.Skin = &pristineSkin
	pristine.Meta = cloneMetaForTest(resp[1].Meta)

	a := resp[0]
	scribble(&a.Meta)
	a.Skin.Name = "mutated"
	a.Skin.Variant = VariantClassic
	a.Skin.Texture.Data.Value = "mutated"
	a.Skin.Texture.URL.Skin = "mutated"
	a.Job.ID = "mutated"

	b := resp[1]
	if b.Skin == a.Skin {
		t.Fatal("waiters share one *Skin")
	}
	if !reflect.DeepEqual(*b, pristine) {
		t.Errorf("second waiter sees the first waiter's changes:\n got  %+v\n want %+v", *b, pristine)
	}
	if got := b.Meta.Header.Get("Mineskin-Breadcrumb"); got != "crumb" || b.Meta.Header.Get("Injected") != "" {
		t.Errorf("header = %v, want the original with no injected key", b.Meta.Header)
	}
	if string(b.Meta.Links) != `{"skin":"https://example.test/s"}` || b.Skin.Name != "steve" || b.Skin.Texture.Data.Value != "val" {
		t.Errorf("links = %s, skin = %+v; want the originals", b.Meta.Links, b.Skin)
	}
	if b.Meta.Warnings[1].Code != "w2" || b.Meta.Messages[0].Message != "msg one" {
		t.Errorf("warnings = %+v, messages = %+v; want the originals", b.Meta.Warnings, b.Meta.Messages)
	}
}

func TestWaitForJob_ResultsAreIndependentOnFailure(t *testing.T) {
	t.Parallel()
	_, errs := waitTwice(t, richFailed)
	var a, b *JobFailedError
	if !errors.As(errs[0], &a) || !errors.As(errs[1], &b) {
		t.Fatalf("errors = %v, %v; want *JobFailedError twice", errs[0], errs[1])
	}
	wantErrs := []CodeAndMessage{{"e1", "err one"}, {"e2", "err two"}}
	scribble(&a.Meta)
	a.Errors[0] = CodeAndMessage{Code: "mutated", Message: "mutated"}
	a.Errors[1].Message = "mutated"
	if !reflect.DeepEqual(b.Errors, wantErrs) {
		t.Errorf("Errors = %+v, want %+v", b.Errors, wantErrs)
	}
	if b.Meta.Warnings[0].Code != "w1" || b.Meta.Header.Get("Injected") != "" || string(b.Meta.Links) != `{"skin":"https://example.test/s"}` {
		t.Errorf("meta = %+v, want the originals", b.Meta)
	}
}

// cloneMetaForTest deep-copies m with plain loops, independently of the code under test.
func cloneMetaForTest(m ResponseMeta) ResponseMeta {
	out := m
	out.Header = http.Header{}
	for k, v := range m.Header {
		out.Header[k] = append([]string(nil), v...)
	}
	out.Warnings = append([]CodeAndMessage(nil), m.Warnings...)
	out.Messages = append([]CodeAndMessage(nil), m.Messages...)
	out.Links = append([]byte(nil), m.Links...)
	return out
}
