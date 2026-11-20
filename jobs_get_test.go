package mineskin

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
)

const jobResultID = "744c993e-0e8a-4f0b-9b6a-2f3c7a1b5d11"

func TestGetJobDecodesJobAndSkin(t *testing.T) {
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
	if got.Job.ID != "job-1" || got.Job.Status != JobStatusCompleted || got.Job.Result.String() != jobResultID {
		t.Errorf("job = %+v", got.Job)
	}
	if got.Job.Breadcrumb != "crumb-j" || got.Breadcrumb != "crumb-j" {
		t.Errorf("job breadcrumb = %q, meta = %q", got.Job.Breadcrumb, got.Breadcrumb)
	}
	if got.Skin == nil || got.Skin.Name != "n" || got.Skin.Breadcrumb != "crumb-j" {
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
	for name, id := range map[string]string{"empty": "", "dot": ".", "dot dot": ".."} {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			srv := newTestServer(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
			got, err := newTestClient(t, srv.URL).GetJob(context.Background(), id)
			if !errors.Is(err, ErrInvalidInput) || got != nil {
				t.Fatalf("got = %+v, err = %v, want ErrInvalidInput", got, err)
			}
			if hits.Load() != 0 {
				t.Error("request was sent")
			}
		})
	}
}

func TestGetJobEscapesIDAsSingleSegment(t *testing.T) {
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
	srv := newTestServer(t, writeJSON(404, `{"success":false,"errors":[{"code":"job_not_found","message":"Job not found"}]}`))
	got, err := newTestClient(t, srv.URL).GetJob(context.Background(), "nope")
	if got != nil {
		t.Errorf("response = %+v, want nil", got)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !errors.Is(err, ErrNotFound) || apiErr.Status != 404 {
		t.Fatalf("err = %T %v, want 404 *APIError matching ErrNotFound", err, err)
	}
}

func TestListJobsDecodes(t *testing.T) {
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
	if len(got.Jobs) != 2 || got.Jobs[0].ID != "a" || got.Jobs[1].Status != JobStatusProcessing {
		t.Errorf("jobs = %+v", got.Jobs)
	}
	for _, j := range got.Jobs {
		if j.Breadcrumb != "" {
			t.Errorf("job %q breadcrumb = %q, want empty", j.ID, j.Breadcrumb)
		}
	}
	if got.Breadcrumb != "crumb-l" {
		t.Errorf("list meta breadcrumb = %q, want crumb-l", got.Breadcrumb)
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
			if _, err := newTestClient(t, s.URL).ListJobs(context.Background()); err == nil {
				t.Errorf("err = %v, want parse error", err)
			}
		})
	}
}

func TestGetJobMalformedResultIsParseError(t *testing.T) {
	srv := newTestServer(t, writeJSON(200, `{"success":true,"job":{"id":"j","status":"completed","result":"not-a-uuid"}}`))
	got, err := newTestClient(t, srv.URL).GetJob(context.Background(), "j")
	if err == nil || got != nil {
		t.Fatalf("got = %+v, err = %v, want a parse error error", got, err)
	}
}
