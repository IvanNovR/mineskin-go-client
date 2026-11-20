package mineskin

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

const (
	grantsFast   = `{"user":"744c993e-0e8a-4f0b-9b6a-2f3c7a1b5d11","grants":{"per_minute":600,"concurrency":3}}`
	grantsSlow   = `{"user":"744c993e-0e8a-4f0b-9b6a-2f3c7a1b5d11","grants":{"per_minute":60,"concurrency":2}}`
	invalidKey   = `{"success":false,"errors":[{"code":"invalid_api_key","message":"bad key"}]}`
	failedJob    = `{"success":true,"job":{"id":"job-1","status":"failed"}}`
	htmlBadGate  = "<html>bad gateway</html>"
	rateLimited  = `{"success":false,"errors":[{"code":"too_many_requests","message":"slow down"}]}`
	autoTestKey  = "msk_id_SECRETPART"
	staleRefresh = 5*time.Minute + 2*time.Second
)

// callAdvancing runs call while advancing the fake clock by 1 ms steps until it
// returns, so a start slot is never missed.
func callAdvancing(t *testing.T, fake *clock.Fake, call func() error) error {
	t.Helper()
	res := goCall(call)
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-res:
			return err
		default:
			fake.Advance(delayTick)
			time.Sleep(waitPoll)
		}
	}
	t.Fatal("call did not finish")
	return nil
}

func limitsNow(c *Client) (time.Duration, int) {
	l := c.genGroup.Tuner.Limits(c.clock.Now())
	return l.Interval, l.Concurrency
}

func TestAutoFirstCallLoadsGrantsBeforeThrottle(t *testing.T) {
	var order []string
	var mu sync.Mutex
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		order = append(order, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/v2/me" {
			writeJSON(200, grantsFast)(w, r)
			return
		}
		writeJSON(200, queueFixture)(w, r)
	})
	c := newTestClient(t, srv.URL, WithAutoTuning())
	if err := queueCall(c)(); err != nil {
		t.Fatalf("Queue() error = %v", err)
	}
	mu.Lock()
	got := strings.Join(order, ",")
	mu.Unlock()
	if got != keyUser+","+keyQueue {
		t.Errorf("request order = %q, want me then queue", got)
	}
	if iv, n := limitsNow(c); iv != 100*time.Millisecond || n != 3 {
		t.Errorf("limits = %v/%d, want 100ms/3 from the grants", iv, n)
	}
}

func TestAutoConcurrentFirstCallsShareOneLoad(t *testing.T) {
	srv := replyServer(t, map[string]reply{keyUser: {status: 200, body: grantsFast}, keyQueue: {status: 200, body: plainQueue}}, keyUser)
	c := newTestClient(t, srv.URL, WithAutoTuning())
	var chans []<-chan error
	for range 5 {
		chans = append(chans, goCall(queueCall(c)))
	}
	waitCount(t, srv, keyUser, 1)
	srv.open(keyUser)
	for _, ch := range chans {
		if err := mustDone(t, "Queue", ch); err != nil {
			t.Errorf("Queue error = %v", err)
		}
	}
	if got := srv.count(keyUser); got != 1 {
		t.Errorf("/v2/me hits = %d, want 1", got)
	}
}

func TestAutoFirstLoadFailureUsesBaselineAndWarns(t *testing.T) {
	srv := replyServer(t, map[string]reply{keyUser: {status: 401, body: invalidKey}, keyQueue: {status: 200, body: plainQueue}})
	var logs bytes.Buffer
	c := newTestClient(t, srv.URL, WithAutoTuning(), WithAPIKey(autoTestKey), WithLogger(captureLogger(&logs)))
	if err := queueCall(c)(); err != nil {
		t.Fatalf("Queue() error = %v", err)
	}
	if iv, n := limitsNow(c); iv != time.Second || n != 1 {
		t.Errorf("limits = %v/%d, want baseline 1s/1", iv, n)
	}
	if got := strings.Count(logs.String(), "level=WARN msg=\"Loading MineSkin grants failed"); got != 1 {
		t.Errorf("load failure warnings = %d, want 1; log:\n%s", got, logs.String())
	}
}

