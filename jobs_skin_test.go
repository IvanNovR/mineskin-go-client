package mineskin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestJobFailedError(t *testing.T) {
	job := Job{ID: "job-9", Status: JobStatusFailed}
	errs := []CodeAndMessage{{Code: "invalid_image", Message: "bad image"}, {Code: "other", Message: "second"}}
	meta := ResponseMeta{Status: 200, Breadcrumb: "bc"}
	tests := []struct {
		name       string
		resp       *JobResponse
		wantErrors []CodeAndMessage
		wantText   string
	}{
		{"first error in the text", &JobResponse{Job: job, Meta: meta, errs: errs}, errs, "mineskin: job job-9 failed (invalid_image): bad image"},
		{"no errors", &JobResponse{Job: job, Meta: meta}, nil, "mineskin: job job-9 failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := newJobFailedError(tt.resp)
			if err.Error() != tt.wantText {
				t.Errorf("Error() = %q, want %q", err.Error(), tt.wantText)
			}
			if !reflect.DeepEqual(err.Errors, tt.wantErrors) {
				t.Errorf("Errors = %v, want %v", err.Errors, tt.wantErrors)
			}
			if err.Job != job || err.Meta.Breadcrumb != "bc" || err.Meta.Status != 200 {
				t.Errorf("Job = %+v Meta = %+v, want the response's", err.Job, err.Meta)
			}
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidInput) {
				t.Error("matches unrelated sentinel")
			}
			var target *JobFailedError
			if !errors.As(fmt.Errorf("wait: %w", err), &target) || target != err {
				t.Error("errors.As through %w failed")
			}
		})
	}
}

func TestNewSentinelsAreDistinct(t *testing.T) {
	for name, s := range map[string]error{"ErrMaxAttempts": ErrMaxAttempts} {
		t.Run(name, func(t *testing.T) {
			wrapped := fmt.Errorf("mineskin: ctx: %w", s)
			if !errors.Is(wrapped, s) {
				t.Errorf("wrapped %v does not match %v", wrapped, s)
			}
			if errors.Is(s, ErrInvalidInput) || errors.Is(s, ErrNotFound) {
				t.Error("matches unrelated sentinel")
			}
			if !strings.HasPrefix(s.Error(), "mineskin: ") {
				t.Errorf("Error() = %q", s.Error())
			}
		})
	}
}

// TestGetSkinWithJobResult pins the documented way to read the skin of a
// finished job whose response does not embed it: GetSkin(ctx, resp.Job.Result).
func TestGetSkinWithJobResult(t *testing.T) {
	resp := &JobResponse{Job: Job{ID: "j", Status: JobStatusCompleted, Result: fixtureUUID(jobResultID)}}
	var hits atomic.Int32
	var method, uri string
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		method, uri = r.Method, r.RequestURI
		writeJSON(200, `{"success":true,"skin":{"uuid":"`+jobResultID+`","name":"fetched"}}`)(w, r)
	})
	got, err := newTestClient(t, srv.URL).GetSkin(context.Background(), resp.Job.Result)
	if err != nil || got.Skin.Name != "fetched" || got.Skin.UUID != resp.Job.Result {
		t.Fatalf("got = %+v, err = %v, want skin %q", got, err, "fetched")
	}
	if hits.Load() != 1 || method != http.MethodGet || uri != "/v2/skins/"+jobResultID {
		t.Errorf("requests = %d, request = %s %s", hits.Load(), method, uri)
	}
}
