package mineskin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

const (
	defaultErrorCode    = "request_failed"
	defaultErrorMessage = "request failed"

	fieldSuccess  = "success"
	fieldErrors   = "errors"
	fieldWarnings = "warnings"
	fieldMessages = "messages"
	fieldLinks    = "links"

	headerMineskinSrv   = "mineskin-server"
	headerMineskinCrumb = transport.HeaderMineskinCrumb

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

// handleResponse turns a raw response into its metadata or an error. It
// mirrors the Java client: the HTTP status is only used when the body has no
// success field. A leading UTF-8 byte order mark is ignored. decodeBody decodes the endpoint specific fields; it receives
// the top-level JSON fields and the complete body.
//
// The steps are, in order: a body that is not a JSON object gives an
// *UnexpectedResponseError; so does a body that fails to unmarshal; a failing
// envelope gives a parse error; an unsuccessful response
// gives an *APIError without decoding the payload; a failing decodeBody on a
// successful response gives a parse error; otherwise the metadata is returned.
func handleResponse(raw transport.Response, decodeBody func(obj map[string]json.RawMessage, body []byte) error) (ResponseMeta, error) {
	body := transport.TrimBOM(raw.Body)
	if !transport.IsJSONObject(body) {
		return ResponseMeta{}, newUnexpectedResponseError(raw.Status, raw.Header, raw.Body, nil, raw.Received)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return ResponseMeta{}, newUnexpectedResponseError(raw.Status, raw.Header, raw.Body, err, raw.Received)
	}
	meta, err := decodeEnvelope(raw, obj)
	if err != nil {
		return ResponseMeta{}, parseFailure(err)
	}
	if !meta.Success {
		return ResponseMeta{}, newAPIError(meta, obj, raw.Header, raw.Received)
	}
	if decodeBody != nil {
		if err := decodeBody(obj, body); err != nil {
			return ResponseMeta{}, parseFailure(err)
		}
	}
	return meta, nil
}

// parseFailure wraps a payload decoding error. A malformed value in a server
// response (for example a UUID that ParseUUID rejects) is a parse failure, not
// bad caller input, so the ErrInvalidInput classification and its prefix are
// dropped from the text and the chain.
func parseFailure(err error) error {
	if errors.Is(err, ErrInvalidInput) {
		plain := strings.Replace(err.Error(), errorPrefix+"invalid input: ", "", 1)
		return fmt.Errorf("%s%s: %s", errorPrefix, parseFailureMessage, plain)
	}
	return fmt.Errorf("%s%s: %w", errorPrefix, parseFailureMessage, err)
}

// decodeEnvelope reads the common fields of a response object.
func decodeEnvelope(raw transport.Response, obj map[string]json.RawMessage) (ResponseMeta, error) {
	headers := transport.FilterHeaders(raw.Header)
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
// reported error or the defaults. The rateLimit, usage and Retry-After values
// are decoded leniently: anything missing or malformed stays nil or zero and
// never replaces the original error (JB-13).
func newAPIError(meta ResponseMeta, obj map[string]json.RawMessage, h http.Header, now time.Time) *APIError {
	e := &APIError{
		Code: defaultErrorCode, Message: defaultErrorMessage, ResponseMeta: meta,
		RateLimit: decodeRateLimit(obj[fieldRateLimit]),
		Usage:     decodeUsage(obj[fieldUsage]),
	}
	e.RetryAfter, _ = transport.ParseRetryAfter(h.Get(transport.HeaderRetryAfter), now)
	if len(meta.Errors) > 0 {
		e.Code = meta.Errors[0].Code
		e.Message = meta.Errors[0].Message
	}
	return e
}
