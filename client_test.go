package mineskin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestNewClientDefaults(t *testing.T) {
	t.Parallel()
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if c.doer.BaseURL != DefaultBaseURL || c.doer.UserAgent != DefaultUserAgent || !c.doer.APIKey.IsEmpty() {
		t.Errorf("defaults wrong: %q %q %q", c.doer.BaseURL, c.doer.UserAgent, c.doer.APIKey.Reveal())
	}
	if c.doer.Timeout != defaultTimeout || defaultTimeout != 10*time.Second {
		t.Errorf("timeout = %v", c.doer.Timeout)
	}
	if c.doer.HTTP.Timeout != 0 {
		t.Errorf("http.Client.Timeout = %v, want 0: the timeout is applied per attempt by the Doer", c.doer.HTTP.Timeout)
	}
	if _, ok := c.doer.HTTP.Transport.(*http.Transport); !ok || c.doer.HTTP.Transport == http.DefaultTransport {
		t.Error("transport should default to an owned clone of http.DefaultTransport")
	}
	if c.doer.HTTP.CheckRedirect == nil || c.logger == nil {
		t.Error("redirect policy and logger must be set")
	}
	c.logger.Info("silent") // must not panic
}

func TestNewClientBaseURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
		wantErr        bool
	}{
		{"trailing slash trimmed", "https://example.com/", "https://example.com", false},
		{"prefix accepted", "http://localhost:8080/prefix/", "http://localhost:8080/prefix", false},
		{"multiple slashes", "https://example.com///", "https://example.com", false},
		{"ftp scheme", "ftp://x", "", true},
		{"no scheme", "api.mineskin.org", "", true},
		{"no host", "https://", "", true},
		{"unparsable", ":%", "", true},
		{"empty", "", "", true},
		{"query rejected", "https://example.com/?a=b", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, err := NewClient(WithBaseURL(tt.in))
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("err = %v, want ErrInvalidInput", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.doer.BaseURL != tt.want {
				t.Errorf("baseURL = %q, want %q", c.doer.BaseURL, tt.want)
			}
		})
	}
}

func TestNewClientUserAgent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		opts     []Option
		wantUA   string
		wantWarn bool
	}{
		{"default warns", nil, DefaultUserAgent, true},
		{"empty falls back and warns", []Option{WithUserAgent("")}, DefaultUserAgent, true},
		{"blank falls back and warns", []Option{WithUserAgent("  ")}, DefaultUserAgent, true},
		{"custom silent", []Option{WithUserAgent("App/1.0")}, "App/1.0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			c, err := NewClient(append(tt.opts, WithLogger(captureLogger(&buf)), WithAPIKey("k"))...)
			if err != nil {
				t.Fatal(err)
			}
			if c.doer.UserAgent != tt.wantUA {
				t.Errorf("userAgent = %q, want %q", c.doer.UserAgent, tt.wantUA)
			}
			if got := strings.Contains(buf.String(), "User-Agent"); got != tt.wantWarn {
				t.Errorf("UA warning = %v, want %v; log: %s", got, tt.wantWarn, buf.String())
			}
		})
	}
}

func TestNewClientAPIKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		key      string
		wantKey  string
		wantWarn bool
	}{
		{"none", "", "", true},
		{"blank", "   ", "", true},
		{"trimmed", "key\n", "key", false},
		{"plain", "abc", "abc", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			c, err := NewClient(WithAPIKey(tt.key), WithUserAgent("A/1"), WithLogger(captureLogger(&buf)))
			if err != nil {
				t.Fatal(err)
			}
			if c.doer.APIKey.Reveal() != tt.wantKey {
				t.Errorf("apiKey = %q, want %q", c.doer.APIKey.Reveal(), tt.wantKey)
			}
			if got := strings.Contains(buf.String(), "without API key"); got != tt.wantWarn {
				t.Errorf("key warning = %v, want %v", got, tt.wantWarn)
			}
		})
	}
}

func TestNewClientNoKeyOptionAtAll(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if _, err := NewClient(WithUserAgent("A/1"), WithLogger(captureLogger(&buf))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "without API key") || !strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("missing warn: %s", buf.String())
	}
}

