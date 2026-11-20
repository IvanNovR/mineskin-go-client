package mineskin

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
	"github.com/IvanNovR/mineskin-go-client/internal/ratelimit"
)

const (
	delayTick  = time.Millisecond
	plainQueue = `{"success":true,"job":{"id":"job-1","status":"waiting"}}`
)

// reply is a canned response of replyServer.
type reply struct {
	status     int
	retryAfter string
	body       string
}

// replyServer answers each "METHOD /path" key with its canned reply (200 with
// a minimal body for unknown keys) and may hold keys on a gate.
func replyServer(t *testing.T, replies map[string]reply, gates ...string) *clientServer {
	t.Helper()
	s := &clientServer{hits: map[string]int{}, gates: map[string]chan struct{}{}}
	for _, k := range gates {
		s.gates[k] = make(chan struct{})
	}
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		s.mu.Lock()
		s.hits[key]++
		gate := s.gates[key]
		s.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		rep, ok := replies[key]
		if !ok {
			rep = reply{status: 200, body: skinMinimal}
		}
		if rep.retryAfter != "" {
			w.Header().Set("Retry-After", rep.retryAfter)
		}
		writeJSON(rep.status, rep.body)(w, r)
	})
	s.URL = srv.URL
	return s
}

// delayClient builds a client with a fake clock and wide-open groups, so only
// the intervals of 1 ns and server delays can hold a request.
func delayClient(t *testing.T, srv *clientServer, opts ...Option) (*Client, *clock.Fake) {
	t.Helper()
	fake := clock.NewFake(pollerT0)
	return newTestClient(t, srv.URL, append([]Option{withClock(fake)}, opts...)...), fake
}

// assertDelayed checks that call, started now on g, stays queued until the
// fake clock has advanced by exactly want (any want <= 0 means no delay).
func assertDelayed(t *testing.T, fake *clock.Fake, g *ratelimit.Group, call func() error, want time.Duration) error {
	t.Helper()
	res := goCall(call)
	if want <= 0 {
		fake.Advance(delayTick)
		return mustDone(t, "undelayed call", res)
	}
	waitWaiting(t, g, 1)
	fake.Advance(delayTick)
	fake.Advance(want - 2*delayTick)
	if got := g.Throttle.Stats().Waiting; got != 1 {
		t.Fatalf("call left the queue before the %v delay elapsed", want)
	}
	fake.Advance(delayTick)
	return mustDone(t, "delayed call", res)
}

func nextRelative(ms int, success bool) string {
	rl := `"rateLimit":{"next":{"relative":` + strconv.Itoa(ms) + `}}`
	if success {
		return `{"success":true,"job":{"id":"job-1","status":"waiting"},` + rl + `}`
	}
	return `{"success":false,"errors":[{"code":"rate_limited","message":"slow down"}],` + rl + `}`
}

func TestServerDelayFromBodySuccess(t *testing.T) {
	srv := replyServer(t, map[string]reply{keyQueue: {200, "", nextRelative(5000, true)}})
	c, fake := delayClient(t, srv)
	if err := queueCall(c)(); err != nil {
		t.Fatalf("first Queue error = %v", err)
	}
	if err := getCalls(c)[keySkin](); err != nil {
		t.Fatalf("GetSkin must not be held by the generate delay: %v", err)
	}
	if err := assertDelayed(t, fake, c.genGroup, queueCall(c), 5*time.Second); err != nil {
		t.Errorf("second Queue error = %v", err)
	}
}

func TestServerDelayFromBodyOnAPIError(t *testing.T) {
	srv := replyServer(t, map[string]reply{keyQueue: {429, "", nextRelative(3000, false)}})
	// Retries off: this tests the delay of a single response; retries are in retry_test.go.
	c, fake := delayClient(t, srv, WithRateLimitRetries(0))
	err := queueCall(c)()
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "rate_limited" || apiErr.Message != "slow down" {
		t.Fatalf("error = %v, want the original *APIError", err)
	}
	err = assertDelayed(t, fake, c.genGroup, queueCall(c), 3*time.Second)
	if !errors.As(err, &apiErr) {
		t.Errorf("second error = %v, want *APIError", err)
	}
}

