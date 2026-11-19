package mineskin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maxResponseBodyBytes is the largest response body the client reads. A larger
// body is an error, never silently truncated.
const maxResponseBodyBytes = 10 << 20

// requestBody is a request payload with its Content-Type. The client never
// logs it.
type requestBody struct {
	contentType string
	data        []byte
}

// reader returns the payload reader; a nil body is an empty one.
func (b *requestBody) reader() io.Reader {
	if b == nil {
		return http.NoBody
	}
	return bytes.NewReader(b.data)
}

// do sends a request to the base URL plus path with an optional body (nil
// sends none) and reads the complete
// response. ctx must not be nil; a nil context yields an error matching
// ErrInvalidInput. Transport and read failures match ErrClient and keep the
// underlying error in the chain, so errors.Is(err, context.Canceled),
// context.DeadlineExceeded and net.Error timeouts keep working. The error text
// holds the method and path only, never headers or the API key.
func (c *Client) do(ctx context.Context, method, path string, body *requestBody) (rawResponse, error) {
	label := method + " " + path
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body.reader())
	if err != nil {
		return rawResponse{}, &clientError{msg: label, err: err, invalidInput: true}
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", body.contentType)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return rawResponse{}, wrapClientError(label, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := readLimited(resp.Body)
	if err != nil {
		return rawResponse{}, wrapClientError(label, err)
	}
	c.logger.Debug("MineSkin request completed",
		"method", method, "path", path, "status", resp.StatusCode,
		"breadcrumb", resp.Header.Get(headerMineskinCrumb), "elapsed", time.Since(start))
	return rawResponse{Status: resp.StatusCode, Header: resp.Header, Body: respBody, Received: c.clock.Now()}, nil
}

// readLimited reads r completely and fails when it holds more than
// maxResponseBodyBytes.
func readLimited(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxResponseBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBodyBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxResponseBodyBytes)
	}
	return body, nil
}

// get sends a GET request through the get group (GetSkin, GetUser, GetJob,
// ListJobs and the poller) and interprets the response with handleResponse.
func (c *Client) get(ctx context.Context, path string, decodeBody func(map[string]json.RawMessage, []byte) error) (ResponseMeta, error) {
	raw, err := c.exchange(ctx, c.getGroup, http.MethodGet, path, nil)
	if err != nil {
		return ResponseMeta{}, err
	}
	return handleResponse(raw, decodeBody)
}

// post sends a POST request with body through the generate group (Queue and
// Generate) and interprets the response with handleResponse.
func (c *Client) post(ctx context.Context, path string, body *requestBody, decodeBody func(map[string]json.RawMessage, []byte) error) (ResponseMeta, error) {
	raw, err := c.exchange(ctx, c.genGroup, http.MethodPost, path, body)
	if err != nil {
		return ResponseMeta{}, err
	}
	return handleResponse(raw, decodeBody)
}
