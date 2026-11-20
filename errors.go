package mineskin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// errorPrefix starts the text of every error the package returns. Exactly one
// prefix appears in a message, at the outermost layer; the internal packages
// add none.
const errorPrefix = "mineskin: "

// Sentinel errors for use with errors.Is.
var (
	// ErrInvalidInput matches configuration and argument validation errors.
	ErrInvalidInput = errors.New("mineskin: invalid input")

	// ErrNotFound matches errors caused by an HTTP 404 response.
	ErrNotFound = errors.New("mineskin: not found")

	// ErrRateLimited matches errors caused by an HTTP 429 response, whether
	// the body was a MineSkin JSON error or something else such as an HTML
	// page of a proxy.
	ErrRateLimited = errors.New("mineskin: rate limited")

	// ErrMaxAttempts matches the error returned when job polling gave up after
	// the maximum number of attempts.
	ErrMaxAttempts = errors.New("mineskin: job polling reached max attempts")

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
// 404 and ErrRateLimited when it is 429. Use errors.As to obtain it.
type APIError struct {
	// Code is the code of the first error of the response, or empty when the
	// server sent none.
	Code string
	// Message is the message of the first error of the response, or empty when
	// the server sent none.
	Message string
	// Errors lists every error reported by the API; empty, never nil, when
	// there were none.
	Errors []CodeAndMessage
	// RateLimit is the rateLimit field of the response; zero when absent or
	// malformed.
	RateLimit RateLimit
	// Usage is the usage field of the response; zero when absent or malformed.
	Usage Usage
	// RetryAfter is the Retry-After header (RFC 9110, delta-seconds or
	// HTTP-date), or 0 when absent or invalid.
	RetryAfter time.Duration
	// Meta holds the status, headers and messages of the response.
	Meta ResponseMeta
}

// Error implements the error interface. The text is
// "mineskin: API error (HTTP <status>[, <code>])[: <message>]"; the code and
// the message are left out when the server sent none.
func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mineskin: API error (HTTP %d", e.Meta.Status)
	if e.Code != "" {
		b.WriteString(", " + e.Code)
	}
	b.WriteString(")")
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	return b.String()
}

// Is reports whether target is the sentinel of the HTTP status: ErrNotFound
// for 404 and ErrRateLimited for 429.
func (e *APIError) Is(target error) bool { return matchesStatus(e.Meta.Status, target) }

// statusSentinel returns the sentinel that classifies an HTTP status: 404 is
// ErrNotFound and 429 is ErrRateLimited. Other statuses have none (nil).
func statusSentinel(status int) error {
	switch status {
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusTooManyRequests:
		return ErrRateLimited
	default:
		return nil
	}
}

// matchesStatus is the Is logic shared by the response error types.
func matchesStatus(status int, target error) bool {
	s := statusSentinel(status)
	return s != nil && target == s //nolint:errorlint // sentinel identity is the contract of Is
}

// IsRetryable reports whether retrying the call that returned err may
// succeed. It is true for an *APIError or *UnexpectedResponseError whose HTTP
// status is 408, 429 or 500 and above except 501 and 505, and for a network
// error that is a timeout. It is false for nil, for a cancelled or expired
// context, for ErrClientClosed, and for everything else. The checks look
// through wrapping, so the result is the same for a wrapped error.
func IsRetryable(err error) bool {
	// The context checks come first: context.DeadlineExceeded implements
	// Timeout() and would otherwise count as a retryable network timeout.
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrClientClosed) {
		return false
	}
	var api *APIError
	if errors.As(err, &api) {
		return transport.IsRetryableStatus(api.Meta.Status)
	}
	var unexpected *UnexpectedResponseError
	if errors.As(err, &unexpected) {
		return transport.IsRetryableStatus(unexpected.Status)
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
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
	// Job is the failed job.
	Job Job
	// Errors lists the errors the API reported with the job; empty when it
	// reported none.
	Errors []CodeAndMessage
	// Meta holds the status, headers and messages of the response.
	Meta ResponseMeta
}

// Error implements the error interface. The text is
// "mineskin: job <id> failed[ (<code>): <message>]", where code and message
// are those of the first entry of Errors.
func (e *JobFailedError) Error() string {
	if len(e.Errors) == 0 {
		return fmt.Sprintf("mineskin: job %s failed", e.Job.ID)
	}
	return fmt.Sprintf("mineskin: job %s failed (%s): %s", e.Job.ID, e.Errors[0].Code, e.Errors[0].Message)
}

// newJobFailedError builds the error for a failed job from its response.
func newJobFailedError(r *JobResponse) *JobFailedError {
	return &JobFailedError{Job: r.Job, Errors: r.errs, Meta: r.Meta}
}
