package mineskin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/poll"
	"github.com/IvanNovR/mineskin-go-client/internal/ratelimit"
	"github.com/IvanNovR/mineskin-go-client/internal/throttle"
	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

const messagePrefix = "mineskin: "

// assertGoStyleMessage checks the error-string rules of the package: exactly
// one "mineskin:" prefix, at the start, and text that starts in lower case
// (an acronym such as API or HTTP is fine; API-provided text is verbatim and
// is never the first word after the prefix in these cases).
func assertGoStyleMessage(t *testing.T, err error) {
	t.Helper()
	msg := err.Error()
	if !strings.HasPrefix(msg, messagePrefix) {
		t.Errorf("error %q lacks the %q prefix", msg, messagePrefix)
		return
	}
	if n := strings.Count(msg, messagePrefix); n != 1 {
		t.Errorf("error %q has %d prefixes, want 1", msg, n)
	}
	first := msg[len(messagePrefix)]
	word, _, _ := strings.Cut(msg[len(messagePrefix):], " ")
	isAcronym := word == strings.ToUpper(word)
	if first >= 'A' && first <= 'Z' && !isAcronym {
		t.Errorf("error %q starts with a capitalised word", msg)
	}
}

// assertParseError checks a plain client-side parse failure: not an API or
// unexpected-response error, not a classification sentinel, Go-style text.
func assertParseError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want a parse error")
	}
	var apiErr *APIError
	var ue *UnexpectedResponseError
	if errors.As(err, &apiErr) || errors.As(err, &ue) {
		t.Errorf("error %v is an API or unexpected-response error, want a plain parse error", err)
	}
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrNotFound) {
		t.Errorf("error %v matches ErrInvalidInput or ErrNotFound", err)
	}
	assertGoStyleMessage(t, err)
}

func TestErrorMessageStyle(t *testing.T) {
	boom := errors.New("boom")
	last := transport.Response{Status: 429, Header: http.Header{}, Body: []byte(`{"success":false,"errors":[{"code":"x","message":"Slow Down"}]}`)}
	htmlLast := transport.Response{Status: 429, Header: http.Header{}, Body: []byte("<html>slow</html>")}
	tests := []struct {
		name string
		err  error
		want string // exact text; empty skips the exact check
	}{
		{"API error verbatim", &APIError{Code: "not_found", Message: "No Such Skin", Meta: ResponseMeta{Status: 404}}, "mineskin: API error (HTTP 404, not_found): No Such Skin"},
		{"unexpected response", newUnexpectedResponseError(502, http.Header{}, []byte("bad gateway"), nil, testNow), "mineskin: HTTP 502: bad gateway"},
		{"unexpected response empty body", newUnexpectedResponseError(502, http.Header{}, nil, nil, testNow), "mineskin: HTTP 502: empty response body"},
		{"job failed", &JobFailedError{Job: Job{ID: "j1"}, Errors: []CodeAndMessage{{Code: "bad_image", Message: "Nope"}}}, "mineskin: job j1 failed (bad_image): Nope"},
		{"validation", invalidInput("id is empty"), "mineskin: invalid input: id is empty"},
		{"closed sentinel", ErrClientClosed, "mineskin: client is closed"},
		{"group closed", mapGroupErr("GET /v2/x", throttle.ErrClosed), "mineskin: client is closed: GET /v2/x"},
		{"tuner closed", mapGroupErr("GET /v2/x", ratelimit.ErrClosed), "mineskin: client is closed: GET /v2/x"},
		{"group ctx error", mapGroupErr("GET /v2/x", context.Canceled), "mineskin: GET /v2/x: context canceled"},
		{"group transport error", mapGroupErr("GET /v2/x", boom), "mineskin: GET /v2/x: boom"},
		{"group build error", mapGroupErr("GET /v2/x", &transport.BuildError{Err: boom}), "mineskin: invalid input: GET /v2/x: boom"},
		{"retry cancelled with API error", mapGroupErr("GET /v2/x", &ratelimit.RetryCancelledError{Err: context.Canceled, Last: last}), ""},
		{"retry cancelled with HTML error", mapGroupErr("GET /v2/x", &ratelimit.RetryCancelledError{Err: context.Canceled, Last: htmlLast}), ""},
		{"poll exhausted", mapPollErr(&poll.ExhaustedError{ID: "j1", Attempts: 3}), "mineskin: job polling reached max attempts: job j1 after 3 attempts"},
		{"poll interval zero", mapPollErr(&poll.IntervalError{}), "mineskin: invalid input: poll interval must be greater than zero"},
		{"poll interval panic text", mapPollErr(&poll.IntervalError{Panic: "kaboom"}), "mineskin: poll interval panicked: kaboom"},
		{"poll interval panic error", mapPollErr(&poll.IntervalError{Panic: boom}), "mineskin: poll interval panicked: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertGoStyleMessage(t, tt.err)
			if tt.want != "" && tt.err.Error() != tt.want {
				t.Errorf("Error() = %q, want %q", tt.err, tt.want)
			}
		})
	}
}