func TestAutoGrantsRequestUsesGetThrottle(t *testing.T) {
	srv := newHitServer(t, keySkin)
	c := newTestClient(t, srv.URL, WithAutoTuning(), WithGetThrottle(oneSlot()))
	held := goCall(getCalls(c)[keySkin])
	waitCount(t, srv, keySkin, 1)
	queued := goCall(queueCall(c))
	waitWaiting(t, c.getGroup, 1)
	if got := srv.count(keyUser); got != 0 {
		t.Fatalf("/v2/me hits = %d while the get slot was held, want 0", got)
	}
	srv.open(keySkin)
	if err := mustDone(t, "GetSkin", held); err != nil {
		t.Errorf("GetSkin error = %v", err)
	}
	if err := mustDone(t, "Queue", queued); err != nil {
		t.Errorf("Queue error = %v", err)
	}
	if got := srv.count(keyUser); got != 1 {
		t.Errorf("/v2/me hits = %d, want 1", got)
	}
}

func TestAutoPenaltyOnRetryableUnexpected(t *testing.T) {
	tests := []struct {
		name    string
		replies map[string]reply
		call    func(c *Client) error
		auto    bool
		penalty bool
	}{
		{"502 html on queue", map[string]reply{keyQueue: {status: 502, body: htmlBadGate}}, func(c *Client) error { return queueCall(c)() }, true, true},
		{"409 html on queue", map[string]reply{keyQueue: {status: 409, body: htmlBadGate}}, func(c *Client) error { return queueCall(c)() }, true, false},
		{"json 429 on queue", map[string]reply{keyQueue: {status: 429, body: rateLimited}}, func(c *Client) error { return queueCall(c)() }, true, false},
		{"502 html on GetSkin", map[string]reply{keySkin: {status: 502, body: htmlBadGate}}, func(c *Client) error { return getCalls(c)[keySkin]() }, true, false},
		{"502 html with fixed settings", map[string]reply{keyQueue: {status: 502, body: htmlBadGate}}, func(c *Client) error { return queueCall(c)() }, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := replyServer(t, tt.replies)
			opts := []Option{}
			if tt.auto {
				opts = append(opts, WithAutoTuning())
			}
			c := newTestClient(t, srv.URL, opts...)
			_ = tt.call(c) // the error itself is covered by the response tests
			if !tt.auto {
				if c.genGroup.Tuner != nil {
					t.Fatal("tuner exists without auto-tuning")
				}
				return
			}
			iv, _ := limitsNow(c)
			want := time.Second
			if tt.penalty {
				if iv <= want+400*time.Millisecond || iv > want+500*time.Millisecond {
					t.Errorf("interval = %v, want about 1.5s", iv)
				}
				return
			}
			if iv != want {
				t.Errorf("interval = %v, want %v (no penalty)", iv, want)
			}
		})
	}
}

func TestAutoPenaltyOnFailedJob(t *testing.T) {
	srv := replyServer(t, map[string]reply{keyJob: {status: 200, body: failedJob}})
	c := newTestClient(t, srv.URL, WithAutoTuning(), fastPolling())
	if _, err := c.WaitForJob(context.Background(), Job{ID: "job-1"}); err == nil {
		t.Fatal("WaitForJob() of a failed job returned no error")
	}
	if iv, _ := limitsNow(c); iv <= 1400*time.Millisecond || iv > 1500*time.Millisecond {
		t.Errorf("interval = %v, want one penalty step on 1s", iv)
	}

	plain := newTestClient(t, srv.URL, fastPolling())
	if _, err := plain.WaitForJob(context.Background(), Job{ID: "job-1"}); err == nil {
		t.Fatal("WaitForJob() of a failed job returned no error")
	}
	if plain.genGroup.Tuner != nil {
		t.Error("tuner exists without auto-tuning")
	}
}

