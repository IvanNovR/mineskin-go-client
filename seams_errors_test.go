package mineskin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/poll"
	"github.com/IvanNovR/mineskin-go-client/internal/ratelimit"
	"github.com/IvanNovR/mineskin-go-client/internal/throttle"
	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// apiErrorFrom runs decodeResponse and returns the resulting *APIError.
func apiErrorFrom(t *testing.T, raw transport.Response) *APIError {
	t.Helper()
	_, err := decodeMeta[struct{}](raw)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	return apiErr
}

func TestAPIErrorCarriesRateLimitAndUsage(t *testing.T) {
	t.Parallel()
	body := `{"success":false,"errors":[{"code":"too_many_requests","message":"slow down"}],` +
		`"rateLimit":{"next":{"relative":3000}},"usage":{"credits":{"used":4,"remaining":6}}}`
	e := apiErrorFrom(t, transport.Response{Status: 429, Body: []byte(body)})
	assertRateLimit(t, e.RateLimit, RateLimit{NextIn: 3 * time.Second})
	if want := (Usage{CreditsUsed: 4, CreditsRemaining: 6}); e.Usage != want {
		t.Errorf("Usage = %+v, want %+v", e.Usage, want)
	}
}

func TestAPIErrorRateLimitLenient(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{"absent", `{"success":false,"errors":[{"code":"c","message":"m"}]}`},
		{"null", `{"success":false,"errors":[{"code":"c","message":"m"}],"rateLimit":null,"usage":null}`},
		{"string", `{"success":false,"errors":[{"code":"c","message":"m"}],"rateLimit":"x","usage":"y"}`},
		{"empty object", `{"success":false,"errors":[{"code":"c","message":"m"}],"rateLimit":{},"usage":{}}`},
		{"next without relative", `{"success":false,"errors":[{"code":"c","message":"m"}],"rateLimit":{"next":{}}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := apiErrorFrom(t, transport.Response{Status: 429, Body: []byte(tc.body)})
			if e.Code != "c" || e.Message != "m" {
				t.Errorf("code/message = %q/%q, want c/m", e.Code, e.Message)
			}
			if e.RateLimit != (RateLimit{}) || e.Usage != (Usage{}) {
				t.Errorf("RateLimit = %+v Usage = %+v, want zero values", e.RateLimit, e.Usage)
			}
		})
	}
}

func TestAPIErrorRetryAfter(t *testing.T) {
	t.Parallel()
	received := testNow
	tests := []struct {
		name   string
		header http.Header
		want   time.Duration
	}{
		{"seconds", http.Header{"Retry-After": {"7"}}, 7 * time.Second},
		{"absent", http.Header{}, 0},
		{"http date", http.Header{"Retry-After": {received.Add(20 * time.Second).Format(http.TimeFormat)}}, 20 * time.Second},
		{"garbage", http.Header{"Retry-After": {"soon"}}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := apiErrorFrom(t, transport.Response{Status: 429, Header: tc.header, Received: received, Body: []byte(`{"success":false}`)})
			if e.RetryAfter != tc.want {
				t.Errorf("RetryAfter = %v, want %v", e.RetryAfter, tc.want)
			}
		})
	}
}

func TestIsRetryableAPIError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status int
		want   bool
	}{{429, true}, {503, true}, {400, false}, {401, false}, {200, false}}
	for _, tc := range tests {
		e := apiErrorFrom(t, transport.Response{Status: tc.status, Body: []byte(`{"success":false}`)})
		if got := IsRetryable(e); got != tc.want {
			t.Errorf("status %d: IsRetryable() = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestErrorMessageStyle(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
			assertGoStyleMessage(t, tt.err)
			if tt.want != "" && tt.err.Error() != tt.want {
				t.Errorf("Error() = %q, want %q", tt.err, tt.want)
			}
		})
	}
}

func TestRetryCancelledMessageHasOnePrefix(t *testing.T) {
	t.Parallel()
	last := transport.Response{Status: 429, Header: http.Header{}, Body: []byte(`{"success":false,"errors":[{"code":"x","message":"slow down"}]}`)}
	err := mapGroupErr("GET /v2/x", &ratelimit.RetryCancelledError{Err: context.Canceled, Last: last})
	want := "mineskin: GET /v2/x: context canceled (while retrying after API error (HTTP 429, x): slow down)"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err, want)
	}
}

func TestPanickingPollIntervalWrapsAnErrorPanic(t *testing.T) {
	t.Parallel()
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

// TestNoInventedDefaults pins that a failure without a server-sent error has
// no made-up code or message.
func TestNoInventedDefaults(t *testing.T) {
	t.Parallel()
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

func apiErrorWithStatus(status int) *APIError {
	return &APIError{Meta: ResponseMeta{Status: status}}
}

func TestIsRetryable(t *testing.T) {
	t.Parallel()
	wrap := func(err error) error { return fmt.Errorf("mineskin: GET /v2/x: %w", err) }
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"invalid input", invalidInput("bad"), false},
		{"API 404", apiErrorWithStatus(404), false},
		{"API 408", apiErrorWithStatus(408), true},
		{"API 429", apiErrorWithStatus(429), true},
		{"API 500", apiErrorWithStatus(500), true},
		{"API 501", apiErrorWithStatus(501), false},
		{"API 503", apiErrorWithStatus(503), true},
		{"API 505", apiErrorWithStatus(505), false},
		{"wrapped API 503", wrap(apiErrorWithStatus(503)), true},
		{"unexpected 502", newUnexpectedResponseError(502, http.Header{}, nil, nil, testNow), true},
		{"unexpected 501", newUnexpectedResponseError(501, http.Header{}, nil, nil, testNow), false},
		{"unexpected 200 truncated", newUnexpectedResponseError(200, http.Header{}, []byte("{"), errors.New("eof"), testNow), false},
		{"net timeout", timeoutNetError{}, true},
		{"wrapped net timeout", wrap(timeoutNetError{}), true},
		{"net error without timeout", &net.OpError{Op: "dial", Err: errors.New("refused")}, false},
		{"context canceled", context.Canceled, false},
		{"wrapped context canceled", wrap(context.Canceled), false},
		// DeadlineExceeded implements Timeout() and must still not retry.
		{"context deadline exceeded", context.DeadlineExceeded, false},
		{"wrapped context deadline exceeded", wrap(context.DeadlineExceeded), false},
		{"client closed", ErrClientClosed, false},
		{"wrapped client closed", closedError("GET /v2/x"), false},
		{"ctx error beside a retryable 429", &retryCancelledError{ctxErr: wrap(context.Canceled), rateLimited: apiErrorWithStatus(429)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsRetryable(tt.err); got != tt.want {
				t.Errorf("IsRetryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestStatusSentinel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status int
		want   error
	}{{404, ErrNotFound}, {429, ErrRateLimited}, {200, nil}, {400, nil}, {500, nil}, {503, nil}}
	for _, tt := range tests {
		if got := statusSentinel(tt.status); got != tt.want { //nolint:errorlint // identity of a sentinel
			t.Errorf("statusSentinel(%d) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestStatusSentinelsThroughBothErrorTypes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status int
		is     error
		isNot  error
	}{{404, ErrNotFound, ErrRateLimited}, {429, ErrRateLimited, ErrNotFound}}
	for _, tt := range tests {
		errs := map[string]error{
			"APIError":                apiErrorWithStatus(tt.status),
			"UnexpectedResponseError": newUnexpectedResponseError(tt.status, http.Header{}, []byte("<html>"), nil, testNow),
			"wrapped":                 fmt.Errorf("mineskin: wrap: %w", apiErrorWithStatus(tt.status)),
		}
		for name, err := range errs {
			if !errors.Is(err, tt.is) || errors.Is(err, tt.isNot) || errors.Is(err, ErrInvalidInput) {
				t.Errorf("%s %d: Is(%v) = %v, Is(%v) = %v", name, tt.status, tt.is, errors.Is(err, tt.is), tt.isNot, errors.Is(err, tt.isNot))
			}
		}
	}
	if errors.Is(apiErrorWithStatus(500), ErrRateLimited) || errors.Is(apiErrorWithStatus(500), ErrNotFound) {
		t.Error("a 500 matches a status sentinel")
	}
}

func TestJobFailedError(t *testing.T) {
	t.Parallel()
	job := Job{ID: "job-9", Status: JobStatusFailed}
	errs := []CodeAndMessage{{Code: "invalid_image", Message: "bad image"}, {Code: "other", Message: "second"}}
	meta := ResponseMeta{Status: 200, Breadcrumb: "bc"}
	tests := []struct {
		name       string
		resp       *JobResponse
		wantErrors []CodeAndMessage
		wantText   string
	}{
		{"first error in the text", &JobResponse{Job: job, Meta: meta, errs: errs}, errs, "mineskin: job job-9 failed (invalid_image): bad image"},
		{"no errors", &JobResponse{Job: job, Meta: meta}, nil, "mineskin: job job-9 failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := newJobFailedError(tt.resp)
			if err.Error() != tt.wantText {
				t.Errorf("Error() = %q, want %q", err.Error(), tt.wantText)
			}
			if !reflect.DeepEqual(err.Errors, tt.wantErrors) {
				t.Errorf("Errors = %v, want %v", err.Errors, tt.wantErrors)
			}
			if err.Job != job || err.Meta.Breadcrumb != "bc" || err.Meta.Status != 200 {
				t.Errorf("Job = %+v Meta = %+v, want the response's", err.Job, err.Meta)
			}
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidInput) {
				t.Error("matches unrelated sentinel")
			}
			var target *JobFailedError
			if !errors.As(fmt.Errorf("wait: %w", err), &target) || target != err {
				t.Error("errors.As through %w failed")
			}
		})
	}
}

func TestInvalidInputError(t *testing.T) {
	t.Parallel()
	err := invalidInput("id %q must not be empty", "x")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("validation error must match ErrInvalidInput")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("validation error must not match ErrNotFound")
	}
	if got, want := err.Error(), `mineskin: invalid input: id "x" must not be empty`; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if errors.Unwrap(err) != ErrInvalidInput { //nolint:errorlint // identity: a plain %w wrap
		t.Fatalf("Unwrap() = %v, want ErrInvalidInput", errors.Unwrap(err))
	}
}
