package mineskin

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// Bodies and expected messages are ported from the Java client's
// tests/src/test/java/org/mineskin/request/RequestHandlerResponseTest.java
// at commit 744c993 of InventivetalentDev/MineskinClient.
const cloudflare502Page = `<!DOCTYPE html>
<html class="no-js" lang="en-US">
<head>
<title>example.com | 502: Bad gateway</title>
</head>
<body>
<div id="cf-wrapper">
    <div id="cf-error-details" class="p-0">
        <header class="mx-auto pt-10 lg:pt-6 lg:px-8 w-240 lg:w-full mb-8">
            <h1 class="inline-block sm:block sm:mb-2 font-light text-60 lg:text-4xl text-black-dark leading-tight mr-2">
                <span class="inline-block">Bad gateway</span>
                <span class="code-label">Error code 502</span>
            </h1>
            <div>
                Visit <a href="https://www.cloudflare.com/5xx-error-landing?utm_source=errorcode_502&utm_campaign=example.com" target="_blank" rel="noopener noreferrer">cloudflare.com</a> for more information.
            </div>
        </header>
        <div class="cf-error-footer cf-wrapper">
            <p class="text-13">
                <span class="cf-footer-item sm:block sm:mb-1">Cloudflare Ray ID: <strong class="font-semibold">0123456789abcdef</strong></span>
            </p>
        </div>
    </div>
</div>
</body>
</html>
`

const nginx502Page = `<html>
<head><title>502 Bad Gateway</title></head>
<body>
<center><h1>502 Bad Gateway</h1></center>
<hr><center>nginx/1.28.0</center>
</body>
</html>
`

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func viaCloudflare() http.Header {
	return hdr("server", "cloudflare", "cf-ray", "a4da9b2dbeae3a73-FRA", "content-type", "text/html")
}

