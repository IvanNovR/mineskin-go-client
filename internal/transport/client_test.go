package transport

import (
	"context"
	"net/http"
	"testing"
)

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
