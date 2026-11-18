package mineskin

import (
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

// do sends a request to the base URL plus path and reads the complete
// response. ctx must not be nil; a nil context yields an error matching
// ErrInvalidInput. Transport and read failures match ErrClient and keep the
// underlying error in the chain, so errors.Is(err, context.Canceled),
// context.DeadlineExceeded and net.Error timeouts keep working. The error text
// holds the method and path only, never headers or the API key.
func (c *Client) do(ctx context.Context, method, path string) (rawResponse, error) {
	label := method + " " + path
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, http.NoBody) //nolint:gosec // G704: the base URL is caller-configured and validated in NewClient
	if err != nil {
		return rawResponse{}, &clientError{msg: label, err: err, invalidInput: true}
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	start := time.Now()
	resp, err := c.httpClient.Do(req) //nolint:gosec // G704: the base URL is caller-configured and validated in NewClient
	if err != nil {
		return rawResponse{}, wrapClientError(label, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readLimited(resp.Body)
	if err != nil {
		return rawResponse{}, wrapClientError(label, err)
	}
	c.logger.Debug("MineSkin request completed",
		"method", method, "path", path, "status", resp.StatusCode,
		"breadcrumb", resp.Header.Get(headerMineskinCrumb), "elapsed", time.Since(start))
	return rawResponse{Status: resp.StatusCode, Header: resp.Header, Body: body}, nil
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

// get sends a GET request and interprets the response with handleResponse.
func (c *Client) get(ctx context.Context, path string, decodeBody func(map[string]json.RawMessage, []byte) error) (ResponseMeta, error) {
	raw, err := c.do(ctx, http.MethodGet, path)
	if err != nil {
		return ResponseMeta{}, err
	}
	return handleResponse(raw, decodeBody)
}