func TestUnexpectedResponseErrorClassification(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		header      http.Header
		body        string
		source      ResponseSource
		cfCode      int
		software    string
		cfRay       string
		contentType string
		snippet     string
		message     string
		retryable   bool
	}{
		{
			name: "cloudflare plain text 530/1033", status: 530,
			header: hdr("server", "cloudflare", "cf-ray", "8f3a1b2c3d4e5f60-FRA", "content-type", "text/plain"),
			body:   "error code: 1033", source: SourceCloudflare, cfCode: 1033,
			cfRay: "8f3a1b2c3d4e5f60-FRA", contentType: "text/plain", snippet: "error code: 1033",
			message:   "HTTP 530 from Cloudflare (error 1033, ray 8f3a1b2c3d4e5f60-FRA): error code: 1033",
			retryable: true,
		},
		{
			name: "cloudflare plain text 409/1001 not retryable", status: 409,
			header: hdr("server", "cloudflare", "cf-ray", "a4da9b2dbeae3a73-FRA", "content-type", "text/plain; charset=UTF-8"),
			body:   "error code: 1001", source: SourceCloudflare, cfCode: 1001,
			cfRay: "a4da9b2dbeae3a73-FRA", contentType: "text/plain; charset=UTF-8", snippet: "error code: 1001",
			message: "HTTP 409 from Cloudflare (error 1001, ray a4da9b2dbeae3a73-FRA): error code: 1001",
		},
		{
			name: "cloudflare 521 code comes from status", status: 521, header: http.Header{}, body: "",
			source: SourceCloudflare, cfCode: 521,
			message: "HTTP 521 from Cloudflare (error 521): empty response body", retryable: true,
		},
		{
			name: "cloudflare generated 502 page uses title", status: 502,
			header: hdr("server", "cloudflare", "cf-ray", "0123456789abcdef-FRA", "content-type", "text/html; charset=UTF-8"),
			body:   cloudflare502Page, source: SourceCloudflare, cfCode: 502, cfRay: "0123456789abcdef-FRA",
			contentType: "text/html; charset=UTF-8", snippet: "example.com | 502: Bad gateway",
			message:   "HTTP 502 from Cloudflare (error 502, ray 0123456789abcdef-FRA): example.com | 502: Bad gateway",
			retryable: true,
		},
		{
			name: "cloudflare 530 code allows markup", status: 530, header: viaCloudflare(),
			body:   "<html><body><span>Error code</span> <span>1033</span></body></html>",
			source: SourceCloudflare, cfCode: 1033, cfRay: "a4da9b2dbeae3a73-FRA", contentType: "text/html",
			snippet:   "<html><body><span>Error code</span> <span>1033</span></body></html>",
			message:   "HTTP 530 from Cloudflare (error 1033, ray a4da9b2dbeae3a73-FRA): <html><body><span>Error code</span> <span>1033</span></body></html>",
			retryable: true,
		},
		{
			name: "cloudflare 528 without code", status: 528, header: http.Header{}, body: "oops",
			source: SourceCloudflare, snippet: "oops", message: "HTTP 528 from Cloudflare: oops", retryable: true,
		},
		{
			name: "nginx page behind cloudflare", status: 502, header: viaCloudflare(), body: nginx502Page,
			source: SourceNginx, software: "nginx/1.28.0", cfRay: "a4da9b2dbeae3a73-FRA", contentType: "text/html",
			snippet:   "502 Bad Gateway",
			message:   "HTTP 502 from nginx/1.28.0 (ray a4da9b2dbeae3a73-FRA): 502 Bad Gateway",
			retryable: true,
		},
		{
			name: "nginx footer wins over 52x status", status: 521, header: http.Header{}, body: nginx502Page,
			source: SourceNginx, software: "nginx/1.28.0", snippet: "502 Bad Gateway",
			message: "HTTP 521 from nginx/1.28.0: 502 Bad Gateway", retryable: true,
		},
		{
			name: "nginx from server header", status: 504, header: hdr("server", "nginx/1.28.0"), body: "",
			source: SourceNginx, software: "nginx/1.28.0",
			message: "HTTP 504 from nginx/1.28.0: empty response body", retryable: true,
		},
		{
			name: "nginx server header is case-insensitive prefix", status: 502, header: hdr("Server", "NGINX"), body: "x",
			source: SourceNginx, software: "NGINX", snippet: "x", message: "HTTP 502 from NGINX: x", retryable: true,
		},
		{
			name: "origin error through cloudflare is unknown", status: 502, header: viaCloudflare(), body: "Bad Gateway",
			source: SourceUnknown, cfRay: "a4da9b2dbeae3a73-FRA", contentType: "text/html", snippet: "Bad Gateway",
			message: "HTTP 502 (ray a4da9b2dbeae3a73-FRA): Bad Gateway", retryable: true,
		},
		{
			name: "empty body", status: 502, header: http.Header{}, body: "",
			source: SourceUnknown, message: "HTTP 502: empty response body", retryable: true,
		},
		{
			name: "cf-ray alone with error code is cloudflare", status: 400, header: hdr("cf-ray", "abc-FRA"),
			body: "error code: 1020", source: SourceCloudflare, cfCode: 1020, cfRay: "abc-FRA", snippet: "error code: 1020",
			message: "HTTP 400 from Cloudflare (error 1020, ray abc-FRA): error code: 1020",
		},
		{
			name: "error code text without cloudflare headers is unknown", status: 400, header: http.Header{},
			body: "error code: 1020", source: SourceUnknown, snippet: "error code: 1020",
			message: "HTTP 400: error code: 1020",
		},
		{
			name: "html title is case-insensitive and multi-line", status: 500, header: http.Header{},
			body:   "<HTML><TITLE lang=en>\n  Server \n\t Error </TITLE><body>ignored</body>",
			source: SourceUnknown, snippet: "Server Error", message: "HTTP 500: Server Error", retryable: true,
		},
		{
			name: "whitespace only body is empty", status: 500, header: http.Header{}, body: " \n\t ",
			source: SourceUnknown, message: "HTTP 500: empty response body", retryable: true,
		},
		{
			name: "long body is truncated to 200 plus ellipsis", status: 500, header: http.Header{},
			body: strings.Repeat("x", 1000), source: SourceUnknown,
			snippet: strings.Repeat("x", 200) + "...", message: "HTTP 500: " + strings.Repeat("x", 200) + "...",
			retryable: true,
		},
		{
			name: "exactly 200 characters is not truncated", status: 500, header: http.Header{},
			body: strings.Repeat("y", 200), source: SourceUnknown,
			snippet: strings.Repeat("y", 200), message: "HTTP 500: " + strings.Repeat("y", 200), retryable: true,
		},
		{
			name: "truncation counts runes not bytes", status: 500, header: http.Header{},
			body: strings.Repeat("é", 201), source: SourceUnknown,
			snippet: strings.Repeat("é", 200) + "...", message: "HTTP 500: " + strings.Repeat("é", 200) + "...",
			retryable: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newUnexpectedResponseError(tc.status, tc.header, []byte(tc.body), nil, testNow)
			if e.Status != tc.status {
				t.Errorf("Status = %d, want %d", e.Status, tc.status)
			}
			if e.Source != tc.source {
				t.Errorf("Source = %q, want %q", e.Source, tc.source)
			}
			if e.CloudflareCode != tc.cfCode {
				t.Errorf("CloudflareCode = %d, want %d", e.CloudflareCode, tc.cfCode)
			}
			if e.ServerSoftware != tc.software {
				t.Errorf("ServerSoftware = %q, want %q", e.ServerSoftware, tc.software)
			}
			if e.CFRay != tc.cfRay {
				t.Errorf("CFRay = %q, want %q", e.CFRay, tc.cfRay)
			}
			if e.ContentType != tc.contentType {
				t.Errorf("ContentType = %q, want %q", e.ContentType, tc.contentType)
			}
			if e.BodySnippet != tc.snippet {
				t.Errorf("BodySnippet = %q, want %q", e.BodySnippet, tc.snippet)
			}
			if got := e.Error(); got != tc.message {
				t.Errorf("Error() = %q, want %q", got, tc.message)
			}
			if e.IsCloudflare() != (tc.source == SourceCloudflare) {
				t.Errorf("IsCloudflare() = %v for source %q", e.IsCloudflare(), tc.source)
			}
			if e.IsRetryable() != tc.retryable {
				t.Errorf("IsRetryable() = %v, want %v", e.IsRetryable(), tc.retryable)
			}
		})
	}
}

