package mineskin

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// withClock replaces the client's clock; it exists only for tests.
func withClock(c clock.Clock) Option { return func(cfg *config) { cfg.clock = c } }

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// newTestServer starts an httptest server that is closed when the test ends.
func newTestServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// newTestClient builds a Client against baseURL; opts are applied after the
// defaults so they can override them.
func newTestClient(t *testing.T, baseURL string, opts ...Option) *Client {
	t.Helper()
	all := append([]Option{WithBaseURL(baseURL), WithUserAgent("test/1.0"), fastThrottles()}, opts...)
	c, err := NewClient(all...)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return c
}

// fastThrottles makes both groups practically unthrottled (1 ns interval, 100
// in flight) so tests that are not about throttling stay fast. Options given
// after it win, so WithAutoTuning may still be added.
func fastThrottles() Option {
	fast := Throttle{Interval: time.Nanosecond, Concurrency: 100}
	return func(c *config) {
		c.generate = generatePacing{throttle: fast}
		c.getThrottle = fast
	}
}

// trackConcurrency wraps h and records how many requests are inside it at
// once. peak returns the highest number seen.
func trackConcurrency(h http.HandlerFunc) (wrapped http.HandlerFunc, peak func() int) {
	var cur, high atomic.Int32
	wrapped = func(w http.ResponseWriter, r *http.Request) {
		n := cur.Add(1)
		defer cur.Add(-1)
		for {
			old := high.Load()
			if n <= old || high.CompareAndSwap(old, n) {
				break
			}
		}
		h(w, r)
	}
	return wrapped, func() int { return int(high.Load()) }
}

// multipartPart is one parsed multipart part.
type multipartPart struct {
	name        string
	filename    string
	contentType string
	data        []byte
}

// parseMultipart parses b with the boundary from its Content-Type and returns
// the parts in wire order.
func parseMultipart(t *testing.T, b *transport.Body) []multipartPart {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(b.ContentType)
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("Content-Type = %q, want multipart/form-data (err %v)", b.ContentType, err)
	}
	mr := multipart.NewReader(bytes.NewReader(b.Data), params["boundary"])
	var parts []multipartPart
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return parts
		}
		if err != nil {
			t.Fatalf("NextPart() error = %v", err)
		}
		data, err := io.ReadAll(p)
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		parts = append(parts, multipartPart{
			name: p.FormName(), filename: p.FileName(),
			contentType: p.Header.Get("Content-Type"), data: data,
		})
	}
}

// fastPolling makes jobs poll every millisecond with a 1 ms first delay.
func fastPolling() Option {
	return WithJobPolling(PollOptions{
		InitialDelay: time.Millisecond,
		Interval:     func(int) time.Duration { return time.Millisecond },
		MaxAttempts:  1000,
	})
}

// waitGoroutinesSettle waits up to 2 s for the goroutine count to fall back to
// before or lower. Close servers and idle connections first.
func waitGoroutinesSettle(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d, want <= %d (leak)", runtime.NumGoroutine(), before)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// runtimeGoroutines returns the current goroutine count.
func runtimeGoroutines() int { return runtime.NumGoroutine() }

// pollerT0 is the start of the fake clocks that drive the poller in tests.
var pollerT0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
