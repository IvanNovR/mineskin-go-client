// Package transport sends HTTP requests to the MineSkin API and interprets the
// raw responses at the byte level. It knows nothing about the public types of
// the client: it returns plain responses and raw errors, and the caller maps
// them. It must not import the root package.
package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
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
// of an invalid method, URL or a nil context. Nothing was sent.
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
	// APIKey, when not empty, is sent as a bearer token. It is never logged.
	APIKey string
	HTTP   *http.Client
	Clock  clock.Clock
	Logger *slog.Logger
}

// Do sends a request to the base URL plus path with an optional body (nil
// sends none) and reads the complete response. ctx must not be nil. The
// errors are raw: a *BuildError when the request cannot be built, otherwise
// the HTTP client or body read error, so errors.Is(err, context.Canceled),
// context.DeadlineExceeded and net.Error timeouts keep working. The caller adds
// the context text; the errors never contain headers or the API key.
func (d *Doer) Do(ctx context.Context, method, path string, body *Body) (Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, d.BaseURL+path, body.reader())
	if err != nil {
		return Response{}, &BuildError{Err: err}
	}
	req.Header.Set("User-Agent", d.UserAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", body.ContentType)
	}
	if d.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+d.APIKey)
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