func TestServerDelayOnGetGroup(t *testing.T) {
	srv := replyServer(t, map[string]reply{keySkin: {200, "", `{"success":true,"skin":{},"rateLimit":{"next":{"relative":4000}}}`}})
	c, fake := delayClient(t, srv)
	calls := getCalls(c)
	if err := calls[keySkin](); err != nil {
		t.Fatalf("GetSkin error = %v", err)
	}
	if err := queueCall(c)(); err != nil {
		t.Fatalf("Queue must not be delayed by a get response: %v", err)
	}
	if err := assertDelayed(t, fake, c.getGroup, calls[keyJob], 4*time.Second); err != nil {
		t.Errorf("GetJob error = %v", err)
	}
}

func TestRetryAfterOnRetryableJSONAndNonJSON(t *testing.T) {
	tests := []struct {
		name string
		rep  reply
		want time.Duration
	}{
		{"503 html with Retry-After", reply{503, "4", "<html>down</html>"}, 4 * time.Second},
		{"429 json takes the later of the two", reply{429, "2", nextRelative(3000, false)}, 3 * time.Second},
		{"503 json takes the later Retry-After", reply{503, "8", nextRelative(3000, false)}, 8 * time.Second},
		{"409 is not retryable", reply{409, "9", `{"success":false,"errors":[{"code":"conflict","message":"x"}]}`}, 0},
		{"200 ignores Retry-After", reply{200, "5", plainQueue}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := replyServer(t, map[string]reply{keyQueue: tt.rep})
			// Retries off: this tests the delay of a single response; retries are in retry_test.go.
			c, fake := delayClient(t, srv, WithRateLimitRetries(0))
			_ = queueCall(c)()
			_ = assertDelayed(t, fake, c.genGroup, queueCall(c), tt.want)
		})
	}
}

func TestServerDelayMissingOrInvalidIsNoDelay(t *testing.T) {
	const head = `{"success":true,"job":{"id":"job-1","status":"waiting"},`
	tests := []struct {
		name    string
		rep     reply
		wantErr bool
	}{
		{"no rateLimit", reply{200, "", plainQueue}, false},
		{"next null", reply{200, "", head + `"rateLimit":{"next":null}}`}, false},
		{"negative relative", reply{200, "", head + `"rateLimit":{"next":{"relative":-5000}}}`}, false},
		{"string relative", reply{200, "", head + `"rateLimit":{"next":{"relative":"5000"}}}`}, false},
		{"fraction relative", reply{200, "", head + `"rateLimit":{"next":{"relative":1.5}}}`}, false},
		{"garbage Retry-After", reply{503, "soon", "<html>down</html>"}, true},
		{"garbage Retry-After on json error", reply{429, "soon", `{"success":false,"errors":[{"code":"c","message":"m"}]}`}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := replyServer(t, map[string]reply{keyQueue: tt.rep})
			// Retries off: this tests the delay of a single response; retries are in retry_test.go.
			c, fake := delayClient(t, srv, WithRateLimitRetries(0))
			if err := queueCall(c)(); (err != nil) != tt.wantErr {
				t.Fatalf("first error = %v, wantErr %v", err, tt.wantErr)
			}
			_ = assertDelayed(t, fake, c.genGroup, queueCall(c), 0)
		})
	}
}

func TestDeadlineAppliedBeforeRelease(t *testing.T) {
	srv := replyServer(t, map[string]reply{keyQueue: {200, "", nextRelative(5000, true)}}, keyQueue)
	c, fake := delayClient(t, srv, WithGenerateThrottle(oneSlot()))
	first := goCall(queueCall(c))
	waitCount(t, srv, keyQueue, 1)
	second := goCall(queueCall(c))
	waitWaiting(t, c.genGroup, 1)
	fake.Advance(delayTick) // the interval is over, only the slot holds the waiter back

	srv.open(keyQueue)
	if err := mustDone(t, "first", first); err != nil {
		t.Fatalf("first error = %v", err)
	}
	st := c.genGroup.Throttle.Stats()
	if st.Waiting != 1 || st.InFlight != 0 {
		t.Fatalf("after release: waiting=%d inFlight=%d, want 1 and 0 (the delay must be set before release)", st.Waiting, st.InFlight)
	}
	fake.Advance(5 * time.Second)
	if err := mustDone(t, "second", second); err != nil {
		t.Errorf("second error = %v", err)
	}
}
