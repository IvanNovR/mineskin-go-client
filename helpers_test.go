package mineskin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
	all := append([]Option{WithBaseURL(baseURL), WithUserAgent("test/1.0")}, opts...)
	c, err := NewClient(all...)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return c
}
