package mineskin

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// Sentinel errors for use with errors.Is.
var (
	// ErrInvalidInput matches configuration and argument validation errors.
	ErrInvalidInput = errors.New("mineskin: invalid input")

	// ErrNotFound matches errors caused by an HTTP 404 response.
	ErrNotFound = errors.New("mineskin: not found")

	// ErrMaxAttempts matches the error returned when job polling gave up after
	// the maximum number of attempts.
	ErrMaxAttempts = errors.New("mineskin: job polling reached max attempts")

	// ErrJobNotCompleted matches the error returned by JobSkin when the job has
	// no result yet.
	ErrJobNotCompleted = errors.New("mineskin: job is not completed")

	// ErrClientClosed matches the error returned by every call made after
	// Client.Close, and by calls that Close interrupts while they are queued,
	// waiting for a job or in flight.
	ErrClientClosed = errors.New("mineskin: client is closed")
)

// CodeAndMessage is a single entry of the errors, warnings or messages lists
// of a MineSkin API response.
type CodeAndMessage struct {
	// Code is the machine-readable code, for example "invalid_api_key".
	Code string `json:"code"`
	// Message is the human-readable description.
	Message string `json:"message"`
}

// APIError is returned when the MineSkin API reports that a request was not
// successful. It matches ErrNotFound with errors.Is when the HTTP status is
// 404. Use errors.As to obtain it.
type APIError struct {
	// Code is the first error code of the response, or "request_failed".
	Code string
	// Message is the first error message of the response, or "Request Failed".
	Message string
	// RateLimit is the rateLimit field of the response, or nil when absent or
	// malformed.
	RateLimit *RateLimitInfo
	// Usage is the usage field of the response, or nil when absent or
	// malformed.
	Usage *UsageInfo
	// RetryAfter is the Retry-After header (RFC 9110, delta-seconds or
	// HTTP-date), or 0 when absent or invalid.
	RetryAfter time.Duration
	// ResponseMeta holds the status, lists and headers of the response.
	ResponseMeta
}

// IsRetryable reports whether the error is likely temporary: status 408, 429,
// or 500 and above except 501 and 505.
func (e *APIError) IsRetryable() bool { return transport.IsRetryableStatus(e.Status) }

// Error implements the error interface.
func (e *APIError) Error() string {
	return fmt.Sprintf("mineskin: API error (HTTP %d, %s): %s", e.Status, e.Code, e.Message)
}

// Is reports whether target is ErrNotFound and the HTTP status is 404.
func (e *APIError) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}

// invalidInput returns an error wrapping ErrInvalidInput with a formatted
// description. The description never contains the API key or the input that
// failed validation.
func invalidInput(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidInput}, args...)...)
}

// JobFailedError is returned when a job has the FAILED status. Use errors.As
// to obtain it.
type JobFailedError struct {
	// Code is the first error code of the response, or "job_failed".
	Code string
	// Message is the first error message of the response, or "Job failed".
	Message string
	// Job is the failed job.
	Job Job
	// ResponseMeta holds the status, lists and headers of the response.
	ResponseMeta
}

// Error implements the error interface.
func (e *JobFailedError) Error() string {
	return fmt.Sprintf("mineskin: job %s failed (%s): %s", e.Job.ID, e.Code, e.Message)
}

// newJobFailedError builds the error for a failed job, using the first
// reported error or the defaults.
func newJobFailedError(r *JobResponse) *JobFailedError {
	e := &JobFailedError{Code: defaultJobFailedCode, Message: defaultJobFailedMessage, Job: r.Job, ResponseMeta: r.ResponseMeta}
	if len(r.Errors) > 0 {
		e.Code = r.Errors[0].Code
		e.Message = r.Errors[0].Message
	}
	return e
}