// The documented snippet shape: at most 200 characters plus "...".
const (
	snippetMaxRunes = 200
	snippetEllipsis = "..."
)

var testNow = time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

func TestUnexpectedResponseErrorRetryAfterHTTPDate(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", testNow.Add(45*time.Second).Format(http.TimeFormat))
	e := newUnexpectedResponseError(503, h, nil, nil, testNow)
	if !e.HasRetryAfter || e.RetryAfter != 45*time.Second {
		t.Errorf("got (%v, %v), want (45s, true)", e.RetryAfter, e.HasRetryAfter)
	}
}

func TestUnexpectedResponseErrorNilBody(t *testing.T) {
	e := newUnexpectedResponseError(502, nil, nil, nil, testNow)
	if got, want := e.Error(), "HTTP 502: empty response body"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestUnexpectedResponseErrorRetryAfter(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    time.Duration
		present bool
	}{
		{"seconds", "30", 30 * time.Second, true},
		{"two minutes", "120", 120 * time.Second, true},
		{"zero", "0", 0, true},
		{"surrounding whitespace", " 5 ", 5 * time.Second, true},
		// JB-18 deviation, D13: the HTTP-date form is supported, and a past
		// date or a negative value is present with a zero delay.
		{"http date in the past", "Wed, 21 Oct 2026 07:28:00 GMT", 0, true},
		{"negative", "-1", 0, true},
		{"fractional", "1.5", 0, false},
		{"empty", "", 0, false},
		{"overflow", "99999999999999999999", 0, false},
		{"too large for duration", "9223372036854775807", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			h.Set("Retry-After", tc.value)
			e := newUnexpectedResponseError(503, h, []byte("Service Unavailable"), nil, testNow)
			if e.HasRetryAfter != tc.present || e.RetryAfter != tc.want {
				t.Errorf("got (%v, %v), want (%v, %v)", e.RetryAfter, e.HasRetryAfter, tc.want, tc.present)
			}
		})
	}
	absent := newUnexpectedResponseError(503, http.Header{}, nil, nil, testNow)
	if absent.HasRetryAfter || absent.RetryAfter != 0 {
		t.Errorf("missing header: got (%v, %v)", absent.RetryAfter, absent.HasRetryAfter)
	}
}

func TestUnexpectedResponseErrorIsRetryable(t *testing.T) {
	tests := map[int]bool{
		200: false, 400: false, 404: false, 408: true, 409: false, 429: true,
		499: false, 500: true, 501: false, 502: true, 503: true, 504: true, 505: false, 521: true, 530: true,
	}
	for status, want := range tests {
		e := newUnexpectedResponseError(status, http.Header{}, nil, nil, testNow)
		if got := e.IsRetryable(); got != want {
			t.Errorf("status %d: IsRetryable() = %v, want %v", status, got, want)
		}
	}
}

