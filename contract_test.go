package mineskin

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This file pins the public error contract: for every error path the table
// asserts the errors.Is / errors.As checks a caller can rely on. It goes
// through the public API only (plus test helpers), so it must keep passing,
// unchanged, while the internals are reorganised.

const (
	notFoundJSON     = `{"success":false,"errors":[{"code":"not_found","message":"no such skin"}]}`
	htmlBadGatewayFx = "<html><head><title>502 Bad Gateway</title></head><body><center><h1>502 Bad Gateway</h1></center><hr><center>nginx</center></body></html>"
	contractRetryMS  = 20
)

// timeoutNetError is a net.Error that reports a timeout.
type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "contract: i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

var _ net.Error = timeoutNetError{}

// contractCase is one error path of the public contract.
type contractCase struct {
	name string
	// run triggers the error path and returns the error the caller sees.
	run func(t *testing.T) error
	// is lists the targets errors.Is must match.
	is []error
	// isNot lists the targets errors.Is must not match.
	isNot []error
	// check runs extra errors.As assertions.
	check func(t *testing.T, err error)
}

func TestContractPublicErrors(t *testing.T) {
	for _, tt := range contractCases() {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run(t)
			if err == nil {
				t.Fatal("error = nil, want an error")
			}
			for _, target := range tt.is {
				if !errors.Is(err, target) {
					t.Errorf("errors.Is(%v, %v) = false, want true", err, target)
				}
			}
			for _, target := range tt.isNot {
				if errors.Is(err, target) {
					t.Errorf("errors.Is(%v, %v) = true, want false", err, target)
				}
			}
			if tt.check != nil {
				tt.check(t, err)
			}
		})
	}
}

func contractCases() []contractCase {
	cases := []contractCase{
		apiNotFoundCase(), htmlBadGatewayCase(), transportErrorCase(),
		ctxCancelledQueuedCase(), ctxCancelledDuring429RetryCase(),
		closeQueuedCase(), closeInFlightCase(), closeWaitingCase(),
		maxAttemptsCase(), jobFailedCase(),
		pollIntervalZeroCase(), pollIntervalPanicCase(), newClientValidationCase(),
	}
	return cases
}

func apiNotFoundCase() contractCase {
	return contractCase{
		name: "API 404",
		run: func(t *testing.T) error {
			srv := newTestServer(t, writeJSON(http.StatusNotFound, notFoundJSON))
			_, err := newTestClient(t, srv.URL).GetSkin(context.Background(), testSkinID)
			return err
		},
		is:    []error{ErrNotFound, ErrClient},
		isNot: []error{ErrInvalidInput, ErrClientClosed},
		check: func(t *testing.T, err error) {
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("errors.As(%v, *APIError) = false", err)
			}
			if apiErr.Status != http.StatusNotFound || apiErr.Code != "not_found" || apiErr.IsRetryable() {
				t.Errorf("APIError = %+v, retryable %v", apiErr, apiErr.IsRetryable())
			}
		},
	}
}

func htmlBadGatewayCase() contractCase {
	return contractCase{
		name: "HTML 502",
		run: func(t *testing.T) error {
			srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, htmlBadGatewayFx)
			})
			_, err := newTestClient(t, srv.URL).GetUser(context.Background())
			return err
		},
		is:    []error{ErrClient},
		isNot: []error{ErrNotFound, ErrInvalidInput},
		check: func(t *testing.T, err error) {
			var ue *UnexpectedResponseError
			if !errors.As(err, &ue) {
				t.Fatalf("errors.As(%v, *UnexpectedResponseError) = false", err)
			}
			if ue.Status != http.StatusBadGateway || !ue.IsRetryable() || ue.Source != SourceNginx {
				t.Errorf("UnexpectedResponseError = %+v", ue)
			}
			var apiErr *APIError
			if errors.As(err, &apiErr) {
				t.Error("an HTML response must not be an *APIError")
			}
		},
	}
}

