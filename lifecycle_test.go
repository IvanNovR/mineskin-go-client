package mineskin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
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
	t.Parallel()
	c := newTestClient(t, "http://127.0.0.1:1")
	for i := range 2 {
		if err := c.Close(); err != nil {
			t.Fatalf("Close() call %d error = %v", i+1, err)
		}
	}
}

func TestCallsAfterCloseFailImmediately(t *testing.T) {
	t.Parallel()
	srv := newHitServer(t)
	c := newTestClient(t, srv.URL)
	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	ctx := context.Background()
	src := FromURL("https://example.com/s.png")
	calls := map[string]func() error{
		"GetSkin":    func() error { _, err := c.GetSkin(ctx, testSkinUUID); return err },
		"GetUser":    func() error { _, err := c.GetUser(ctx); return err },
		"GetJob":     func() error { _, err := c.GetJob(ctx, "job-1"); return err },
		"ListJobs":   func() error { _, err := c.ListJobs(ctx); return err },
		"Queue":      func() error { _, err := c.Queue(ctx, src, GenerateOptions{}); return err },
		"Generate":   func() error { _, err := c.Generate(ctx, src, GenerateOptions{}); return err },
		"WaitForJob": func() error { _, err := c.WaitForJob(ctx, Job{ID: "job-1"}); return err },
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
	t.Parallel()
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
	t.Parallel()
	started := make(chan struct{})
	handlerDone := make(chan struct{})
	srv := newTestServer(t, func(_ http.ResponseWriter, r *http.Request) {
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
	case <-time.After(waitTimeout(t)):
		t.Fatal("handler did not see its context cancelled")
	}
}

func TestCloseFailsWaitForJob(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	started := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}))
	fake := clock.NewFake(pollerT0)
	c := newTestClient(t, srv.URL, fastPolling(), withClock(fake))
	res := goCall(func() error {
		_, err := c.WaitForJob(context.Background(), Job{ID: "job-1"})
		return err
	})
	// The first poll waits on the fake clock; step it until the request is in flight.
	eventually(t, "a poll request in flight", func() bool {
		fake.Advance(time.Millisecond)
		select {
		case <-started:
			return true
		default:
			return false
		}
	})

	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if c.poller.Running() {
		t.Fatal("poller goroutine still running after Close returned")
	}
	if err := mustDone(t, "WaitForJob", res); !errors.Is(err, ErrClientClosed) {
		t.Errorf("WaitForJob error = %v, want ErrClientClosed", err)
	}
	if got := fake.Timers(); got != 0 {
		t.Errorf("pending fake timers after Close = %d, want 0", got)
	}
	// Close blocks until the cancelled in-flight request's handler has returned.
	srv.Close()
}

func TestCloseDoesNotTouchCallerTransport(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	c := newTestClient(t, "http://127.0.0.1:1")
	if c.doer.HTTP.Transport == http.DefaultTransport {
		t.Error("client uses http.DefaultTransport directly, want an owned clone")
	}
	if c.idleCloser == nil {
		t.Error("idleCloser is nil for an owned transport")
	}
}

func TestCallerCancelVsCloseDistinguished(t *testing.T) {
	t.Parallel()
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

// TestCheckOpenFollowsBaseContext pins that the closed state is derived from
// the base context: cancelling it is enough, no separate flag exists.
func TestCheckOpenFollowsBaseContext(t *testing.T) {
	t.Parallel()
	c, err := NewClient(WithUserAgent("test/1.0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.checkOpen(); err != nil {
		t.Fatalf("checkOpen() before cancel = %v, want nil", err)
	}
	c.cancelBase(ErrClientClosed)
	if err := c.checkOpen(); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("checkOpen() after cancel = %v, want ErrClientClosed", err)
	}
}

// mustPanic fails the test unless f panics.
func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s did not panic", name)
		}
	}()
	f()
}

// TestNilContextPanics pins the documented contract (M4-34): a nil ctx panics,
// as in the standard library, instead of returning an error.
func TestNilContextPanics(t *testing.T) {
	t.Parallel()
	srv := newHitServer(t)
	c := newTestClient(t, srv.URL, fastPolling())
	var nilCtx context.Context
	mustPanic(t, "GetUser(nil)", func() { _, _ = c.GetUser(nilCtx) })
	mustPanic(t, "WaitForJob(nil)", func() { _, _ = c.WaitForJob(nilCtx, Job{ID: "job-1"}) })
}

// TestConcurrentCloseWithCallsInFlight closes the client from several
// goroutines while requests are held in the server: every Close returns, every
// in-flight call fails with ErrClientClosed and no call made afterwards sends
// a request.
func TestConcurrentCloseWithCallsInFlight(t *testing.T) {
	t.Parallel()
	const inFlight, closers = 4, 8
	srv := newHitServer(t, keySkin) // the gate is never opened
	c := newTestClient(t, srv.URL)
	ctx := context.Background()

	calls := make([]<-chan error, inFlight)
	for i := range calls {
		calls[i] = goCall(skinCall(ctx, c))
	}
	waitCount(t, srv, keySkin, inFlight)

	closeDone := make(chan error, closers)
	for range closers {
		go func() { closeDone <- c.Close() }()
	}
	for range closers {
		select {
		case err := <-closeDone:
			if err != nil {
				t.Errorf("Close() error = %v, want nil", err)
			}
		case <-time.After(waitTimeout(t)):
			t.Fatal("a concurrent Close did not return")
		}
	}
	for i, ch := range calls {
		if err := mustDone(t, "in-flight call", ch); !errors.Is(err, ErrClientClosed) {
			t.Errorf("in-flight call %d error = %v, want ErrClientClosed", i, err)
		}
	}

	before := map[string]int{}
	for _, k := range []string{keySkin, keyUser, keyQueue} {
		before[k] = srv.count(k)
	}
	after := map[string]func() error{
		keySkin: skinCall(ctx, c),
		keyUser: func() error { _, err := c.GetUser(ctx); return err },
		keyQueue: func() error {
			_, err := c.Queue(ctx, FromURL("https://example.com/s.png"), GenerateOptions{})
			return err
		},
	}
	for k, call := range after {
		if err := call(); !errors.Is(err, ErrClientClosed) {
			t.Errorf("call after Close (%s) error = %v, want ErrClientClosed", k, err)
		}
		if got := srv.count(k); got != before[k] {
			t.Errorf("%s hits = %d after Close, want %d (no request)", k, got, before[k])
		}
	}
}