func TestUnexpectedResponseErrorIs(t *testing.T) {
	notFound := newUnexpectedResponseError(404, http.Header{}, []byte("nope"), nil, testNow)
	if !errors.Is(notFound, ErrClient) || !errors.Is(notFound, ErrNotFound) {
		t.Error("404 must match ErrClient and ErrNotFound")
	}
	if errors.Is(notFound, ErrInvalidInput) {
		t.Error("must not match ErrInvalidInput")
	}
	bad := newUnexpectedResponseError(502, http.Header{}, nil, nil, testNow)
	if !errors.Is(bad, ErrClient) {
		t.Error("502 must match ErrClient")
	}
	if errors.Is(bad, ErrNotFound) {
		t.Error("502 must not match ErrNotFound")
	}
}

func TestUnexpectedResponseErrorUnwrapAndAs(t *testing.T) {
	cause := errors.New("unexpected end of JSON input")
	e := newUnexpectedResponseError(200, http.Header{}, []byte(`{"success":true,"user":`), cause, testNow)
	if !errors.Is(e, cause) || !errors.Is(e.Unwrap(), cause) {
		t.Error("cause must be reachable through Unwrap")
	}
	if e.IsRetryable() {
		t.Error("200 must not be retryable")
	}
	var target *UnexpectedResponseError
	wrapped := wrapClientError("outer", e)
	if !errors.As(wrapped, &target) || target != e {
		t.Error("errors.As must find the UnexpectedResponseError through a wrapper")
	}
	if newUnexpectedResponseError(200, http.Header{}, nil, nil, testNow).Unwrap() != nil {
		t.Error("Unwrap without cause must be nil")
	}
}

func TestUnexpectedResponseErrorLargeBodyIsCapped(t *testing.T) {
	tests := map[string]struct {
		body       string
		wantSnip   string
		wantSource ResponseSource
	}{
		"title near start": {
			body:       "<html><head><title>502 Bad Gateway</title></head>" + strings.Repeat("x", 10<<20),
			wantSnip:   "502 Bad Gateway",
			wantSource: SourceUnknown,
		},
		"nginx footer near start": {
			body:       "<html><center>nginx/1.28.0</center>" + strings.Repeat("y", 10<<20),
			wantSnip:   "<html><center>nginx/1.28.0</center>" + strings.Repeat("y", snippetMaxRunes-len("<html><center>nginx/1.28.0</center>")) + snippetEllipsis,
			wantSource: SourceNginx,
		},
		"plain": {
			body:       strings.Repeat("a", 10<<20),
			wantSnip:   strings.Repeat("a", snippetMaxRunes) + snippetEllipsis,
			wantSource: SourceUnknown,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			e := newUnexpectedResponseError(502, nil, []byte(tc.body), nil, testNow)
			if e.BodySnippet != tc.wantSnip || e.Source != tc.wantSource {
				t.Errorf("snippet = %.60q source = %q", e.BodySnippet, e.Source)
			}
		})
	}
	if transport.SnippetInputMaxBytes != 64<<10 {
		t.Errorf("transport.SnippetInputMaxBytes = %d", transport.SnippetInputMaxBytes)
	}
}

func TestUnexpectedResponseErrorStripsControlCharacters(t *testing.T) {
	h := hdr("server", "nginx/1.\x1b[31m28\x07", "cf-ray", "abc\x1b[0m-FRA")
	e := newUnexpectedResponseError(502, h, []byte("<title>a\x1b[31mred\x00\x7f</title>\r\nx"), nil, testNow)
	if e.BodySnippet != "a[31mred" {
		t.Errorf("BodySnippet = %q", e.BodySnippet)
	}
	if e.ServerSoftware != "nginx/1.[31m28" {
		t.Errorf("ServerSoftware = %q", e.ServerSoftware)
	}
	if e.CFRay != "abc[0m-FRA" {
		t.Errorf("CFRay = %q", e.CFRay)
	}
	body := newUnexpectedResponseError(502, nil, []byte("a\tb\r\nc\x1bd"), nil, testNow)
	if body.BodySnippet != "a b cd" {
		t.Errorf("whitespace must collapse before stripping: %q", body.BodySnippet)
	}
}
