package mineskin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestAPIErrorMessageContainsStatusCodeAndMessage(t *testing.T) {
	tests := []struct {
		name string
		err  *APIError
		want string
	}{
		{"code and message", &APIError{Code: "invalid_api_key", Message: "bad key", Meta: ResponseMeta{Status: 401}}, "mineskin: API error (HTTP 401, invalid_api_key): bad key"},
		{"message only", &APIError{Message: "bad key", Meta: ResponseMeta{Status: 401}}, "mineskin: API error (HTTP 401): bad key"},
		{"code only", &APIError{Code: "invalid_api_key", Meta: ResponseMeta{Status: 401}}, "mineskin: API error (HTTP 401, invalid_api_key)"},
		{"neither", &APIError{Meta: ResponseMeta{Status: 403}}, "mineskin: API error (HTTP 403)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAPIErrorIs(t *testing.T) {
	tests := []struct {
		name   string
		status int
		target error
		want   bool
	}{
		{"not found 404", 404, ErrNotFound, true},
		{"not found 400", 400, ErrNotFound, false},
		{"not found 500", 500, ErrNotFound, false},
		{"invalid input", 404, ErrInvalidInput, false},
		{"unrelated", 404, context.Canceled, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &APIError{Meta: ResponseMeta{Status: tt.status}}
			if got := errors.Is(e, tt.target); got != tt.want {
				t.Fatalf("errors.Is(status %d, %v) = %v, want %v", tt.status, tt.target, got, tt.want)
			}
		})
	}
}

func TestAPIErrorAsThroughWrapper(t *testing.T) {
	orig := &APIError{Code: "x", Message: "y", Meta: ResponseMeta{Status: 429}}
	wrapped := fmt.Errorf("outer: %w", orig)
	var got *APIError
	if !errors.As(wrapped, &got) || got != orig {
		t.Fatalf("errors.As did not recover the APIError")
	}
}

func TestInvalidInputError(t *testing.T) {
	err := invalidInput("id %q must not be empty", "x")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("validation error must match ErrInvalidInput")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("validation error must not match ErrNotFound")
	}
	if got, want := err.Error(), `mineskin: invalid input: id "x" must not be empty`; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if errors.Unwrap(err) != ErrInvalidInput { //nolint:errorlint // identity: a plain %w wrap
		t.Fatalf("Unwrap() = %v, want ErrInvalidInput", errors.Unwrap(err))
	}
}

func TestSentinelMessagesDoNotLeak(t *testing.T) {
	for _, e := range []error{ErrInvalidInput, ErrNotFound} {
		if !strings.HasPrefix(e.Error(), "mineskin: ") {
			t.Fatalf("sentinel %q lacks package prefix", e)
		}
	}
}

func TestResponseMetaZeroValue(t *testing.T) {
	var m ResponseMeta
	if m.Status != 0 || m.Header != nil || m.Breadcrumb != "" || m.Warnings != nil || m.Messages != nil || m.Links != nil {
		t.Fatalf("zero ResponseMeta should be empty: %+v", m)
	}
	_ = CodeAndMessage{Code: "c", Message: "m"}
}

// TestSentinelsArePlainErrors pins that the sentinels are plain errors.New
// values with no umbrella matching: no custom Is method, no shared target.
func TestSentinelsArePlainErrors(t *testing.T) {
	for name, s := range map[string]error{
		"ErrMaxAttempts": ErrMaxAttempts, "ErrJobNotCompleted": ErrJobNotCompleted, "ErrClientClosed": ErrClientClosed,
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := s.(interface{ Is(error) bool }); ok {
				t.Errorf("%T has an Is method, want a plain errors.New value", s)
			}
		})
	}
	for _, e := range []error{&APIError{Meta: ResponseMeta{Status: 500}}, &JobFailedError{}} {
		if errors.Is(e, ErrInvalidInput) || errors.Is(e, ErrNotFound) {
			t.Errorf("%T matches an unrelated sentinel", e)
		}
	}
}
