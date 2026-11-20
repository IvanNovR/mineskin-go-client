package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

const testKey = "msk_k_secret"

var epoch = time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

func newDoer(t *testing.T, h http.HandlerFunc) *Doer {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Doer{
		BaseURL: srv.URL, UserAgent: "test/1.0", APIKey: NewSecret(testKey),
		HTTP: srv.Client(), Clock: clock.NewFake(epoch),
	}
}

func TestDoSendsHeadersAndPath(t *testing.T) {
	var got *http.Request
	var gotBody []byte
	d := newDoer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{}`)
	})
	body := &Body{ContentType: "application/json", Data: []byte(`{"a":1}`)}
	resp, err := d.Do(context.Background(), http.MethodPost, "/v2/skins/a%2Fb", body)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if got.URL.EscapedPath() != "/v2/skins/a%2Fb" || got.Method != http.MethodPost {
		t.Errorf("request = %s %s", got.Method, got.URL.EscapedPath())
	}
	for k, want := range map[string]string{
		"User-Agent": "test/1.0", "Accept": "application/json",
		"Authorization": "Bearer " + testKey, "Content-Type": "application/json",
	} {
		if got.Header.Get(k) != want {
			t.Errorf("header %s = %q, want %q", k, got.Header.Get(k), want)
		}
	}
	if !bytes.Equal(gotBody, body.Data) {
		t.Errorf("body = %q", gotBody)
	}
	if resp.Status != 200 || string(resp.Body) != `{}` || !resp.Received.Equal(epoch) {
		t.Errorf("response = %+v", resp)
	}
}

func TestDoWithoutBodyOrKey(t *testing.T) {
	var got *http.Request
	d := newDoer(t, func(w http.ResponseWriter, r *http.Request) { got = r })
	d.APIKey = Secret{}
	if _, err := d.Do(context.Background(), http.MethodGet, "/x", nil); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if got.Header.Get("Authorization") != "" || got.Header.Get("Content-Type") != "" || got.ContentLength > 0 {
		t.Errorf("unexpected headers or body: %v (len %d)", got.Header, got.ContentLength)
	}
}

func TestDoBuildErrors(t *testing.T) {
	d := newDoer(t, func(http.ResponseWriter, *http.Request) {})
	for name, call := range map[string]func() error{
		"invalid path": func() error { _, err := d.Do(context.Background(), http.MethodGet, "/v2/\x7f", nil); return err },
		"bad method":   func() error { _, err := d.Do(context.Background(), "B AD", "/x", nil); return err },
	} {
		var build *BuildError
		err := call()
		if !errors.As(err, &build) || build.Err == nil || err.Error() != build.Err.Error() {
			t.Errorf("%s: error = %v, want *BuildError", name, err)
		}
	}
}

func TestDoReturnsRawErrors(t *testing.T) {
	d := newDoer(t, func(http.ResponseWriter, *http.Request) {})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.Do(ctx, http.MethodGet, "/x", nil)
	var build *BuildError
	if !errors.Is(err, context.Canceled) || errors.As(err, &build) {
		t.Errorf("error = %v, want a raw context.Canceled", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("error leaks the API key: %v", err)
	}
}

func TestDoBodySizeLimit(t *testing.T) {
	for _, tt := range []struct {
		name    string
		size    int
		wantErr bool
	}{{"at the cap", MaxResponseBodyBytes, false}, {"one over", MaxResponseBodyBytes + 1, true}} {
		t.Run(tt.name, func(t *testing.T) {
			d := newDoer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(make([]byte, tt.size)) })
			resp, err := d.Do(context.Background(), http.MethodGet, "/x", nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && len(resp.Body) != tt.size {
				t.Errorf("body length = %d", len(resp.Body))
			}
		})
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadLimited(t *testing.T) {
	if got, err := ReadLimited(strings.NewReader("abc")); err != nil || string(got) != "abc" {
		t.Errorf("ReadLimited = %q, %v", got, err)
	}
	cause := errors.New("boom")
	if _, err := ReadLimited(failingReader{cause}); !errors.Is(err, cause) {
		t.Errorf("ReadLimited error = %v, want the read error", err)
	}
	if _, err := ReadLimited(io.LimitReader(zeroReader{}, MaxResponseBodyBytes+1)); err == nil {
		t.Error("ReadLimited over the cap returned no error")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestDoLogsDebugWithoutSecrets(t *testing.T) {
	var buf bytes.Buffer
	d := newDoer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Mineskin-Breadcrumb", "crumb1")
	})
	d.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if _, err := d.Do(context.Background(), http.MethodGet, "/v2/x", nil); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	out := buf.String()
	for _, want := range []string{"MineSkin request completed", "method=GET", "path=/v2/x", "status=200", "breadcrumb=crumb1"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q\n%s", want, out)
		}
	}
	if strings.Contains(out, testKey) || strings.Contains(out, "Bearer") {
		t.Error("log contains the API key")
	}
}

func TestDoDefaultsClockAndLogger(t *testing.T) {
	d := newDoer(t, func(http.ResponseWriter, *http.Request) {})
	d.Clock, d.Logger = nil, nil
	before := time.Now()
	resp, err := d.Do(context.Background(), http.MethodGet, "/x", nil)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if resp.Received.Before(before) {
		t.Errorf("Received = %v, want a real-clock time", resp.Received)
	}
}

func TestDoTimeoutBoundsTheWholeExchange(t *testing.T) {
	release := make(chan struct{})
	d := newDoer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"succ`)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	d.Timeout = 50 * time.Millisecond
	start := time.Now()
	_, err := d.Do(context.Background(), http.MethodGet, "/x", nil)
	if !errors.Is(err, context.DeadlineExceeded) || !IsRequestTimeout(err) {
		t.Fatalf("error = %v, want a request timeout wrapping context.DeadlineExceeded from the body read", err)
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("error = %v, want a net.Error with Timeout() == true", err)
	}
	if strings.Contains(err.Error(), "mineskin:") {
		t.Errorf("error %q has the public prefix", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v, the timeout was not enforced", elapsed)
	}
}

