package mineskin

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
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
		Interval:     PollIntervalFunc(func(int) time.Duration { return time.Millisecond }),
		MaxAttempts:  1000,
	})
}

// pollerT0 is the start of the fake clocks that drive the poller in tests.
var pollerT0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

const (
	// fallbackWaitTimeout bounds every blocking wait of a test.
	fallbackWaitTimeout = 20 * time.Second
	// deadlineMargin is kept free before the test binary's own deadline so a
	// stuck wait fails with a goroutine dump instead of a bare panic.
	deadlineMargin = 2 * time.Second
	// pollEvery is how often eventually and driveClock re-check.
	pollEvery = time.Millisecond
)

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

// failTimeout fails the test and dumps every goroutine to ease diagnosis.
func failTimeout(t *testing.T, what string) {
	t.Helper()
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	t.Fatalf("timed out waiting for %s\n%s", what, buf)
}

// eventually polls cond until it holds or waitTimeout passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout(t))
	for !cond() {
		if time.Now().After(deadline) {
			failTimeout(t, what)
		}
		pause()
	}
}

// driveClock runs fn in a goroutine and advances fake by step until fn
// returns, then returns fn's error. No real delay is waited for beyond one
// poll tick, so a fake timer is never missed.
func driveClock(t *testing.T, fake *clock.Fake, step time.Duration, fn func() error) error {
	t.Helper()
	res := make(chan error, 1)
	go func() { res <- fn() }()
	deadline := time.Now().Add(waitTimeout(t))
	for time.Now().Before(deadline) {
		select {
		case err := <-res:
			return err
		default:
		}
		fake.Advance(step)
		pause()
	}
	failTimeout(t, "the call to finish while advancing the fake clock")
	return nil
}

// waitCtx returns a context that ends after waitTimeout.
func waitCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout(t))
	t.Cleanup(cancel)
	return ctx
}

// hdr builds a header from key/value pairs; a repeated key keeps every value.
func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

// writeJSON replies with status and the fixed JSON body.
func writeJSON(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body)) //nolint:gosec // G705: test server writes fixed JSON fixtures
	}
}

// testImage is a small opaque image with distinct pixels.
func testImage() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 3))
	for x := range uint8(4) {
		for y := range uint8(3) {
			img.Set(int(x), int(y), color.NRGBA{R: x * 40, G: y * 60, B: 7, A: 255})
		}
	}
	return img
}

// testPNG is testImage encoded as PNG.
func testPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, testImage()); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	return buf.Bytes()
}

// pause waits one poll tick between two checks of shared state. It is the only
// real-time wait the polling helpers use; no assertion depends on its length.
func pause() { time.Sleep(pollEvery) }

// pollOnce is a fast polling configuration with the given interval function.
func pollOnce(attempts int, interval func(int) time.Duration) Option {
	return WithJobPolling(PollOptions{InitialDelay: time.Millisecond, MaxAttempts: attempts, Interval: PollIntervalFunc(interval)})
}

const jobResultID = "744c993e-0e8a-4f0b-9b6a-2f3c7a1b5d11"

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

const testSkinID = "744c993e-0e8a-4f0b-9b6a-2f3c7a1b5d11"

// testSkinUUID is testSkinID parsed.
var testSkinUUID = fixtureUUID(testSkinID)

// fixtureUUID parses s or panics; for test fixtures only.
func fixtureUUID(s string) UUID {
	id, err := ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return id
}

const skinFixture = `{"success":true,"skin":{"uuid":"744c993e0e8a4f0b9b6a2f3c7a1b5d11","name":"Steve","variant":"slim","visibility":"unlisted",
"texture":{"data":{"value":"VAL","signature":"SIG"},"hash":{"skin":"sh","cape":"ch"},"url":{"skin":"su","cape":"cu"}},
"generator":{"timestamp":1700000000123,"duration":1500},"views":7,"duplicate":true}}`

const queueFixture = `{"success":true,"job":{"id":"job-1","status":"waiting","timestamp":1700000000000},
"rateLimit":{"next":{"relative":500},"limit":{"limit":10,"remaining":9,
"hour":{"limit":100,"remaining":99,"used":1,"reset":1700003600}}},
"usage":{"credits":{"used":2,"remaining":98}}}`

func jobJSON(id, status string) string {
	if status == "completed" {
		return `{"success":true,"job":{"id":"` + id + `","status":"completed","result":"` + jobResultID + `"}}`
	}
	return `{"success":true,"job":{"id":"` + id + `","status":"` + status + `"}}`
}

const messagePrefix = "mineskin: "
