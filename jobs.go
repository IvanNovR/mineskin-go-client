package mineskin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"time"
)

const (
	fieldJobs = "jobs"

	defaultJobFailedCode    = "job_failed"
	defaultJobFailedMessage = "Job failed"
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
	// JobStatusUnknown is the status for jobs whose state is not known.
	JobStatusUnknown JobStatus = "unknown"
)

// IsPending reports whether the job is still waiting or processing.
func (s JobStatus) IsPending() bool {
	return s == JobStatusWaiting || s == JobStatusProcessing
}

// IsDone reports whether the job is completed or failed.
func (s JobStatus) IsDone() bool {
	return s == JobStatusCompleted || s == JobStatusFailed
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
	// Breadcrumb is the mineskin-breadcrumb header of the response.
	Breadcrumb string
}

// UnmarshalJSON decodes a job. timestamp and eta are epoch milliseconds; an
// absent, null or 0 value gives the zero time. An absent, null or empty result
// gives the zero UUID; a malformed result is an error. A null value leaves j
// unchanged.
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
	out := Job{ID: fields.ID, Status: fields.Status, Breadcrumb: j.Breadcrumb}
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

// isPollDone reports whether polling for j can stop: the job failed, or
// completed with a result. Completed without a result and unknown statuses
// keep polling.
func isPollDone(j Job) bool {
	return j.Status == JobStatusFailed ||
		(j.Status == JobStatusCompleted && j.Result != UUID{})
}

// JobResponse is the response to a job lookup.
type JobResponse struct {
	ResponseMeta
	// Job is the requested job.
	Job Job
	// Skin is the resulting skin when the response embeds it, or nil.
	Skin *SkinInfo
}

// JobListResponse is the response to listing jobs.
type JobListResponse struct {
	ResponseMeta
	// Jobs are the queued jobs; empty, never nil, when there are none.
	Jobs []Job
}

// GetJob fetches a job by its id. An empty id, ".", or ".." fails with
// ErrInvalidInput without sending a request; any other id is escaped as a
// single path segment.
func (c *Client) GetJob(ctx context.Context, id string) (*JobResponse, error) {
	if err := validateJobID(id); err != nil {
		return nil, err
	}
	var out JobResponse
	meta, err := c.get(ctx, queuePath+"/"+url.PathEscape(id), func(obj map[string]json.RawMessage, _ []byte) error {
		if v := obj[fieldJob]; len(v) > 0 {
			if err := json.Unmarshal(v, &out.Job); err != nil {
				return err
			}
		}
		if v := obj[fieldSkin]; len(v) > 0 && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			var skin SkinInfo
			if err := json.Unmarshal(v, &skin); err != nil {
				return err
			}
			out.Skin = &skin
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out.ResponseMeta = meta
	out.Job.Breadcrumb = meta.Breadcrumb
	if out.Skin != nil {
		out.Skin.Breadcrumb = meta.Breadcrumb
	}
	return &out, nil
}

// ListJobs lists the jobs in the queue. The Breadcrumb of the returned jobs is
// empty; the breadcrumb of the list request is on the ResponseMeta.
func (c *Client) ListJobs(ctx context.Context) (*JobListResponse, error) {
	out := JobListResponse{Jobs: []Job{}}
	meta, err := c.get(ctx, queuePath, func(obj map[string]json.RawMessage, _ []byte) error {
		v := obj[fieldJobs]
		if len(v) == 0 {
			return nil
		}
		var jobs []Job
		if err := json.Unmarshal(v, &jobs); err != nil {
			return err
		}
		if jobs != nil {
			out.Jobs = jobs
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out.ResponseMeta = meta
	return &out, nil
}

// validateJobID rejects ids that are empty or would collapse the path.
func validateJobID(id string) error {
	if id == "" || id == "." || id == ".." {
		return newInvalidInputError("job id is empty or a dot segment")
	}
	return nil
}

// JobSkin returns the skin of a job. A failed job gives a *JobFailedError
// without a request, even if the response embeds a skin. An embedded skin is
// returned without a request; otherwise the skin named by the job result is
// fetched with GetSkin and its errors are returned unchanged. A job without a
// result gives ErrJobNotCompleted, and a nil response gives ErrInvalidInput.
func (c *Client) JobSkin(ctx context.Context, r *JobResponse) (*SkinInfo, error) {
	if r == nil {
		return nil, newInvalidInputError("job response is nil")
	}
	if r.Job.Status == JobStatusFailed {
		return nil, newJobFailedError(r)
	}
	if r.Skin != nil {
		return r.Skin, nil
	}
	if r.Job.Result == (UUID{}) {
		return nil, ErrJobNotCompleted
	}
	resp, err := c.GetSkin(ctx, r.Job.Result.String())
	if err != nil {
		return nil, err
	}
	return &resp.Skin, nil
}
