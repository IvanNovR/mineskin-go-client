package mineskin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// testWire is a minimal wire struct: like the Java UserResponseImpl test body,
// it requires the "user" field to be a string.
type testWire struct {
	User string `json:"user"`
}

// errBadJob is the error wireWithCause fails with.
var errBadJob = errors.New("bad job")

// wireWithCause is a wire struct whose payload decoding always fails.
type wireWithCause struct{}

func (*wireWithCause) UnmarshalJSON([]byte) error { return errBadJob }

// decodeMeta runs decodeResponse and returns only the metadata and error.
func decodeMeta[W any](raw transport.Response) (ResponseMeta, error) {
	_, meta, err := decodeResponse[W](raw, discardLogger, "/test")
	return meta, err
}

func TestHandleResponse_Success(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"valid json", 200, `{"success":true,"user":"abc","grants":{}}`},
		{"success absent with 200", 200, `{"user":"abc"}`},
		{"success true with 500 counts as success", 500, `{"success":true}`},
		{"leading whitespace", 200, " \n\t{\"success\":true}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			meta, err := decodeMeta[testWire](transport.Response{Status: tt.status, Body: []byte(tt.body)})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if meta.Status != tt.status {
				t.Errorf("meta = %+v", meta)
			}
		})
	}
}

func TestHandleResponse_APIError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		status      int
		body        string
		wantCode    string
		wantMessage string
		wantNotFnd  bool
	}{
		{"invalid api key", 401, `{"success":false,"errors":[{"code":"invalid_api_key","message":"Invalid API key"}]}`, "invalid_api_key", "Invalid API key", false},
		{"first error wins", 400, `{"success":false,"errors":[{"code":"a","message":"A"},{"code":"b","message":"B"}]}`, "a", "A", false},
		{"fallbacks without errors", 400, `{"success":false}`, "", "", false},
		{"fallbacks with empty errors", 400, `{"success":false,"errors":[]}`, "", "", false},
		{"success absent with 404", 404, `{"errors":[{"code":"skin_not_found","message":"nope"}]}`, "skin_not_found", "nope", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeMeta[struct{}](transport.Response{Status: tt.status, Body: []byte(tt.body)})
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("want *APIError, got %T %v", err, err)
			}
			if apiErr.Code != tt.wantCode || apiErr.Message != tt.wantMessage || apiErr.Meta.Status != tt.status {
				t.Errorf("apiErr = %+v", apiErr)
			}
			if errors.Is(err, ErrNotFound) != tt.wantNotFnd {
				t.Errorf("Is(ErrNotFound) mismatch for %v", err)
			}
			if errors.Is(err, ErrRateLimited) != (tt.status == http.StatusTooManyRequests) {
				t.Errorf("Is(ErrRateLimited) mismatch for %v", err)
			}
		})
	}
}

func TestHandleResponse_Unexpected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		status    int
		body      string
		wantCause bool
		retryable bool
	}{
		{"empty body", 502, "", false, true},
		{"whitespace only", 502, " \n\t ", false, true},
		{"array", 200, `[1, 2, 3]`, false, false},
		{"string", 200, `"ok"`, false, false},
		{"null", 200, `null`, false, false},
		{"html", 502, `<html>Bad Gateway</html>`, false, true},
		{"truncated json", 200, `{"success":true,"user":`, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeMeta[testWire](transport.Response{Status: tt.status, Body: []byte(tt.body)})
			var ue *UnexpectedResponseError
			if !errors.As(err, &ue) {
				t.Fatalf("want *UnexpectedResponseError, got %T %v", err, err)
			}
			if (ue.cause != nil) != tt.wantCause || IsRetryable(ue) != tt.retryable || ue.Status != tt.status {
				t.Errorf("ue = %+v", ue)
			}
		})
	}
}

