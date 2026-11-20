package mineskin

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

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

func mhdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func TestHandleResponseSuccess(t *testing.T) {
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

func TestHandleResponseAPIError(t *testing.T) {
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

func TestHandleResponseUnexpected(t *testing.T) {
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

func TestHandleResponseUnexpectedUsesHeaders(t *testing.T) {
	raw := transport.Response{
		Status: 530,
		Header: mhdr("Server", "cloudflare", "CF-Ray", "8f3a-FRA", "Content-Type", "text/plain"),
		Body:   []byte("error code: 1033"),
	}
	_, err := decodeMeta[struct{}](raw)
	want := "mineskin: HTTP 530 from Cloudflare (error 1033, ray 8f3a-FRA): error code: 1033"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

func TestHandleResponseParseError(t *testing.T) {
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

func TestDecodeResponseReturnsWire(t *testing.T) {
	body := []byte(`{"success":true,"user":"abc"}`)
	wire, meta, err := decodeResponse[testWire](transport.Response{Status: 200, Body: body}, discardLogger, "/test")
	if err != nil || wire.User != "abc" || meta.Status != 200 {
		t.Fatalf("wire = %+v meta = %+v err = %v", wire, meta, err)
	}
}

func TestDecodeResponsePayloadErrorKeepsCauseAndText(t *testing.T) {
	body := `{"success":true,"job":{"id":"x"}}`
	_, err := decodeMeta[wireWithCause](transport.Response{Status: 200, Body: []byte(body)})
	assertParseError(t, err)
	if !errors.Is(err, errBadJob) || err.Error() != "mineskin: failed to parse response: bad job" {
		t.Errorf("err = %v", err)
	}
}

func TestDecodeResponseAPIErrorSkipsPayload(t *testing.T) {
	_, err := decodeMeta[wireWithCause](transport.Response{Status: 400, Body: []byte(`{"success":false}`)})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || errors.Is(err, errBadJob) {
		t.Errorf("want *APIError without payload decoding, got %v", err)
	}
}

func TestDecodeResponseSuccessWithBadPayloadIsParseError(t *testing.T) {
	_, err := decodeMeta[wireWithCause](transport.Response{Status: 200, Body: []byte(`{"success":true}`)})
	assertParseError(t, err)
}

func TestHandleResponseListsAndLinks(t *testing.T) {
	t.Run("lists default to empty non-nil", func(t *testing.T) {
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
		meta, err := decodeMeta[struct{}](transport.Response{Status: 200, Body: []byte(`{"errors":null,"warnings":null,"messages":null}`)})
		if err != nil || meta.Warnings == nil || meta.Messages == nil {
			t.Errorf("meta=%+v err=%v", meta, err)
		}
	})
	t.Run("lists and links are filled", func(t *testing.T) {
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

func TestHandleResponseHeaders(t *testing.T) {
	raw := transport.Response{
		Status: 200,
		Header: mhdr("Mineskin-Server", "api-1", "Mineskin-Breadcrumb", "crumb", "X-Mineskin-Foo", "bar", "Content-Type", "application/json"),
		Body:   []byte(`{}`),
	}
	meta, err := decodeMeta[struct{}](raw)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Breadcrumb != "crumb" {
		t.Errorf("Breadcrumb = %q, want crumb", meta.Breadcrumb)
	}
	want := http.Header{"Mineskin-Server": {"api-1"}, "Mineskin-Breadcrumb": {"crumb"}, "X-Mineskin-Foo": {"bar"}}
	if !reflect.DeepEqual(meta.Header, want) {
		t.Errorf("Header = %v, want %v", meta.Header, want)
	}
	if meta.Status != 200 {
		t.Errorf("Status = %d, want 200", meta.Status)
	}
}

func TestHandleResponseAPIErrorCarriesHeaders(t *testing.T) {
	raw := transport.Response{Status: 401, Header: mhdr("Mineskin-Breadcrumb", "b1"), Body: []byte(`{"success":false}`)}
	_, err := decodeMeta[struct{}](raw)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Meta.Breadcrumb != "b1" || apiErr.Meta.Header.Get("Mineskin-Breadcrumb") != "b1" {
		t.Errorf("err = %#v", err)
	}
}

func TestHandleResponseDuplicateHeadersDoNotFail(t *testing.T) {
	raw := transport.Response{Status: 200, Header: http.Header{"Mineskin-Server": {"a", "b"}}, Body: []byte(`{}`)}
	meta, err := decodeMeta[struct{}](raw)
	if err != nil || !reflect.DeepEqual(meta.Header["Mineskin-Server"], []string{"a", "b"}) {
		t.Errorf("meta=%+v err=%v", meta, err)
	}
}

func TestHandleResponseUnexpectedIsClientError(t *testing.T) {
	_, err := decodeMeta[struct{}](transport.Response{Status: 530, Body: []byte("error code: 1033")})
	var ue *UnexpectedResponseError
	if !errors.As(err, &ue) {
		t.Errorf("want *UnexpectedResponseError: %v", err)
	}
}

func TestHandleResponseAcceptsUTF8BOM(t *testing.T) {
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

func TestDecodeResponseHeaderIsAClone(t *testing.T) {
	src := mhdr("Mineskin-Breadcrumb", "crumb", "Set-Cookie", "secret=1")
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

func TestDecodeResponseDuplicateBreadcrumbsAreJoined(t *testing.T) {
	raw := transport.Response{Status: 200, Header: http.Header{"Mineskin-Breadcrumb": {"a", "b"}}, Body: []byte(`{}`)}
	meta, err := decodeMeta[struct{}](raw)
	if err != nil || meta.Breadcrumb != "a,b" {
		t.Errorf("Breadcrumb = %q err = %v, want a,b", meta.Breadcrumb, err)
	}
}

func TestDecodeResponseAPIErrorKeepsAllErrors(t *testing.T) {
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

func TestDecodeResponseAPIErrorWithoutErrorsHasEmptyCode(t *testing.T) {
	_, err := decodeMeta[struct{}](transport.Response{Status: 403, Body: []byte(`{}`)})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "" || apiErr.Message != "" || apiErr.Errors == nil || len(apiErr.Errors) != 0 {
		t.Fatalf("apiErr = %+v, want empty Code, Message and an empty non-nil Errors", apiErr)
	}
}
