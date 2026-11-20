package ratelimit

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
	"github.com/IvanNovR/mineskin-go-client/internal/throttle"
	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// groupT0 is near the real now, because context deadlines use the real clock.
var groupT0 = time.Now().Truncate(time.Second)

const (
	okBody        = `{"success":true}`
	rateLimitJSON = `{"success":false,"errors":[{"code":"too_many_requests","message":"slow down"}]}`
	gatewayHTML   = "<html>bad gateway</html>"
)

// newTestGroup builds a group on a fake clock with a wide-open throttle.
func newTestGroup(retries int, limits throttle.Limits) (*Group, *clock.Fake) {
	fake := clock.NewFake(groupT0)
	return &Group{
		Name:     "test",
		Throttle: throttle.New(fake, func(time.Time) throttle.Limits { return limits }),
		Retries:  retries,
		Clock:    fake,
	}, fake
}

func resp(fake *clock.Fake, status int, body string) transport.Response {
	return transport.Response{Status: status, Header: http.Header{}, Body: []byte(body), Received: fake.Now()}
}

// sequence returns a send func that answers with the given responses in
// order and counts its calls.
func sequence(calls *atomic.Int32, rs ...transport.Response) func(context.Context) (transport.Response, error) {
	return func(context.Context) (transport.Response, error) {
		i := int(calls.Add(1)) - 1
		return rs[min(i, len(rs)-1)], nil
	}
}

var openLimits = throttle.Limits{Concurrency: 5}

func TestDoReturnsResponseAndFreesSlot(t *testing.T) {
	g, fake := newTestGroup(0, openLimits)
	want := resp(fake, 200, okBody)
	got, err := g.Do(context.Background(), func(context.Context) (transport.Response, error) { return want, nil }, nil)
	if err != nil || got.Status != 200 || string(got.Body) != okBody {
		t.Fatalf("Do() = %+v, %v", got, err)
	}
	if st := g.Throttle.Stats(); st.InFlight != 0 {
		t.Errorf("in flight = %d, want 0", st.InFlight)
	}
}

func TestDoStartGate(t *testing.T) {
	g, fake := newTestGroup(2, openLimits)
	var sent atomic.Int32
	_, err := g.Do(context.Background(), sequence(&sent, resp(fake, 200, okBody)), func() bool { return false })
	if !errors.Is(err, ErrSkipped) {
		t.Fatalf("error = %v, want ErrSkipped", err)
	}
	if sent.Load() != 0 {
		t.Error("a skipped request was sent")
	}
	if st := g.Throttle.Stats(); st.InFlight != 0 {
		t.Errorf("in flight = %d, want 0 (the slot must be freed)", st.InFlight)
	}
}

func TestDoGateIsCheckedOnEveryAttempt(t *testing.T) {
	g, fake := newTestGroup(2, openLimits)
	var sent, gates atomic.Int32
	proceed := func() bool { return gates.Add(1) == 1 } // allows the first attempt only
	_, err := g.Do(context.Background(), sequence(&sent, resp(fake, 429, rateLimitJSON)), proceed)
	if !errors.Is(err, ErrSkipped) || sent.Load() != 1 || gates.Load() != 2 {
		t.Errorf("error = %v, sent = %d, gate calls = %d; want ErrSkipped, 1, 2", err, sent.Load(), gates.Load())
	}
}

func TestDoReturnsSendErrorUnchangedAndFreesSlot(t *testing.T) {
	g, _ := newTestGroup(2, throttle.Limits{Concurrency: 1})
	boom := errors.New("boom")
	for i := range 3 {
		_, err := g.Do(context.Background(), func(context.Context) (transport.Response, error) { return transport.Response{}, boom }, nil)
		if err != boom { //nolint:errorlint // identity: the error must pass through unwrapped
			t.Fatalf("call %d error = %v, want the same error value", i, err)
		}
	}
}