func TestHandleResponse_UnexpectedUsesHeaders(t *testing.T) {
	t.Parallel()
	raw := transport.Response{
		Status: 530,
		Header: hdr("Server", "cloudflare", "CF-Ray", "8f3a-FRA", "Content-Type", "text/plain"),
		Body:   []byte("error code: 1033"),
	}
	_, err := decodeMeta[struct{}](raw)
	want := "mineskin: HTTP 530 from Cloudflare (error 1033, ray 8f3a-FRA): error code: 1033"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

func TestHandleResponse_ParseError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{"wrong type in main field", `{"success":true,"user":42}`},
		{"errors wrong type", `{"success":false,"errors":"boom"}`},
		{"warnings wrong type", `{"success":true,"warnings":{}}`},
		{"messages wrong type", `{"success":true,"messages":[1]}`},
		{"success wrong type", `{"success":"yes"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeMeta[testWire](transport.Response{Status: 200, Body: []byte(tt.body)})
			var ue *UnexpectedResponseError
			var apiErr *APIError
			if err == nil || errors.As(err, &ue) || errors.As(err, &apiErr) {
				t.Fatalf("want plain parse error, got %T %v", err, err)
			}
			if errors.Is(err, ErrInvalidInput) {
				t.Errorf("must not match ErrInvalidInput: %v", err)
			}
			var typeErr *json.UnmarshalTypeError
			if !errors.As(err, &typeErr) {
				t.Errorf("cause not kept: %v", err)
			}
		})
	}
}

func TestDecodeResponse_ReturnsWire(t *testing.T) {
	t.Parallel()
	body := []byte(`{"success":true,"user":"abc"}`)
	wire, meta, err := decodeResponse[testWire](transport.Response{Status: 200, Body: body}, discardLogger, "/test")
	if err != nil || wire.User != "abc" || meta.Status != 200 {
		t.Fatalf("wire = %+v meta = %+v err = %v", wire, meta, err)
	}
}

func TestDecodeResponse_PayloadErrorKeepsCauseAndText(t *testing.T) {
	t.Parallel()
	body := `{"success":true,"job":{"id":"x"}}`
	_, err := decodeMeta[wireWithCause](transport.Response{Status: 200, Body: []byte(body)})
	assertParseError(t, err)
	if !errors.Is(err, errBadJob) || err.Error() != "mineskin: failed to parse response: bad job" {
		t.Errorf("err = %v", err)
	}
}

func TestDecodeResponse_APIErrorSkipsPayload(t *testing.T) {
	t.Parallel()
	_, err := decodeMeta[wireWithCause](transport.Response{Status: 400, Body: []byte(`{"success":false}`)})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || errors.Is(err, errBadJob) {
		t.Errorf("want *APIError without payload decoding, got %v", err)
	}
}

func TestDecodeResponse_SuccessWithBadPayloadIsParseError(t *testing.T) {
	t.Parallel()
	_, err := decodeMeta[wireWithCause](transport.Response{Status: 200, Body: []byte(`{"success":true}`)})
	assertParseError(t, err)
}

func TestHandleResponse_ListsAndLinks(t *testing.T) {
	t.Parallel()
	t.Run("lists default to empty non-nil", func(t *testing.T) {
		t.Parallel()
		meta, err := decodeMeta[struct{}](transport.Response{Status: 200, Body: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		if meta.Warnings == nil || meta.Messages == nil || len(meta.Warnings)+len(meta.Messages) != 0 {
			t.Errorf("lists = %#v %#v", meta.Warnings, meta.Messages)
		}
		if meta.Links != nil {
			t.Errorf("Links = %s, want nil", meta.Links)
		}
		if meta.Header == nil {
			t.Error("Header must be non-nil")
		}
	})
	t.Run("null lists are empty", func(t *testing.T) {
		t.Parallel()
		meta, err := decodeMeta[struct{}](transport.Response{Status: 200, Body: []byte(`{"errors":null,"warnings":null,"messages":null}`)})
		if err != nil || meta.Warnings == nil || meta.Messages == nil {
			t.Errorf("meta=%+v err=%v", meta, err)
		}
	})
	t.Run("lists and links are filled", func(t *testing.T) {
		t.Parallel()
		body := `{"success":true,"warnings":[{"code":"w","message":"W"}],"messages":[{"code":"m","message":"M"}],"links":{"self":"/x"}}`
		meta, err := decodeMeta[struct{}](transport.Response{Status: 200, Body: []byte(body)})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(meta.Warnings, []CodeAndMessage{{"w", "W"}}) ||
			!reflect.DeepEqual(meta.Messages, []CodeAndMessage{{"m", "M"}}) {
			t.Errorf("meta = %+v", meta)
		}
		if string(meta.Links) != `{"self":"/x"}` {
			t.Errorf("Links = %s", meta.Links)
		}
	})
}

func TestHandleResponse_APIErrorCarriesHeaders(t *testing.T) {
	t.Parallel()
	raw := transport.Response{Status: 401, Header: hdr("Mineskin-Breadcrumb", "b1"), Body: []byte(`{"success":false}`)}
	_, err := decodeMeta[struct{}](raw)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Meta.Breadcrumb != "b1" || apiErr.Meta.Header.Get("Mineskin-Breadcrumb") != "b1" {
		t.Errorf("err = %#v", err)
	}
}

func TestHandleResponse_UnexpectedIsClientError(t *testing.T) {
	t.Parallel()
	_, err := decodeMeta[struct{}](transport.Response{Status: 530, Body: []byte("error code: 1033")})
	var ue *UnexpectedResponseError
	if !errors.As(err, &ue) {
		t.Errorf("want *UnexpectedResponseError: %v", err)
	}
}

func TestHandleResponse_AcceptsUTF8BOM(t *testing.T) {
	t.Parallel()
	body := append([]byte("\xef\xbb\xbf"), `{"success":true,"user":"x"}`...)
	meta, err := decodeMeta[testWire](transport.Response{Status: 200, Body: body})
	if err != nil || meta.Status != 200 {
		t.Fatalf("meta = %+v err = %v", meta, err)
	}
	body = append([]byte("\xef\xbb\xbf"), `{"success":false,"errors":[{"code":"c","message":"m"}]}`...)
	_, err = decodeMeta[testWire](transport.Response{Status: 400, Body: body})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Errorf("err = %v, want *APIError", err)
	}
}

func TestDecodeResponse_HeaderIsAClone(t *testing.T) {
	t.Parallel()
	src := hdr("Mineskin-Breadcrumb", "crumb", "Set-Cookie", "secret=1")
	meta, err := decodeMeta[struct{}](transport.Response{Status: 200, Header: src, Body: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	meta.Header.Set("Mineskin-Breadcrumb", "changed")
	if got := src.Get("Mineskin-Breadcrumb"); got != "crumb" {
		t.Errorf("source header = %q, want crumb (Meta.Header must be a clone)", got)
	}
	if meta.Header.Get("Set-Cookie") != "" {
		t.Errorf("Set-Cookie passed on: %v", meta.Header)
	}
}

func TestDecodeResponse_DuplicateBreadcrumbsAreJoined(t *testing.T) {
	t.Parallel()
	raw := transport.Response{Status: 200, Header: http.Header{"Mineskin-Breadcrumb": {"a", "b"}}, Body: []byte(`{}`)}
	meta, err := decodeMeta[struct{}](raw)
	if err != nil || meta.Breadcrumb != "a,b" {
		t.Errorf("Breadcrumb = %q err = %v, want a,b", meta.Breadcrumb, err)
	}
}

func TestDecodeResponse_APIErrorKeepsAllErrors(t *testing.T) {
	t.Parallel()
	body := `{"success":false,"errors":[{"code":"a","message":"A"},{"code":"b","message":"B"}],"warnings":[{"code":"w","message":"W"}],"links":{"x":1}}`
	_, err := decodeMeta[struct{}](transport.Response{Status: 400, Body: []byte(body)})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if want := []CodeAndMessage{{"a", "A"}, {"b", "B"}}; !reflect.DeepEqual(apiErr.Errors, want) {
		t.Errorf("Errors = %v, want %v", apiErr.Errors, want)
	}
	if want := []CodeAndMessage{{"w", "W"}}; !reflect.DeepEqual(apiErr.Meta.Warnings, want) || string(apiErr.Meta.Links) != `{"x":1}` {
		t.Errorf("Meta = %+v", apiErr.Meta)
	}
}

func TestDecodeResponse_APIErrorWithoutErrorsHasEmptyCode(t *testing.T) {
	t.Parallel()
	_, err := decodeMeta[struct{}](transport.Response{Status: 403, Body: []byte(`{}`)})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "" || apiErr.Message != "" || apiErr.Errors == nil || len(apiErr.Errors) != 0 {
		t.Fatalf("apiErr = %+v, want empty Code, Message and an empty non-nil Errors", apiErr)
	}
}

const (
	malformedMsg = "MineSkin response field malformed, ignored"
	// garbled is a distinctive value that must never appear in a log line.
	garbled = "ZZgarbledZZ"
)

// malformedLines returns the log lines that report dropped fields.
func malformedLines(buf *bytes.Buffer) []string {
	var out []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, malformedMsg) {
			out = append(out, line)
		}
	}
	return out
}

func assertOneMalformedLine(t *testing.T, buf *bytes.Buffer, wantParts ...string) {
	t.Helper()
	lines := malformedLines(buf)
	if len(lines) != 1 {
		t.Fatalf("malformed log lines = %d, want 1:\n%s", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "level=WARN") {
		t.Errorf("line = %s, want level WARN", lines[0])
	}
	for _, part := range wantParts {
		if !strings.Contains(lines[0], part) {
			t.Errorf("line = %s, want it to contain %q", lines[0], part)
		}
	}
	if strings.Contains(buf.String(), garbled) {
		t.Errorf("log contains the malformed value %q:\n%s", garbled, buf.String())
	}
}

func TestQueue_MalformedUsageIsLoggedOnceWithoutValue(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	body := `{"success":true,"job":{"id":"j","status":"waiting"},"usage":{"credits":{"used":1,"remaining":"` + garbled + `"}}}`
	srv := newTestServer(t, writeJSON(200, body))
	c := newTestClient(t, srv.URL, WithLogger(captureLogger(&buf)))

	got, err := c.Queue(context.Background(), FromURL("https://example.com/a.png"), GenerateOptions{})

	if err != nil {
		t.Fatalf("Queue() error = %v", err)
	}
	if want := (Usage{CreditsUsed: 1}); got.Usage != want {
		t.Errorf("Usage = %+v, want %+v", got.Usage, want)
	}
	assertOneMalformedLine(t, &buf, "path=/v2/queue", "usage.credits.remaining")
}

func TestGenerate_MalformedRateLimitIsLogged(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	body := `{"success":true,"skin":{"name":"n"},"rateLimit":{"next":{"relative":"` + garbled + `"},"limit":{"limit":5}}}`
	srv := newTestServer(t, writeJSON(200, body))
	c := newTestClient(t, srv.URL, WithLogger(captureLogger(&buf)))

	got, err := c.Generate(context.Background(), FromURL("https://example.com/a.png"), GenerateOptions{})

	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	assertRateLimit(t, got.RateLimit, RateLimit{Limit: 5})
	assertOneMalformedLine(t, &buf, "path=/v2/generate", "rateLimit.next.relative")
}

func TestMalformedFields_AbsentFieldsAreNotLogged(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	bodies := []string{
		`{"success":true,"job":{"id":"j","status":"waiting"}}`,
		`{"success":true,"job":{"id":"j","status":"waiting"},"rateLimit":null,"usage":null}`,
		`{"success":true,"job":{"id":"j","status":"waiting"},"rateLimit":{},"usage":{"credits":{}}}`,
		`{"success":true,"job":{"id":"j","status":"waiting"},"rateLimit":{"next":{"relative":500}},"usage":{"credits":{"used":1,"remaining":2}}}`,
	}
	for _, body := range bodies {
		srv := newTestServer(t, writeJSON(200, body))
		c := newTestClient(t, srv.URL, WithLogger(captureLogger(&buf)))
		if _, err := c.Queue(context.Background(), FromURL("https://example.com/a.png"), GenerateOptions{}); err != nil {
			t.Fatalf("Queue(%s) error = %v", body, err)
		}
	}
	if lines := malformedLines(&buf); len(lines) != 0 {
		t.Errorf("unexpected malformed log lines:\n%s", strings.Join(lines, "\n"))
	}
}

func TestAPIError_MalformedDataAndRetryAfterAreLoggedOnce(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	body := `{"success":false,"errors":[{"code":"c","message":"m"}],"rateLimit":"` + garbled + `","usage":{"credits":5}}`
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", garbled)
		writeJSON(400, body)(w, r)
	})
	c := newTestClient(t, srv.URL, WithLogger(captureLogger(&buf)))

	_, err := c.GetSkin(context.Background(), testSkinUUID)

	apiErr := asAPIError(t, err)
	if apiErr.Code != "c" || apiErr.RetryAfter != 0 || apiErr.RateLimit != (RateLimit{}) || apiErr.Usage != (Usage{}) {
		t.Errorf("apiErr = %+v, want the original error with dropped data", apiErr)
	}
	assertOneMalformedLine(t, &buf, "rateLimit", "usage.credits", "Retry-After")
}

func TestAPIError_GarbledRetryAfterIsLogged(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", garbled)
		writeJSON(429, `{"success":false}`)(w, r)
	})
	c := newTestClient(t, srv.URL, WithLogger(captureLogger(&buf)), WithRateLimitRetries(0))

	_, err := c.GetSkin(context.Background(), testSkinUUID)

	if apiErr := asAPIError(t, err); apiErr.RetryAfter != 0 {
		t.Errorf("RetryAfter = %v, want 0", apiErr.RetryAfter)
	}
	assertOneMalformedLine(t, &buf, "fields=Retry-After")
}

func TestAPIError_ValidOrAbsentRetryAfterIsNotLogged(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	for _, header := range []string{"", "7", time.Now().UTC().Add(time.Hour).Format(http.TimeFormat), "-3"} {
		srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			if header != "" {
				w.Header().Set("Retry-After", header)
			}
			writeJSON(400, `{"success":false}`)(w, r)
		})
		c := newTestClient(t, srv.URL, WithLogger(captureLogger(&buf)))
		if _, err := c.GetSkin(context.Background(), testSkinUUID); err == nil {
			t.Fatal("GetSkin() error = nil, want an API error")
		}
	}
	if lines := malformedLines(&buf); len(lines) != 0 {
		t.Errorf("unexpected malformed log lines:\n%s", strings.Join(lines, "\n"))
	}
}

func TestUnexpectedResponse_GarbledRetryAfterIsLogged(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", garbled)
		writeJSON(503, `<html>unavailable</html>`)(w, r)
	})
	c := newTestClient(t, srv.URL, WithLogger(captureLogger(&buf)))

	_, err := c.GetSkin(context.Background(), testSkinUUID)

	var ue *UnexpectedResponseError
	if !asError(err, &ue) || ue.RetryAfter != 0 {
		t.Fatalf("err = %v, want *UnexpectedResponseError without RetryAfter", err)
	}
	assertOneMalformedLine(t, &buf, "fields=Retry-After")
}

// asAPIError returns the *APIError in err's chain or fails the test.
func asAPIError(t *testing.T, err error) *APIError {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	return apiErr
}

// asError is errors.As with the result only.
func asError[T error](err error, target *T) bool { return errors.As(err, target) }

func TestDecodeMeta_Headers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		header         http.Header
		wantBreadcrumb string
		wantHeader     http.Header
	}{
		{
			name: "keeps only MineSkin headers",
			header: hdr("Mineskin-Server", "api-1", "Mineskin-Breadcrumb", "crumb",
				"X-Mineskin-Foo", "bar", "Content-Type", "application/json"),
			wantBreadcrumb: "crumb",
			wantHeader:     http.Header{"Mineskin-Server": {"api-1"}, "Mineskin-Breadcrumb": {"crumb"}, "X-Mineskin-Foo": {"bar"}},
		},
		{
			name:       "duplicate headers do not fail",
			header:     http.Header{"Mineskin-Server": {"a", "b"}},
			wantHeader: http.Header{"Mineskin-Server": {"a", "b"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw := transport.Response{Status: 200, Header: tt.header, Body: []byte(`{}`)}

			meta, err := decodeMeta[struct{}](raw)

			if err != nil {
				t.Fatal(err)
			}
			if meta.Breadcrumb != tt.wantBreadcrumb {
				t.Errorf("Breadcrumb = %q, want %q", meta.Breadcrumb, tt.wantBreadcrumb)
			}
			if !reflect.DeepEqual(meta.Header, tt.wantHeader) {
				t.Errorf("Header = %v, want %v", meta.Header, tt.wantHeader)
			}
			if meta.Status != 200 {
				t.Errorf("Status = %d, want 200", meta.Status)
			}
		})
	}
}
