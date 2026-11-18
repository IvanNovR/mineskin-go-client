package mineskin

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestNewClientDefaults(t *testing.T) {
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if c.baseURL != DefaultBaseURL || c.userAgent != DefaultUserAgent || c.apiKey != "" {
		t.Errorf("defaults wrong: %q %q %q", c.baseURL, c.userAgent, c.apiKey)
	}
	if c.httpClient.Timeout != DefaultTimeout || DefaultTimeout != 10*time.Second {
		t.Errorf("timeout = %v", c.httpClient.Timeout)
	}
	if c.httpClient.Transport != http.DefaultTransport {
		t.Error("transport should default to http.DefaultTransport")
	}
	if c.httpClient.CheckRedirect == nil || c.logger == nil {
		t.Error("redirect policy and logger must be set")
	}
	c.logger.Info("silent") // must not panic
}

func TestNewClientBaseURL(t *testing.T) {
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
			c, err := NewClient(WithBaseURL(tt.in))
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidInput) || !errors.Is(err, ErrClient) {
					t.Fatalf("err = %v, want ErrInvalidInput and ErrClient", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.baseURL != tt.want {
				t.Errorf("baseURL = %q, want %q", c.baseURL, tt.want)
			}
		})
	}
}

func TestNewClientUserAgent(t *testing.T) {
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
			var buf bytes.Buffer
			c, err := NewClient(append(tt.opts, WithLogger(captureLogger(&buf)), WithAPIKey("k"))...)
			if err != nil {
				t.Fatal(err)
			}
			if c.userAgent != tt.wantUA {
				t.Errorf("userAgent = %q, want %q", c.userAgent, tt.wantUA)
			}
			if got := strings.Contains(buf.String(), "User-Agent"); got != tt.wantWarn {
				t.Errorf("UA warning = %v, want %v; log: %s", got, tt.wantWarn, buf.String())
			}
		})
	}
}

func TestNewClientAPIKey(t *testing.T) {
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
			var buf bytes.Buffer
			c, err := NewClient(WithAPIKey(tt.key), WithUserAgent("A/1"), WithLogger(captureLogger(&buf)))
			if err != nil {
				t.Fatal(err)
			}
			if c.apiKey != tt.wantKey {
				t.Errorf("apiKey = %q, want %q", c.apiKey, tt.wantKey)
			}
			if got := strings.Contains(buf.String(), "without API key"); got != tt.wantWarn {
				t.Errorf("key warning = %v, want %v", got, tt.wantWarn)
			}
		})
	}
}

func TestNewClientNoKeyOptionAtAll(t *testing.T) {
	var buf bytes.Buffer
	if _, err := NewClient(WithUserAgent("A/1"), WithLogger(captureLogger(&buf))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "without API key") || !strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("missing warn: %s", buf.String())
	}
}

func TestNewClientKeyNeverLogged(t *testing.T) {
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
	for _, d := range []time.Duration{0, -time.Second} {
		if _, err := NewClient(WithTimeout(d)); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("WithTimeout(%v) err = %v, want ErrInvalidInput", d, err)
		}
	}
	c, err := NewClient(WithTimeout(3 * time.Second))
	if err != nil || c.httpClient.Timeout != 3*time.Second {
		t.Errorf("timeout not applied: %v %v", c, err)
	}
}

type stubTransport struct{}

func (stubTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, errors.New("stub") }

func TestOptionNilFallbacks(t *testing.T) {
	c, err := NewClient(WithTransport(nil), WithLogger(nil), WithUserAgent("A/1"), WithAPIKey("k"))
	if err != nil {
		t.Fatal(err)
	}
	if c.httpClient.Transport != http.DefaultTransport || c.logger == nil {
		t.Error("nil options must fall back to defaults")
	}
	c.logger.Warn("must not panic")

	st := stubTransport{}
	c, err = NewClient(WithTransport(st))
	if err != nil || c.httpClient.Transport != st {
		t.Errorf("custom transport not used: %v", err)
	}
}

func TestNewClientNilOption(t *testing.T) {
	if _, err := NewClient(nil); err != nil {
		t.Errorf("nil option should be ignored: %v", err)
	}
}

func TestNewClientErrorNeverContainsKey(t *testing.T) {
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
			via := make([]*http.Request, len(tt.via))
			for i, v := range tt.via {
				via[i] = redirectReq(t, v)
			}
			err := checkRedirect(redirectReq(t, tt.next), via)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && (!errors.Is(err, ErrClient) || errors.Is(err, ErrInvalidInput)) {
				t.Errorf("err should match ErrClient only: %v", err)
			}
		})
	}
	if err := checkRedirect(redirectReq(t, "https://a/"), nil); err != nil {
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

func TestNewClientWarnsOnPlainHTTPWithKey(t *testing.T) {
	const key = "msk_abc_secretpart"
	tests := []struct {
		name, url, key string
		wantWarn       bool
	}{
		{"remote http with key", "http://api.example.com", key, true},
		{"remote http no key", "http://api.example.com", "", false},
		{"https with key", "https://api.example.com", key, false},
		{"localhost", "http://localhost:8080", key, false},
		{"ipv4 loopback", "http://127.0.0.1:8080", key, false},
		{"ipv4 loopback range", "http://127.5.5.5", key, false},
		{"ipv6 loopback", "http://[::1]:8080", key, false},
		{"remote ip", "http://10.0.0.1", key, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if _, err := NewClient(WithBaseURL(tt.url), WithAPIKey(tt.key), WithUserAgent("A/1"), WithLogger(captureLogger(&buf))); err != nil {
				t.Fatal(err)
			}
			out := buf.String()
			if got := strings.Contains(out, "plain HTTP"); got != tt.wantWarn {
				t.Errorf("warn = %v, want %v; log: %s", got, tt.wantWarn, out)
			}
			if strings.Contains(out, "secretpart") || strings.Contains(out, key) {
				t.Errorf("log leaks key: %s", out)
			}
		})
	}
}

func TestNewClientRejectsControlCharacters(t *testing.T) {
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
			_, err := NewClient(opts...)
			if !errors.Is(err, ErrInvalidInput) || !errors.Is(err, ErrClient) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			if strings.Contains(err.Error(), "secretpart") {
				t.Errorf("error contains key: %v", err)
			}
		})
	}
}
