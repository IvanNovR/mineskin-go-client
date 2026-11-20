package mineskin

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

const (
	tooSoonJSON   = `{"success":false,"errors":[{"code":"rate_limit","message":"request too soon"}]}`
	retryTestName = "retry-test"
)

// seqServer answers the nth request with the nth reply (the last one repeats)
// and records every request body and Content-Type.
type seqServer struct {
	URL     string
	mu      sync.Mutex
	bodies  [][]byte
	ctypes  []string
	replies []reply
}

func newSeqServer(t *testing.T, replies ...reply) *seqServer {
	t.Helper()
	s := &seqServer{replies: replies}
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		rep := s.replies[min(len(s.bodies), len(s.replies)-1)]
		s.bodies = append(s.bodies, body)
		s.ctypes = append(s.ctypes, r.Header.Get("Content-Type"))
		s.mu.Unlock()
		if rep.retryAfter != "" {
			w.Header().Set("Retry-After", rep.retryAfter)
		}
		writeJSON(rep.status, rep.body)(w, r)
	})
	s.URL = srv.URL
	return s
}

func (s *seqServer) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func skinCall(ctx context.Context, c *Client) func() error {
	return func() error { _, err := c.GetSkin(ctx, testSkinID); return err }
}

func TestRetryDefaultConstant(t *testing.T) {
	if defaultRateLimitRetries != 2 {
		t.Fatalf("defaultRateLimitRetries = %d, want 2", defaultRateLimitRetries)
	}
}

func TestRetryRecoversFromJSON429(t *testing.T) {
	srv := newSeqServer(t, reply{status: 429, body: tooSoonJSON}, reply{status: 200, body: skinMinimal})
	c := newTestClient(t, srv.URL)
	if err := skinCall(context.Background(), c)(); err != nil {
		t.Fatalf("GetSkin() error = %v, want success after one retry", err)
	}
	if got := srv.hits(); got != 2 {
		t.Errorf("hits = %d, want 2", got)
	}
}

func TestRetryRecoversFromNonJSON429(t *testing.T) {
	srv := newSeqServer(t, reply{status: 429, body: "<html>slow down</html>"}, reply{status: 200, body: skinMinimal})
	c := newTestClient(t, srv.URL)
	if err := skinCall(context.Background(), c)(); err != nil {
		t.Fatalf("GetSkin() error = %v, want success after one retry", err)
	}
}

func TestRetryGivesUpAfterConfiguredRetries(t *testing.T) {
	tests := []struct {
		name     string
		opts     []Option
		wantHits int
	}{
		{"default is two retries", nil, 3},
		{"zero disables", []Option{WithRateLimitRetries(0)}, 1},
		{"five retries", []Option{WithRateLimitRetries(5)}, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newSeqServer(t, reply{status: 429, body: tooSoonJSON})
			c := newTestClient(t, srv.URL, tt.opts...)
			err := queueCall(c)()
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Meta.Status != 429 || apiErr.Code != "rate_limit" {
				t.Fatalf("error = %v, want the original 429 *APIError", err)
			}
			if got := srv.hits(); got != tt.wantHits {
				t.Errorf("hits = %d, want %d", got, tt.wantHits)
			}
		})
	}
}

func TestRetryExhaustedNonJSON429IsUnexpectedResponse(t *testing.T) {
	srv := newSeqServer(t, reply{status: 429, body: "<html>slow down</html>"})
	c := newTestClient(t, srv.URL)
	var ue *UnexpectedResponseError
	if err := generateCall(c)(); !errors.As(err, &ue) || ue.Status != 429 {
		t.Fatalf("error = %v, want *UnexpectedResponseError with status 429", err)
	}
	if got := srv.hits(); got != 3 {
		t.Errorf("hits = %d, want 3", got)
	}
}

func TestRetryNeverRetriesOtherStatuses(t *testing.T) {
	for _, status := range []int{400, 401, 404, 408, 500, 502, 503} {
		srv := newSeqServer(t, reply{status: status, retryAfter: "0", body: tooSoonJSON})
		c := newTestClient(t, srv.URL)
		if err := queueCall(c)(); err == nil {
			t.Fatalf("status %d: no error", status)
		}
		if got := srv.hits(); got != 1 {
			t.Errorf("status %d: hits = %d, want 1", status, got)
		}
	}
}

func TestWithRateLimitRetriesRejectsNegative(t *testing.T) {
	_, err := NewClient(WithRateLimitRetries(-1))
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("NewClient() error = %v, want ErrInvalidInput", err)
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	return buf.Bytes()
}

