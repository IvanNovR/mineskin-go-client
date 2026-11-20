package mineskin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/poll"
	"github.com/IvanNovR/mineskin-go-client/internal/ratelimit"
	"github.com/IvanNovR/mineskin-go-client/internal/throttle"
	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// TestSeamClassifiersAgreeWithDecodeResponse pins the neutral classifiers that
// internal/ratelimit uses (transport.IsFailure, transport.IsUnexpected) against
// the root's decodeResponse, which is the truth for the public errors.
func TestSeamClassifiersAgreeWithDecodeResponse(t *testing.T) {
	const bom = "\xef\xbb\xbf"
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"200 ok", 200, `{"success":true}`},
		{"200 empty object", 200, `{}`},
		{"429 empty object", 429, `{}`},
		{"429 api error", 429, `{"success":false,"errors":[{"code":"c","message":"m"}]}`},
		{"429 success true", 429, `{"success":true}`},
		{"BOM ok", 200, bom + `{"success":true}`},
		{"BOM api error", 429, bom + `{"success":false}`},
		{"success is a string", 200, `{"success":"x"}`},
		{"success is a string on 429", 429, `{"success":"x"}`},
		{"errors is a string", 429, `{"success":true,"errors":"x"}`},
		{"warnings malformed", 200, `{"success":true,"warnings":{}}`},
		{"messages malformed", 200, `{"success":true,"messages":[1]}`},
		{"html 429", 429, "<html>rate limited</html>"},
		{"html 502", 502, "<html>bad gateway</html>"},
		{"truncated JSON", 429, `{"success":fal`},
		{"empty body", 429, ""},
		{"array body", 200, `[]`},
		{"null body", 200, `null`},
		{"leading whitespace", 429, "\n  {\"success\":false}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := transport.Response{Status: tt.status, Header: http.Header{}, Body: []byte(tt.body), Received: pollerT0}
			_, err := decodeMeta[struct{}](r)
			if got, want := transport.IsFailure(r), err != nil; got != want {
				t.Errorf("IsFailure = %v, decodeResponse error = %v", got, err)
			}
			var ue *UnexpectedResponseError
			if got, want := transport.IsUnexpected(r), errors.As(err, &ue); got != want {
				t.Errorf("IsUnexpected = %v, decodeResponse yields *UnexpectedResponseError = %v", got, want)
			}
			if got, want := transport.IsRetryableStatus(tt.status), tt.status == 408 || tt.status == 429 || tt.status >= 500 && tt.status != 501 && tt.status != 505; got != want {
				t.Errorf("IsRetryableStatus = %v, want %v", got, want)
			}
			if ue != nil && IsRetryable(ue) != transport.IsRetryableStatus(tt.status) {
				t.Errorf("UnexpectedResponseError.IsRetryable = %v disagrees with transport", IsRetryable(ue))
			}
		})
	}
}

func TestSeamServerDelayAgreesWithRateLimitDecoding(t *testing.T) {
	bodies := []string{
		`{"rateLimit":{"next":{"relative":5000}}}`, `{"rateLimit":{"next":{"relative":-1}}}`,
		`{"rateLimit":{"next":{"relative":"5"}}}`, `{"rateLimit":{"next":{"relative":1.5}}}`,
		`{"rateLimit":{"next":null}}`, `{"rateLimit":"x"}`, `{}`, `<html>`, "\xef\xbb\xbf" + `{"rateLimit":{"next":{"relative":7}}}`,
		`{"rateLimit":{"next":{"relative":9223372036854775807}}}`,
	}
	for _, body := range bodies {
		r := transport.Response{Status: 200, Header: http.Header{}, Body: []byte(body)}
		var want time.Duration
		var wire struct {
			RateLimit *RateLimitInfo `json:"rateLimit"`
		}
		if err := json.Unmarshal(transport.TrimBOM(r.Body), &wire); err == nil && wire.RateLimit != nil && wire.RateLimit.Next != nil {
			want = wire.RateLimit.Next.Relative
		}
		if got := ratelimit.ServerDelay(r); got != want {
			t.Errorf("ServerDelay(%q) = %v, public decoding = %v", body, got, want)
		}
	}
}

