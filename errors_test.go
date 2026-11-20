package mineskin_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client"
)

func TestAPIErrorMessageContainsStatusCodeAndMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  *mineskin.APIError
		want string
	}{
		{"code and message", &mineskin.APIError{Code: "invalid_api_key", Message: "bad key", Meta: mineskin.ResponseMeta{Status: 401}}, "mineskin: API error (HTTP 401, invalid_api_key): bad key"},
		{"message only", &mineskin.APIError{Message: "bad key", Meta: mineskin.ResponseMeta{Status: 401}}, "mineskin: API error (HTTP 401): bad key"},
		{"code only", &mineskin.APIError{Code: "invalid_api_key", Meta: mineskin.ResponseMeta{Status: 401}}, "mineskin: API error (HTTP 401, invalid_api_key)"},
		{"neither", &mineskin.APIError{Meta: mineskin.ResponseMeta{Status: 403}}, "mineskin: API error (HTTP 403)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAPIErrorIs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		target error
		want   bool
	}{
		{"not found 404", 404, mineskin.ErrNotFound, true},
		{"not found 400", 400, mineskin.ErrNotFound, false},
		{"not found 500", 500, mineskin.ErrNotFound, false},
		{"invalid input", 404, mineskin.ErrInvalidInput, false},
		{"unrelated", 404, context.Canceled, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := &mineskin.APIError{Meta: mineskin.ResponseMeta{Status: tt.status}}
			if got := errors.Is(e, tt.target); got != tt.want {
				t.Fatalf("errors.Is(status %d, %v) = %v, want %v", tt.status, tt.target, got, tt.want)
			}
		})
	}
}

func TestAPIErrorAsThroughWrapper(t *testing.T) {
	t.Parallel()
	orig := &mineskin.APIError{Code: "x", Message: "y", Meta: mineskin.ResponseMeta{Status: 429}}
	wrapped := fmt.Errorf("outer: %w", orig)
	var got *mineskin.APIError
	if !errors.As(wrapped, &got) || got != orig {
		t.Fatalf("errors.As did not recover the APIError")
	}
}

func TestSentinelMessagesDoNotLeak(t *testing.T) {
	t.Parallel()
	for _, e := range []error{mineskin.ErrInvalidInput, mineskin.ErrNotFound} {
		if !strings.HasPrefix(e.Error(), "mineskin: ") {
			t.Fatalf("sentinel %q lacks package prefix", e)
		}
	}
}

func TestResponseMetaZeroValue(t *testing.T) {
	t.Parallel()
	var m mineskin.ResponseMeta
	if m.Status != 0 || m.Header != nil || m.Breadcrumb != "" || m.Warnings != nil || m.Messages != nil || m.Links != nil {
		t.Fatalf("zero ResponseMeta should be empty: %+v", m)
	}
	_ = mineskin.CodeAndMessage{Code: "c", Message: "m"}
}

// TestSentinelsArePlainErrors pins that the sentinels are plain errors.New
// values with no umbrella matching: no custom Is method, no shared target.
func TestSentinelsArePlainErrors(t *testing.T) {
	t.Parallel()
	for name, s := range map[string]error{
		"ErrMaxAttempts": mineskin.ErrMaxAttempts, "ErrClientClosed": mineskin.ErrClientClosed,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, ok := s.(interface{ Is(error) bool }); ok {
				t.Errorf("%T has an Is method, want a plain errors.New value", s)
			}
		})
	}
	for _, e := range []error{&mineskin.APIError{Meta: mineskin.ResponseMeta{Status: 500}}, &mineskin.JobFailedError{}} {
		if errors.Is(e, mineskin.ErrInvalidInput) || errors.Is(e, mineskin.ErrNotFound) {
			t.Errorf("%T matches an unrelated sentinel", e)
		}
	}
}

func TestEndToEndMessagesHaveOnePrefix(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv := newTestServer(t, writeJSON(http.StatusOK, jobJSON("j1", "waiting")))
	c := newTestClient(t, srv.URL, pollOnce(1, func(int) time.Duration { return time.Millisecond }))
	_, err := c.WaitForJob(ctx, mineskin.Job{ID: "j1"})
	assertGoStyleMessage(t, err)
	_, err = c.GetUser(ctx)
	assertGoStyleMessage(t, err)
	_ = c.Close()
	_, err = c.GetUser(context.Background())
	assertGoStyleMessage(t, err)
	_, err = c.WaitForJob(context.Background(), mineskin.Job{ID: "j1"})
	assertGoStyleMessage(t, err)
}