func TestNewClientKeyNeverLogged(t *testing.T) {
	t.Parallel()
	const key = "msk_abc123_secretpart"
	tests := []struct {
		name   string
		key    string
		wantID bool
	}{
		{"msk key", key, true},
		{"msk key whitespace", " " + key + "\n", true},
		{"msk malformed", "msk_onlytwo", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			if _, err := NewClient(WithAPIKey(tt.key), WithUserAgent("A/1"), WithLogger(captureLogger(&buf))); err != nil {
				t.Fatal(err)
			}
			out := buf.String()
			if tt.wantID && (!strings.Contains(out, "abc123") || !strings.Contains(out, "level=DEBUG")) {
				t.Errorf("key id not logged at debug: %s", out)
			}
			for _, leak := range []string{"secretpart", key, "onlytwo"} {
				if strings.Contains(out, leak) {
					t.Errorf("log leaks %q: %s", leak, out)
				}
			}
		})
	}
}

func TestNewClientTimeout(t *testing.T) {
	t.Parallel()
	for _, d := range []time.Duration{0, -time.Second} {
		if _, err := NewClient(WithTimeout(d)); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("WithTimeout(%v) err = %v, want ErrInvalidInput", d, err)
		}
	}
	c, err := NewClient(WithTimeout(3 * time.Second))
	if err != nil || c.doer.Timeout != 3*time.Second || c.doer.HTTP.Timeout != 0 {
		t.Errorf("timeout not applied: %v %v", c, err)
	}
}

type stubTransport struct{}

func (stubTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, errors.New("stub") }

func TestOptionNilFallbacks(t *testing.T) {
	t.Parallel()
	c, err := NewClient(WithTransport(nil), WithLogger(nil), WithUserAgent("A/1"), WithAPIKey("k"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.doer.HTTP.Transport.(*http.Transport); !ok || c.idleCloser == nil || c.logger == nil {
		t.Error("nil options must fall back to defaults (owned transport clone)")
	}
	c.logger.Warn("must not panic")

	st := stubTransport{}
	c, err = NewClient(WithTransport(st))
	if err != nil || c.doer.HTTP.Transport != st {
		t.Errorf("custom transport not used: %v", err)
	}
}

func TestNewClientNilOption(t *testing.T) {
	t.Parallel()
	if _, err := NewClient(nil); err != nil {
		t.Errorf("nil option should be ignored: %v", err)
	}
}

func TestNewClientErrorNeverContainsKey(t *testing.T) {
	t.Parallel()
	_, err := NewClient(WithAPIKey("msk_abc_secretpart"), WithBaseURL("ftp://x"), WithTimeout(0))
	if err == nil || strings.Contains(err.Error(), "secretpart") {
		t.Errorf("err = %v", err)
	}
}

func redirectReq(t *testing.T, raw string) *http.Request {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Request{URL: u}
}

func TestCheckRedirect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		via     []string
		next    string
		wantErr bool
	}{
		{"https to https", []string{"https://a.example/x"}, "https://b.example/y", false},
		{"http to http", []string{"http://a.example/x"}, "http://b.example/y", false},
		{"http to https", []string{"http://a.example/x"}, "https://a.example/y", false},
		{"https to http refused", []string{"https://a.example/x"}, "http://a.example/y", true},
		{"downgrade later in chain", []string{"http://a/x", "https://a/y"}, "http://a/z", true},
		{"nine hops allowed", hops(9), "https://a.example/n", false},
		{"ten hops refused", hops(10), "https://a.example/n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			via := make([]*http.Request, len(tt.via))
			for i, v := range tt.via {
				via[i] = redirectReq(t, v)
			}
			err := transport.CheckRedirect(redirectReq(t, tt.next), via)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && errors.Is(err, ErrInvalidInput) {
				t.Errorf("a redirect refusal must not be invalid input: %v", err)
			}
		})
	}
	if err := transport.CheckRedirect(redirectReq(t, "https://a/"), nil); err != nil {
		t.Errorf("empty via: %v", err)
	}
}

func hops(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "https://a.example/h"
	}
	return out
}

func TestNewClientRejectsUserinfoInBaseURL(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"https://user:hunter2@example.com", "https://user@example.com", "https://:pw@example.com"} {
		_, err := NewClient(WithBaseURL(raw))
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%q: err = %v, want ErrInvalidInput", raw, err)
		}
		for _, leak := range []string{"hunter2", "user", "pw@"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("error echoes userinfo %q: %v", leak, err)
			}
		}
	}
}

