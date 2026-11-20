package mineskin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestJobFailedError(t *testing.T) {
	job := Job{ID: "job-9", Status: JobStatusFailed}
	withErrs := &JobResponse{Job: job, ResponseMeta: ResponseMeta{
		Status: 200, Errors: []CodeAndMessage{{Code: "invalid_image", Message: "bad image"}, {Code: "other", Message: "second"}},
	}}
	tests := []struct {
		name        string
		resp        *JobResponse
		wantCode    string
		wantMessage string
	}{
		{"first error", withErrs, "invalid_image", "bad image"},
		{"fallback", &JobResponse{Job: job}, "job_failed", "Job failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := newJobFailedError(tt.resp)
			if err.Code != tt.wantCode || err.Message != tt.wantMessage {
				t.Fatalf("code/message = %q/%q, want %q/%q", err.Code, err.Message, tt.wantCode, tt.wantMessage)
			}
			for _, want := range []string{"job-9", tt.wantCode, tt.wantMessage} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Error() = %q, missing %q", err.Error(), want)
				}
			}
			if err.Job != job {
				t.Errorf("Job = %+v, want %+v", err.Job, job)
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
	for name, s := range map[string]error{"ErrMaxAttempts": ErrMaxAttempts, "ErrJobNotCompleted": ErrJobNotCompleted} {
		t.Run(name, func(t *testing.T) {
			wrapped := wrapClientError("ctx", s)
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
	if errors.Is(ErrMaxAttempts, ErrJobNotCompleted) {
		t.Error("sentinels must be distinct")
	}
}

func TestJobSkin(t *testing.T) {
	result, err := ParseUUID(jobResultID)
	if err != nil {
		t.Fatal(err)
	}
	failedErrs := ResponseMeta{Errors: []CodeAndMessage{{Code: "invalid_image", Message: "bad image"}}}
	tests := []struct {
		name     string
		resp     *JobResponse
		handler  http.HandlerFunc
		wantHits int32
		wantName string
		check    func(t *testing.T, err error)
	}{
		{name: "nil", check: func(t *testing.T, err error) {
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("err = %v, want ErrInvalidInput", err)
			}
		}},
		{name: "failed with skin present", resp: &JobResponse{
			ResponseMeta: failedErrs, Job: Job{ID: "j", Status: JobStatusFailed, Result: result}, Skin: &SkinInfo{Name: "embedded"},
		}, check: func(t *testing.T, err error) {
			var jf *JobFailedError
			if !errors.As(err, &jf) || jf.Code != "invalid_image" {
				t.Errorf("err = %v, want *JobFailedError invalid_image", err)
			}
		}},
		{name: "embedded skin", resp: &JobResponse{
			Job: Job{ID: "j", Status: JobStatusCompleted, Result: result}, Skin: &SkinInfo{Name: "embedded"},
		}, wantName: "embedded"},
		{name: "result without skin", resp: &JobResponse{Job: Job{ID: "j", Status: JobStatusCompleted, Result: result}},
			handler:  writeJSON(200, `{"success":true,"skin":{"uuid":"`+jobResultID+`","name":"fetched"}}`),
			wantHits: 1, wantName: "fetched"},
		{name: "no result", resp: &JobResponse{Job: Job{ID: "j", Status: JobStatusProcessing}}, check: func(t *testing.T, err error) {
			if !errors.Is(err, ErrJobNotCompleted) {
				t.Errorf("err = %v, want ErrJobNotCompleted", err)
			}
		}},
		{name: "GetSkin error passed through", resp: &JobResponse{Job: Job{ID: "j", Status: JobStatusCompleted, Result: result}},
			handler: writeJSON(404, `{"success":false,"errors":[{"code":"skin_not_found","message":"nope"}]}`), wantHits: 1,
			check: func(t *testing.T, err error) {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Code != "skin_not_found" || !errors.Is(err, ErrNotFound) {
					t.Errorf("err = %v, want *APIError skin_not_found", err)
				}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			var method, uri string
			srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				method, uri = r.Method, r.RequestURI
				if tt.handler != nil {
					tt.handler(w, r)
				}
			})
			got, err := newTestClient(t, srv.URL).JobSkin(context.Background(), tt.resp)
			if hits.Load() != tt.wantHits {
				t.Errorf("requests = %d, want %d", hits.Load(), tt.wantHits)
			}
			if tt.wantHits == 1 && (method != http.MethodGet || uri != "/v2/skins/"+jobResultID) {
				t.Errorf("request = %s %s", method, uri)
			}
			if tt.check != nil {
				if got != nil {
					t.Errorf("skin = %+v, want nil on error", got)
				}
				tt.check(t, err)
				return
			}
			if err != nil || got == nil || got.Name != tt.wantName {
				t.Fatalf("got = %+v, err = %v, want skin %q", got, err, tt.wantName)
			}
		})
	}
}

func ExampleClient_JobSkin() {
	c, err := NewClient()
	if err != nil {
		return
	}
	// A bare Job, for example from ListJobs, is wrapped in a JobResponse.
	var j Job
	skin, err := c.JobSkin(context.Background(), &JobResponse{Job: j})
	fmt.Println(skin == nil, errors.Is(err, ErrJobNotCompleted))
	// Output: true true
}
