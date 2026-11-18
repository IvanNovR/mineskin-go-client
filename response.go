package mineskin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

const (
	defaultErrorCode    = "request_failed"
	defaultErrorMessage = "Request Failed"

	fieldSuccess  = "success"
	fieldErrors   = "errors"
	fieldWarnings = "warnings"
	fieldMessages = "messages"
	fieldLinks    = "links"

	headerPrefix        = "mineskin-"
	headerPrefixExtra   = "x-mineskin-"
	headerMineskinSrv   = "mineskin-server"
	headerMineskinCrumb = "mineskin-breadcrumb"
	headerValueSep      = ","

	parseFailureMessage = "failed to parse response"
)

// ResponseMeta describes the envelope of a MineSkin API response.
type ResponseMeta struct {
	// Status is the HTTP status code.
	Status int
	// Success is the value of the response's success field, or whether the
	// status is 200 when the field is absent.
	Success bool
	// Errors lists the errors reported by the API.
	Errors []CodeAndMessage
	// Warnings lists the warnings reported by the API.
	Warnings []CodeAndMessage
	// Messages lists the informational messages reported by the API.
	Messages []CodeAndMessage
	// Headers holds the mineskin-* and x-mineskin-* response headers, keyed
	// by lower-case name.
	Headers map[string]string
	// Server is the value of the mineskin-server header.
	Server string
	// Breadcrumb is the value of the mineskin-breadcrumb header.
	Breadcrumb string
	// Links is the raw links field of the response, or nil when absent.
	Links json.RawMessage
}

// rawResponse is a fully read HTTP response, before any interpretation.
type rawResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// handleResponse turns a raw response into its metadata or an error. It
// mirrors the Java client: the HTTP status is only used when the body has no
// success field. decodeBody decodes the endpoint specific fields; it receives
// the top-level JSON fields and the complete body.
//
// The steps are, in order: a body that is not a JSON object gives an
// *UnexpectedResponseError; so does a body that fails to unmarshal; a failing
// envelope gives a parse error matching ErrClient; an unsuccessful response
// gives an *APIError without decoding the payload; a failing decodeBody on a
// successful response gives a parse error; otherwise the metadata is returned.
func handleResponse(raw rawResponse, decodeBody func(obj map[string]json.RawMessage, body []byte) error) (ResponseMeta, error) {
	if !looksLikeJSONObject(raw.Body) {
		return ResponseMeta{}, newUnexpectedResponseError(raw.Status, raw.Header, raw.Body, nil)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw.Body, &obj); err != nil {
		return ResponseMeta{}, newUnexpectedResponseError(raw.Status, raw.Header, raw.Body, err)
	}
	meta, err := decodeEnvelope(raw, obj)
	if err != nil {
		return ResponseMeta{}, wrapClientError(parseFailureMessage, err)
	}
	if !meta.Success {
		return ResponseMeta{}, newAPIError(meta)
	}
	if decodeBody != nil {
		if err := decodeBody(obj, raw.Body); err != nil {
			return ResponseMeta{}, wrapClientError(parseFailureMessage, err)
		}
	}
	return meta, nil
}

// looksLikeJSONObject reports whether the first non-whitespace byte is '{'.
func looksLikeJSONObject(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n\f\v")
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// decodeEnvelope reads the common fields of a response object.
func decodeEnvelope(raw rawResponse, obj map[string]json.RawMessage) (ResponseMeta, error) {
	headers := filterHeaders(raw.Header)
	meta := ResponseMeta{
		Status:     raw.Status,
		Success:    raw.Status == http.StatusOK,
		Headers:    headers,
		Server:     headers[headerMineskinSrv],
		Breadcrumb: headers[headerMineskinCrumb],
	}
	if v, ok := obj[fieldSuccess]; ok {
		if err := json.Unmarshal(v, &meta.Success); err != nil {
			return ResponseMeta{}, err
		}
	}
	for field, dst := range map[string]*[]CodeAndMessage{
		fieldErrors: &meta.Errors, fieldWarnings: &meta.Warnings, fieldMessages: &meta.Messages,
	} {
		if err := decodeList(obj[field], dst); err != nil {
			return ResponseMeta{}, err
		}
	}
	meta.Links = obj[fieldLinks]
	return meta, nil
}

// decodeList decodes a list field, leaving an empty non-nil slice when the
// field is absent or null.
func decodeList(v json.RawMessage, dst *[]CodeAndMessage) error {
	*dst = []CodeAndMessage{}
	if len(v) == 0 {
		return nil
	}
	if err := json.Unmarshal(v, dst); err != nil {
		return err
	}
	if *dst == nil {
		*dst = []CodeAndMessage{}
	}
	return nil
}

// newAPIError builds the error for an unsuccessful response, using the first
// reported error or the defaults.
func newAPIError(meta ResponseMeta) *APIError {
	e := &APIError{Code: defaultErrorCode, Message: defaultErrorMessage, ResponseMeta: meta}
	if len(meta.Errors) > 0 {
		e.Code = meta.Errors[0].Code
		e.Message = meta.Errors[0].Message
	}
	return e
}

// filterHeaders keeps the mineskin-* and x-mineskin-* headers, keyed by
// lower-case name. Repeated headers are joined with commas, so duplicates
// never cause a failure.
func filterHeaders(h http.Header) map[string]string {
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
