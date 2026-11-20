// Helpers of the external (package mineskin_test) tests. Test helpers cannot be
// shared with the internal tests of helpers_test.go, so the few generic ones
// they need are repeated here; keep both copies in step.
package mineskin_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client"
)

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
	var apiErr *mineskin.APIError
	var ue *mineskin.UnexpectedResponseError
	if errors.As(err, &apiErr) || errors.As(err, &ue) {
		t.Errorf("error %v is an API or unexpected-response error, want a plain parse error", err)
	}
	if errors.Is(err, mineskin.ErrInvalidInput) || errors.Is(err, mineskin.ErrNotFound) {
		t.Errorf("error %v matches ErrInvalidInput or ErrNotFound", err)
	}
	assertGoStyleMessage(t, err)
}

// fastPolling makes jobs poll every millisecond with a 1 ms first delay.
func fastPolling() mineskin.Option {
	return mineskin.WithJobPolling(mineskin.PollOptions{
		InitialDelay: time.Millisecond,
		Interval:     mineskin.PollIntervalFunc(func(int) time.Duration { return time.Millisecond }),
		MaxAttempts:  1000,
	})
}

// fixtureUUID parses s or panics; for test fixtures only.
func fixtureUUID(s string) mineskin.UUID {
	id, err := mineskin.ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return id
}

func jobJSON(id, status string) string {
	if status == "completed" {
		return `{"success":true,"job":{"id":"` + id + `","status":"completed","result":"` + jobResultID + `"}}`
	}
	return `{"success":true,"job":{"id":"` + id + `","status":"` + status + `"}}`
}

const jobResultID = "744c993e-0e8a-4f0b-9b6a-2f3c7a1b5d11"

// newTestClient builds a Client against baseURL; opts are applied after the
// defaults so they can override them.
func newTestClient(t *testing.T, baseURL string, opts ...mineskin.Option) *mineskin.Client {
	t.Helper()
	all := append([]mineskin.Option{mineskin.WithBaseURL(baseURL), mineskin.WithUserAgent("test/1.0"), mineskin.FastThrottles()}, opts...)
	c, err := mineskin.NewClient(all...)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return c
}

// newTestServer starts an httptest server that is closed when the test ends.
func newTestServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// pollOnce is a fast polling configuration with the given interval function.
func pollOnce(attempts int, interval func(int) time.Duration) mineskin.Option {
	return mineskin.WithJobPolling(mineskin.PollOptions{InitialDelay: time.Millisecond, MaxAttempts: attempts, Interval: mineskin.PollIntervalFunc(interval)})
}

const queueFixture = `{"success":true,"job":{"id":"job-1","status":"waiting","timestamp":1700000000000},
"rateLimit":{"next":{"relative":500},"limit":{"limit":10,"remaining":9,
"hour":{"limit":100,"remaining":99,"used":1,"reset":1700003600}}},
"usage":{"credits":{"used":2,"remaining":98}}}`

const skinFixture = `{"success":true,"skin":{"uuid":"744c993e0e8a4f0b9b6a2f3c7a1b5d11","name":"Steve","variant":"slim","visibility":"unlisted",
"texture":{"data":{"value":"VAL","signature":"SIG"},"hash":{"skin":"sh","cape":"ch"},"url":{"skin":"su","cape":"cu"}},
"generator":{"timestamp":1700000000123,"duration":1500},"views":7,"duplicate":true}}`

const testSkinID = "744c993e-0e8a-4f0b-9b6a-2f3c7a1b5d11"

// testSkinUUID is testSkinID parsed.
var testSkinUUID = fixtureUUID(testSkinID)

// waitCtx returns a context that ends after waitTimeout.
func waitCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout(t))
	t.Cleanup(cancel)
	return ctx
}

// writeJSON replies with status and the fixed JSON body.
func writeJSON(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

const messagePrefix = "mineskin: "

// waitTimeout is the single bound for blocking waits: fallbackWaitTimeout,
// shortened to fit before the test deadline when there is one.
func waitTimeout(t *testing.T) time.Duration {
	t.Helper()
	limit := fallbackWaitTimeout
	if dl, ok := t.Deadline(); ok {
		limit = min(limit, time.Until(dl)-deadlineMargin)
	}
	return max(limit, time.Millisecond)
}

const (
	// fallbackWaitTimeout bounds every blocking wait of a test.
	fallbackWaitTimeout = 20 * time.Second
	// deadlineMargin is kept free before the test binary's own deadline so a
	// stuck wait fails with a goroutine dump instead of a bare panic.
	deadlineMargin = 2 * time.Second
)

func assertRateLimit(t *testing.T, got, want mineskin.RateLimit) {
	t.Helper()
	if !got.NextAt.Equal(want.NextAt) || got.NextIn != want.NextIn || got.Delay != want.Delay ||
		got.Limit != want.Limit || got.Remaining != want.Remaining {
		t.Errorf("RateLimit = %+v, want %+v", got, want)
	}
	assertWindow(t, "Minute", got.Minute, want.Minute)
	assertWindow(t, "Hour", got.Hour, want.Hour)
}

func assertWindow(t *testing.T, name string, got, want mineskin.WindowLimit) {
	t.Helper()
	if got.Limit != want.Limit || got.Remaining != want.Remaining || got.Used != want.Used || !got.Reset.Equal(want.Reset) {
		t.Errorf("%s = %+v, want %+v", name, got, want)
	}
}
