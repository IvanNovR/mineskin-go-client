package mineskin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// do sends a request through the transport and wraps its raw errors. ctx must
// not be nil; a nil context or an unbuildable request yields an error matching
// ErrInvalidInput. Transport and read failures match ErrClient and keep the
// underlying error in the chain, so errors.Is(err, context.Canceled),
// context.DeadlineExceeded and net.Error timeouts keep working. The error text
// holds the method and path only, never headers or the API key.
func (c *Client) do(ctx context.Context, method, path string, body *transport.Body) (transport.Response, error) {
	resp, err := c.doer.Do(ctx, method, path, body)
	if err != nil {
		return transport.Response{}, wrapTransportError(method+" "+path, err)
	}
	return resp, nil
}

// wrapTransportError maps a raw transport error to a client error: a request
// that could not be built is invalid input, anything else a client failure.
func wrapTransportError(label string, err error) error {
	var build *transport.BuildError
	if errors.As(err, &build) {
		return &clientError{msg: label, err: build.Err, invalidInput: true}
	}
	return wrapClientError(label, err)
}

// get sends a GET request through the get group (GetSkin, GetUser, GetJob,
// ListJobs and the poller) and interprets the response with handleResponse.
func (c *Client) get(ctx context.Context, path string, decodeBody func(map[string]json.RawMessage, []byte) error) (ResponseMeta, error) {
	return c.getGated(ctx, path, nil, decodeBody)
}

// getGated is get with a start gate (see exchange); a nil proceed is no gate.
func (c *Client) getGated(ctx context.Context, path string, proceed func() bool, decodeBody func(map[string]json.RawMessage, []byte) error) (ResponseMeta, error) {
	raw, err := c.exchange(ctx, c.getGroup, http.MethodGet, path, nil, proceed)
	if err != nil {
		return ResponseMeta{}, err
	}
	return handleResponse(raw, decodeBody)
}

// post sends a POST request with body through the generate group (Queue and
// Generate) and interprets the response with handleResponse.
func (c *Client) post(ctx context.Context, path string, body *transport.Body, decodeBody func(map[string]json.RawMessage, []byte) error) (ResponseMeta, error) {
	raw, err := c.exchange(ctx, c.genGroup, http.MethodPost, path, body, nil)
	if err != nil {
		return ResponseMeta{}, err
	}
	meta, err := handleResponse(raw, decodeBody)
	if c.genGroup.tuner != nil && isRetryableUnexpected(err) {
		c.genGroup.tuner.reportFailure()
	}
	return meta, err
}

// isRetryableUnexpected reports whether err is an *UnexpectedResponseError
// that is likely temporary; a JSON error (*APIError) never is (D11).
func isRetryableUnexpected(err error) bool {
	var ue *UnexpectedResponseError
	return errors.As(err, &ue) && ue.IsRetryable()
}
