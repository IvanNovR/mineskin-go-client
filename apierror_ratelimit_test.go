package mineskin

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// apiErrorFrom runs decodeResponse and returns the resulting *APIError.
func apiErrorFrom(t *testing.T, raw transport.Response) *APIError {
	t.Helper()
	_, err := decodeMeta[struct{}](raw)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	return apiErr
}

func TestAPIErrorCarriesRateLimitAndUsage(t *testing.T) {
	body := `{"success":false,"errors":[{"code":"too_many_requests","message":"slow down"}],` +
		`"rateLimit":{"next":{"relative":3000}},"usage":{"credits":{"used":4,"remaining":6}}}`
	e := apiErrorFrom(t, transport.Response{Status: 429, Body: []byte(body)})
	assertRateLimit(t, e.RateLimit, RateLimit{NextIn: 3 * time.Second})
	if want := (Usage{CreditsUsed: 4, CreditsRemaining: 6}); e.Usage != want {
		t.Errorf("Usage = %+v, want %+v", e.Usage, want)
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
			e := apiErrorFrom(t, transport.Response{Status: 429, Body: []byte(tc.body)})
			if e.Code != "c" || e.Message != "m" {
				t.Errorf("code/message = %q/%q, want c/m", e.Code, e.Message)
			}
			if e.RateLimit != (RateLimit{}) || e.Usage != (Usage{}) {
				t.Errorf("RateLimit = %+v Usage = %+v, want zero values", e.RateLimit, e.Usage)
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
			e := apiErrorFrom(t, transport.Response{Status: 429, Header: tc.header, Received: received, Body: []byte(`{"success":false}`)})
			if e.RetryAfter != tc.want {
				t.Errorf("RetryAfter = %v, want %v", e.RetryAfter, tc.want)
			}
		})
	}
}

func TestIsRetryableAPIError(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{{429, true}, {503, true}, {400, false}, {401, false}, {200, false}}
	for _, tc := range tests {
		e := apiErrorFrom(t, transport.Response{Status: tc.status, Body: []byte(`{"success":false}`)})
		if got := IsRetryable(e); got != tc.want {
			t.Errorf("status %d: IsRetryable() = %v, want %v", tc.status, got, tc.want)
		}
	}
}