func TestNewClientPlainHTTPWithKey(t *testing.T) {
	t.Parallel()
	const key = "msk_abc_secretpart"
	tests := []struct {
		name, url, key string
		insecure       bool
		wantReject     bool
		wantWarn       bool
	}{
		{"remote http with key is rejected", "http://api.example.com", key, false, true, false},
		{"remote ip http with key is rejected", "http://10.0.0.1", key, false, true, false},
		{"remote http with key and opt-in warns", "http://api.example.com", key, true, false, true},
		{"remote ip http with key and opt-in warns", "http://10.0.0.1", key, true, false, true},
		{"remote http no key", "http://api.example.com", "", false, false, false},
		{"remote http blank key", "http://api.example.com", "  ", false, false, false},
		{"remote http no key with opt-in", "http://api.example.com", "", true, false, false},
		{"https with key", "https://api.example.com", key, false, false, false},
		{"https with key and opt-in", "https://api.example.com", key, true, false, false},
		{"localhost", "http://localhost:8080", key, false, false, false},
		{"ipv4 loopback", "http://127.0.0.1:8080", key, false, false, false},
		{"ipv4 loopback range", "http://127.5.5.5", key, false, false, false},
		{"ipv6 loopback", "http://[::1]:8080", key, false, false, false},
		{"loopback with opt-in does not warn", "http://127.0.0.1:8080", key, true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			opts := []Option{WithBaseURL(tt.url), WithAPIKey(tt.key), WithUserAgent("A/1"), WithLogger(captureLogger(&buf))}
			if tt.insecure {
				opts = append(opts, WithInsecureHTTP())
			}
			c, err := NewClient(opts...)
			out := buf.String()
			if strings.Contains(out, "secretpart") || strings.Contains(out, key) {
				t.Errorf("log leaks key: %s", out)
			}
			if tt.wantReject {
				if !errors.Is(err, ErrInvalidInput) || c != nil {
					t.Fatalf("NewClient() = %v, %v; want nil client and ErrInvalidInput", c, err)
				}
				const want = "mineskin: invalid input: API key over plain http to a non-loopback host; use https or WithInsecureHTTP()"
				if err.Error() != want {
					t.Errorf("error = %q, want %q", err.Error(), want)
				}
				if strings.Contains(err.Error(), "secretpart") || strings.Contains(err.Error(), "api.example.com") {
					t.Errorf("error echoes the key or host: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			if got := strings.Contains(out, "plain HTTP"); got != tt.wantWarn {
				t.Errorf("warn = %v, want %v; log: %s", got, tt.wantWarn, out)
			}
		})
	}
}

func TestNewClientRejectsControlCharacters(t *testing.T) {
	t.Parallel()
	const key = "msk_abc_secretpart"
	tests := map[string][]Option{
		"key CR":       {WithAPIKey(key + "\rX-Evil: 1")},
		"key LF":       {WithAPIKey("a\nb" + key)},
		"key NUL":      {WithAPIKey(key + "\x00x")},
		"key DEL":      {WithAPIKey(key + "\x7fx")},
		"UA CR":        {WithUserAgent("App/1\r\nX-Evil: 1")},
		"UA ESC":       {WithUserAgent("App/\x1b[0m")},
		"UA and key":   {WithUserAgent("A\n"), WithAPIKey(key + "\n" + key)},
		"UA tab inner": {WithUserAgent("App\t1")},
	}
	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := NewClient(opts...)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			if strings.Contains(err.Error(), "secretpart") {
				t.Errorf("error contains key: %v", err)
			}
		})
	}
}

func TestRedirectToAnotherHostDropsAuthorization(t *testing.T) {
	t.Parallel()
	var seen atomic.Pointer[string]
	other := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		seen.Store(&got)
		okHandler(w, r)
	})
	var firstAuth atomic.Pointer[string]
	first := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		firstAuth.Store(&got)
		http.Redirect(w, r, other.URL+"/v2/x", http.StatusFound)
	})
	c := newTestClient(t, first.URL, WithAPIKey("msk_abc_secretpart"))
	if _, err := c.do(context.Background(), http.MethodGet, "/v2/x", nil); err != nil {
		t.Fatalf("do() error = %v", err)
	}
	if got := firstAuth.Load(); got == nil || *got != "Bearer msk_abc_secretpart" {
		t.Errorf("first hop Authorization = %v, want the bearer key", got)
	}
	if got := seen.Load(); got == nil || *got != "" {
		t.Errorf("redirected host received Authorization %v, want none", got)
	}
}

