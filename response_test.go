package mineskin

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
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
			meta, err := handleResponse(rawResponse{Status: tt.status, Body: []byte(tt.body)}, userDecoder)
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
		{"fallbacks without errors", 400, `{"success":false}`, "request_failed", "Request Failed", false},
		{"fallbacks with empty errors", 400, `{"success":false,"errors":[]}`, "request_failed", "Request Failed", false},
		{"success absent with 404", 404, `{"errors":[{"code":"skin_not_found","message":"nope"}]}`, "skin_not_found", "nope", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := handleResponse(rawResponse{Status: tt.status, Body: []byte(tt.body)}, noopDecoder)
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("want *APIError, got %T %v", err, err)
			}
			if apiErr.Code != tt.wantCode || apiErr.Message != tt.wantMessage || apiErr.Status != tt.status || apiErr.Success {
				t.Errorf("apiErr = %+v", apiErr)
			}
			if !errors.Is(err, ErrClient) || errors.Is(err, ErrNotFound) != tt.wantNotFnd {
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
			_, err := handleResponse(rawResponse{Status: tt.status, Body: []byte(tt.body)}, userDecoder)
			var ue *UnexpectedResponseError
			if !errors.As(err, &ue) {
				t.Fatalf("want *UnexpectedResponseError, got %T %v", err, err)
			}
			if (ue.Err != nil) != tt.wantCause || ue.IsRetryable() != tt.retryable || ue.Status != tt.status {
				t.Errorf("ue = %+v", ue)
			}
			if !errors.Is(err, ErrClient) {
				t.Error("must match ErrClient")
			}
		})
	}
}

func TestHandleResponseUnexpectedUsesHeaders(t *testing.T) {
	raw := rawResponse{
		Status: 530,
		Header: mhdr("Server", "cloudflare", "CF-Ray", "8f3a-FRA", "Content-Type", "text/plain"),
		Body:   []byte("error code: 1033"),
	}
	_, err := handleResponse(raw, noopDecoder)
	want := "HTTP 530 from Cloudflare (error 1033, ray 8f3a-FRA): error code: 1033"
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
			_, err := handleResponse(rawResponse{Status: 200, Body: []byte(tt.body)}, userDecoder)
			var ue *UnexpectedResponseError
			var apiErr *APIError
			if err == nil || errors.As(err, &ue) || errors.As(err, &apiErr) {
				t.Fatalf("want plain parse error, got %T %v", err, err)
			}
			if !errors.Is(err, ErrClient) || errors.Is(err, ErrInvalidInput) {
				t.Errorf("must match ErrClient only: %v", err)
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
	_, err := handleResponse(rawResponse{Status: 200, Body: body}, func(obj map[string]json.RawMessage, b []byte) error {
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
	_, err = handleResponse(rawResponse{Status: 200, Body: body}, func(map[string]json.RawMessage, []byte) error { return cause })
	if !errors.Is(err, cause) || !errors.Is(err, ErrClient) || err.Error() != "mineskin: failed to parse response: bad job" {
		t.Errorf("err = %v", err)
	}
}

func TestHandleResponseDecodeBodyFailureBeatsAPIError(t *testing.T) {
	// Java builds the response (decoding the body) before the success check,
	// so decodeBody runs, and its failure is reported as a parse error.
	_, err := handleResponse(rawResponse{Status: 400, Body: []byte(`{"success":false}`)},
		func(map[string]json.RawMessage, []byte) error { return errors.New("boom") })
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Errorf("decodeBody failure must win over APIError: %v", err)
	}
}

func TestHandleResponseListsAndLinks(t *testing.T) {
	t.Run("lists default to empty non-nil", func(t *testing.T) {
		meta, err := handleResponse(rawResponse{Status: 200, Body: []byte(`{}`)}, noopDecoder)
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
		meta, err := handleResponse(rawResponse{Status: 200, Body: []byte(`{"errors":null,"warnings":null,"messages":null}`)}, noopDecoder)
		if err != nil || meta.Errors == nil || meta.Warnings == nil || meta.Messages == nil {
			t.Errorf("meta=%+v err=%v", meta, err)
		}
	})
	t.Run("lists and links are filled", func(t *testing.T) {
		body := `{"success":true,"warnings":[{"code":"w","message":"W"}],"messages":[{"code":"m","message":"M"}],"links":{"self":"/x"}}`
		meta, err := handleResponse(rawResponse{Status: 200, Body: []byte(body)}, noopDecoder)
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
	raw := rawResponse{
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
	raw := rawResponse{Status: 401, Header: mhdr("Mineskin-Breadcrumb", "b1"), Body: []byte(`{"success":false}`)}
	_, err := handleResponse(raw, noopDecoder)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Breadcrumb != "b1" || apiErr.Headers["mineskin-breadcrumb"] != "b1" {
		t.Errorf("err = %#v", err)
	}
}

func TestFilterHeaders(t *testing.T) {
	tests := []struct {
		name string
		in   http.Header
		want map[string]string
	}{
		{"nil", nil, map[string]string{}},
		{"empty", http.Header{}, map[string]string{}},
		{"unrelated dropped", mhdr("Content-Type", "x", "Server", "nginx"), map[string]string{}},
		{"lowercased", mhdr("Mineskin-Server", "a"), map[string]string{"mineskin-server": "a"}},
		{"x prefix", mhdr("X-MineSkin-Id", "1"), map[string]string{"x-mineskin-id": "1"}},
		{"duplicates joined (JB-08)", mhdr("Mineskin-Tag", "a", "mineskin-tag", "b", "MINESKIN-TAG", "c"), map[string]string{"mineskin-tag": "a,b,c"}},
		{"non-canonical map keys merged", http.Header{"mineskin-x": {"1"}, "Mineskin-X": {"2"}}, map[string]string{"mineskin-x": "2,1"}},
		{"prefix must be followed by dash", mhdr("Mineskinfoo", "a", "X-Mineskinfoo", "b"), map[string]string{}},
		{"empty value kept", http.Header{"Mineskin-Empty": {""}}, map[string]string{"mineskin-empty": ""}},
		{"no values", http.Header{"Mineskin-None": nil}, map[string]string{"mineskin-none": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filterHeaders(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHandleResponseDuplicateHeadersDoNotFail(t *testing.T) {
	raw := rawResponse{Status: 200, Header: http.Header{"Mineskin-Server": {"a", "b"}}, Body: []byte(`{}`)}
	meta, err := handleResponse(raw, noopDecoder)
	if err != nil || meta.Server != "a,b" {
		t.Errorf("meta=%+v err=%v", meta, err)
	}
}

func TestHandleResponseUnexpectedIsClientError(t *testing.T) {
	_, err := handleResponse(rawResponse{Status: 530, Body: []byte("error code: 1033")}, noopDecoder)
	if !errors.Is(err, ErrClient) {
		t.Errorf("must match ErrClient: %v", err)
	}
}
