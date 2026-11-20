// Package transport sends HTTP requests to the MineSkin API and interprets the
// raw responses at the byte level. It knows nothing about the public types of
// the client: it returns plain responses and raw errors, and the caller maps
// them. It must not import the root package.
package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

// MaxResponseBodyBytes is the largest response body that is read. A larger
// body is an error, never silently truncated.
const MaxResponseBodyBytes = 10 << 20

// HeaderRetryAfter is the Retry-After header name.
const HeaderRetryAfter = "Retry-After"

// HeaderMineskinCrumb is the header that carries the MineSkin breadcrumb.
const HeaderMineskinCrumb = "mineskin-breadcrumb"

// Body is a request payload with its Content-Type. It is never logged.
type Body struct {
	ContentType string
	Data        []byte
}

// reader returns the payload reader; a nil body is an empty one.
func (b *Body) reader() io.Reader {
	if b == nil {
		return http.NoBody
	}
	return bytes.NewReader(b.Data)
}

// Response is a fully read HTTP response, before any interpretation.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	// Received is the Doer clock time at which the response arrived.
	Received time.Time
}

// BuildError reports that the request could not be built, for example because
// of an invalid method or URL. Nothing was sent.
type BuildError struct {
	Err error
}

// Error implements the error interface.
func (e *BuildError) Error() string { return e.Err.Error() }

// Unwrap returns the underlying cause.
func (e *BuildError) Unwrap() error { return e.Err }

// Doer sends requests to BaseURL plus a path. The zero value is not usable:
// BaseURL and HTTP must be set. Clock and Logger default to the real clock and
// a discarding logger.
type Doer struct {
	BaseURL   string
	UserAgent string
	// APIKey, when not empty, is sent as a bearer token. It is redacted when
	// formatted or logged.
	APIKey Secret
	HTTP   *http.Client
	// Timeout bounds each call of Do, including reading the response body;
	// zero or less means no timeout. It is applied per call with a context
	// deadline, so every attempt of a retried request gets its own budget.
	Timeout time.Duration
	Clock   clock.Clock
	Logger  *slog.Logger
}

// Do sends a request to the base URL plus path with an optional body (nil
// sends none) and reads the complete response. ctx must not be nil (a nil
// ctx panics, as in the standard library). The errors are raw: a *BuildError
// when the request cannot be built, otherwise
// the HTTP client or body read error, so errors.Is(err, context.Canceled),
// context.DeadlineExceeded and net.Error timeouts keep working. The caller adds
// the context text; the errors never contain headers or the API key.
func (d *Doer) Do(ctx context.Context, method, path string, body *Body) (Response, error) {
	resp, err := d.send(ctx, method, path, body)
	if err != nil && d.Timeout > 0 && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		// Only the per-attempt deadline expired, not the caller's context.
		return Response{}, &TimeoutError{Err: err}
	}
	return resp, err
}

// TimeoutError reports that a request attempt exceeded Doer.Timeout while the
// caller's context was still live. It is a net.Error timeout and unwraps to
// the underlying error, which matches context.DeadlineExceeded.
type TimeoutError struct {
	Err error
}

// Error implements the error interface.
func (e *TimeoutError) Error() string { return e.Err.Error() }

// Unwrap returns the underlying error.
func (e *TimeoutError) Unwrap() error { return e.Err }

// Timeout implements net.Error and is always true.
func (e *TimeoutError) Timeout() bool { return true }

// Temporary implements net.Error.
func (e *TimeoutError) Temporary() bool { return true }

var _ net.Error = (*TimeoutError)(nil)

// IsRequestTimeout reports whether err is, or wraps, a *TimeoutError: a
// request attempt that exceeded its own timeout rather than the caller's
// context.
func IsRequestTimeout(err error) bool {
	var te *TimeoutError
	return errors.As(err, &te)
}

// send performs one attempt under the per-attempt timeout.
func (d *Doer) send(ctx context.Context, method, path string, body *Body) (Response, error) {
	if d.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, method, d.BaseURL+path, body.reader())
	if err != nil {
		return Response{}, &BuildError{Err: err}
	}
	req.Header.Set("User-Agent", d.UserAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", body.ContentType)
	}
	if !d.APIKey.IsEmpty() {
		req.Header.Set("Authorization", "Bearer "+d.APIKey.Reveal())
	}

	start := time.Now()
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := ReadLimited(resp.Body)
	if err != nil {
		return Response{}, err
	}
	d.logger().Debug("MineSkin request completed",
		"method", method, "path", path, "status", resp.StatusCode,
		"breadcrumb", resp.Header.Get(HeaderMineskinCrumb), "elapsed", time.Since(start))
	return Response{Status: resp.StatusCode, Header: resp.Header, Body: respBody, Received: d.clock().Now()}, nil
}

func (d *Doer) logger() *slog.Logger {
	if d.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return d.Logger
}

func (d *Doer) clock() clock.Clock {
	if d.Clock == nil {
		return clock.Real()
	}
	return d.Clock
}

// ReadLimited reads r completely and fails when it holds more than
// MaxResponseBodyBytes.
func ReadLimited(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, MaxResponseBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxResponseBodyBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", MaxResponseBodyBytes)
	}
	return body, nil
}
