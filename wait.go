package mineskin

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/IvanNovR/mineskin-go-client/internal/poll"
)

// WaitForJob waits until job is completed or failed and returns its final
// state. Waits for several jobs share one background poller, which uses a
// single list request instead of one request per job when more than a few are
// pending. Waits for the same job id share one stream of requests; each waiter
// gets its own deep copy of the response (or of the error's data), so a caller
// may modify its result freely.
//
// WaitForJob panics when ctx is nil, as the standard library does. A job id
// that is empty, "." or ".." fails with ErrInvalidInput without a request. The
// total wait is bounded only by ctx and PollOptions.MaxAttempts; WithTimeout
// bounds each status request, not the wait. A failed job gives a
// *JobFailedError, and a job still unfinished after PollOptions.MaxAttempts
// checks gives ErrMaxAttempts. When ctx ends, the error matches ctx.Err(); the
// job stops being polled unless another waiter still needs it.
// Errors of the status requests are returned as they are. A wait that Close
// interrupts, and any wait started after Close, fails with ErrClientClosed.
//
// When more than a few jobs are pending, the poller checks them with one list
// request. A job reported done by the list is completed with whatever the
// follow-up GET returns, so a completed job may carry no result yet: its
// Job.Result is then the zero UUID, which GetSkin rejects.
func (c *Client) WaitForJob(ctx context.Context, job Job) (*JobResponse, error) {
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	if err := validateJobID(job.ID); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("mineskin: wait for job: %w", err)
	}
	pj := c.poller.Register(job.ID, job.ETA)
	select {
	case <-pj.Done():
		return copyResult(pj)
	case <-ctx.Done():
		select {
		case <-pj.Done(): // a finished result wins over a cancelled ctx
			return copyResult(pj)
		default:
		}
		c.poller.Leave(pj)
		return nil, fmt.Errorf("mineskin: wait for job: %w", ctx.Err())
	}
}

// copyResult returns the outcome of a finished job with its own response
// copy: the poller's errors are mapped to the public ones and a failed job
// becomes a *JobFailedError.
func copyResult(w *poll.Waiter[*JobResponse]) (*JobResponse, error) {
	resp, state, err := w.Result()
	if err != nil {
		return nil, mapPollErr(err)
	}
	if state.Failed {
		return nil, newJobFailedError(cloneJobResponse(resp))
	}
	return cloneJobResponse(resp), nil
}

// cloneJobResponse returns a deep copy of r that shares no memory with it:
// the header map, the warning, message and error lists, the raw links and the
// skin are all copied, so one waiter's changes never reach another.
func cloneJobResponse(r *JobResponse) *JobResponse {
	cp := *r
	cp.Meta.Header = r.Meta.Header.Clone()
	cp.Meta.Warnings = slices.Clone(r.Meta.Warnings)
	cp.Meta.Messages = slices.Clone(r.Meta.Messages)
	cp.Meta.Links = bytes.Clone(r.Meta.Links)
	cp.errs = slices.Clone(r.errs)
	if r.Skin != nil {
		skin := *r.Skin // Skin holds only values, so a struct copy is deep
		cp.Skin = &skin
	}
	return &cp
}