func transportErrorCase() contractCase {
	return contractCase{
		name: "transport error",
		run: func(t *testing.T) error {
			rt := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, timeoutNetError{} })
			_, err := newTestClient(t, "http://127.0.0.1:1", WithTransport(rt)).GetUser(context.Background())
			return err
		},
		is:    []error{ErrClient},
		isNot: []error{ErrNotFound, ErrInvalidInput, ErrClientClosed},
		check: func(t *testing.T, err error) {
			var ne net.Error
			if !errors.As(err, &ne) || !ne.Timeout() {
				t.Errorf("errors.As(%v, net.Error) timeout = false, want a timeout", err)
			}
			if strings.Contains(err.Error(), "test/1.0") {
				t.Errorf("error text %q leaks request data", err)
			}
		},
	}
}

func ctxCancelledQueuedCase() contractCase {
	return contractCase{
		name: "ctx cancelled while queued",
		run: func(t *testing.T) error {
			srv := newHitServer(t, keySkin)
			c := newTestClient(t, srv.URL, WithGetThrottle(oneSlot()))
			held := goCall(getCalls(c)[keySkin])
			waitCount(t, srv, keySkin, 1)
			ctx, cancel := context.WithCancel(context.Background())
			res := goCall(func() error { _, err := c.GetSkin(ctx, testSkinID); return err })
			cancel()
			err := mustDone(t, "queued GetSkin", res)
			srv.open(keySkin)
			if heldErr := mustDone(t, "held GetSkin", held); heldErr != nil {
				t.Errorf("held call error = %v, want nil", heldErr)
			}
			return err
		},
		is:    []error{context.Canceled, ErrClient},
		isNot: []error{ErrClientClosed, context.DeadlineExceeded},
	}
}

func ctxCancelledDuring429RetryCase() contractCase {
	return contractCase{
		name: "ctx cancelled during a 429 retry",
		run: func(t *testing.T) error {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			var sends atomic.Int32
			rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if sends.Add(1) > 1 {
					// The retry is in flight: the caller gives up.
					cancel()
					return nil, req.Context().Err()
				}
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(nextRelative(contractRetryMS, false))),
					Request:    req,
				}, nil
			})
			c := newTestClient(t, "http://127.0.0.1:1", WithTransport(rt))
			_, err := c.GetSkin(ctx, testSkinID)
			if got := sends.Load(); got != 2 {
				t.Errorf("sends = %d, want 2", got)
			}
			return err
		},
		is:    []error{context.Canceled, ErrClient},
		isNot: []error{ErrClientClosed},
		check: func(t *testing.T, err error) {
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
				t.Errorf("errors.As(%v, *APIError) status 429 = false", err)
			}
		},
	}
}

func closeQueuedCase() contractCase {
	return contractCase{
		name: "Close while queued",
		run: func(t *testing.T) error {
			srv := newHitServer(t, keySkin)
			c := newTestClient(t, srv.URL, WithGetThrottle(oneSlot()))
			held := goCall(getCalls(c)[keySkin])
			waitCount(t, srv, keySkin, 1)
			queued := goCall(getCalls(c)[keySkin])
			eventually(t, "second call queued", func() bool { return c.getGroup.Throttle.Stats().Waiting == 1 })
			_ = c.Close()
			_ = mustDone(t, "held", held)
			return mustDone(t, "queued", queued)
		},
		is:    []error{ErrClientClosed, ErrClient},
		isNot: []error{context.Canceled},
	}
}

func closeInFlightCase() contractCase {
	return contractCase{
		name: "Close while in flight",
		run: func(t *testing.T) error {
			started := make(chan struct{})
			srv := newTestServer(t, func(_ http.ResponseWriter, r *http.Request) {
				close(started)
				<-r.Context().Done()
			})
			c := newTestClient(t, srv.URL)
			res := goCall(func() error { _, err := c.GetUser(context.Background()); return err })
			mustReceive(t, "request start", started)
			_ = c.Close()
			return mustDone(t, "GetUser", res)
		},
		is:    []error{ErrClientClosed, ErrClient},
		isNot: []error{context.Canceled},
	}
}