func TestSeamMapGroupErr(t *testing.T) {
	const label = "GET /v2/x"
	boom := errors.New("boom")
	build := &transport.BuildError{Err: errors.New("bad url")}
	last := transport.Response{Status: 429, Header: http.Header{}, Body: []byte(`{"success":false}`)}

	tests := []struct {
		name  string
		err   error
		check func(t *testing.T, got error)
	}{
		{"throttle closed", throttle.ErrClosed, func(t *testing.T, got error) { mustIs(t, got, ErrClientClosed) }},
		{"tuner closed", ratelimit.ErrClosed, func(t *testing.T, got error) { mustIs(t, got, ErrClientClosed) }},
		{"ctx error is wrapped", context.Canceled, func(t *testing.T, got error) { mustIs(t, got, context.Canceled) }},
		{"send error is wrapped once with the label", boom, func(t *testing.T, got error) {
			mustIs(t, got, boom)
			if want := "mineskin: GET /v2/x: boom"; got.Error() != want {
				t.Errorf("error = %q, want %q", got, want)
			}
		}},
		{"unbuildable request is invalid input", build, func(t *testing.T, got error) {
			mustIs(t, got, ErrInvalidInput, build.Err)
			if strings.Count(got.Error(), "mineskin:") != 1 {
				t.Errorf("error = %q, want one prefix", got)
			}
		}},
		{"gate skip passes through", ratelimit.ErrSkipped, func(t *testing.T, got error) {
			if got != errCheckSkipped { //nolint:errorlint // identity
				t.Errorf("error = %v, want errCheckSkipped", got)
			}
		}},
		{"retry cancelled keeps ctx error and the 429", &ratelimit.RetryCancelledError{Err: context.Canceled, Last: last}, func(t *testing.T, got error) {
			mustIs(t, got, context.Canceled)
			var apiErr *APIError
			if !errors.As(got, &apiErr) {
				t.Errorf("error = %v, want *APIError in the chain", got)
			}
		}},
		{"retry cancelled without a 429 error is the ctx error alone", &ratelimit.RetryCancelledError{Err: context.Canceled, Last: transport.Response{Status: 200, Body: []byte(`{"success":true}`)}}, func(t *testing.T, got error) {
			var rc *retryCancelledError
			if errors.As(got, &rc) {
				t.Errorf("error = %v, want no retryCancelledError", got)
			}
			mustIs(t, got, context.Canceled)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.check(t, mapGroupErr(label, tt.err)) })
	}
}

func mustIs(t *testing.T, got error, targets ...error) {
	t.Helper()
	for _, target := range targets {
		if !errors.Is(got, target) {
			t.Errorf("error = %v, want errors.Is %v", got, target)
		}
	}
}

