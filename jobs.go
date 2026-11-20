package mineskin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"time"
)

// JobStatus is the state of a queued generation job.
type JobStatus string

// Known job statuses. Other values received from the API are kept unchanged.
const (
	// JobStatusWaiting means the job is queued and not yet started.
	JobStatusWaiting JobStatus = "waiting"
	// JobStatusProcessing means the job is being generated.
	JobStatusProcessing JobStatus = "processing"
	// JobStatusCompleted means the job finished successfully.
	JobStatusCompleted JobStatus = "completed"
	// JobStatusFailed means the job failed.
	JobStatusFailed JobStatus = "failed"
)

// IsPending reports whether the job is still waiting or processing.
func (s JobStatus) IsPending() bool {
	return s == JobStatusWaiting || s == JobStatusProcessing
}

// Job is a queued skin generation job.
type Job struct {
	// ID is the job id.
	ID string
	// Status is the job state; unknown wire values are kept unchanged.
	Status JobStatus
	// Timestamp is when the job was created; zero when absent.
	Timestamp time.Time
	// ETA is the estimated completion time; zero when absent.
	ETA time.Time
	// Result is the resulting skin UUID; zero until the job has a result.
	Result UUID
}

// UnmarshalJSON decodes a job. timestamp and eta are epoch milliseconds; an
// absent, null or 0 value gives the zero time. An absent, null or empty result
// gives the zero UUID; a malformed result is an error. A null value leaves j
// unchanged; any other value replaces all of j.
func (j *Job) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil
	}
	var fields struct {
		ID        string          `json:"id"`
		Status    JobStatus       `json:"status"`
		Timestamp json.RawMessage `json:"timestamp"`
		ETA       json.RawMessage `json:"eta"`
		Result    *string         `json:"result"`
	}
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	out := Job{ID: fields.ID, Status: fields.Status}
	var err error
	if out.Timestamp, err = optionalMillisTime(fields.Timestamp); err != nil {
		return err
	}
	if out.ETA, err = optionalMillisTime(fields.ETA); err != nil {
		return err
	}
	if fields.Result != nil && *fields.Result != "" {
		if out.Result, err = ParseUUID(*fields.Result); err != nil {
			return err
		}
	}
	*j = out
	return nil
}

// optionalMillisTime converts epoch milliseconds to a time; absent, null or 0
// give the zero time.
func optionalMillisTime(v json.RawMessage) (time.Time, error) {
	ms, ok, err := optionalInt64(v)
	if err != nil || !ok || ms == 0 {
		return time.Time{}, err
	}
	return time.UnixMilli(ms), nil
}

// IsTerminal reports whether the job will not change any more: it failed, or
// it completed and has a result. A job that is completed without a result yet
// is not terminal, and neither is a job with a status this client does not
// know; WaitForJob keeps polling both.
func (j Job) IsTerminal() bool {
	return j.Status == JobStatusFailed ||
		(j.Status == JobStatusCompleted && !j.Result.IsZero())
}

// JobResponse is the response to a job lookup. Responses returned by
// WaitForJob may share pointers (Skin, the Meta lists) with other waiters on
// the same job; treat them as read-only.
type JobResponse struct {
	// Meta holds the status, headers and messages of the response.
	Meta ResponseMeta
	// Job is the requested job.
	Job Job
	// Skin is the resulting skin when the response embeds it, or nil. When it
	// is nil, fetch the skin of a completed job with
	// c.GetSkin(ctx, resp.Job.Result).
	Skin *Skin
	// errs are the errors the API sent with the response; they become the
	// Errors of the *JobFailedError of a failed job.
	errs []CodeAndMessage
}

// JobListResponse is the response to listing jobs.
type JobListResponse struct {
	// Meta holds the status, headers and messages of the response.
	Meta ResponseMeta
	// Jobs are the queued jobs; empty, never nil, when there are none.
	Jobs []Job
}

// GetJob fetches a job by its id. An empty id, ".", or ".." fails with
// ErrInvalidInput without sending a request; any other id is escaped as a
// single path segment.
func (c *Client) GetJob(ctx context.Context, id string) (*JobResponse, error) {
	return c.getJobGated(ctx, id, nil)
}

// getJobGated is GetJob with a start gate: once the request holds a get
// throttle slot, proceed (when non-nil) is called and a false result abandons
// the request with errCheckSkipped, sending nothing. The poller uses it so a
// job whose waiters left while its check was queued is not fetched.
func (c *Client) getJobGated(ctx context.Context, id string, proceed func() bool) (*JobResponse, error) {
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	if err := validateJobID(id); err != nil {
		return nil, err
	}
	wire, meta, err := getResponse[jobWire](ctx, c, queuePath+"/"+url.PathEscape(id), proceed)
	if err != nil {
		return nil, err
	}
	return &JobResponse{Meta: meta, Job: wire.Job, Skin: wire.Skin, errs: wire.Errors}, nil
}

// jobWire is the wire shape of a job lookup. An absent or null job leaves the
// zero Job and an absent or null skin leaves Skin nil.
type jobWire struct {
	envelope
	Job  Job   `json:"job"`
	Skin *Skin `json:"skin"`
}

// jobListWire is the wire shape of the job list; an absent or null list is
// made empty by ListJobs.
type jobListWire struct {
	Jobs []Job `json:"jobs"`
}

// ListJobs lists the jobs in the queue. The breadcrumb of the request is on
// the Meta of the response.
func (c *Client) ListJobs(ctx context.Context) (*JobListResponse, error) {
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	wire, meta, err := getResponse[jobListWire](ctx, c, queuePath, nil)
	if err != nil {
		return nil, err
	}
	out := JobListResponse{Meta: meta, Jobs: wire.Jobs}
	if out.Jobs == nil {
		out.Jobs = []Job{}
	}
	return &out, nil
}

// validateJobID rejects ids that are empty or would collapse the path.
func validateJobID(id string) error {
	if id == "" || id == "." || id == ".." {
		return invalidInput("job id is empty or a dot segment")
	}
	return nil
}
