package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
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
		BaseURL: srv.URL, UserAgent: "test/1.0", APIKey: testKey,
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
	d.APIKey = ""
	if _, err := d.Do(context.Background(), http.MethodGet, "/x", nil); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if got.Header.Get("Authorization") != "" || got.Header.Get("Content-Type") != "" || got.ContentLength > 0 {
		t.Errorf("unexpected headers or body: %v (len %d)", got.Header, got.ContentLength)
	}
}

func TestDoBuildErrors(t *testing.T) {
	d := newDoer(t, func(http.ResponseWriter, *http.Request) {})
	var nilCtx context.Context
	for name, call := range map[string]func() error{
		"invalid path": func() error { _, err := d.Do(context.Background(), http.MethodGet, "/v2/\x7f", nil); return err },
		"nil context":  func() error { _, err := d.Do(nilCtx, http.MethodGet, "/x", nil); return err },
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

func TestCheckRedirect(t *testing.T) {
	req := func(u string) *http.Request {
		r, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	hops := func(n int) []*http.Request {
		out := make([]*http.Request, n)
		for i := range out {
			out[i] = req("https://a.example/h")
		}
		return out
	}
	tests := []struct {
		name    string
		via     []*http.Request
		next    string
		wantErr bool
	}{
		{"https to https", hops(1), "https://b.example/y", false},
		{"http to https", []*http.Request{req("http://a/x")}, "https://a/y", false},
		{"https to http refused", hops(1), "http://a/y", true},
		{"downgrade later in chain", []*http.Request{req("http://a/x"), req("https://a/y")}, "http://a/z", true},
		{"nine hops allowed", hops(9), "https://a/n", false},
		{"ten hops refused", hops(10), "https://a/n", true},
		{"empty via", nil, "https://a/", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckRedirect(req(tt.next), tt.via); (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDefaultTransportIsPrivateClone(t *testing.T) {
	tr := DefaultTransport()
	if tr == nil || tr == http.DefaultTransport {
		t.Fatalf("DefaultTransport() = %v, want a private clone", tr)
	}
	if DefaultTransport() == tr {
		t.Error("DefaultTransport() returned a shared instance")
	}
}
