package mineskin

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	malformedMsg = "MineSkin response field malformed, ignored"
	// garbled is a distinctive value that must never appear in a log line.
	garbled = "ZZgarbledZZ"
)

// malformedLines returns the log lines that report dropped fields.
func malformedLines(buf *bytes.Buffer) []string {
	var out []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, malformedMsg) {
			out = append(out, line)
		}
	}
	return out
}

func assertOneMalformedLine(t *testing.T, buf *bytes.Buffer, wantParts ...string) {
	t.Helper()
	lines := malformedLines(buf)
	if len(lines) != 1 {
		t.Fatalf("malformed log lines = %d, want 1:\n%s", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "level=WARN") {
		t.Errorf("line = %s, want level WARN", lines[0])
	}
	for _, part := range wantParts {
		if !strings.Contains(lines[0], part) {
			t.Errorf("line = %s, want it to contain %q", lines[0], part)
		}
	}
	if strings.Contains(buf.String(), garbled) {
		t.Errorf("log contains the malformed value %q:\n%s", garbled, buf.String())
	}
}

func TestQueueMalformedUsageIsLoggedOnceWithoutValue(t *testing.T) {
	var buf bytes.Buffer
	body := `{"success":true,"job":{"id":"j","status":"waiting"},"usage":{"credits":{"used":1,"remaining":"` + garbled + `"}}}`
	srv := newTestServer(t, writeJSON(200, body))
	c := newTestClient(t, srv.URL, WithLogger(captureLogger(&buf)))

	got, err := c.Queue(context.Background(), FromURL("https://example.com/a.png"), nil)

	if err != nil {
		t.Fatalf("Queue() error = %v", err)
	}
	if want := (Usage{CreditsUsed: 1}); got.Usage != want {
		t.Errorf("Usage = %+v, want %+v", got.Usage, want)
	}
	assertOneMalformedLine(t, &buf, "path=/v2/queue", "usage.credits.remaining")
}

func TestGenerateMalformedRateLimitIsLogged(t *testing.T) {
	var buf bytes.Buffer
	body := `{"success":true,"skin":{"name":"n"},"rateLimit":{"next":{"relative":"` + garbled + `"},"limit":{"limit":5}}}`
	srv := newTestServer(t, writeJSON(200, body))
	c := newTestClient(t, srv.URL, WithLogger(captureLogger(&buf)))

	got, err := c.Generate(context.Background(), FromURL("https://example.com/a.png"), nil)

	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	assertRateLimit(t, got.RateLimit, RateLimit{Limit: 5})
	assertOneMalformedLine(t, &buf, "path=/v2/generate", "rateLimit.next.relative")
}

func TestMalformedAbsentFieldsAreNotLogged(t *testing.T) {
	var buf bytes.Buffer
	bodies := []string{
		`{"success":true,"job":{"id":"j","status":"waiting"}}`,
		`{"success":true,"job":{"id":"j","status":"waiting"},"rateLimit":null,"usage":null}`,
		`{"success":true,"job":{"id":"j","status":"waiting"},"rateLimit":{},"usage":{"credits":{}}}`,
		`{"success":true,"job":{"id":"j","status":"waiting"},"rateLimit":{"next":{"relative":500}},"usage":{"credits":{"used":1,"remaining":2}}}`,
	}
	for _, body := range bodies {
		srv := newTestServer(t, writeJSON(200, body))
		c := newTestClient(t, srv.URL, WithLogger(captureLogger(&buf)))
		if _, err := c.Queue(context.Background(), FromURL("https://example.com/a.png"), nil); err != nil {
			t.Fatalf("Queue(%s) error = %v", body, err)
		}
	}
	if lines := malformedLines(&buf); len(lines) != 0 {
		t.Errorf("unexpected malformed log lines:\n%s", strings.Join(lines, "\n"))
	}
}

func TestAPIErrorMalformedDataAndRetryAfterAreLoggedOnce(t *testing.T) {
	var buf bytes.Buffer
	body := `{"success":false,"errors":[{"code":"c","message":"m"}],"rateLimit":"` + garbled + `","usage":{"credits":5}}`
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", garbled)
		writeJSON(400, body)(w, r)
	})
	c := newTestClient(t, srv.URL, WithLogger(captureLogger(&buf)))

	_, err := c.GetSkin(context.Background(), testSkinUUID)

	apiErr := asAPIError(t, err)
	if apiErr.Code != "c" || apiErr.RetryAfter != 0 || apiErr.RateLimit != (RateLimit{}) || apiErr.Usage != (Usage{}) {
		t.Errorf("apiErr = %+v, want the original error with dropped data", apiErr)
	}
	assertOneMalformedLine(t, &buf, "rateLimit", "usage.credits", "Retry-After")
}

func TestAPIErrorGarbledRetryAfterIsLogged(t *testing.T) {
	var buf bytes.Buffer
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", garbled)
		writeJSON(429, `{"success":false}`)(w, r)
	})
	c := newTestClient(t, srv.URL, WithLogger(captureLogger(&buf)), WithRateLimitRetries(0))

	_, err := c.GetSkin(context.Background(), testSkinUUID)

	if apiErr := asAPIError(t, err); apiErr.RetryAfter != 0 {
		t.Errorf("RetryAfter = %v, want 0", apiErr.RetryAfter)
	}
	assertOneMalformedLine(t, &buf, "fields=Retry-After")
}

func TestAPIErrorValidOrAbsentRetryAfterIsNotLogged(t *testing.T) {
	var buf bytes.Buffer
	for _, header := range []string{"", "7", time.Now().UTC().Add(time.Hour).Format(http.TimeFormat), "-3"} {
		srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			if header != "" {
				w.Header().Set("Retry-After", header)
			}
			writeJSON(400, `{"success":false}`)(w, r)
		})
		c := newTestClient(t, srv.URL, WithLogger(captureLogger(&buf)))
		if _, err := c.GetSkin(context.Background(), testSkinUUID); err == nil {
			t.Fatal("GetSkin() error = nil, want an API error")
		}
	}
	if lines := malformedLines(&buf); len(lines) != 0 {
		t.Errorf("unexpected malformed log lines:\n%s", strings.Join(lines, "\n"))
	}
}

func TestUnexpectedResponseGarbledRetryAfterIsLogged(t *testing.T) {
	var buf bytes.Buffer
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", garbled)
		writeJSON(503, `<html>unavailable</html>`)(w, r)
	})
	c := newTestClient(t, srv.URL, WithLogger(captureLogger(&buf)))

	_, err := c.GetSkin(context.Background(), testSkinUUID)

	var ue *UnexpectedResponseError
	if !asError(err, &ue) || ue.RetryAfter != 0 {
		t.Fatalf("err = %v, want *UnexpectedResponseError without RetryAfter", err)
	}
	assertOneMalformedLine(t, &buf, "fields=Retry-After")
}

// asAPIError returns the *APIError in err's chain or fails the test.
func asAPIError(t *testing.T, err error) *APIError {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	return apiErr
}

// asError is errors.As with the result only.
func asError[T error](err error, target *T) bool { return errors.As(err, target) }
