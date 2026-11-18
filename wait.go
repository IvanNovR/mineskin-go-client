package mineskin

import "context"

// WaitForJob waits until job is completed or failed and returns its final
// state. Waits for several jobs share one background poller, which uses a
// single list request instead of one request per job when more than a few are
// pending. Waits for the same job id share one stream of requests; each waiter
// gets its own shallow copy of the response.
//
// A nil ctx, or a job id that is empty, "." or "..", fails with
// ErrInvalidInput without a request. A failed job gives a *JobFailedError, and
// a job still unfinished after PollOptions.MaxAttempts checks gives
// ErrMaxAttempts. When ctx ends, the error matches both ctx.Err() and
// ErrClient; the job stops being polled unless another waiter still needs it.
// Errors of the status requests are returned as they are.
func (c *Client) WaitForJob(ctx context.Context, job Job) (*JobResponse, error) {
	if ctx == nil {
		return nil, newInvalidInputError("context is nil")
	}
	if err := validateJobID(job.ID); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, wrapClientError("wait for job", err)
	}
	pj := c.poller.register(job)
	select {
	case <-pj.done:
		return copyResult(pj)
	case <-ctx.Done():
		c.poller.leave(pj)
		return nil, wrapClientError("wait for job", ctx.Err())
	}
}

// copyResult returns the outcome of a finished pj with its own response copy.
func copyResult(pj *pendingJob) (*JobResponse, error) {
	if pj.err != nil {
		return nil, pj.err
	}
	cp := *pj.resp
	return &cp, nil
}
