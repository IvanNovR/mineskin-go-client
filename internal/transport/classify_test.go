package transport

import (
	"net/http"
	"testing"
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