func TestDoThrottleErrorsPassThrough(t *testing.T) {
	g, fake := newTestGroup(0, openLimits)
	g.Throttle.Close()
	_, err := g.Do(context.Background(), sequence(new(atomic.Int32), resp(fake, 200, okBody)), nil)
	if !errors.Is(err, throttle.ErrClosed) {
		t.Errorf("error = %v, want throttle.ErrClosed", err)
	}
	g2, _ := newTestGroup(0, openLimits)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g2.Do(ctx, nil, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestDoSetsServerDelayBeforeRelease(t *testing.T) {
	g, fake := newTestGroup(0, openLimits)
	r := resp(fake, 200, `{"success":true,"rateLimit":{"next":{"relative":5000}}}`)
	if _, err := g.Do(context.Background(), func(context.Context) (transport.Response, error) { return r, nil }, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := g.Throttle.EarliestStart(), fake.Now().Add(5*time.Second); !got.Equal(want) {
		t.Errorf("earliest start = %v, want %v", got, want)
	}
}

func TestDoRetries429(t *testing.T) {
	tests := []struct {
		name      string
		retries   int
		first     string
		wantSends int32
		wantOK    bool
	}{
		{"json 429 recovers", 2, rateLimitJSON, 2, true},
		{"html 429 recovers", 2, gatewayHTML, 2, true},
		{"retries off", 0, rateLimitJSON, 1, false},
		{"not a failure is not retried", 2, okBody, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, fake := newTestGroup(tt.retries, openLimits)
			var sent atomic.Int32
			send := sequence(&sent, resp(fake, 429, tt.first), resp(fake, 200, okBody))
			got, err := g.Do(context.Background(), send, nil)
			if err != nil || sent.Load() != tt.wantSends {
				t.Fatalf("error = %v, sends = %d, want %d", err, sent.Load(), tt.wantSends)
			}
			if (got.Status == 200) != tt.wantOK {
				t.Errorf("final status = %d, want 200 = %v", got.Status, tt.wantOK)
			}
		})
	}
}

func TestDoGivesUpAfterConfiguredRetries(t *testing.T) {
	g, fake := newTestGroup(2, openLimits)
	var sent atomic.Int32
	got, err := g.Do(context.Background(), sequence(&sent, resp(fake, 429, rateLimitJSON)), nil)
	if err != nil || got.Status != 429 || sent.Load() != 3 {
		t.Errorf("status = %d, sends = %d, err = %v; want the last 429 after 3 sends", got.Status, sent.Load(), err)
	}
}

func TestDoNeverRetriesOtherStatuses(t *testing.T) {
	for _, status := range []int{400, 404, 500, 503} {
		g, fake := newTestGroup(2, openLimits)
		var sent atomic.Int32
		if _, err := g.Do(context.Background(), sequence(&sent, resp(fake, status, rateLimitJSON)), nil); err != nil || sent.Load() != 1 {
			t.Errorf("status %d: sends = %d, err = %v, want 1 send", status, sent.Load(), err)
		}
	}
}

func TestDoReturns429WhenDelayEndsAfterCtxDeadline(t *testing.T) {
	g, fake := newTestGroup(2, openLimits)
	// The 2 h server delay is capped at 1 h, so the deadline must lie before that.
	ctx, cancel := context.WithDeadline(context.Background(), fake.Now().Add(30*time.Minute))
	defer cancel()
	var sent atomic.Int32
	body := `{"success":false,"rateLimit":{"next":{"relative":7200000}}}`
	got, err := g.Do(ctx, sequence(&sent, resp(fake, 429, body)), nil)
	if err != nil || got.Status != 429 || sent.Load() != 1 {
		t.Errorf("status = %d, sends = %d, err = %v; want the original 429, no retry", got.Status, sent.Load(), err)
	}
}

func TestDoCtxEndingDuringRetryReturnsRetryCancelled(t *testing.T) {
	g, fake := newTestGroup(2, openLimits)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sent atomic.Int32
	first := resp(fake, 429, rateLimitJSON)
	send := func(ctx context.Context) (transport.Response, error) {
		if sent.Add(1) == 1 {
			return first, nil
		}
		cancel()
		return transport.Response{}, ctx.Err()
	}
	_, err := g.Do(ctx, send, nil)
	var rc *RetryCancelledError
	if !errors.As(err, &rc) {
		t.Fatalf("error = %v, want *RetryCancelledError", err)
	}
	if !errors.Is(err, context.Canceled) || rc.Last.Status != 429 || string(rc.Last.Body) != rateLimitJSON {
		t.Errorf("error = %v, last = %+v; want context.Canceled and the 429", err, rc.Last)
	}
}

func TestDoFirstAttemptCancelIsNotRetryCancelled(t *testing.T) {
	g, _ := newTestGroup(2, openLimits)
	ctx, cancel := context.WithCancel(context.Background())
	send := func(ctx context.Context) (transport.Response, error) {
		cancel()
		return transport.Response{}, ctx.Err()
	}
	_, err := g.Do(ctx, send, nil)
	var rc *RetryCancelledError
	if errors.As(err, &rc) || !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want a plain context.Canceled", err)
	}
}

func TestDoPenaltyIsPerResponse(t *testing.T) {
	tests := []struct {
		name      string
		retries   int
		responses []transport.Response
		penalties int
	}{
		{"retried html 429 and final 200", 2, []transport.Response{{Status: 429, Body: []byte(gatewayHTML)}, {Status: 200, Body: []byte(okBody)}}, 1},
		{"html 429 retried twice and exhausted", 2, []transport.Response{{Status: 429, Body: []byte(gatewayHTML)}}, 3},
		{"html 502 final", 2, []transport.Response{{Status: 502, Body: []byte(gatewayHTML)}}, 1},
		{"json 429 never penalises", 2, []transport.Response{{Status: 429, Body: []byte(rateLimitJSON)}}, 0},
		{"html 409 is not retryable", 2, []transport.Response{{Status: 409, Body: []byte(gatewayHTML)}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, fake := newTestGroup(tt.retries, throttle.Limits{Concurrency: 5})
			var kicks atomic.Int32
			tn := NewTuner(TunerConfig{Clock: fake, BaseCtx: context.Background(), BG: new(syncWG),
				Load: func(context.Context) (Grants, error) { return Grants{}, nil }})
			tn.SetKick(func() { kicks.Add(1) })
			g.Tuner = tn
			g.Throttle = throttle.New(fake, tn.Limits)
			var sent atomic.Int32
			rs := make([]transport.Response, len(tt.responses))
			for i, r := range tt.responses {
				r.Header, r.Received = http.Header{}, fake.Now()
				rs[i] = r
			}
			done := make(chan error, 1)
			go func() { _, err := g.Do(context.Background(), sequence(&sent, rs...), nil); done <- err }()
			advanceUntil(t, fake, done)
			// the grants load itself kicks once
			if got := int(kicks.Load()) - 1; got != tt.penalties {
				t.Errorf("penalties = %d, want %d", got, tt.penalties)
			}
		})
	}
}

func TestDoLogsOneDebugLinePerRetry(t *testing.T) {
	g, fake := newTestGroup(2, openLimits)
	var buf bytes.Buffer
	g.Logger = captureLogger(&buf)
	var sent atomic.Int32
	if _, err := g.Do(context.Background(), sequence(&sent, resp(fake, 429, rateLimitJSON), resp(fake, 200, okBody)), nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(buf.String(), "retrying rate-limited request"); got != 1 || !strings.Contains(buf.String(), "group=test") {
		t.Errorf("log lines = %d, log:\n%s", got, buf.String())
	}
}

func TestDoCapsServerDelayAndGroupRecovers(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		header string
		body   string
	}{
		{"Retry-After", 429, "99999999", rateLimitJSON},
		{"next.relative", 200, "", `{"success":true,"rateLimit":{"next":{"relative":9223372036854}}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g, fake := newTestGroup(0, openLimits)
			r := resp(fake, tt.status, tt.body)
			if tt.header != "" {
				r.Header.Set(transport.HeaderRetryAfter, tt.header)
			}
			if _, err := g.Do(context.Background(), func(context.Context) (transport.Response, error) { return r, nil }, nil); err != nil {
				t.Fatal(err)
			}
			if got, want := g.Throttle.EarliestStart(), fake.Now().Add(time.Hour); !got.Equal(want) {
				t.Errorf("earliest start = %v, want exactly one hour ahead (%v)", got, want)
			}
			fake.Advance(time.Hour)
			if got := g.Throttle.EarliestStart(); got.After(fake.Now()) {
				t.Errorf("earliest start = %v after Advance(1h), want not after now (%v)", got, fake.Now())
			}
		})
	}
}
