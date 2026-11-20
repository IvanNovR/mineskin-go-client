package mineskin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

const testAPIKey = "msk_abc12345_supersecretvalue"

func okHandler(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte(`{"success":true}`))
}

func TestDoSendsHeaders(t *testing.T) {
	var got http.Header
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		okHandler(w, r)
	})

	tests := []struct {
		name     string
		opts     []Option
		wantAuth string
	}{
		{"with key", []Option{WithAPIKey(testAPIKey)}, "Bearer " + testAPIKey},
		{"key is trimmed", []Option{WithAPIKey("  " + testAPIKey + "\n")}, "Bearer " + testAPIKey},
		{"without key", nil, ""},
		{"blank key", []Option{WithAPIKey("   ")}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, srv.URL, tt.opts...)
			if _, err := c.do(context.Background(), http.MethodGet, "/v2/x", nil); err != nil {
				t.Fatalf("do() error = %v", err)
			}
			if v := got.Get("Authorization"); v != tt.wantAuth {
				t.Errorf("Authorization = %q, want %q", v, tt.wantAuth)
			}
			if _, present := got["Authorization"]; present != (tt.wantAuth != "") {
				t.Errorf("Authorization present = %v", present)
			}
			if v := got.Get("Accept"); v != "application/json" {
				t.Errorf("Accept = %q", v)
			}
			if v := got.Get("User-Agent"); v != "test/1.0" {
				t.Errorf("User-Agent = %q", v)
			}
		})
	}
}

func TestDoURLIsBasePlusPath(t *testing.T) {
	var uri, method string
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		uri, method = r.RequestURI, r.Method
		okHandler(w, r)
	})
	c := newTestClient(t, srv.URL+"/")
	raw, err := c.do(context.Background(), http.MethodGet, "/v2/skins/a%2Fb", nil)
	if err != nil {
		t.Fatalf("do() error = %v", err)
	}
	if uri != "/v2/skins/a%2Fb" || method != http.MethodGet {
		t.Errorf("server saw %s %q", method, uri)
	}
	if raw.Status != 200 || string(raw.Body) != `{"success":true}` {
		t.Errorf("raw = %d %q", raw.Status, raw.Body)
	}
}

func TestDoNilContextIsInvalidInput(t *testing.T) {
	var hits atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		okHandler(w, r)
	})
	c := newTestClient(t, srv.URL)
	var nilCtx context.Context
	_, err := c.do(nilCtx, http.MethodGet, "/v2/x", nil)
	var build *transport.BuildError
	if !errors.As(err, &build) {
		t.Fatalf("err = %v, want *transport.BuildError", err)
	}
	if hits.Load() != 0 {
		t.Error("a request was sent for a nil context")
	}
}

func TestDoInvalidPathIsInvalidInput(t *testing.T) {
	c := newTestClient(t, "http://example.invalid")
	_, err := c.do(context.Background(), http.MethodGet, "/v2/\x7f", nil)
	var build *transport.BuildError
	if !errors.As(err, &build) {
		t.Fatalf("err = %v, want *transport.BuildError", err)
	}
	_, err = c.get(context.Background(), "/v2/\x7f", noopDecoder)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("get err = %v, want ErrInvalidInput", err)
	}
}

func TestTimeoutWhileBodyStalls(t *testing.T) {
	release := make(chan struct{})
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte(`{"succ`))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })

	c := newTestClient(t, srv.URL, WithTimeout(80*time.Millisecond))
	start := time.Now()
	_, err := c.do(context.Background(), http.MethodGet, "/v2/x", nil)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %v, timeout not enforced on the body read", time.Since(start))
	}
	var ne net.Error
	isNetTimeout := errors.As(err, &ne) && ne.Timeout()
	if !errors.Is(err, context.DeadlineExceeded) && !isNetTimeout {
		t.Errorf("err is neither a deadline nor a net timeout: %v", err)
	}
}

func TestContextCancelReturnsCanceled(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := newTestServer(t, func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })

	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	_, err := c.do(ctx, http.MethodGet, "/v2/x", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", err)
	}
}

func TestRedirectHTTPSToHTTPIsRefused(t *testing.T) {
	var plainHits atomic.Int32
	plain := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		okHandler(w, r)
	})
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/v2/x", http.StatusFound)
	}))
	t.Cleanup(tlsSrv.Close)

	c := newTestClient(t, tlsSrv.URL, WithTransport(tlsSrv.Client().Transport), WithAPIKey(testAPIKey))
	_, err := c.get(context.Background(), "/v2/x", noopDecoder)
	assertGoStyleMessage(t, err)
	if plainHits.Load() != 0 {
		t.Error("plain http server was contacted after an https->http redirect")
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Error("error contains the API key")
	}
}

func TestRedirectHTTPSToHTTPSIsFollowed(t *testing.T) {
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/start" {
			http.Redirect(w, r, "/v2/final", http.StatusFound)
			return
		}
		okHandler(w, r)
	}))
	t.Cleanup(tlsSrv.Close)

	c := newTestClient(t, tlsSrv.URL, WithTransport(tlsSrv.Client().Transport))
	raw, err := c.do(context.Background(), http.MethodGet, "/v2/start", nil)
	if err != nil {
		t.Fatalf("do() error = %v", err)
	}
	if raw.Status != 200 || string(raw.Body) != `{"success":true}` {
		t.Errorf("raw = %d %q", raw.Status, raw.Body)
	}
}

