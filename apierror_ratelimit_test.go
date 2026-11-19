package mineskin

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// apiErrorFrom runs handleResponse and returns the resulting *APIError.
func apiErrorFrom(t *testing.T, raw rawResponse) *APIError {
	t.Helper()
	_, err := handleResponse(raw, noopDecoder)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	return apiErr
}

func TestAPIErrorCarriesRateLimitAndUsage(t *testing.T) {
	body := `{"success":false,"errors":[{"code":"too_many_requests","message":"slow down"}],` +
		`"rateLimit":{"next":{"relative":3000}},"usage":{"credits":{"used":4,"remaining":6}}}`
	e := apiErrorFrom(t, rawResponse{Status: 429, Body: []byte(body)})
	if e.RateLimit == nil || e.RateLimit.Next == nil || e.RateLimit.Next.Relative != 3*time.Second {
		t.Errorf("RateLimit = %+v, want Next.Relative 3s", e.RateLimit)
	}
	if e.Usage == nil || e.Usage.Credits == nil || e.Usage.Credits.Used != 4 {
		t.Errorf("Usage = %+v, want credits used 4", e.Usage)
	}
}

func TestAPIErrorRateLimitLenient(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"absent", `{"success":false,"errors":[{"code":"c","message":"m"}]}`},
		{"null", `{"success":false,"errors":[{"code":"c","message":"m"}],"rateLimit":null,"usage":null}`},
		{"string", `{"success":false,"errors":[{"code":"c","message":"m"}],"rateLimit":"x","usage":"y"}`},
		{"empty object", `{"success":false,"errors":[{"code":"c","message":"m"}],"rateLimit":{},"usage":{}}`},
		{"next without relative", `{"success":false,"errors":[{"code":"c","message":"m"}],"rateLimit":{"next":{}}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := apiErrorFrom(t, rawResponse{Status: 429, Body: []byte(tc.body)})
			if e.Code != "c" || e.Message != "m" {
				t.Errorf("code/message = %q/%q, want c/m", e.Code, e.Message)
			}
			if e.RateLimit != nil && e.RateLimit.Next != nil && e.RateLimit.Next.Relative != 0 {
				t.Errorf("Relative = %v, want 0", e.RateLimit.Next.Relative)
			}
		})
	}
}

func TestAPIErrorRetryAfter(t *testing.T) {
	received := testNow
	tests := []struct {
		name   string
		header http.Header
		want   time.Duration
	}{
		{"seconds", http.Header{"Retry-After": {"7"}}, 7 * time.Second},
		{"absent", http.Header{}, 0},
		{"http date", http.Header{"Retry-After": {received.Add(20 * time.Second).Format(http.TimeFormat)}}, 20 * time.Second},
		{"garbage", http.Header{"Retry-After": {"soon"}}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := apiErrorFrom(t, rawResponse{Status: 429, Header: tc.header, Received: received, Body: []byte(`{"success":false}`)})
			if e.RetryAfter != tc.want {
				t.Errorf("RetryAfter = %v, want %v", e.RetryAfter, tc.want)
			}
		})
	}
}

func TestAPIErrorIsRetryable(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{{429, true}, {503, true}, {400, false}, {401, false}, {200, false}}
	for _, tc := range tests {
		e := apiErrorFrom(t, rawResponse{Status: tc.status, Body: []byte(`{"success":false}`)})
		if got := e.IsRetryable(); got != tc.want {
			t.Errorf("status %d: IsRetryable() = %v, want %v", tc.status, got, tc.want)
		}
	}
}
