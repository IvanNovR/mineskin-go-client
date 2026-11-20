package mineskin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const queueFixture = `{"success":true,"job":{"id":"job-1","status":"waiting","timestamp":1700000000000},
"rateLimit":{"next":{"relative":500},"limit":{"limit":10,"remaining":9,
"hour":{"limit":100,"remaining":99,"used":1,"reset":1700003600}}},
"usage":{"credits":{"used":2,"remaining":98}}}`

func TestQueueURLSendsJSON(t *testing.T) {
	var method, path, ctype string
	var body []byte
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, ctype = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("MineSkin-Breadcrumb", "crumb-q")
		writeJSON(200, queueFixture)(w, r)
	})
	got, err := newTestClient(t, srv.URL).Queue(context.Background(),
		FromURL("https://example.com/s.png"), GenerateOptions{Name: "n", Variant: VariantSlim})
	if err != nil {
		t.Fatalf("Queue() error = %v", err)
	}
	if method != http.MethodPost || path != "/v2/queue" || ctype != jsonContentType {
		t.Errorf("request = %s %s %q", method, path, ctype)
	}
	var sent map[string]string
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	if sent["url"] != "https://example.com/s.png" || sent["name"] != "n" || sent["variant"] != "slim" {
		t.Errorf("body = %v", sent)
	}
	if got.Job.ID != "job-1" || got.Job.Status != JobStatusWaiting {
		t.Errorf("job = %+v", got.Job)
	}
	if got.Meta.Breadcrumb != "crumb-q" || got.Meta.Status != 200 {
		t.Errorf("meta = %+v", got.Meta)
	}
	wantRL := RateLimit{NextIn: 500 * time.Millisecond, Limit: 10, Remaining: 9,
		Hour: WindowLimit{Limit: 100, Remaining: 99, Used: 1, Reset: time.Unix(1700003600, 0)}}
	assertRateLimit(t, got.RateLimit, wantRL)
	if want := (Usage{CreditsUsed: 2, CreditsRemaining: 98}); got.Usage != want {
		t.Errorf("usage = %+v, want %+v", got.Usage, want)
	}
}

func TestQueueUploadSendsMultipart(t *testing.T) {
	var gotFile, gotName, gotVariant string
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm() error = %v", err)
		} else {
			f, _, err := r.FormFile("file")
			if err != nil {
				t.Errorf("FormFile() error = %v", err)
			} else {
				data, _ := io.ReadAll(f)
				gotFile = string(data)
			}
			gotName, gotVariant = r.FormValue("name"), r.FormValue("variant")
		}
		writeJSON(200, queueFixture)(w, r)
	})
	_, err := newTestClient(t, srv.URL).Queue(context.Background(),
		FromReader(strings.NewReader("PNGDATA")), GenerateOptions{Name: "up", Variant: VariantClassic})
	if err != nil {
		t.Fatalf("Queue() error = %v", err)
	}
	if gotFile != "PNGDATA" || gotName != "up" || gotVariant != "classic" {
		t.Errorf("file=%q name=%q variant=%q", gotFile, gotName, gotVariant)
	}
}

func TestGenerateDecodesSkin(t *testing.T) {
	var method, path string
	body := strings.Replace(skinFixture, `{"success":true,`,
		`{"success":true,"rateLimit":{"limit":{"limit":5,"remaining":4}},"usage":{"credits":{"used":1,"remaining":9}},`, 1)
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Header().Set("MineSkin-Breadcrumb", "crumb-g")
		writeJSON(200, body)(w, r)
	})
	id, err := ParseUUID(testSkinID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := newTestClient(t, srv.URL).Generate(context.Background(), FromUser(id), GenerateOptions{})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if method != http.MethodPost || path != "/v2/generate" {
		t.Errorf("request = %s %s", method, path)
	}
	if got.Skin.UUID.String() != testSkinID || got.Skin.Name != "Steve" || got.Skin.Texture.Data.Value != "VAL" {
		t.Errorf("skin = %+v", got.Skin)
	}
	if got.Meta.Breadcrumb != "crumb-g" || got.Meta.Status != 200 {
		t.Errorf("meta = %+v", got.Meta)
	}
	assertRateLimit(t, got.RateLimit, RateLimit{Limit: 5, Remaining: 4})
	if want := (Usage{CreditsUsed: 1, CreditsRemaining: 9}); got.Usage != want {
		t.Errorf("usage = %+v, want %+v", got.Usage, want)
	}
}

