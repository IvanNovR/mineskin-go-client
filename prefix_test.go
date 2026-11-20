package mineskin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDecodeErrors_HaveExactlyOnePrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		generator string
	}{
		{"duration out of range", `{"duration":-5}`},
		{"duration string", `{"duration":"12"}`},
		{"timestamp string", `{"timestamp":"12"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := `{"success":true,"skin":{"uuid":"` + testSkinUUID.String() + `","generator":` + tt.generator + `}}`
			srv := newTestServer(t, writeJSON(200, body))
			c := newTestClient(t, srv.URL)
			t.Cleanup(func() { _ = c.Close() })

			_, err := c.GetSkin(context.Background(), testSkinUUID)

			if err == nil {
				t.Fatal("GetSkin() error = nil, want a decode error")
			}
			assertGoStyleMessage(t, err)
		})
	}
}

func TestRedirectLoop_HasExactlyOnePrefix(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/v2/loop", http.StatusFound)
	})
	c := newTestClient(t, srv.URL)
	t.Cleanup(func() { _ = c.Close() })

	_, err := c.GetSkin(context.Background(), testSkinUUID)

	if err == nil {
		t.Fatal("GetSkin() error = nil, want a redirect error")
	}
	assertGoStyleMessage(t, err)
	if !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Errorf("error %q does not name the redirect limit", err)
	}
	if got := hits.Load(); got != 10 {
		t.Errorf("server hits = %d, want 10 (the 11th request is refused before it is sent)", got)
	}
}

func TestClose_NeverLeaksInternalPrefix(t *testing.T) {
	t.Parallel()
	srv := newHitServer(t)
	c := newTestClient(t, srv.URL)
	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	ctx := context.Background()
	src := FromURL("https://example.com/s.png")
	calls := map[string]func() error{
		"GetSkin":    func() error { _, err := c.GetSkin(ctx, testSkinUUID); return err },
		"GetUser":    func() error { _, err := c.GetUser(ctx); return err },
		"GetJob":     func() error { _, err := c.GetJob(ctx, "job-1"); return err },
		"ListJobs":   func() error { _, err := c.ListJobs(ctx); return err },
		"Queue":      func() error { _, err := c.Queue(ctx, src, GenerateOptions{}); return err },
		"Generate":   func() error { _, err := c.Generate(ctx, src, GenerateOptions{}); return err },
		"WaitForJob": func() error { _, err := c.WaitForJob(ctx, Job{ID: "job-1"}); return err },
	}
	for name, call := range calls {
		err := call()
		if !errors.Is(err, ErrClientClosed) {
			t.Errorf("%s error = %v, want ErrClientClosed", name, err)
			continue
		}
		assertGoStyleMessage(t, err)
		if strings.Contains(err.Error(), "ratelimit:") || strings.Contains(err.Error(), "throttle:") {
			t.Errorf("%s error %q leaks an internal prefix", name, err)
		}
	}
}
