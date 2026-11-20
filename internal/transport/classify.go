package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
)

const (
	headerPrefix      = "mineskin-"
	headerPrefixExtra = "x-mineskin-"
	headerValueSep    = ","
)

// utf8BOM is the byte order mark some proxies prepend to JSON bodies.
var utf8BOM = []byte("\xef\xbb\xbf")

// TrimBOM removes a leading UTF-8 byte order mark.
func TrimBOM(body []byte) []byte { return bytes.TrimPrefix(body, utf8BOM) }

// IsJSONObject reports whether the first non-whitespace byte is '{'.
func IsJSONObject(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n\f\v")
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// IsUnexpected reports whether r is not a MineSkin JSON object: the body, after
// an optional byte order mark, is not a JSON object or is invalid JSON.
func IsUnexpected(r Response) bool {
	_, ok, _ := decodeEnvelope(r)
	return !ok
}

// codeAndMessage is the shape of an entry of the errors, warnings and messages
// lists, used only to check that the lists are well formed.
type codeAndMessage struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// envelope is the part of a response the classifiers look at.
type envelope struct {
	Success  *bool            `json:"success"`
	Errors   []codeAndMessage `json:"errors"`
	Warnings []codeAndMessage `json:"warnings"`
	Messages []codeAndMessage `json:"messages"`
}

// decodeEnvelope decodes the envelope of r's JSON object body. ok is false
// when the body is not a JSON object or is not valid JSON; wellFormed is false
// when a field of the envelope has the wrong type.
func decodeEnvelope(r Response) (env envelope, ok, wellFormed bool) {
	body := TrimBOM(r.Body)
	if !IsJSONObject(body) {
		return envelope{}, false, false
	}
	err := json.Unmarshal(body, &env)
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return envelope{}, false, false
	}
	return env, true, err == nil
}

// IsFailure reports whether r is a failed response: an unexpected one, one
// whose envelope cannot be decoded (a success field that is not a boolean, or
// a malformed errors, warnings or messages list), one whose success field is
// not true, or, when success is absent, one whose status is not 200.
func IsFailure(r Response) bool {
	env, ok, wellFormed := decodeEnvelope(r)
	if !ok || !wellFormed {
		return true
	}
	if env.Success != nil {
		return !*env.Success
	}
	return r.Status != http.StatusOK
}

// FilterHeaders keeps the mineskin-* and x-mineskin-* headers, keyed by
// lower-case name. Repeated headers are joined with commas, so duplicates
// never cause a failure.
func FilterHeaders(h http.Header) map[string]string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	joined := make(map[string][]string)
	for _, k := range keys {
		name := strings.ToLower(k)
		if strings.HasPrefix(name, headerPrefix) || strings.HasPrefix(name, headerPrefixExtra) {
			joined[name] = append(joined[name], h[k]...)
		}
	}
	out := make(map[string]string, len(joined))
	for name, values := range joined {
		out[name] = strings.Join(values, headerValueSep)
	}
	return out
}