func closeWaitingCase() contractCase {
	return contractCase{
		name: "Close while waiting for a job",
		run: func(t *testing.T) error {
			srv := newHitServer(t)
			c := newTestClient(t, srv.URL, WithJobPolling(PollOptions{InitialDelay: time.Hour}))
			res := goCall(func() error {
				_, err := c.WaitForJob(context.Background(), Job{ID: "job-1"})
				return err
			})
			eventually(t, "poller running", c.poller.Running)
			_ = c.Close()
			return mustDone(t, "WaitForJob", res)
		},
		is:    []error{ErrClientClosed, ErrClient},
		isNot: []error{context.Canceled},
	}
}

// pollOnce is a fast polling configuration with the given interval function.
func pollOnce(attempts int, interval func(int) time.Duration) Option {
	return WithJobPolling(PollOptions{InitialDelay: time.Millisecond, MaxAttempts: attempts, Interval: interval})
}

func waitFor(t *testing.T, srvBody string, opt Option) error {
	t.Helper()
	srv := newTestServer(t, writeJSON(http.StatusOK, srvBody))
	c := newTestClient(t, srv.URL, opt)
	_, err := c.WaitForJob(waitCtx(t), Job{ID: "j1"})
	return err
}

func maxAttemptsCase() contractCase {
	return contractCase{
		name: "ErrMaxAttempts",
		run: func(t *testing.T) error {
			return waitFor(t, jobJSON("j1", "waiting"), pollOnce(2, func(int) time.Duration { return time.Millisecond }))
		},
		is:    []error{ErrMaxAttempts, ErrClient},
		isNot: []error{ErrInvalidInput, ErrClientClosed},
	}
}

func jobFailedCase() contractCase {
	return contractCase{
		name: "JobFailedError",
		run: func(t *testing.T) error {
			body := `{"success":true,"job":{"id":"j1","status":"failed"},"errors":[{"code":"bad_image","message":"nope"}]}`
			return waitFor(t, body, pollOnce(5, func(int) time.Duration { return time.Millisecond }))
		},
		is:    []error{ErrClient},
		isNot: []error{ErrMaxAttempts, ErrInvalidInput},
		check: func(t *testing.T, err error) {
			var jf *JobFailedError
			if !errors.As(err, &jf) {
				t.Fatalf("errors.As(%v, *JobFailedError) = false", err)
			}
			if jf.Job.ID != "j1" || jf.Code != "bad_image" || jf.Message != "nope" {
				t.Errorf("JobFailedError = %+v", jf)
			}
		},
	}
}

func pollIntervalZeroCase() contractCase {
	return contractCase{
		name: "PollInterval returns 0",
		run: func(t *testing.T) error {
			return waitFor(t, jobJSON("j1", "waiting"), pollOnce(5, func(int) time.Duration { return 0 }))
		},
		is:    []error{ErrInvalidInput, ErrClient},
		isNot: []error{ErrMaxAttempts, ErrClientClosed},
	}
}

func pollIntervalPanicCase() contractCase {
	return contractCase{
		name: "PollInterval panics",
		run: func(t *testing.T) error {
			return waitFor(t, jobJSON("j1", "waiting"), pollOnce(5, func(int) time.Duration { panic("boom") }))
		},
		is:    []error{ErrClient},
		isNot: []error{ErrInvalidInput, ErrMaxAttempts, ErrClientClosed},
		check: func(t *testing.T, err error) {
			if !strings.Contains(err.Error(), "panicked") || !strings.Contains(err.Error(), "boom") {
				t.Errorf("error text %q lacks the panic", err)
			}
		},
	}
}

func newClientValidationCase() contractCase {
	return contractCase{
		name: "NewClient validation",
		run: func(*testing.T) error {
			c, err := NewClient(WithBaseURL("ftp://example.com"), WithAPIKey("msk_secret_value"))
			if c != nil {
				_ = c.Close()
			}
			return err
		},
		is:    []error{ErrInvalidInput, ErrClient},
		isNot: []error{ErrNotFound, ErrClientClosed},
		check: func(t *testing.T, err error) {
			if strings.Contains(err.Error(), "msk_secret_value") {
				t.Errorf("error text %q leaks the API key", err)
			}
		},
	}
}