func TestWithTransportInterception(t *testing.T) {
	var seenHeader string
	var seenStatus int
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		seenHeader = r.Header.Get("X-Test")
		okHandler(w, r)
	})
	wrapper := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("X-Test", "intercepted")
		resp, err := http.DefaultTransport.RoundTrip(r)
		if resp != nil {
			seenStatus = resp.StatusCode
		}
		return resp, err
	})
	c := newTestClient(t, srv.URL, WithTransport(wrapper))
	if _, err := c.do(context.Background(), http.MethodGet, "/v2/x", nil); err != nil {
		t.Fatalf("do() error = %v", err)
	}
	if seenHeader != "intercepted" || seenStatus != 200 {
		t.Errorf("interception: header %q status %d", seenHeader, seenStatus)
	}
}

func TestTransportErrorsDoNotLeakAPIKey(t *testing.T) {
	var logs bytes.Buffer
	closed := httptest.NewServer(http.HandlerFunc(okHandler))
	closedURL := closed.URL
	closed.Close()

	failing := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("boom")
	})
	tests := []struct {
		name string
		opts []Option
	}{
		{"connection refused", nil},
		{"custom transport", []Option{WithTransport(failing)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := append([]Option{WithAPIKey(testAPIKey), WithLogger(captureLogger(&logs))}, tt.opts...)
			c := newTestClient(t, closedURL, opts...)
			_, err := c.get(context.Background(), "/v2/skins/abc", noopDecoder)
			assertGoStyleMessage(t, err)
			if strings.Contains(err.Error(), testAPIKey) || strings.Contains(err.Error(), "Bearer") {
				t.Errorf("error leaks credentials: %v", err)
			}
			if !strings.HasPrefix(err.Error(), "mineskin: GET /v2/skins/abc") {
				t.Errorf("error = %q, want prefix with method and path", err.Error())
			}
		})
	}
	if strings.Contains(logs.String(), testAPIKey) || strings.Contains(logs.String(), "supersecretvalue") {
		t.Error("logs leak the API key")
	}
}

func TestDoBodySizeLimit(t *testing.T) {
	tests := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{"exactly at the cap", transport.MaxResponseBodyBytes, false},
		{"one byte over the cap", transport.MaxResponseBodyBytes + 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := bytes.Repeat([]byte("a"), tt.size)
			srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(payload)
			})
			c := newTestClient(t, srv.URL, WithTimeout(30*time.Second))
			raw, err := c.do(context.Background(), http.MethodGet, "/v2/x", nil)
			if tt.wantErr {
				if err == nil {
					t.Fatal("do() error = nil, want an error")
				}
				_, getErr := c.get(context.Background(), "/v2/x", noopDecoder)
				assertGoStyleMessage(t, getErr)
				return
			}
			if err != nil || len(raw.Body) != tt.size {
				t.Fatalf("err = %v, len = %d", err, len(raw.Body))
			}
		})
	}
}

func TestDoLogsDebugWithoutSecrets(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Mineskin-Breadcrumb", "crumb123")
		okHandler(w, r)
	})
	var logs bytes.Buffer
	c := newTestClient(t, srv.URL, WithAPIKey(testAPIKey), WithLogger(captureLogger(&logs)))
	if _, err := c.do(context.Background(), http.MethodGet, "/v2/x", nil); err != nil {
		t.Fatalf("do() error = %v", err)
	}
	out := logs.String()
	for _, want := range []string{"GET", "/v2/x", "status=200", "crumb123", "elapsed="} {
		if !strings.Contains(out, want) {
			t.Errorf("logs missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "supersecretvalue") || strings.Contains(out, "Bearer") {
		t.Errorf("logs leak credentials:\n%s", out)
	}
}

func TestGetHappyPath(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Mineskin-Breadcrumb", "bc")
		_, _ = w.Write([]byte(`{"success":true,"user":"jeb"}`))
	})
	c := newTestClient(t, srv.URL)
	var user string
	meta, err := c.get(context.Background(), "/v2/x", func(obj map[string]json.RawMessage, _ []byte) error {
		return json.Unmarshal(obj["user"], &user)
	})
	if err != nil {
		t.Fatalf("get() error = %v", err)
	}
	if user != "jeb" || !meta.Success || meta.Status != 200 || meta.Breadcrumb != "bc" {
		t.Errorf("user=%q meta=%+v", user, meta)
	}
}

func TestGetAPIError(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":"skin_not_found","message":"Skin not found"}]}`))
	})
	c := newTestClient(t, srv.URL)
	_, err := c.get(context.Background(), "/v2/x", noopDecoder)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 || apiErr.Code != "skin_not_found" {
		t.Fatalf("err = %v, want 404 APIError", err)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Error("expected ErrNotFound")
	}
}

