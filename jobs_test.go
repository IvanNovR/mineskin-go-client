package mineskin_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client"
)

func TestJobStatusIsPending(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status  mineskin.JobStatus
		pending bool
	}{
		{mineskin.JobStatusWaiting, true},
		{mineskin.JobStatusProcessing, true},
		{mineskin.JobStatusCompleted, false},
		{mineskin.JobStatusFailed, false},
		{mineskin.JobStatus("unknown"), false},
		{mineskin.JobStatus("weird"), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			t.Parallel()
			if got := tt.status.IsPending(); got != tt.pending {
				t.Errorf("IsPending() = %v, want %v", got, tt.pending)
			}
		})
	}
}

func TestJobUnmarshalJSON(t *testing.T) {
	t.Parallel()
	wantResult := mineskin.UUID{0x01, 0x23, 0xab, 0xcd, 0x45, 0x67, 0x89, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	const dashed = "0123abcd-4567-89ef-0123-456789abcdef"
	const undashed = "0123abcd456789ef0123456789abcdef"

	tests := []struct {
		name    string
		in      string
		want    mineskin.Job
		wantErr bool
	}{
		{
			name: "full job",
			in:   `{"id":"job-1","status":"completed","timestamp":1700000000000,"eta":1700000005000,"result":"` + dashed + `"}`,
			want: mineskin.Job{ID: "job-1", Status: mineskin.JobStatusCompleted, Timestamp: time.UnixMilli(1700000000000),
				ETA: time.UnixMilli(1700000005000), Result: wantResult},
		},
		{name: "timestamp and eta absent", in: `{"id":"a","status":"waiting"}`, want: mineskin.Job{ID: "a", Status: mineskin.JobStatusWaiting}},
		{name: "timestamp and eta zero", in: `{"id":"a","status":"waiting","timestamp":0,"eta":0}`, want: mineskin.Job{ID: "a", Status: mineskin.JobStatusWaiting}},
		{name: "timestamp and eta null", in: `{"id":"a","status":"waiting","timestamp":null,"eta":null}`, want: mineskin.Job{ID: "a", Status: mineskin.JobStatusWaiting}},
		{name: "result absent", in: `{"id":"a","status":"processing"}`, want: mineskin.Job{ID: "a", Status: mineskin.JobStatusProcessing}},
		{name: "result null", in: `{"id":"a","status":"processing","result":null}`, want: mineskin.Job{ID: "a", Status: mineskin.JobStatusProcessing}},
		{name: "result empty string", in: `{"id":"a","status":"processing","result":""}`, want: mineskin.Job{ID: "a", Status: mineskin.JobStatusProcessing}},
		{name: "undashed result", in: `{"id":"a","status":"completed","result":"` + undashed + `"}`, want: mineskin.Job{ID: "a", Status: mineskin.JobStatusCompleted, Result: wantResult}},
		{name: "malformed result", in: `{"id":"a","status":"completed","result":"not-a-uuid"}`, wantErr: true},
		{name: "non-string result", in: `{"id":"a","status":"completed","result":123}`, wantErr: true},
		{name: "unknown status kept", in: `{"id":"a","status":"weird"}`, want: mineskin.Job{ID: "a", Status: mineskin.JobStatus("weird")}},
		{name: "non-string id", in: `{"id":123,"status":"waiting"}`, wantErr: true},
		{name: "non-integer timestamp", in: `{"id":"a","timestamp":"x"}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got mineskin.Job
			err := json.Unmarshal([]byte(tt.in), &got)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got job %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.ID != tt.want.ID || got.Status != tt.want.Status || got.Result != tt.want.Result ||
				!got.Timestamp.Equal(tt.want.Timestamp) || !got.ETA.Equal(tt.want.ETA) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestJobIsTerminal(t *testing.T) {
	t.Parallel()
	result := mineskin.UUID{1}
	tests := []struct {
		name string
		job  mineskin.Job
		want bool
	}{
		{"failed", mineskin.Job{Status: mineskin.JobStatusFailed}, true},
		{"completed with result", mineskin.Job{Status: mineskin.JobStatusCompleted, Result: result}, true},
		{"completed without result", mineskin.Job{Status: mineskin.JobStatusCompleted}, false},
		{"unknown", mineskin.Job{Status: mineskin.JobStatus("weird"), Result: result}, false},
		{"waiting", mineskin.Job{Status: mineskin.JobStatusWaiting}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.job.IsTerminal(); got != tt.want {
				t.Errorf("IsTerminal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestJobUnmarshalJSONIgnoresPreviousState(t *testing.T) {
	t.Parallel()
	prev := mineskin.Job{ID: "old", Status: mineskin.JobStatusFailed, Result: mineskin.UUID{9}, ETA: time.UnixMilli(5)}
	if err := json.Unmarshal([]byte(`{"id":"new","status":"waiting"}`), &prev); err != nil {
		t.Fatal(err)
	}
	if want := (mineskin.Job{ID: "new", Status: mineskin.JobStatusWaiting}); prev != want {
		t.Errorf("Job = %+v, want %+v", prev, want)
	}
}

func TestGetJobDecodesJobAndSkin(t *testing.T) {
	t.Parallel()
	var method, uri string
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		method, uri = r.Method, r.RequestURI
		w.Header().Set("MineSkin-Breadcrumb", "crumb-j")
		writeJSON(200, `{"success":true,"job":{"id":"job-1","status":"completed","result":"`+jobResultID+`"},"skin":{"uuid":"`+jobResultID+`","name":"n"}}`)(w, r)
	})
	got, err := newTestClient(t, srv.URL).GetJob(context.Background(), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet || uri != "/v2/queue/job-1" {
		t.Errorf("request = %s %s", method, uri)
	}
	if got.Job.ID != "job-1" || got.Job.Status != mineskin.JobStatusCompleted || got.Job.Result.String() != jobResultID {
		t.Errorf("job = %+v", got.Job)
	}
	if got.Meta.Breadcrumb != "crumb-j" {
		t.Errorf("meta breadcrumb = %q, want crumb-j", got.Meta.Breadcrumb)
	}
	if got.Skin == nil || got.Skin.Name != "n" {
		t.Errorf("skin = %+v", got.Skin)
	}

	srv2 := newTestServer(t, writeJSON(200, `{"success":true,"job":{"id":"job-2","status":"waiting"}}`))
	got, err = newTestClient(t, srv2.URL).GetJob(context.Background(), "job-2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Skin != nil {
		t.Errorf("skin = %+v, want nil", got.Skin)
	}
}

func TestGetJobRejectsInvalidIDWithoutRequest(t *testing.T) {
	t.Parallel()
	for name, id := range map[string]string{"empty": "", "dot": ".", "dot dot": ".."} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var hits atomic.Int32
			srv := newTestServer(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
			got, err := newTestClient(t, srv.URL).GetJob(context.Background(), id)
			if !errors.Is(err, mineskin.ErrInvalidInput) || got != nil {
				t.Fatalf("got = %+v, err = %v, want ErrInvalidInput", got, err)
			}
			if hits.Load() != 0 {
				t.Error("request was sent")
			}
		})
	}
}

func TestGetJobEscapesIDAsSingleSegment(t *testing.T) {
	t.Parallel()
	var uri string
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		uri = r.RequestURI
		writeJSON(200, `{"success":true,"job":{"id":"x"}}`)(w, r)
	})
	if _, err := newTestClient(t, srv.URL).GetJob(context.Background(), "a/b?c#d e"); err != nil {
		t.Fatal(err)
	}
	if want := "/v2/queue/a%2Fb%3Fc%23d%20e"; uri != want {
		t.Errorf("RequestURI = %q, want %q", uri, want)
	}
}

func TestGetJobNotFound(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, writeJSON(404, `{"success":false,"errors":[{"code":"job_not_found","message":"Job not found"}]}`))
	got, err := newTestClient(t, srv.URL).GetJob(context.Background(), "nope")
	if got != nil {
		t.Errorf("response = %+v, want nil", got)
	}
	var apiErr *mineskin.APIError
	if !errors.As(err, &apiErr) || !errors.Is(err, mineskin.ErrNotFound) || apiErr.Meta.Status != 404 {
		t.Fatalf("err = %T %v, want 404 *APIError matching ErrNotFound", err, err)
	}
}

func TestListJobsDecodes(t *testing.T) {
	t.Parallel()
	var method, uri string
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		method, uri = r.Method, r.RequestURI
		w.Header().Set("MineSkin-Breadcrumb", "crumb-l")
		writeJSON(200, `{"success":true,"jobs":[{"id":"a","status":"waiting"},{"id":"b","status":"processing"}]}`)(w, r)
	})
	got, err := newTestClient(t, srv.URL).ListJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet || uri != "/v2/queue" {
		t.Errorf("request = %s %s", method, uri)
	}
	if len(got.Jobs) != 2 || got.Jobs[0].ID != "a" || got.Jobs[1].Status != mineskin.JobStatusProcessing {
		t.Errorf("jobs = %+v", got.Jobs)
	}
	if got.Meta.Breadcrumb != "crumb-l" {
		t.Errorf("list meta breadcrumb = %q, want crumb-l", got.Meta.Breadcrumb)
	}

	for name, body := range map[string]string{
		"absent": `{"success":true}`,
		"null":   `{"success":true,"jobs":null}`,
		"empty":  `{"success":true,"jobs":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestServer(t, writeJSON(200, body))
			got, err := newTestClient(t, s.URL).ListJobs(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.Jobs == nil || len(got.Jobs) != 0 {
				t.Errorf("Jobs = %#v, want empty non-nil", got.Jobs)
			}
		})
	}

	for name, body := range map[string]string{
		"object": `{"success":true,"jobs":{}}`,
		"string": `{"success":true,"jobs":"x"}`,
	} {
		t.Run("non-array "+name, func(t *testing.T) {
			s := newTestServer(t, writeJSON(200, body))
			_, err := newTestClient(t, s.URL).ListJobs(context.Background())
			assertParseError(t, err)
		})
	}
}

func TestGetJobMalformedResultIsParseError(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, writeJSON(200, `{"success":true,"job":{"id":"j","status":"completed","result":"not-a-uuid"}}`))
	got, err := newTestClient(t, srv.URL).GetJob(context.Background(), "j")
	assertParseError(t, err)
	if got != nil {
		t.Fatalf("got = %+v, want nil", got)
	}
}

func TestNewSentinelsAreDistinct(t *testing.T) {
	t.Parallel()
	for name, s := range map[string]error{"ErrMaxAttempts": mineskin.ErrMaxAttempts} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			wrapped := fmt.Errorf("mineskin: ctx: %w", s)
			if !errors.Is(wrapped, s) {
				t.Errorf("wrapped %v does not match %v", wrapped, s)
			}
			if errors.Is(s, mineskin.ErrInvalidInput) || errors.Is(s, mineskin.ErrNotFound) {
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
	t.Parallel()
	resp := &mineskin.JobResponse{Job: mineskin.Job{ID: "j", Status: mineskin.JobStatusCompleted, Result: fixtureUUID(jobResultID)}}
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
