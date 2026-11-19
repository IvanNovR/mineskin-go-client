package mineskin

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/ratelimit"
	"github.com/IvanNovR/mineskin-go-client/internal/throttle"
	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// TestSeamClassifiersAgreeWithHandleResponse pins the neutral classifiers that
// internal/ratelimit uses (transport.IsFailure, transport.IsUnexpected) against
// the root's handleResponse, which is the truth for the public errors.
func TestSeamClassifiersAgreeWithHandleResponse(t *testing.T) {
	const bom = "\xef\xbb\xbf"
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"200 ok", 200, `{"success":true}`},
		{"200 empty object", 200, `{}`},
		{"429 empty object", 429, `{}`},
		{"429 api error", 429, `{"success":false,"errors":[{"code":"c","message":"m"}]}`},
		{"429 success true", 429, `{"success":true}`},
		{"BOM ok", 200, bom + `{"success":true}`},
		{"BOM api error", 429, bom + `{"success":false}`},
		{"success is a string", 200, `{"success":"x"}`},
		{"success is a string on 429", 429, `{"success":"x"}`},
		{"errors is a string", 429, `{"success":true,"errors":"x"}`},
		{"warnings malformed", 200, `{"success":true,"warnings":{}}`},
		{"messages malformed", 200, `{"success":true,"messages":[1]}`},
		{"html 429", 429, "<html>rate limited</html>"},
		{"html 502", 502, "<html>bad gateway</html>"},
		{"truncated JSON", 429, `{"success":fal`},
		{"empty body", 429, ""},
		{"array body", 200, `[]`},
		{"null body", 200, `null`},
		{"leading whitespace", 429, "\n  {\"success\":false}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := transport.Response{Status: tt.status, Header: http.Header{}, Body: []byte(tt.body), Received: pollerT0}
			_, err := handleResponse(r, acceptAnyBody)
			if got, want := transport.IsFailure(r), err != nil; got != want {
				t.Errorf("IsFailure = %v, handleResponse error = %v", got, err)
			}
			var ue *UnexpectedResponseError
			if got, want := transport.IsUnexpected(r), errors.As(err, &ue); got != want {
				t.Errorf("IsUnexpected = %v, handleResponse yields *UnexpectedResponseError = %v", got, want)
			}
			if got, want := transport.IsRetryableStatus(tt.status), tt.status == 408 || tt.status == 429 || tt.status >= 500 && tt.status != 501 && tt.status != 505; got != want {
				t.Errorf("IsRetryableStatus = %v, want %v", got, want)
			}
			if ue != nil && ue.IsRetryable() != transport.IsRetryableStatus(tt.status) {
				t.Errorf("UnexpectedResponseError.IsRetryable = %v disagrees with transport", ue.IsRetryable())
			}
		})
	}
}

func TestSeamServerDelayAgreesWithRateLimitDecoding(t *testing.T) {
	bodies := []string{
		`{"rateLimit":{"next":{"relative":5000}}}`, `{"rateLimit":{"next":{"relative":-1}}}`,
		`{"rateLimit":{"next":{"relative":"5"}}}`, `{"rateLimit":{"next":{"relative":1.5}}}`,
		`{"rateLimit":{"next":null}}`, `{"rateLimit":"x"}`, `{}`, `<html>`, "\xef\xbb\xbf" + `{"rateLimit":{"next":{"relative":7}}}`,
		`{"rateLimit":{"next":{"relative":9223372036854775807}}}`,
	}
	for _, body := range bodies {
		r := transport.Response{Status: 200, Header: http.Header{}, Body: []byte(body)}
		var want time.Duration
		if obj := jsonObject(transport.TrimBOM(r.Body)); obj != nil {
			if rl := decodeRateLimit(obj[fieldRateLimit]); rl != nil && rl.Next != nil {
				want = rl.Next.Relative
			}
		}
		if got := ratelimit.ServerDelay(r); got != want {
			t.Errorf("ServerDelay(%q) = %v, public decoding = %v", body, got, want)
		}
	}
}

func TestSeamMapGroupErr(t *testing.T) {
	const label = "GET /v2/x"
	boom := errors.New("boom")
	sent := &clientError{msg: label, err: boom}
	last := transport.Response{Status: 429, Header: http.Header{}, Body: []byte(`{"success":false}`)}

	tests := []struct {
		name  string
		err   error
		check func(t *testing.T, got error)
	}{
		{"throttle closed", throttle.ErrClosed, func(t *testing.T, got error) { mustIs(t, got, ErrClientClosed, ErrClient) }},
		{"tuner closed", ratelimit.ErrClosed, func(t *testing.T, got error) { mustIs(t, got, ErrClientClosed, ErrClient) }},
		{"ctx error is wrapped", context.Canceled, func(t *testing.T, got error) { mustIs(t, got, context.Canceled, ErrClient) }},
		{"send error passes through", sent, func(t *testing.T, got error) {
			if got != error(sent) { //nolint:errorlint // identity: no double wrapping
				t.Errorf("error = %v, want the same *clientError", got)
			}
		}},
		{"gate skip passes through", ratelimit.ErrSkipped, func(t *testing.T, got error) {
			if got != errCheckSkipped { //nolint:errorlint // identity
				t.Errorf("error = %v, want errCheckSkipped", got)
			}
		}},
		{"retry cancelled keeps ctx error and the 429", &ratelimit.RetryCancelledError{Err: context.Canceled, Last: last}, func(t *testing.T, got error) {
			mustIs(t, got, context.Canceled, ErrClient)
			var apiErr *APIError
			if !errors.As(got, &apiErr) {
				t.Errorf("error = %v, want *APIError in the chain", got)
			}
		}},
		{"retry cancelled without a 429 error is the ctx error alone", &ratelimit.RetryCancelledError{Err: context.Canceled, Last: transport.Response{Status: 200, Body: []byte(`{"success":true}`)}}, func(t *testing.T, got error) {
			var rc *retryCancelledError
			if errors.As(got, &rc) {
				t.Errorf("error = %v, want no retryCancelledError", got)
			}
			mustIs(t, got, context.Canceled, ErrClient)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.check(t, mapGroupErr(label, tt.err)) })
	}
}

func mustIs(t *testing.T, got error, targets ...error) {
	t.Helper()
	for _, target := range targets {
		if !errors.Is(got, target) {
			t.Errorf("error = %v, want errors.Is %v", got, target)
		}
	}
}