func TestRetryReplaysRequestBodies(t *testing.T) {
	pngData := testPNG(t)
	sources := map[string]Source{
		"image":  FromImage(image.NewNRGBA(image.Rect(0, 0, 64, 64))),
		"reader": FromReader(bytes.NewReader(pngData)), // a one-shot reader
		"url":    FromURL("https://example.com/s.png"),
		"user":   FromUser(UUID{1, 2, 3}),
	}
	for name, src := range sources {
		t.Run(name, func(t *testing.T) {
			srv := newSeqServer(t, reply{status: 429, body: tooSoonJSON}, reply{status: 200, body: skinMinimal})
			c := newTestClient(t, srv.URL)
			if _, err := c.Generate(context.Background(), src, &GenerateOptions{Name: retryTestName}); err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if srv.hits() != 2 {
				t.Fatalf("hits = %d, want 2", srv.hits())
			}
			if len(srv.bodies[0]) == 0 || !bytes.Equal(srv.bodies[0], srv.bodies[1]) {
				t.Errorf("retry body differs from the first (len %d vs %d)", len(srv.bodies[0]), len(srv.bodies[1]))
			}
			if srv.ctypes[0] != srv.ctypes[1] {
				t.Errorf("Content-Type differs: %q vs %q", srv.ctypes[0], srv.ctypes[1])
			}
		})
	}
}

func TestRetryWaitsForServerDelayOfItsGroup(t *testing.T) {
	srv := newSeqServer(t, reply{status: 429, body: nextRelative(3000, false)}, reply{status: 200, body: skinMinimal})
	c, fake := delayClient(t, &clientServer{URL: srv.URL})
	if err := assertDelayed(t, fake, c.getGroup, skinCall(context.Background(), c), 3*time.Second); err != nil {
		t.Fatalf("GetSkin() error = %v, want success after the delay", err)
	}
	if got := srv.hits(); got != 2 {
		t.Errorf("hits = %d, want 2", got)
	}
}

func TestRetryWithoutDelayWaitsForThrottleInterval(t *testing.T) {
	srv := newSeqServer(t, reply{status: 429, body: tooSoonJSON}, reply{status: 200, body: skinMinimal})
	c, fake := delayClient(t, &clientServer{URL: srv.URL}, WithGetThrottle(Throttle{Interval: 500 * time.Millisecond, Concurrency: 1}))
	if err := assertDelayed(t, fake, c.getGroup, skinCall(context.Background(), c), 500*time.Millisecond); err != nil {
		t.Fatalf("GetSkin() error = %v", err)
	}
}

