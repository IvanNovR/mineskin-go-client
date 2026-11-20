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
// ErrNotFound with errors.Is when the HTTP status is 404. Use errors.As to
// obtain it.
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
	// HTTP-date form (RFC 9110). Zero means none: the header was absent or
	// invalid, or it asked for no wait (a past date or a negative value).
	RetryAfter time.Duration
	// cause is the underlying error, for example a JSON parse error, or nil.
	cause error
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
	retryAfter, _ := transport.ParseRetryAfter(h.Get(transport.HeaderRetryAfter), now)
	return &UnexpectedResponseError{
		Status:         status,
		ContentType:    h.Get(headerContentType),
		BodySnippet:    transport.Summarize(text),
		Source:         ResponseSource(o.Source),
		ServerSoftware: o.ServerSoftware,
		CFRay:          cfRay,
		CloudflareCode: o.CloudflareCode,
		RetryAfter:     retryAfter,
		cause:          cause,
	}
}

// Error implements the error interface. The format is
// "mineskin: HTTP <status>[ from Cloudflare| from <server>][ (error <code>, ray <ray>)]: <snippet>".
func (e *UnexpectedResponseError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mineskin: HTTP %d", e.Status)
	switch e.Source {
	case SourceCloudflare:
		b.WriteString(" from Cloudflare")
	case SourceNginx:
		b.WriteString(" from " + e.ServerSoftware)
	case SourceUnknown:
		// No source suffix: the origin of the response is not known.
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
func (e *UnexpectedResponseError) Unwrap() error { return e.cause }

// Is reports whether target is the sentinel of the HTTP status: ErrNotFound
// for 404 and ErrRateLimited for 429.
func (e *UnexpectedResponseError) Is(target error) bool { return matchesStatus(e.Status, target) }
