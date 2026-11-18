package mineskin

import (
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
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
	// snippetInputMaxBytes caps the body prefix examined for the snippet and
	// source detection, bounding regex work on huge bodies.
	snippetInputMaxBytes = 64 << 10
	snippetMaxRunes      = 200
	snippetEllipsis      = "..."

	statusCloudflareOriginMin  = 520 // 520-527: the status is the Cloudflare error code
	statusCloudflareOriginMax  = 527
	statusCloudflareWrappedMin = 528 // 528-530: the code comes from the body
	statusCloudflareWrappedMax = 530

	headerServer      = "Server"
	headerCFRay       = "CF-Ray"
	headerContentType = "Content-Type"
	headerRetryAfter  = "Retry-After"

	serverPrefixNginx = "nginx"
	serverCloudflare  = "cloudflare"
	emptyBodyText     = "empty response body"
)

var (
	htmlTitlePattern = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	// cloudflareCodePattern matches "error code: 1033", tolerating markup
	// between the label and the number in HTML pages.
	cloudflareCodePattern = regexp.MustCompile(`(?i)error code:?\s*(?:<[^>]*>\s*)*(\d{3,4})`)
	// nginxFooterPattern matches the footer of an nginx error page, for
	// example <hr><center>nginx/1.28.0</center>.
	nginxFooterPattern = regexp.MustCompile(`(?i)<center>\s*(nginx(?:/[\w.]+)?)\s*</center>`)
	whitespaceRun      = regexp.MustCompile(`\s+`)
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
	// RetryAfter is the Retry-After delay. Only the delta-seconds form is
	// supported; it is meaningful only when HasRetryAfter is true.
	RetryAfter time.Duration
	// HasRetryAfter reports whether a valid Retry-After was present.
	HasRetryAfter bool
	// Err is the underlying cause, for example a JSON parse error, or nil.
	Err error
}

// origin is the detected generator of a response.
type origin struct {
	source         ResponseSource
	serverSoftware string
	cloudflareCode int
}

// newUnexpectedResponseError builds the error from the response status,
// headers and body. cause may be nil.
func newUnexpectedResponseError(status int, h http.Header, body []byte, cause error) *UnexpectedResponseError {
	if len(body) > snippetInputMaxBytes {
		body = body[:snippetInputMaxBytes]
	}
	text := string(body)
	server := stripControl(h.Get(headerServer))
	cfRay := stripControl(h.Get(headerCFRay))
	// Behind Cloudflare every response has these headers, so they only say the
	// response passed through Cloudflare, not that Cloudflare generated it.
	viaCloudflare := cfRay != "" || strings.EqualFold(server, serverCloudflare)
	o := detectOrigin(status, server, viaCloudflare, text)
	retryAfter, hasRetryAfter := parseRetryAfter(h.Get(headerRetryAfter))
	return &UnexpectedResponseError{
		Status:         status,
		ContentType:    h.Get(headerContentType),
		BodySnippet:    summarize(text),
		Source:         o.source,
		ServerSoftware: o.serverSoftware,
		CFRay:          cfRay,
		CloudflareCode: o.cloudflareCode,
		RetryAfter:     retryAfter,
		HasRetryAfter:  hasRetryAfter,
		Err:            cause,
	}
}

// detectOrigin applies the source detection rules in their fixed order.
func detectOrigin(status int, server string, viaCloudflare bool, body string) origin {
	if m := nginxFooterPattern.FindStringSubmatch(body); m != nil {
		return origin{source: SourceNginx, serverSoftware: m[1]}
	}
	if len(server) >= len(serverPrefixNginx) && strings.EqualFold(server[:len(serverPrefixNginx)], serverPrefixNginx) {
		return origin{source: SourceNginx, serverSoftware: server}
	}
	if status >= statusCloudflareOriginMin && status <= statusCloudflareOriginMax {
		return origin{source: SourceCloudflare, cloudflareCode: status}
	}
	code := findCloudflareCode(body)
	if status >= statusCloudflareWrappedMin && status <= statusCloudflareWrappedMax {
		// 530 wraps a 1xxx error, for example 1033.
		return origin{source: SourceCloudflare, cloudflareCode: code}
	}
	if viaCloudflare && code != 0 {
		return origin{source: SourceCloudflare, cloudflareCode: code}
	}
	return origin{source: SourceUnknown}
}

// findCloudflareCode returns the error code in body, or zero.
func findCloudflareCode(body string) int {
	m := cloudflareCodePattern.FindStringSubmatch(body)
	if m == nil {
		return 0
	}
	code, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return code
}

// summarize returns the HTML title, or the body, collapsed and shortened.
func summarize(body string) string {
	text := body
	if m := htmlTitlePattern.FindStringSubmatch(body); m != nil {
		text = m[1]
	}
	text = strings.TrimSpace(stripControl(whitespaceRun.ReplaceAllString(text, " ")))
	if runes := []rune(text); len(runes) > snippetMaxRunes {
		return string(runes[:snippetMaxRunes]) + snippetEllipsis
	}
	return text
}

// stripControl removes control characters, such as ANSI escape sequences, so
// that attacker-controlled response text cannot manipulate a terminal or log.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// parseRetryAfter parses the delta-seconds form of Retry-After. The HTTP-date
// form, negative or oversized values are reported as absent.
func parseRetryAfter(value string) (time.Duration, bool) {
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || seconds < 0 || seconds > int64(math.MaxInt64/time.Second) {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
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
func (e *UnexpectedResponseError) IsRetryable() bool {
	return e.Status == http.StatusRequestTimeout ||
		e.Status == http.StatusTooManyRequests ||
		(e.Status >= http.StatusInternalServerError &&
			e.Status != http.StatusNotImplemented &&
			e.Status != http.StatusHTTPVersionNotSupported)
}

// IsCloudflare reports whether Cloudflare itself generated the error. An
// origin error that merely passed through Cloudflare returns false; see CFRay.
func (e *UnexpectedResponseError) IsCloudflare() bool { return e.Source == SourceCloudflare }