func TestSeamStartGateDeclinedRequestSendsNothing(t *testing.T) {
	var hits atomic.Int32
	srv := newTestServer(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	c := newTestClient(t, srv.URL)

	_, err := c.getJobGated(context.Background(), "j1", func() bool { return false })

	if !errors.Is(err, errCheckSkipped) {
		t.Errorf("err = %v, want errCheckSkipped", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("server hits = %d, want 0", n)
	}
}

func TestSeamJobSourceGetMapsSkippedGate(t *testing.T) {
	var hits atomic.Int32
	srv := newTestServer(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	c := newTestClient(t, srv.URL)

	_, _, err := jobSource{c}.Get(context.Background(), "j1", func() bool { return false })

	if err != poll.ErrSkipped { //nolint:errorlint // identity
		t.Errorf("err = %v, want poll.ErrSkipped", err)
	}
	if hits.Load() != 0 {
		t.Error("a declined gate sent a request")
	}
}

func TestSeamJobSourceGetDerivesState(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   poll.State
	}{
		{"waiting", "waiting", poll.State{ID: "j1"}},
		{"failed", "failed", poll.State{ID: "j1", Done: true, Failed: true}},
		{"completed with result", "completed", poll.State{ID: "j1", Done: true}},
		{"completed without result", "completed-no-result", poll.State{ID: "j1"}},
		{"unknown status", "unknown", poll.State{ID: "j1"}},
		{"unrecognised status", "weird", poll.State{ID: "j1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := jobJSON("j1", tt.status)
			if tt.status == "completed-no-result" {
				body = `{"success":true,"job":{"id":"j1","status":"completed"}}`
			}
			srv := newTestServer(t, writeJSON(200, body))
			c := newTestClient(t, srv.URL)

			resp, got, err := jobSource{c}.Get(context.Background(), "j1", nil)

			if err != nil || resp == nil || resp.Job.ID != "j1" {
				t.Fatalf("Get() = %v, %v", resp, err)
			}
			if got.ID != tt.want.ID || got.Done != tt.want.Done || got.Failed != tt.want.Failed {
				t.Errorf("state = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSeamJobSourceGetErrorPassesThrough(t *testing.T) {
	srv := newTestServer(t, writeJSON(404, `{"success":false,"errors":[{"code":"not_found","message":"no"}]}`))
	c := newTestClient(t, srv.URL)

	_, _, err := jobSource{c}.Get(context.Background(), "j1", nil)

	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestSeamJobSourceList(t *testing.T) {
	srv := newTestServer(t, writeJSON(200, `{"success":true,"jobs":[{"id":"a","status":"waiting"},{"id":"b","status":"failed"}]}`))
	c := newTestClient(t, srv.URL)

	got, err := jobSource{c}.List(context.Background())

	if err != nil || len(got) != 2 {
		t.Fatalf("List() = %v, %v", got, err)
	}
	if got[0].ID != "a" || got[0].Done || got[1].ID != "b" || !got[1].Failed || !got[1].Done {
		t.Errorf("states = %+v", got)
	}
}

func TestSeamJobSourceListError(t *testing.T) {
	srv := newTestServer(t, writeJSON(404, `{"success":false,"errors":[{"code":"not_found","message":"no"}]}`))
	c := newTestClient(t, srv.URL)

	if _, err := (jobSource{c}).List(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestSeamMapPollErr(t *testing.T) {
	plain := errors.New("plain")
	tests := []struct {
		name string
		in   error
		is   []error
		text string
	}{
		{"exhausted", &poll.ExhaustedError{ID: "j1", Attempts: 3}, []error{ErrMaxAttempts}, "job j1 after 3 attempts"},
		{"zero interval", &poll.IntervalError{Value: 0}, []error{ErrInvalidInput}, "poll interval must be greater than zero"},
		{"negative interval", &poll.IntervalError{Value: -time.Second}, []error{ErrInvalidInput}, ""},
		{"panicking interval", &poll.IntervalError{Panic: "boom"}, []error{}, "poll interval panicked: boom"},
		{"closed passes through", ErrClientClosed, []error{ErrClientClosed}, ""},
		{"other passes through", plain, []error{plain}, "plain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapPollErr(tt.in)
			for _, want := range tt.is {
				if !errors.Is(got, want) {
					t.Errorf("mapPollErr(%v) = %v, want errors.Is %v", tt.in, got, want)
				}
			}
			if tt.text != "" && !strings.Contains(got.Error(), tt.text) {
				t.Errorf("error %q lacks %q", got, tt.text)
			}
		})
	}
}

// TestSeamSourceValuesMatchTransport pins the string values the root casts
// from transport.Origin.Source to ResponseSource.
func TestSeamSourceValuesMatchTransport(t *testing.T) {
	pairs := []struct {
		name string
		raw  string
		want ResponseSource
	}{
		{"cloudflare", transport.SourceCloudflare, SourceCloudflare},
		{"nginx", transport.SourceNginx, SourceNginx},
		{"unknown", transport.SourceUnknown, SourceUnknown},
	}
	for _, p := range pairs {
		if ResponseSource(p.raw) != p.want {
			t.Errorf("%s: transport %q != public %q", p.name, p.raw, p.want)
		}
	}
}

// TestPollDebugLogsCarryPath pins the "path" attribute of the poller debug
// logs, which the client passes to internal/poll as opaque attributes.
func TestPollDebugLogsCarryPath(t *testing.T) {
	var buf bytes.Buffer
	srv := newTestServer(t, writeJSON(200, jobJSON("j1", "completed")))
	c := newTestClient(t, srv.URL, WithLogger(captureLogger(&buf)), pollOnce(3, func(int) time.Duration { return time.Millisecond }))

	if _, err := c.WaitForJob(waitCtx(t), Job{ID: "j1"}); err != nil {
		t.Fatalf("WaitForJob() error = %v", err)
	}

	if got := buf.String(); !strings.Contains(got, `msg="polling job"`) || !strings.Contains(got, "path=/v2/queue/{id}") {
		t.Errorf("log lacks the polling job path attribute:\n%s", got)
	}
}