func TestRetryReturns429WhenDelayEndsAfterCtxDeadline(t *testing.T) {
	srv := newSeqServer(t, reply{status: 429, body: nextRelative(600_000, false)})
	fake := clock.NewFake(time.Now())
	c := newTestClient(t, srv.URL, withClock(fake))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := mustDone(t, "GetSkin", goCall(skinCall(ctx, c)))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Meta.Status != 429 {
		t.Fatalf("error = %v, want the original 429 *APIError", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v must not be a deadline error", err)
	}
	if got := srv.hits(); got != 1 {
		t.Errorf("hits = %d, want 1", got)
	}
}

func TestRetryWaitsWhenDelayEndsBeforeCtxDeadline(t *testing.T) {
	srv := newSeqServer(t, reply{status: 429, body: nextRelative(1000, false)}, reply{status: 200, body: skinMinimal})
	fake := clock.NewFake(time.Now())
	c := newTestClient(t, srv.URL, withClock(fake))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := assertDelayed(t, fake, c.getGroup, skinCall(ctx, c), time.Second); err != nil {
		t.Fatalf("GetSkin() error = %v", err)
	}
}

func TestRetryWaitEndsOnCloseAndCancel(t *testing.T) {
	t.Run("close", func(t *testing.T) {
		srv := newSeqServer(t, reply{status: 429, body: nextRelative(3000, false)})
		c, _ := delayClient(t, &clientServer{URL: srv.URL})
		res := goCall(skinCall(context.Background(), c))
		waitWaiting(t, c.getGroup, 1)
		_ = c.Close()
		if err := mustDone(t, "GetSkin", res); !errors.Is(err, ErrClientClosed) {
			t.Errorf("error = %v, want ErrClientClosed", err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		srv := newSeqServer(t, reply{status: 429, body: nextRelative(3000, false)})
		c, _ := delayClient(t, &clientServer{URL: srv.URL})
		ctx, cancel := context.WithCancel(context.Background())
		res := goCall(skinCall(ctx, c))
		waitWaiting(t, c.getGroup, 1)
		cancel()
		// The caller's ctx ended during the retry wait: the error is both
		// the cancellation and the 429.
		assertCtxAnd429(t, mustDone(t, "GetSkin", res), context.Canceled)
		if got := srv.hits(); got != 1 {
			t.Errorf("hits = %d, want 1", got)
		}
	})
}

func TestRetryChecksStartGateOnEveryAttempt(t *testing.T) {
	t.Run("gate open", func(t *testing.T) {
		srv := newSeqServer(t, reply{status: 429, body: tooSoonJSON}, reply{status: 200, body: jobFixture})
		c := newTestClient(t, srv.URL)
		var calls int
		if _, err := c.getJobGated(context.Background(), "job-1", func() bool { calls++; return true }); err != nil {
			t.Fatalf("getJobGated() error = %v", err)
		}
		if calls != 2 {
			t.Errorf("gate calls = %d, want 2", calls)
		}
	})
	t.Run("gate closes before the retry", func(t *testing.T) {
		srv := newSeqServer(t, reply{status: 429, body: tooSoonJSON}, reply{status: 200, body: jobFixture})
		c := newTestClient(t, srv.URL)
		var calls int
		_, err := c.getJobGated(context.Background(), "job-1", func() bool { calls++; return calls == 1 })
		if !errors.Is(err, errCheckSkipped) {
			t.Fatalf("error = %v, want errCheckSkipped", err)
		}
		if got := srv.hits(); got != 1 {
			t.Errorf("hits = %d, want 1", got)
		}
	})
}

func TestRetryLogsOneDebugLineWithoutKey(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv := newSeqServer(t, reply{status: 429, body: tooSoonJSON}, reply{status: 200, body: skinMinimal})
	c := newTestClient(t, srv.URL, WithLogger(logger), WithAPIKey("msk_abc_topsecretvalue"))
	if err := skinCall(context.Background(), c)(); err != nil {
		t.Fatalf("GetSkin() error = %v", err)
	}
	out := buf.String()
	if n := strings.Count(out, "retrying rate-limited request"); n != 1 {
		t.Errorf("retry log lines = %d, want 1\n%s", n, out)
	}
	for _, want := range []string{"group=get", "attempt=1", "delay="} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "topsecretvalue") {
		t.Error("log contains the API key")
	}
}

// runWithFakeTime runs call and, once per retry that waits in c's generate
// group, advances fake by 1.5 s (the penalised interval), until call returns.
func runWithFakeTime(t *testing.T, fake *clock.Fake, c *Client, srv *clientServer, call func() error) error {
	t.Helper()
	res := goCall(call)
	advancedAt := -1
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-res:
			return err
		default:
		}
		if hits := srv.count(keyQueue); c.genGroup.Throttle.Stats().Waiting == 1 && hits != advancedAt {
			advancedAt = hits
			fake.Advance(1500 * time.Millisecond)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("call did not finish")
	return nil
}

func TestRetryPenaltyIsPerResponse(t *testing.T) {
	tests := []struct {
		name         string
		retries      int
		body         string
		minIv, maxIv time.Duration
	}{
		{"non-JSON 429 counts every response", 1, htmlBadGate, 1800 * time.Millisecond, 1900 * time.Millisecond},
		{"non-JSON 429 without retries counts once", 0, htmlBadGate, 1500 * time.Millisecond, 1500 * time.Millisecond},
		{"JSON 429 adds no penalty", 1, tooSoonJSON, time.Second, time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := replyServer(t, map[string]reply{keyQueue: {status: 429, body: tt.body}})
			fake := clock.NewFake(pollerT0)
			c := newTestClient(t, srv.URL, withClock(fake), WithAutoTuning(), WithRateLimitRetries(tt.retries))
			if err := runWithFakeTime(t, fake, c, srv, queueCall(c)); err == nil {
				t.Fatal("Queue() returned no error")
			}
			if iv, _ := limitsNow(c); iv < tt.minIv || iv > tt.maxIv {
				t.Errorf("interval = %v, want %v..%v", iv, tt.minIv, tt.maxIv)
			}
		})
	}
}

func assertOriginal429(t *testing.T, err error) {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Meta.Status != 429 {
		t.Fatalf("error = %v, want the original 429 *APIError", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v must not be a deadline error", err)
	}
}

// assertCtxAnd429 checks that err matches the ctx error and the
// saved 429 (as *APIError and as text).
func assertCtxAnd429(t *testing.T, err, ctxErr error) {
	t.Helper()
	if !errors.Is(err, ctxErr) {
		t.Errorf("error = %v, want it to match %v", err, ctxErr)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Meta.Status != 429 {
		t.Errorf("error = %v, want it to hold the 429 *APIError", err)
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error text %q lacks the 429", err.Error())
	}
}

func TestRetryReturns429WhenIntervalEndsAfterCtxDeadline(t *testing.T) {
	// A bare 429 (no delay, D30): the retry would wait for the 10 minute
	// interval, which ends after the ctx deadline.
	srv := newSeqServer(t, reply{status: 429, body: tooSoonJSON})
	fake := clock.NewFake(time.Now())
	c := newTestClient(t, srv.URL, withClock(fake), WithGetThrottle(Throttle{Interval: 10 * time.Minute, Concurrency: 1}))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	assertOriginal429(t, mustDone(t, "GetSkin", goCall(skinCall(ctx, c))))
	if got := srv.hits(); got != 1 {
		t.Errorf("hits = %d, want 1", got)
	}
}

func TestRetryWaitEndedByCtxDeadlineReturns429(t *testing.T) {
	// The delay looks fine at first, then another response pushes the group
	// deadline past the ctx deadline while the retry waits.
	srv := replyServer(t, map[string]reply{
		keySkin: {status: 429, retryAfter: "1", body: nextRelative(1000, false)},
		keyJob:  {status: 429, body: nextRelative(3_600_000, false)},
	}, keyJob)
	c, fake := delayClient(t, srv)
	other := goCall(getCalls(c)[keyJob])
	waitCount(t, srv, keyJob, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res := goCall(skinCall(ctx, c))
	waitWaiting(t, c.getGroup, 1) // held back by the start interval
	fake.Advance(delayTick)
	waitCount(t, srv, keySkin, 1)
	waitWaiting(t, c.getGroup, 1) // the retry, waiting for the 1 s delay
	srv.open(keyJob)
	eventually(t, "group deadline pushed", func() bool {
		return c.getGroup.Throttle.Stats().NotBefore.Sub(c.clock.Now()) > time.Hour/2
	})
	err := mustDone(t, "GetSkin", res)
	assertCtxAnd429(t, err, context.DeadlineExceeded)
	var apiErr *APIError
	if errors.As(err, &apiErr) && (apiErr.RateLimit.NextIn != time.Second || apiErr.RetryAfter != time.Second) {
		t.Errorf("429 lost its rate limit data: RateLimit=%+v RetryAfter=%v", apiErr.RateLimit, apiErr.RetryAfter)
	}
	_ = c.Close() // ends the other call, which is retrying too
	_ = mustDone(t, "other call", other)
}

func TestRetryWaitEndedByCtxKeepsNonJSON429(t *testing.T) {
	srv := newSeqServer(t, reply{status: 429, body: "<html>slow down</html>"})
	fake := clock.NewFake(time.Now())
	c := newTestClient(t, srv.URL, withClock(fake), WithGetThrottle(Throttle{Interval: time.Second, Concurrency: 1}))
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	res := goCall(skinCall(ctx2, c))
	waitWaiting(t, c.getGroup, 1)
	cancel2()
	err := mustDone(t, "GetSkin", res)
	var ue *UnexpectedResponseError
	if !errors.Is(err, context.Canceled) || !errors.As(err, &ue) || ue.Status != 429 {
		t.Errorf("error = %v, want context.Canceled and the 429 *UnexpectedResponseError", err)
	}
}

func TestRetryCancelledWaitCountsPenaltyOnce(t *testing.T) {
	run := func(t *testing.T, retries int, cancelDuringWait bool) time.Duration {
		t.Helper()
		srv := replyServer(t, map[string]reply{keyQueue: {status: 429, body: htmlBadGate}})
		fake := clock.NewFake(pollerT0)
		c := newTestClient(t, srv.URL, withClock(fake), WithAutoTuning(), WithRateLimitRetries(retries))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		res := goCall(func() error {
			_, err := c.Queue(ctx, FromURL("https://example.com/s.png"), nil)
			return err
		})
		if cancelDuringWait {
			waitWaiting(t, c.genGroup, 1) // the retry, held by the penalised interval
			cancel()
		}
		var ue *UnexpectedResponseError
		err := mustDone(t, "Queue", res)
		if !errors.As(err, &ue) || ue.Status != 429 {
			t.Fatalf("error = %v, want the 429 *UnexpectedResponseError", err)
		}
		iv, _ := limitsNow(c)
		return iv
	}
	control := run(t, 0, false) // one 429 response, one penalty
	cancelled := run(t, 2, true)
	if control != time.Second+500*time.Millisecond {
		t.Fatalf("control interval = %v, want one penalty step on 1s", control)
	}
	if cancelled != control {
		t.Errorf("interval after a cancelled retry wait = %v, want %v (the 429 counted once)", cancelled, control)
	}
}
