package transport

import (
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestIsUnexpected(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"JSON object", `{"success":true}`, false},
		{"empty object", `{}`, false},
		{"leading whitespace", " \n\t{\"a\":1}", false},
		{"UTF-8 BOM", "\xef\xbb\xbf{\"a\":1}", false},
		{"empty body", ``, true},
		{"HTML page", `<html><title>502</title></html>`, true},
		{"JSON array", `[1,2]`, true},
		{"JSON string", `"x"`, true},
		{"truncated JSON", `{"success":tr`, true},
		{"trailing garbage", `{"a":1} x`, true},
		{"BOM then HTML", "\xef\xbb\xbf<html>", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsUnexpected(Response{Status: http.StatusOK, Body: []byte(tt.body)}); got != tt.want {
				t.Errorf("IsUnexpected(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func TestIsUnexpectedNilBody(t *testing.T) {
	if !IsUnexpected(Response{Status: http.StatusBadGateway}) {
		t.Error("IsUnexpected(nil body) = false, want true")
	}
}

func TestIsFailure(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"success true 200", 200, `{"success":true}`, false},
		{"success true on 429", 429, `{"success":true}`, false},
		{"success false 200", 200, `{"success":false}`, true},
		{"success absent 200", 200, `{}`, false},
		{"success absent 429", 429, `{}`, true},
		{"success absent 404", 404, `{"errors":[]}`, true},
		{"success null 200", 200, `{"success":null}`, false},
		{"success null 429", 429, `{"success":null}`, true},
		{"success wrong type", 200, `{"success":"x"}`, true},
		{"success number", 200, `{"success":1}`, true},
		{"BOM success true", 200, "\xef\xbb\xbf{\"success\":true}", false},
		{"HTML 502", 502, `<html></html>`, true},
		{"HTML 200", 200, `<html></html>`, true},
		{"truncated JSON", 200, `{"success":true`, true},
		{"empty body 429", 429, ``, true},
		{"malformed errors list", 429, `{"success":true,"errors":"x"}`, true},
		{"malformed warnings entry", 200, `{"success":true,"warnings":[{"code":5}]}`, true},
		{"null lists", 200, `{"errors":null,"warnings":null,"messages":null}`, false},
		{"valid lists", 200, `{"success":true,"errors":[{"code":"a","message":"b"}],"messages":[]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsFailure(Response{Status: tt.status, Body: []byte(tt.body)}); got != tt.want {
				t.Errorf("IsFailure(%d, %q) = %v, want %v", tt.status, tt.body, got, tt.want)
			}
		})
	}
}

var testNow = time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func TestFilterHeaders(t *testing.T) {
	tests := []struct {
		name string
		in   http.Header
		want http.Header
	}{
		{"nil", nil, http.Header{}},
		{"empty", http.Header{}, http.Header{}},
		{"unrelated dropped", hdr("Content-Type", "x", "Server", "nginx", "Set-Cookie", "a=b"), http.Header{}},
		{"canonical name", hdr("Mineskin-Server", "a"), http.Header{"Mineskin-Server": {"a"}}},
		{"x prefix", hdr("X-MineSkin-Id", "1"), http.Header{"X-Mineskin-Id": {"1"}}},
		{"repeated values kept (JB-08)", http.Header{"Mineskin-Tag": {"a", "b", "c"}}, http.Header{"Mineskin-Tag": {"a", "b", "c"}}},
		{"non-canonical map keys merged", http.Header{"mineskin-x": {"1"}, "Mineskin-X": {"2"}}, http.Header{"Mineskin-X": {"2", "1"}}},
		{"prefix must be followed by dash", hdr("Mineskinfoo", "a", "X-Mineskinfoo", "b"), http.Header{}},
		{"empty value kept", http.Header{"Mineskin-Empty": {""}}, http.Header{"Mineskin-Empty": {""}}},
		{"no values", http.Header{"Mineskin-None": nil}, http.Header{"Mineskin-None": nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FilterHeaders(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestFilterHeadersIsAClone(t *testing.T) {
	in := http.Header{"Mineskin-Tag": {"a"}}
	out := FilterHeaders(in)
	out["Mineskin-Tag"][0] = "changed"
	out.Add("Mineskin-Tag", "more")
	if !reflect.DeepEqual(in, http.Header{"Mineskin-Tag": {"a"}}) {
		t.Errorf("input modified: %v", in)
	}
}

func TestTrimBOMAndIsJSONObject(t *testing.T) {
	if got := string(TrimBOM([]byte("\xef\xbb\xbf{}"))); got != "{}" {
		t.Errorf("TrimBOM = %q, want {}", got)
	}
	if got := string(TrimBOM([]byte("{}"))); got != "{}" {
		t.Errorf("TrimBOM without BOM = %q", got)
	}
	for body, want := range map[string]bool{"{}": true, " \r\n\t\f\v{": true, "": false, "  ": false, "[": false, "x{": false} {
		if got := IsJSONObject([]byte(body)); got != want {
			t.Errorf("IsJSONObject(%q) = %v, want %v", body, got, want)
		}
	}
}