func TestDoTimeoutIsPerCall(t *testing.T) {
	// Each call (handler delay) fits the budget with generous slack for a loaded
	// -race run; the two together do not.
	const (
		timeout      = 600 * time.Millisecond
		handlerDelay = 400 * time.Millisecond
	)
	d := newDoer(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(handlerDelay) // real clock: the budget is a context deadline
		_, _ = io.WriteString(w, `{}`)
	})
	d.Timeout = timeout
	start := time.Now()
	for i := range 2 {
		if _, err := d.Do(context.Background(), http.MethodGet, "/x", nil); err != nil {
			t.Fatalf("call %d error = %v, want success: each call has its own budget", i, err)
		}
	}
	if elapsed := time.Since(start); elapsed <= timeout {
		t.Errorf("two calls took %v, want more than %v so the test proves a per-call budget", elapsed, timeout)
	}
}

type deadlineSpy struct {
	next        http.RoundTripper
	hasDeadline bool
}

func (s *deadlineSpy) RoundTrip(r *http.Request) (*http.Response, error) {
	_, s.hasDeadline = r.Context().Deadline()
	return s.next.RoundTrip(r)
}

func TestDoTimeoutDeadlineOnlyWhenSet(t *testing.T) {
	for _, tt := range []struct {
		name    string
		timeout time.Duration
		want    bool
	}{
		{"timeout set", time.Minute, true},
		{"no timeout", 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := newDoer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{}`) })
			spy := &deadlineSpy{next: d.HTTP.Transport}
			d.HTTP = &http.Client{Transport: spy}
			d.Timeout = tt.timeout
			if _, err := d.Do(context.Background(), http.MethodGet, "/x", nil); err != nil {
				t.Fatalf("Do() error = %v", err)
			}
			if spy.hasDeadline != tt.want {
				t.Errorf("request context has a deadline = %v, want %v", spy.hasDeadline, tt.want)
			}
		})
	}
}

func TestDoCallerDeadlineIsNotARequestTimeout(t *testing.T) {
	d := newDoer(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	d.Timeout = time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := d.Do(ctx, http.MethodGet, "/x", nil)
	if !errors.Is(err, context.DeadlineExceeded) || IsRequestTimeout(err) {
		t.Fatalf("error = %v, want the caller's deadline and not a request timeout", err)
	}
}

func TestDoMeasuresElapsedWithTheInjectedClock(t *testing.T) {
	fake := clock.NewFake(epoch)
	d := newDoer(t, func(w http.ResponseWriter, _ *http.Request) {
		fake.Advance(250 * time.Millisecond) // the "request" takes 250 ms on the fake clock
		_, _ = io.WriteString(w, `{}`)
	})
	d.Clock = fake
	var logs bytes.Buffer
	d.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	resp, err := d.Do(context.Background(), http.MethodGet, "/x", nil)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if want := epoch.Add(250 * time.Millisecond); !resp.Received.Equal(want) {
		t.Errorf("Received = %v, want %v", resp.Received, want)
	}
	if !strings.Contains(logs.String(), "elapsed=250ms") {
		t.Errorf("log = %s, want elapsed=250ms measured on the injected clock", logs.String())
	}
}
