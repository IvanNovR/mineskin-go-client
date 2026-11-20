package mineskin

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// userDecoder mimics the Java UserResponseImpl test body: the decoder requires
// the "user" field to be a string.
func userDecoder(obj map[string]json.RawMessage, _ []byte) error {
	raw, ok := obj["user"]
	if !ok {
		return nil
	}
	var s string
	return json.Unmarshal(raw, &s)
}

func noopDecoder(map[string]json.RawMessage, []byte) error { return nil }

func mhdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func TestHandleResponseSuccess(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantSuccess bool
	}{
		{"valid json", 200, `{"success":true,"user":"abc","grants":{}}`, true},
		{"success absent with 200", 200, `{"user":"abc"}`, true},
		{"success true with 500 counts as success", 500, `{"success":true}`, true},
		{"leading whitespace", 200, " \n\t{\"success\":true}", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, err := handleResponse(transport.Response{Status: tt.status, Body: []byte(tt.body)}, userDecoder)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if meta.Success != tt.wantSuccess || meta.Status != tt.status {
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
		{"fallbacks without errors", 400, `{"success":false}`, "request_failed", "request failed", false},
		{"fallbacks with empty errors", 400, `{"success":false,"errors":[]}`, "request_failed", "request failed", false},
		{"success absent with 404", 404, `{"errors":[{"code":"skin_not_found","message":"nope"}]}`, "skin_not_found", "nope", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := handleResponse(transport.Response{Status: tt.status, Body: []byte(tt.body)}, noopDecoder)
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("want *APIError, got %T %v", err, err)
			}
			if apiErr.Code != tt.wantCode || apiErr.Message != tt.wantMessage || apiErr.Status != tt.status || apiErr.Success {
				t.Errorf("apiErr = %+v", apiErr)
			}
			if err == nil || errors.Is(err, ErrNotFound) != tt.wantNotFnd {
				t.Errorf("Is mismatch for %v", err)
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
			_, err := handleResponse(transport.Response{Status: tt.status, Body: []byte(tt.body)}, userDecoder)
			var ue *UnexpectedResponseError
			if !errors.As(err, &ue) {
				t.Fatalf("want *UnexpectedResponseError, got %T %v", err, err)
			}
			if (ue.Err != nil) != tt.wantCause || ue.IsRetryable() != tt.retryable || ue.Status != tt.status {
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
	_, err := handleResponse(raw, noopDecoder)
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
			_, err := handleResponse(transport.Response{Status: 200, Body: []byte(tt.body)}, userDecoder)
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

func TestHandleResponseDecodeBodyReceivesInputs(t *testing.T) {
	body := []byte(`{"success":true,"job":{"id":"x"}}`)
	called := false
	_, err := handleResponse(transport.Response{Status: 200, Body: body}, func(obj map[string]json.RawMessage, b []byte) error {
		called = true
		if string(obj["job"]) != `{"id":"x"}` || string(b) != string(body) {
			t.Errorf("obj/body mismatch: %v %s", obj, b)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("err=%v called=%v", err, called)
	}
	cause := errors.New("bad job")
	_, err = handleResponse(transport.Response{Status: 200, Body: body}, func(map[string]json.RawMessage, []byte) error { return cause })
	if !errors.Is(err, cause) || err.Error() != "mineskin: failed to parse response: bad job" {
		t.Errorf("err = %v", err)
	}
}

func TestHandleResponseAPIErrorSkipsDecodeBody(t *testing.T) {
	called := false
	_, err := handleResponse(transport.Response{Status: 400, Body: []byte(`{"success":false}`)},
		func(map[string]json.RawMessage, []byte) error { called = true; return errors.New("boom") })
	var apiErr *APIError
	if !errors.As(err, &apiErr) || called {
		t.Errorf("want *APIError without decodeBody, got %v (called=%v)", err, called)
	}
}

func TestHandleResponseSuccessWithBadPayloadIsParseError(t *testing.T) {
	_, err := handleResponse(transport.Response{Status: 200, Body: []byte(`{"success":true}`)},
		func(map[string]json.RawMessage, []byte) error { return errors.New("boom") })
	var apiErr *APIError
	if err == nil || errors.As(err, &apiErr) {
		t.Errorf("want parse error, got %v", err)
	}
}

func TestHandleResponseListsAndLinks(t *testing.T) {
	t.Run("lists default to empty non-nil", func(t *testing.T) {
		meta, err := handleResponse(transport.Response{Status: 200, Body: []byte(`{}`)}, noopDecoder)
		if err != nil {
			t.Fatal(err)
		}
		if meta.Errors == nil || meta.Warnings == nil || meta.Messages == nil ||
			len(meta.Errors)+len(meta.Warnings)+len(meta.Messages) != 0 {
			t.Errorf("lists = %#v %#v %#v", meta.Errors, meta.Warnings, meta.Messages)
		}
		if meta.Links != nil {
			t.Errorf("Links = %s, want nil", meta.Links)
		}
		if meta.Headers == nil {
			t.Error("Headers must be non-nil")
		}
	})
	t.Run("null lists are empty", func(t *testing.T) {
		meta, err := handleResponse(transport.Response{Status: 200, Body: []byte(`{"errors":null,"warnings":null,"messages":null}`)}, noopDecoder)
		if err != nil || meta.Errors == nil || meta.Warnings == nil || meta.Messages == nil {
			t.Errorf("meta=%+v err=%v", meta, err)
		}
	})
	t.Run("lists and links are filled", func(t *testing.T) {
		body := `{"success":true,"warnings":[{"code":"w","message":"W"}],"messages":[{"code":"m","message":"M"}],"links":{"self":"/x"}}`
		meta, err := handleResponse(transport.Response{Status: 200, Body: []byte(body)}, noopDecoder)
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
	meta, err := handleResponse(raw, noopDecoder)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Server != "api-1" || meta.Breadcrumb != "crumb" {
		t.Errorf("server=%q breadcrumb=%q", meta.Server, meta.Breadcrumb)
	}
	want := map[string]string{"mineskin-server": "api-1", "mineskin-breadcrumb": "crumb", "x-mineskin-foo": "bar"}
	if !reflect.DeepEqual(meta.Headers, want) {
		t.Errorf("Headers = %v", meta.Headers)
	}
}

func TestHandleResponseAPIErrorCarriesHeaders(t *testing.T) {
	raw := transport.Response{Status: 401, Header: mhdr("Mineskin-Breadcrumb", "b1"), Body: []byte(`{"success":false}`)}
	_, err := handleResponse(raw, noopDecoder)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Breadcrumb != "b1" || apiErr.Headers["mineskin-breadcrumb"] != "b1" {
		t.Errorf("err = %#v", err)
	}
}

func TestHandleResponseDuplicateHeadersDoNotFail(t *testing.T) {
	raw := transport.Response{Status: 200, Header: http.Header{"Mineskin-Server": {"a", "b"}}, Body: []byte(`{}`)}
	meta, err := handleResponse(raw, noopDecoder)
	if err != nil || meta.Server != "a,b" {
		t.Errorf("meta=%+v err=%v", meta, err)
	}
}

func TestHandleResponseUnexpectedIsClientError(t *testing.T) {
	_, err := handleResponse(transport.Response{Status: 530, Body: []byte("error code: 1033")}, noopDecoder)
	var ue *UnexpectedResponseError
	if !errors.As(err, &ue) {
		t.Errorf("want *UnexpectedResponseError: %v", err)
	}
}

func TestHandleResponseAcceptsUTF8BOM(t *testing.T) {
	body := append([]byte("\xef\xbb\xbf"), `{"success":true,"user":"x"}`...)
	meta, err := handleResponse(transport.Response{Status: 200, Body: body}, userDecoder)
	if err != nil || !meta.Success {
		t.Fatalf("meta = %+v err = %v", meta, err)
	}
	body = append([]byte("\xef\xbb\xbf"), `{"success":false,"errors":[{"code":"c","message":"m"}]}`...)
	_, err = handleResponse(transport.Response{Status: 400, Body: body}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Errorf("err = %v, want *APIError", err)
	}
}
