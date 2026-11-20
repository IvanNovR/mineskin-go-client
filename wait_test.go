package mineskin_test

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client"
)

func TestWaitForJob_Completes(t *testing.T) {
	t.Parallel()
	var gets atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		status := []string{"waiting", "processing", "completed"}[min(int(gets.Add(1))-1, 2)]
		writeJSON(200, jobJSON("j1", status))(w, r)
	})
	c := newTestClient(t, srv.URL, fastPolling())

	got, err := c.WaitForJob(waitCtx(t), mineskin.Job{ID: "j1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Job.Status != mineskin.JobStatusCompleted || got.Job.Result.String() != jobResultID {
		t.Errorf("job = %+v", got.Job)
	}
	if gets.Load() != 3 {
		t.Errorf("GET count = %d, want 3", gets.Load())
	}
}

func TestWaitForJob_Failed(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, writeJSON(200, `{"success":true,"errors":[{"code":"bad_image","message":"Bad image"}],"job":{"id":"j1","status":"failed"}}`))
	c := newTestClient(t, srv.URL, fastPolling())

	got, err := c.WaitForJob(waitCtx(t), mineskin.Job{ID: "j1"})
	var jfe *mineskin.JobFailedError
	if !errors.As(err, &jfe) || got != nil {
		t.Fatalf("got = %+v err = %v, want *JobFailedError", got, err)
	}
	if jfe.Job.ID != "j1" || len(jfe.Errors) != 1 || jfe.Errors[0].Code != "bad_image" {
		t.Errorf("err = %+v", jfe)
	}
}

func TestWaitForJob_MaxAttempts(t *testing.T) {
	t.Parallel()
	var gets atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gets.Add(1)
		writeJSON(200, jobJSON("j1", "waiting"))(w, r)
	})
	c := newTestClient(t, srv.URL, mineskin.WithJobPolling(mineskin.PollOptions{
		InitialDelay: time.Millisecond, MaxAttempts: 3,
		Interval: mineskin.PollIntervalFunc(func(int) time.Duration { return time.Millisecond }),
	}))

	_, err := c.WaitForJob(waitCtx(t), mineskin.Job{ID: "j1"})
	if !errors.Is(err, mineskin.ErrMaxAttempts) {
		t.Fatalf("err = %v, want ErrMaxAttempts", err)
	}
	if gets.Load() != 3 {
		t.Errorf("GET count = %d, want 3", gets.Load())
	}
}

func TestWaitForJob_EscapesID(t *testing.T) {
	t.Parallel()
	var uri atomic.Value
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		uri.Store(r.RequestURI)
		writeJSON(200, jobJSON("a/b?c#d e", "completed"))(w, r)
	})
	c := newTestClient(t, srv.URL, fastPolling())

	if _, err := c.WaitForJob(waitCtx(t), mineskin.Job{ID: "a/b?c#d e"}); err != nil {
		t.Fatal(err)
	}
	if want := "/v2/queue/a%2Fb%3Fc%23d%20e"; uri.Load() != want {
		t.Errorf("request URI = %v, want %s", uri.Load(), want)
	}
}
