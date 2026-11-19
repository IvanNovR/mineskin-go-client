package mineskin

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// ResponseSource identifies what generated a non-JSON error response.
type ResponseSource string

// Known response sources.
const (
	// SourceCloudflare means Cloudflare itself generated the error, for
	// example a 521 (origin down) or a 530 with error code 1033.
	SourceCloudflare ResponseSource = "cloudflare"
	// SourceNginx means an nginx error page, for example a 502 from the
	// reverse proxy in front of the API.
	SourceNginx ResponseSource = "nginx"
	// SourceUnknown means the source could not be determined.
	SourceUnknown ResponseSource = "unknown"
)

const (
	headerServer      = "Server"
	headerCFRay       = "CF-Ray"
	headerContentType = "Content-Type"

	serverCloudflare = "cloudflare"
	emptyBodyText    = "empty response body"
)

// UnexpectedResponseError is returned when the API answers with something
// that is not a MineSkin JSON object, such as a Cloudflare error page, an
// nginx 502 page, an empty proxy error or truncated JSON. It matches
// ErrClient with errors.Is, and ErrNotFound when the HTTP status is 404. Use
// errors.As to obtain it.
type UnexpectedResponseError struct {
	// Status is the HTTP status code.
	Status int
	// ContentType is the Content-Type header, or empty.
	ContentType string
	// BodySnippet is the HTML title if present, else the body, with
	// whitespace collapsed and at most 200 characters plus "...".
	BodySnippet string
	// Source tells what generated the response.
	Source ResponseSource
	// ServerSoftware is the server that generated an nginx page, for
	// example "nginx/1.28.0", or empty.
	ServerSoftware string
	// CFRay is the Cloudflare Ray ID. It is present on every response that
	// passed through Cloudflare, not only on Cloudflare-generated errors.
	CFRay string
	// CloudflareCode is the Cloudflare error code, for example 521, or 1033
	// for a 530 wrapping it. Zero means unknown.
	CloudflareCode int
	// RetryAfter is the Retry-After delay, from either the delta-seconds or the
	// HTTP-date form (RFC 9110). A past date or a negative value gives 0. It is
	// meaningful only when HasRetryAfter is true.
	RetryAfter time.Duration
	// HasRetryAfter reports whether a valid Retry-After was present, including
	// one that resolved to a zero delay.
	HasRetryAfter bool
	// Err is the underlying cause, for example a JSON parse error, or nil.
	Err error
}

// newUnexpectedResponseError builds the error from the response status,
// headers and body. cause may be nil. now is the time the response was
// received, used to resolve an HTTP-date Retry-After.
func newUnexpectedResponseError(status int, h http.Header, body []byte, cause error, now time.Time) *UnexpectedResponseError {
	if len(body) > transport.SnippetInputMaxBytes {
		body = body[:transport.SnippetInputMaxBytes]
	}
	text := string(body)
	server := transport.StripControl(h.Get(headerServer))
	cfRay := transport.StripControl(h.Get(headerCFRay))
	// Behind Cloudflare every response has these headers, so they only say the
	// response passed through Cloudflare, not that Cloudflare generated it.
	viaCloudflare := cfRay != "" || strings.EqualFold(server, serverCloudflare)
	o := transport.DetectOrigin(status, server, viaCloudflare, text)
	retryAfter, hasRetryAfter := transport.ParseRetryAfter(h.Get(transport.HeaderRetryAfter), now)
	return &UnexpectedResponseError{
		Status:         status,
		ContentType:    h.Get(headerContentType),
		BodySnippet:    transport.Summarize(text),
		Source:         ResponseSource(o.Source),
		ServerSoftware: o.ServerSoftware,
		CFRay:          cfRay,
		CloudflareCode: o.CloudflareCode,
		RetryAfter:     retryAfter,
		HasRetryAfter:  hasRetryAfter,
		Err:            cause,
	}
}

// Error implements the error interface. The format is
// "HTTP <status>[ from Cloudflare| from <server>][ (error <code>, ray <ray>)]: <snippet>".
func (e *UnexpectedResponseError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP %d", e.Status)
	switch e.Source {
	case SourceCloudflare:
		b.WriteString(" from Cloudflare")
	case SourceNginx:
		b.WriteString(" from " + e.ServerSoftware)
	}
	var details []string
	if e.CloudflareCode != 0 {
		details = append(details, fmt.Sprintf("error %d", e.CloudflareCode))
	}
	if e.CFRay != "" {
		details = append(details, "ray "+e.CFRay)
	}
	if len(details) > 0 {
		b.WriteString(" (" + strings.Join(details, ", ") + ")")
	}
	snippet := e.BodySnippet
	if snippet == "" {
		snippet = emptyBodyText
	}
	b.WriteString(": " + snippet)
	return b.String()
}

// Unwrap returns the underlying cause, if any.
func (e *UnexpectedResponseError) Unwrap() error { return e.Err }

// Is reports whether target is ErrClient, or ErrNotFound for an HTTP 404.
func (e *UnexpectedResponseError) Is(target error) bool {
	switch target {
	case ErrClient:
		return true
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	default:
		return false
	}
}

// IsRetryable reports whether the error is likely temporary: status 408, 429,
// or 500 and above except 501 and 505.
func (e *UnexpectedResponseError) IsRetryable() bool { return transport.IsRetryableStatus(e.Status) }

// IsCloudflare reports whether Cloudflare itself generated the error. An
// origin error that merely passed through Cloudflare returns false; see CFRay.
func (e *UnexpectedResponseError) IsCloudflare() bool { return e.Source == SourceCloudflare }
