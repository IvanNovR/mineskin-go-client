package mineskin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
)

func apiErrorWithStatus(status int) *APIError {
	return &APIError{Meta: ResponseMeta{Status: status}}
}

func TestIsRetryable(t *testing.T) {
	wrap := func(err error) error { return fmt.Errorf("mineskin: GET /v2/x: %w", err) }
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"invalid input", invalidInput("bad"), false},
		{"API 404", apiErrorWithStatus(404), false},
		{"API 408", apiErrorWithStatus(408), true},
		{"API 429", apiErrorWithStatus(429), true},
		{"API 500", apiErrorWithStatus(500), true},
		{"API 501", apiErrorWithStatus(501), false},
		{"API 503", apiErrorWithStatus(503), true},
		{"API 505", apiErrorWithStatus(505), false},
		{"wrapped API 503", wrap(apiErrorWithStatus(503)), true},
		{"unexpected 502", newUnexpectedResponseError(502, http.Header{}, nil, nil, testNow), true},
		{"unexpected 501", newUnexpectedResponseError(501, http.Header{}, nil, nil, testNow), false},
		{"unexpected 200 truncated", newUnexpectedResponseError(200, http.Header{}, []byte("{"), errors.New("eof"), testNow), false},
		{"net timeout", timeoutNetError{}, true},
		{"wrapped net timeout", wrap(timeoutNetError{}), true},
		{"net error without timeout", &net.OpError{Op: "dial", Err: errors.New("refused")}, false},
		{"context canceled", context.Canceled, false},
		{"wrapped context canceled", wrap(context.Canceled), false},
		// DeadlineExceeded implements Timeout() and must still not retry.
		{"context deadline exceeded", context.DeadlineExceeded, false},
		{"wrapped context deadline exceeded", wrap(context.DeadlineExceeded), false},
		{"client closed", ErrClientClosed, false},
		{"wrapped client closed", closedError("GET /v2/x"), false},
		{"ctx error beside a retryable 429", &retryCancelledError{ctxErr: wrap(context.Canceled), rateLimited: apiErrorWithStatus(429)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryable(tt.err); got != tt.want {
				t.Errorf("IsRetryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestStatusSentinel(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{{404, ErrNotFound}, {429, ErrRateLimited}, {200, nil}, {400, nil}, {500, nil}, {503, nil}}
	for _, tt := range tests {
		if got := statusSentinel(tt.status); got != tt.want { //nolint:errorlint // identity of a sentinel
			t.Errorf("statusSentinel(%d) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestStatusSentinelsThroughBothErrorTypes(t *testing.T) {
	tests := []struct {
		status int
		is     error
		isNot  error
	}{{404, ErrNotFound, ErrRateLimited}, {429, ErrRateLimited, ErrNotFound}}
	for _, tt := range tests {
		errs := map[string]error{
			"APIError":                apiErrorWithStatus(tt.status),
			"UnexpectedResponseError": newUnexpectedResponseError(tt.status, http.Header{}, []byte("<html>"), nil, testNow),
			"wrapped":                 fmt.Errorf("mineskin: wrap: %w", apiErrorWithStatus(tt.status)),
		}
		for name, err := range errs {
			if !errors.Is(err, tt.is) || errors.Is(err, tt.isNot) || errors.Is(err, ErrInvalidInput) {
				t.Errorf("%s %d: Is(%v) = %v, Is(%v) = %v", name, tt.status, tt.is, errors.Is(err, tt.is), tt.isNot, errors.Is(err, tt.isNot))
			}
		}
	}
	if errors.Is(apiErrorWithStatus(500), ErrRateLimited) || errors.Is(apiErrorWithStatus(500), ErrNotFound) {
		t.Error("a 500 matches a status sentinel")
	}
}