func TestAutoTuningOnlyAffectsGenerate(t *testing.T) {
	srv := replyServer(t, map[string]reply{keyUser: {status: 200, body: grantsSlow}, keyQueue: {status: 200, body: plainQueue}}, keySkin)
	c := newTestClient(t, srv.URL, WithAutoTuning(), WithGetThrottle(Throttle{}))
	if c.getGroup.Tuner != nil {
		t.Fatal("get group has a tuner")
	}
	if err := queueCall(c)(); err != nil {
		t.Fatal(err)
	}
	var chans []<-chan error
	for range DefaultGetConcurrency {
		chans = append(chans, goCall(getCalls(c)[keySkin]))
	}
	waitCount(t, srv, keySkin, DefaultGetConcurrency)
	srv.open(keySkin)
	for _, ch := range chans {
		if err := mustDone(t, "GetSkin", ch); err != nil {
			t.Error(err)
		}
	}
}

func TestAutoNeverLogsAPIKey(t *testing.T) {
	for name, meReply := range map[string]reply{
		"ok":     {status: 200, body: grantsFast},
		"failed": {status: 401, body: invalidKey},
	} {
		t.Run(name, func(t *testing.T) {
			srv := replyServer(t, map[string]reply{keyUser: meReply, keyQueue: {status: 200, body: plainQueue}})
			var logs bytes.Buffer
			c := newTestClient(t, srv.URL, WithAutoTuning(), WithAPIKey(autoTestKey), WithLogger(captureLogger(&logs)))
			if err := queueCall(c)(); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(logs.String(), "SECRETPART") {
				t.Errorf("log contains the key secret:\n%s", logs.String())
			}
			if logs.Len() == 0 {
				t.Error("expected log output")
			}
		})
	}
}

func TestAutoFirstLoadCallerCancel(t *testing.T) {
	srv := replyServer(t, map[string]reply{keyUser: {status: 200, body: grantsFast}, keyQueue: {status: 200, body: plainQueue}}, keyUser)
	c := newTestClient(t, srv.URL, WithAutoTuning())
	ctx, cancel := context.WithCancel(context.Background())
	first := goCall(func() error {
		_, err := c.Queue(ctx, FromURL("https://example.com/s.png"), nil)
		return err
	})
	waitCount(t, srv, keyUser, 1)
	cancel()
	err := mustDone(t, "cancelled Queue", first)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrClient) {
		t.Errorf("error = %v, want context.Canceled and ErrClient", err)
	}
	srv.open(keyUser)
	if err := queueCall(c)(); err != nil {
		t.Fatalf("second Queue() error = %v", err)
	}
	if got := srv.count(keyUser); got != 1 {
		t.Errorf("/v2/me hits = %d, want 1 (the load must survive the cancel)", got)
	}
}

func TestCloseCancelsRefreshInFlight(t *testing.T) {
	var meHits atomic.Int32
	refreshCancelled := make(chan struct{})
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/me" {
			writeJSON(200, queueFixture)(w, r)
			return
		}
		if meHits.Add(1) == 1 {
			writeJSON(200, grantsFast)(w, r)
			return
		}
		<-r.Context().Done()
		close(refreshCancelled)
	})
	before := runtimeGoroutines()
	fake := clock.NewFake(pollerT0)
	c := newTestClient(t, srv.URL, withClock(fake), WithAutoTuning())
	if err := callAdvancing(t, fake, queueCall(c)); err != nil {
		t.Fatal(err)
	}
	fake.Advance(staleRefresh)
	if err := callAdvancing(t, fake, queueCall(c)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "refresh to start", func() bool { return meHits.Load() == 2 })
	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	mustReceive(t, "refresh request cancelled by Close", refreshCancelled)
	waitGoroutinesSettle(t, before+1)
}