func TestGetPropagatesTransportError(t *testing.T) {
	boom := errors.New("boom")
	c := newTestClient(t, "http://example.invalid", WithTransport(roundTripFunc(
		func(*http.Request) (*http.Response, error) { return nil, boom })))
	_, err := c.get(context.Background(), "/v2/x", noopDecoder)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap boom", err)
	}
	if !strings.HasPrefix(err.Error(), "mineskin: GET /v2/x:") {
		t.Errorf("err = %q, want the method and path label", err)
	}
	assertGoStyleMessage(t, err)
}

func TestDoConcurrent(t *testing.T) {
	srv := newTestServer(t, okHandler)
	c := newTestClient(t, srv.URL, WithAPIKey(testAPIKey))
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.get(context.Background(), "/v2/x", noopDecoder)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent get() error = %v", err)
		}
	}
}

func TestDoSendsBody(t *testing.T) {
	payload := []byte(`{"url":"https://example.com/a.png"}`)
	var gotMethod, gotType string
	var gotLen int64
	var gotBody []byte
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotType = r.Header.Get("Content-Type")
		gotLen = r.ContentLength
		gotBody, _ = io.ReadAll(r.Body)
		okHandler(w, r)
	})
	c := newTestClient(t, srv.URL)
	_, err := c.do(context.Background(), http.MethodPost, "/v2/queue",
		&transport.Body{ContentType: "application/json", Data: payload})
	if err != nil {
		t.Fatalf("do() error = %v", err)
	}
	if gotMethod != http.MethodPost || gotType != "application/json" {
		t.Errorf("method=%q content-type=%q", gotMethod, gotType)
	}
	if !bytes.Equal(gotBody, payload) || gotLen != int64(len(payload)) {
		t.Errorf("body=%q len=%d", gotBody, gotLen)
	}
}

func TestDoGetHasNoBody(t *testing.T) {
	var gotType string
	var gotBody []byte
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, gotTypePresent := r.Header["Content-Type"]
		if gotTypePresent {
			gotType = r.Header.Get("Content-Type")
		}
		gotBody, _ = io.ReadAll(r.Body)
		okHandler(w, r)
	})
	c := newTestClient(t, srv.URL)
	if _, err := c.do(context.Background(), http.MethodGet, "/v2/x", nil); err != nil {
		t.Fatalf("do() error = %v", err)
	}
	if gotType != "" || len(gotBody) != 0 {
		t.Errorf("content-type=%q body=%q, want none", gotType, gotBody)
	}
}

func TestPostHappyPath(t *testing.T) {
	var gotBody []byte
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Mineskin-Breadcrumb", "bc")
		_, _ = w.Write([]byte(`{"success":true,"user":"jeb"}`))
	})
	c := newTestClient(t, srv.URL)
	var user string
	meta, err := c.post(context.Background(), "/v2/x",
		&transport.Body{ContentType: "application/json", Data: []byte(`{"a":1}`)},
		func(obj map[string]json.RawMessage, _ []byte) error {
			return json.Unmarshal(obj["user"], &user)
		})
	if err != nil {
		t.Fatalf("post() error = %v", err)
	}
	if user != "jeb" || !meta.Success || meta.Status != 200 || meta.Breadcrumb != "bc" {
		t.Errorf("user=%q meta=%+v", user, meta)
	}
	if string(gotBody) != `{"a":1}` {
		t.Errorf("server body = %q", gotBody)
	}
}

func TestPostAPIError(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":"skin_not_found","message":"Skin not found"}]}`))
	})
	c := newTestClient(t, srv.URL)
	_, err := c.post(context.Background(), "/v2/x", nil, noopDecoder)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 || apiErr.Code != "skin_not_found" {
		t.Fatalf("err = %v, want 404 APIError", err)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Error("expected ErrNotFound")
	}
}

func TestDoPostRedirect307ReplaysBody(t *testing.T) {
	payload := []byte(`{"user":"abc"}`)
	var finalBody []byte
	var finalMethod string
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/start" {
			http.Redirect(w, r, "/v2/final", http.StatusTemporaryRedirect)
			return
		}
		finalMethod = r.Method
		finalBody, _ = io.ReadAll(r.Body)
		okHandler(w, r)
	})
	c := newTestClient(t, srv.URL)
	_, err := c.do(context.Background(), http.MethodPost, "/v2/start",
		&transport.Body{ContentType: "application/json", Data: payload})
	if err != nil {
		t.Fatalf("do() error = %v", err)
	}
	if finalMethod != http.MethodPost || !bytes.Equal(finalBody, payload) {
		t.Errorf("after redirect method=%q body=%q", finalMethod, finalBody)
	}
}

func TestDoRecordsReceivedTime(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	fake := clock.NewFake(testNow)
	c := newTestClient(t, srv.URL, withClock(fake))
	raw, err := c.do(context.Background(), http.MethodGet, "/x", nil)
	if err != nil {
		t.Fatalf("do() error = %v", err)
	}
	if !raw.Received.Equal(fake.Now()) {
		t.Errorf("Received = %v, want %v", raw.Received, fake.Now())
	}
}