func TestCheckRedirectStripsAuthorizationOnHostChange(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		first    string
		next     string
		wantAuth string
	}{
		{"different host", "https://api.mineskin.org/v2/x", "https://evil.example/y", ""},
		{"sibling subdomain api2", "https://api.mineskin.org/v2/x", "https://api2.mineskin.org/v2/x", ""},
		{"different port", "http://127.0.0.1:8080/x", "http://127.0.0.1:9090/x", ""},
		{"same host keeps it", "https://api.mineskin.org/v2/x", "https://api.mineskin.org/v2/y", "Bearer k"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			first, next := redirectReq(t, tt.first), redirectReq(t, tt.next)
			first.Header = http.Header{"Authorization": {"Bearer k"}}
			next.Header = http.Header{"Authorization": {"Bearer k"}, "User-Agent": {"A/1"}}
			if err := transport.CheckRedirect(next, []*http.Request{first}); err != nil {
				t.Fatal(err)
			}
			if got := next.Header.Get("Authorization"); got != tt.wantAuth {
				t.Errorf("Authorization = %q, want %q", got, tt.wantAuth)
			}
			if next.Header.Get("User-Agent") != "A/1" {
				t.Error("other headers must be kept")
			}
		})
	}
}

func TestNewClientLogsKeyIDOnlyForWellFormedKeys(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		key    string
		wantID string
	}{
		{"well formed", "msk_abc123_secretpart", "abc123"},
		{"secret with underscores", "msk_abc123_secret_part", "abc123"},
		{"no id separator", "msk_SECRETVALUE", ""},
		{"empty id", "msk__SECRETVALUE", ""},
		{"empty secret", "msk_abc123_", ""},
		{"id with symbols", "msk_ab-c$_SECRETVALUE", ""},
		{"id too long", "msk_" + strings.Repeat("a", 33) + "_SECRETVALUE", ""},
		{"id at the length limit", "msk_" + strings.Repeat("a", 32) + "_SECRETVALUE", strings.Repeat("a", 32)},
		{"wrong prefix", "abc_def_SECRETVALUE", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			if _, err := NewClient(WithAPIKey(tt.key), WithUserAgent("A/1"), WithLogger(captureLogger(&buf))); err != nil {
				t.Fatal(err)
			}
			out := buf.String()
			if tt.wantID == "" {
				if strings.Contains(out, "key_id") {
					t.Errorf("key id logged for a malformed key: %s", out)
				}
			} else if !strings.Contains(out, "key_id="+tt.wantID) {
				t.Errorf("log = %s, want key_id=%s", out, tt.wantID)
			}
			if strings.Contains(out, "SECRETVALUE") || strings.Contains(out, "secretpart") || strings.Contains(out, "secret_part") {
				t.Errorf("log leaks the secret part: %s", out)
			}
		})
	}
}

func TestRedirectAwayAndBackRestoresAuthorizationOnTheOriginalHost(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var trail []string // "host-label:authorization" per request
	record := func(label string, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		trail = append(trail, label+":"+r.Header.Get("Authorization"))
	}
	var aURL, bURL string
	b := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		record("B", r)
		http.Redirect(w, r, aURL+"/v2/final", http.StatusFound)
	})
	bURL = b.URL
	a := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		record("A", r)
		if r.URL.Path == "/v2/start" {
			http.Redirect(w, r, bURL+"/v2/mid", http.StatusFound)
			return
		}
		okHandler(w, r)
	})
	aURL = a.URL
	c := newTestClient(t, a.URL, WithAPIKey("msk_abc_secretpart"))
	if _, err := c.do(context.Background(), http.MethodGet, "/v2/start", nil); err != nil {
		t.Fatalf("do() error = %v", err)
	}
	want := []string{"A:Bearer msk_abc_secretpart", "B:", "A:Bearer msk_abc_secretpart"}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(trail, want) {
		t.Errorf("requests = %q, want %q", trail, want)
	}
}

const redactKey = "msk_id_SECRETPART"

// assertNoKey fails when out holds the key or any distinctive part of it.
func assertNoKey(t *testing.T, what, out string) {
	t.Helper()
	for _, part := range []string{redactKey, "SECRETPART", "msk_id"} {
		if strings.Contains(out, part) {
			t.Errorf("%s leaks %q: %s", what, part, out)
		}
	}
}