func TestQueueAbsentRateLimitIsZero(t *testing.T) {
	srv := newTestServer(t, writeJSON(200, `{"success":true,"job":{"id":"j","status":"waiting"}}`))
	got, err := newTestClient(t, srv.URL).Queue(context.Background(), FromURL("https://example.com/a.png"), GenerateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.RateLimit.NextAt.IsZero() || got.RateLimit != (RateLimit{}) || got.Usage != (Usage{}) {
		t.Errorf("rateLimit=%+v usage=%+v, want zero", got.RateLimit, got.Usage)
	}
}

func TestQueueRejectsInvalidSourceWithoutRequest(t *testing.T) {
	var hits atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		writeJSON(200, queueFixture)(w, r)
	})
	c := newTestClient(t, srv.URL)
	sources := map[string]Source{
		"nil":          nil,
		"relative":     FromURL("/just/a/path"),
		"unparsable":   FromURL("http://a b/"),
		"nil reader":   FromReader(nil),
		"empty path":   FromFile(""),
		"nil image":    FromImage(nil),
		"empty":        FromURL(""),
		"no host":      FromURL("https://"),
		"missing file": FromFile("/nonexistent/skin.png"),
	}
	for name, src := range sources {
		t.Run(name, func(t *testing.T) {
			for _, call := range []func() error{
				func() error { _, err := c.Queue(context.Background(), src, GenerateOptions{}); return err },
				func() error { _, err := c.Generate(context.Background(), src, GenerateOptions{}); return err },
			} {
				if err := call(); err == nil {
					t.Error("error = nil, want error")
				} else if name != "missing file" && !errors.Is(err, ErrInvalidInput) {
					t.Errorf("error = %v, want ErrInvalidInput", err)
				}
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("server hits = %d, want 0", n)
	}
}

func TestQueueAPIError(t *testing.T) {
	srv := newTestServer(t, writeJSON(429, `{"success":false,"errors":[{"code":"too_many_requests","message":"slow down"}],"job":"garbage","rateLimit":5}`))
	got, err := newTestClient(t, srv.URL).Queue(context.Background(), FromURL("https://example.com/a.png"), GenerateOptions{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if got != nil || apiErr.Meta.Status != 429 || apiErr.Code != "too_many_requests" {
		t.Errorf("got=%v apiErr=%+v", got, apiErr)
	}
}

func TestGenerateHonoursTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := newTestServer(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	c := newTestClient(t, srv.URL, WithTimeout(80*time.Millisecond))
	start := time.Now()
	_, err := c.Generate(context.Background(), FromURL("https://example.com/a.png"), GenerateOptions{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Errorf("error = %v, want a net.Error with Timeout() == true", err)
	}
	if !IsRetryable(err) {
		t.Error("IsRetryable(per-request timeout) = false, want true")
	}
	if got := strings.Count(err.Error(), "mineskin:"); got != 1 {
		t.Errorf("error %q has %d mineskin: prefixes, want 1", err, got)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
}

func TestCallerDeadlineIsNotRetryable(t *testing.T) {
	release := make(chan struct{})
	srv := newTestServer(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	c := newTestClient(t, srv.URL, WithTimeout(time.Minute))
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err := c.Generate(ctx, FromURL("https://example.com/a.png"), GenerateOptions{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if IsRetryable(err) {
		t.Error("IsRetryable(caller ctx deadline) = true, want false")
	}
}
