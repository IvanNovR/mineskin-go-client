package mineskin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// closeSpyTransport counts CloseIdleConnections calls.
type closeSpyTransport struct {
	closed atomic.Int32
}

func (s *closeSpyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("spy: no network")
}

func (s *closeSpyTransport) CloseIdleConnections() { s.closed.Add(1) }

func TestCloseIsIdempotent(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1")
	for i := range 2 {
		if err := c.Close(); err != nil {
			t.Fatalf("Close() call %d error = %v", i+1, err)
		}
	}
}

func TestCallsAfterCloseFailImmediately(t *testing.T) {
	srv := newHitServer(t)
	c := newTestClient(t, srv.URL)
	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	ctx := context.Background()
	src := FromURL("https://example.com/s.png")
	done := &JobResponse{Job: Job{ID: "job-1", Status: JobStatusCompleted, Result: UUID{1}}}
	calls := map[string]func() error{
		"GetSkin":       func() error { _, err := c.GetSkin(ctx, testSkinID); return err },
		"GetSkinByUUID": func() error { _, err := c.GetSkinByUUID(ctx, UUID{1}); return err },
		"GetUser":       func() error { _, err := c.GetUser(ctx); return err },
		"GetJob":        func() error { _, err := c.GetJob(ctx, "job-1"); return err },
		"ListJobs":      func() error { _, err := c.ListJobs(ctx); return err },
		"Queue":         func() error { _, err := c.Queue(ctx, src, nil); return err },
		"Generate":      func() error { _, err := c.Generate(ctx, src, nil); return err },
		"WaitForJob":    func() error { _, err := c.WaitForJob(ctx, Job{ID: "job-1"}); return err },
		"JobSkin":       func() error { _, err := c.JobSkin(ctx, done); return err },
	}
	for name, call := range calls {
		err := call()
		if !errors.Is(err, ErrClientClosed) {
			t.Errorf("%s error = %v, want ErrClientClosed", name, err)
		}
	}
	for _, key := range []string{keySkin, keyUser, keyJob, keyList, keyQueue, keyGenerate} {
		if got := srv.count(key); got != 0 {
			t.Errorf("%s hits = %d, want 0", key, got)
		}
	}
}

func TestCloseFailsQueuedWaiters(t *testing.T) {
	srv := newHitServer(t, keySkin)
	c := newTestClient(t, srv.URL, WithGetThrottle(oneSlot()))
	held := goCall(getCalls(c)[keySkin])
	waitCount(t, srv, keySkin, 1)
	queued := make([]<-chan error, 3)
	for i := range queued {
		queued[i] = goCall(getCalls(c)[keySkin])
	}
	waitWaiting(t, c.getGroup, 3)

	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	for i, ch := range queued {
		if err := mustDone(t, "queued", ch); !errors.Is(err, ErrClientClosed) {
			t.Errorf("queued[%d] error = %v, want ErrClientClosed", i, err)
		}
	}
	if err := mustDone(t, "held", held); !errors.Is(err, ErrClientClosed) {
		t.Errorf("held error = %v, want ErrClientClosed", err)
	}
}

func TestCloseCancelsInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	handlerDone := make(chan struct{})
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(handlerDone)
	})
	c := newTestClient(t, srv.URL)
	res := goCall(func() error { _, err := c.GetUser(context.Background()); return err })
	<-started

	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := mustDone(t, "GetUser", res); !errors.Is(err, ErrClientClosed) {
		t.Errorf("GetUser error = %v, want ErrClientClosed", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(waitTimeout):
		t.Fatal("handler did not see its context cancelled")
	}
}

func TestCloseFailsWaitForJob(t *testing.T) {
	srv := newHitServer(t)
	c := newTestClient(t, srv.URL, WithJobPolling(PollOptions{InitialDelay: time.Hour}))
	res := goCall(func() error {
		_, err := c.WaitForJob(context.Background(), Job{ID: "job-1"})
		return err
	})
	eventually(t, "poller running", c.poller.Running)

	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := mustDone(t, "WaitForJob", res); !errors.Is(err, ErrClientClosed) {
		t.Errorf("WaitForJob error = %v, want ErrClientClosed", err)
	}
	if c.poller.Running() {
		t.Error("poller still running after Close")
	}
}

func TestCloseWaitsForPollerExit(t *testing.T) {
	before := runtimeGoroutines()
	started := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}))
	c := newTestClient(t, srv.URL, fastPolling())
	res := goCall(func() error {
		_, err := c.WaitForJob(context.Background(), Job{ID: "job-1"})
		return err
	})
	<-started // a poll request is in flight

	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if c.poller.Running() {
		t.Fatal("poller goroutine still running after Close returned")
	}
	if err := mustDone(t, "WaitForJob", res); !errors.Is(err, ErrClientClosed) {
		t.Errorf("WaitForJob error = %v, want ErrClientClosed", err)
	}
	srv.Close()
	waitGoroutinesSettle(t, before)
}

func TestCloseDoesNotTouchCallerTransport(t *testing.T) {
	spy := &closeSpyTransport{}
	c := newTestClient(t, "http://127.0.0.1:1", WithTransport(spy))
	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := spy.closed.Load(); got != 0 {
		t.Errorf("CloseIdleConnections calls = %d, want 0", got)
	}
}

func TestCloseClosesOwnedTransport(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1")
	var calls atomic.Int32
	c.idleCloser = func() { calls.Add(1) }
	_ = c.Close()
	_ = c.Close()
	if got := calls.Load(); got != 1 {
		t.Errorf("idle closer calls = %d, want 1", got)
	}
}

func TestOwnedTransportIsNotTheDefault(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1")
	if c.doer.HTTP.Transport == http.DefaultTransport {
		t.Error("client uses http.DefaultTransport directly, want an owned clone")
	}
	if c.idleCloser == nil {
		t.Error("idleCloser is nil for an owned transport")
	}
}

func TestCallerCancelVsCloseDistinguished(t *testing.T) {
	srv := newHitServer(t, keyUser)
	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	res := goCall(func() error { _, err := c.GetUser(ctx); return err })
	waitCount(t, srv, keyUser, 1)
	cancel()
	err := mustDone(t, "cancelled GetUser", res)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrClientClosed) {
		t.Errorf("caller cancel error = %v, want context.Canceled and not ErrClientClosed", err)
	}

	res = goCall(func() error { _, err := c.GetUser(context.Background()); return err })
	waitCount(t, srv, keyUser, 2)
	_ = c.Close()
	err = mustDone(t, "closed GetUser", res)
	if !errors.Is(err, ErrClientClosed) {
		t.Errorf("Close error = %v, want ErrClientClosed", err)
	}
}