func TestClientAndConfigRedactAPIKeyWhenFormatted(t *testing.T) {
	t.Parallel()
	c, err := NewClient(WithAPIKey("  "+redactKey+"\n"), WithUserAgent("A/1"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	WithAPIKey(redactKey)(cfg)
	assertNoKey(t, "Client", fmt.Sprintf("%v %+v %#v", c, c, c))
	assertNoKey(t, "config pointer", fmt.Sprintf("%v %+v %#v", cfg, cfg, cfg))
	assertNoKey(t, "config value", fmt.Sprintf("%v %+v %#v", *cfg, *cfg, *cfg))
	assertNoKey(t, "Doer pointer", fmt.Sprintf("%v %+v %#v", c.doer, c.doer, c.doer))
	assertNoKey(t, "Doer value", fmt.Sprintf("%v %+v %#v", *c.doer, *c.doer, *c.doer))
	if got := c.doer.APIKey.Reveal(); got != redactKey {
		t.Errorf("Reveal() = %q, want the trimmed key %q (the header still needs it)", got, redactKey)
	}
}

func TestClientRedactsAPIKeyInSlogOutput(t *testing.T) {
	t.Parallel()
	c, err := NewClient(WithAPIKey(redactKey), WithUserAgent("A/1"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	WithAPIKey(redactKey)(cfg)
	for name, newHandler := range map[string]func(*bytes.Buffer) slog.Handler{
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
	} {
		var buf bytes.Buffer
		slog.New(newHandler(&buf)).Info("dump", "client", c, "doer", c.doer, "cfg", cfg, "key", c.doer.APIKey)
		assertNoKey(t, name+" handler", buf.String())
		if !strings.Contains(buf.String(), "[REDACTED]") {
			t.Errorf("%s handler output = %s, want [REDACTED] for the key", name, buf.String())
		}
	}
}

// TestClient_ConcurrentUse is a race-detector smoke test: many goroutines mix
// the call kinds against one client.
func TestClient_ConcurrentUse(t *testing.T) {
	t.Parallel()
	const workers = 20
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case keySkin:
			writeJSON(200, skinFixture)(w, r)
		case keyQueue:
			writeJSON(200, jobFixture)(w, r)
		case keyJob:
			writeJSON(200, jobJSON("job-1", "completed"))(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	c := newTestClient(t, srv.URL, fastPolling())
	ctx := waitCtx(t)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch i % 3 {
			case 0:
				if _, err := c.GetSkin(ctx, testSkinUUID); err != nil {
					t.Errorf("worker %d GetSkin() error = %v", i, err)
				}
			case 1:
				if _, err := c.Queue(ctx, FromURL("https://example.test/skin.png"), GenerateOptions{}); err != nil {
					t.Errorf("worker %d Queue() error = %v", i, err)
				}
			default:
				got, err := c.WaitForJob(ctx, Job{ID: "job-1"})
				if err != nil || got.Job.Status != JobStatusCompleted {
					t.Errorf("worker %d WaitForJob() = %+v, %v; want a completed job", i, got, err)
				}
			}
		}()
	}
	wg.Wait()

	if err := c.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

// A MineSkin-* header sent more than once must neither fail the call nor lose
// a value (JB-08: the Java Apache backend crashes on duplicate header names).
func TestGetSkin_RepeatedResponseHeaders(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("MineSkin-Server", "api-1")
		w.Header().Add("MineSkin-Server", "api-2")
		w.Header().Add("MineSkin-Breadcrumb", "a")
		w.Header().Add("MineSkin-Breadcrumb", "b")
		writeJSON(200, skinFixture)(w, r)
	})

	got, err := newTestClient(t, srv.URL).GetSkin(waitCtx(t), testSkinUUID)

	if err != nil {
		t.Fatalf("GetSkin() error = %v", err)
	}
	if want := []string{"api-1", "api-2"}; !reflect.DeepEqual(got.Meta.Header.Values("MineSkin-Server"), want) {
		t.Errorf("MineSkin-Server values = %v, want %v", got.Meta.Header.Values("MineSkin-Server"), want)
	}
	if got.Meta.Breadcrumb != "a,b" {
		t.Errorf("Breadcrumb = %q, want a,b", got.Meta.Breadcrumb)
	}
}