func TestRetryCancelledMessageHasOnePrefix(t *testing.T) {
	last := transport.Response{Status: 429, Header: http.Header{}, Body: []byte(`{"success":false,"errors":[{"code":"x","message":"slow down"}]}`)}
	err := mapGroupErr("GET /v2/x", &ratelimit.RetryCancelledError{Err: context.Canceled, Last: last})
	want := "mineskin: GET /v2/x: context canceled (while retrying after API error (HTTP 429, x): slow down)"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err, want)
	}
}

func TestPanickingPollIntervalWrapsAnErrorPanic(t *testing.T) {
	cause := fmt.Errorf("interval bug: %w", context.DeadlineExceeded)
	got := mapPollErr(&poll.IntervalError{Panic: cause})
	if !errors.Is(got, cause) || !errors.Is(got, context.DeadlineExceeded) {
		t.Errorf("mapPollErr(error panic) = %v, want it to wrap the panic error", got)
	}
	plain := mapPollErr(&poll.IntervalError{Panic: "text"})
	if errors.Unwrap(plain) != nil {
		t.Errorf("a non-error panic must not wrap anything, got %v", errors.Unwrap(plain))
	}
}

func TestEndToEndMessagesHaveOnePrefix(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv := newTestServer(t, writeJSON(http.StatusOK, jobJSON("j1", "waiting")))
	c := newTestClient(t, srv.URL, pollOnce(1, func(int) time.Duration { return time.Millisecond }))
	_, err := c.WaitForJob(ctx, Job{ID: "j1"})
	assertGoStyleMessage(t, err)
	_, err = c.GetUser(ctx)
	assertGoStyleMessage(t, err)
	_ = c.Close()
	_, err = c.GetUser(context.Background())
	assertGoStyleMessage(t, err)
	_, err = c.WaitForJob(context.Background(), Job{ID: "j1"})
	assertGoStyleMessage(t, err)
}

// TestNoInventedDefaults pins that a failure without a server-sent error has
// no made-up code or message.
func TestNoInventedDefaults(t *testing.T) {
	err := newJobFailedError(&JobResponse{Job: Job{ID: "j1"}})
	if len(err.Errors) != 0 || err.Error() != "mineskin: job j1 failed" {
		t.Errorf("job failure = %+v (%q), want no errors and %q", err, err.Error(), "mineskin: job j1 failed")
	}
	_, herr := decodeMeta[struct{}](transport.Response{Status: 400, Body: []byte(`{"success":false}`)})
	var apiErr *APIError
	if !errors.As(herr, &apiErr) || apiErr.Code != "" || apiErr.Message != "" || apiErr.Error() != "mineskin: API error (HTTP 400)" {
		t.Errorf("API error = %+v, want empty Code and Message and text %q", apiErr, "mineskin: API error (HTTP 400)")
	}
}
