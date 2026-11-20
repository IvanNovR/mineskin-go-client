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
	headerMineskinCrumb = transport.HeaderMineskinCrumb

	parseFailureMessage = "failed to parse response"
)

// envelope holds the fields every MineSkin response has in common. Absent
// lists stay nil here and are made empty by newMeta.
type envelope struct {
	Success  *bool            `json:"success"`
	Errors   []CodeAndMessage `json:"errors"`
	Warnings []CodeAndMessage `json:"warnings"`
	Messages []CodeAndMessage `json:"messages"`
	Links    json.RawMessage  `json:"links"`
}

// failureWire is the envelope plus the fields an unsuccessful response may
// carry, decoded before the endpoint specific payload.
type failureWire struct {
	envelope
	RateLimit *RateLimitInfo `json:"rateLimit"`
	Usage     *UsageInfo     `json:"usage"`
}

// ResponseMeta describes the envelope of a MineSkin API response. Every
// response type has it in a field named Meta, and so do *APIError and
// *JobFailedError.
type ResponseMeta struct {
	// Status is the HTTP status code.
	Status int
	// Header holds the mineskin-* and x-mineskin-* response headers, keyed by
	// canonical name. It is a copy: other headers are not passed on and the
	// caller may modify it.
	Header http.Header
	// Breadcrumb is the value of the mineskin-breadcrumb header, which MineSkin
	// asks to be quoted in support requests. Repeated headers are joined with
	// commas.
	Breadcrumb string
	// Warnings lists the warnings reported by the API.
	Warnings []CodeAndMessage
	// Messages lists the informational messages reported by the API.
	Messages []CodeAndMessage
	// Links is the raw links field of the response, or nil when absent.
	Links json.RawMessage
}

// decodeResponse turns a raw response into the endpoint's wire struct W and
// the response metadata, or an error. It mirrors the Java client: the HTTP
// status is only used when the body has no success field. A leading UTF-8 byte
// order mark is ignored.
//
// The steps are, in order: a body that is not a JSON object, or is not valid
// JSON, gives an *UnexpectedResponseError; a failing envelope gives a parse
// error; an unsuccessful response gives an *APIError without decoding the
// payload; a failing payload on a successful response gives a parse error;
// otherwise the wire struct and the metadata are returned.
func decodeResponse[W any](raw transport.Response) (W, ResponseMeta, error) {
	var wire W
	body := transport.TrimBOM(raw.Body)
	if !transport.IsJSONObject(body) {
		return wire, ResponseMeta{}, newUnexpectedResponseError(raw.Status, raw.Header, raw.Body, nil, raw.Received)
	}
	var head failureWire
	if err := json.Unmarshal(body, &head); err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			return wire, ResponseMeta{}, newUnexpectedResponseError(raw.Status, raw.Header, raw.Body, err, raw.Received)
		}
		return wire, ResponseMeta{}, parseFailure(err)
	}
	meta := newMeta(raw, head.envelope)
	if !head.succeeded(raw.Status) {
		return wire, ResponseMeta{}, newAPIError(meta, head, raw.Header, raw.Received)
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return wire, ResponseMeta{}, parseFailure(err)
	}
	return wire, meta, nil
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

// succeeded reports whether the response was successful: the value of its
// success field, or whether the status is 200 when the field is absent.
func (e envelope) succeeded(status int) bool {
	if e.Success != nil {
		return *e.Success
	}
	return status == http.StatusOK
}

// newMeta builds the metadata from the raw response and its decoded envelope.
// The lists are never nil.
func newMeta(raw transport.Response, env envelope) ResponseMeta {
	header := transport.FilterHeaders(raw.Header)
	return ResponseMeta{
		Status:     raw.Status,
		Header:     header,
		Breadcrumb: strings.Join(header.Values(headerMineskinCrumb), ","),
		Warnings:   nonNilList(env.Warnings),
		Messages:   nonNilList(env.Messages),
		Links:      env.Links,
	}
}

// nonNilList returns l, or an empty non-nil slice when l is nil.
func nonNilList(l []CodeAndMessage) []CodeAndMessage {
	if l == nil {
		return []CodeAndMessage{}
	}
	return l
}

// newAPIError builds the error for an unsuccessful response. Code and Message
// are those of the first reported error and stay empty when the server sent
// none. The rateLimit, usage and Retry-After values are decoded leniently:
// anything missing or malformed stays zero and never replaces the original
// error (JB-13).
func newAPIError(meta ResponseMeta, head failureWire, h http.Header, now time.Time) *APIError {
	e := &APIError{
		Errors: nonNilList(head.Errors), Meta: meta,
		RateLimit: head.RateLimit, Usage: head.Usage,
	}
	e.RetryAfter, _ = transport.ParseRetryAfter(h.Get(transport.HeaderRetryAfter), now)
	if len(e.Errors) > 0 {
		e.Code = e.Errors[0].Code
		e.Message = e.Errors[0].Message
	}
	return e
}
