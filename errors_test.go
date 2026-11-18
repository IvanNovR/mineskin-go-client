package mineskin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestAPIErrorMessageContainsStatusCodeAndMessage(t *testing.T) {
	e := &APIError{Code: "invalid_api_key", Message: "bad key", ResponseMeta: ResponseMeta{Status: 401}}
	want := "mineskin: API error (HTTP 401, invalid_api_key): bad key"
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestAPIErrorIs(t *testing.T) {
	tests := []struct {
		name   string
		status int
		target error
		want   bool
	}{
		{"client 500", 500, ErrClient, true},
		{"client 404", 404, ErrClient, true},
		{"not found 404", 404, ErrNotFound, true},
		{"not found 400", 400, ErrNotFound, false},
		{"not found 500", 500, ErrNotFound, false},
		{"invalid input", 404, ErrInvalidInput, false},
		{"unrelated", 404, context.Canceled, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &APIError{ResponseMeta: ResponseMeta{Status: tt.status}}
			if got := errors.Is(e, tt.target); got != tt.want {
				t.Fatalf("errors.Is(status %d, %v) = %v, want %v", tt.status, tt.target, got, tt.want)
			}
		})
	}
}

func TestAPIErrorAsThroughWrapper(t *testing.T) {
	orig := &APIError{Code: "x", Message: "y", ResponseMeta: ResponseMeta{Status: 429}}
	wrapped := fmt.Errorf("outer: %w", orig)
	var got *APIError
	if !errors.As(wrapped, &got) || got != orig {
		t.Fatalf("errors.As did not recover the APIError")
	}
	if !errors.Is(wrapped, ErrClient) {
		t.Fatalf("wrapped APIError should match ErrClient")
	}
}

func TestClientErrorValidation(t *testing.T) {
	err := newInvalidInputError("id must not be empty")
	if !errors.Is(err, ErrInvalidInput) || !errors.Is(err, ErrClient) {
		t.Fatalf("validation error must match ErrInvalidInput and ErrClient")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("validation error must not match ErrNotFound")
	}
	if got := err.Error(); got != "mineskin: id must not be empty" {
		t.Fatalf("Error() = %q", got)
	}
	var ce *clientError
	if !errors.As(err, &ce) || ce.Unwrap() != nil {
		t.Fatalf("expected *clientError without cause")
	}
}

func TestClientErrorWrapsCause(t *testing.T) {
	err := wrapClientError("request failed", context.DeadlineExceeded)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("must match context.DeadlineExceeded")
	}
	if !errors.Is(err, ErrClient) {
		t.Fatalf("must match ErrClient")
	}
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrNotFound) {
		t.Fatalf("transport error must not match ErrInvalidInput or ErrNotFound")
	}
	want := "mineskin: request failed: context deadline exceeded"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestWrapClientErrorNilCause(t *testing.T) {
	err := wrapClientError("boom", nil)
	if got := err.Error(); got != "mineskin: boom" {
		t.Fatalf("Error() = %q", got)
	}
	if !errors.Is(err, ErrClient) {
		t.Fatalf("must match ErrClient")
	}
}

func TestSentinelMessagesDoNotLeak(t *testing.T) {
	for _, e := range []error{ErrClient, ErrInvalidInput, ErrNotFound} {
		if !strings.HasPrefix(e.Error(), "mineskin: ") {
			t.Fatalf("sentinel %q lacks package prefix", e)
		}
	}
}

func TestResponseMetaZeroValue(t *testing.T) {
	var m ResponseMeta
	if m.Status != 0 || m.Success || m.Errors != nil || m.Headers != nil || m.Links != nil {
		t.Fatalf("zero ResponseMeta should be empty: %+v", m)
	}
	_ = CodeAndMessage{Code: "c", Message: "m"}
}
