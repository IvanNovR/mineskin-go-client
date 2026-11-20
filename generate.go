package mineskin

import (
	"context"
	"encoding/json"
)

const (
	queuePath      = "/v2/queue"
	generatePath   = "/v2/generate"
	fieldJob       = "job"
	fieldRateLimit = "rateLimit"
	fieldUsage     = "usage"
)

// QueueResponse is the response to a queued generation request.
type QueueResponse struct {
	ResponseMeta
	// Job is the queued job.
	Job Job
	// RateLimit is the rate limit state, or nil when absent.
	RateLimit *RateLimitInfo
	// Usage is the usage state, or nil when absent.
	Usage *UsageInfo
}

// GenerateResponse is the response to a synchronous generation request.
type GenerateResponse struct {
	ResponseMeta
	// Skin is the generated skin.
	Skin SkinInfo
	// RateLimit is the rate limit state, or nil when absent.
	RateLimit *RateLimitInfo
	// Usage is the usage state, or nil when absent.
	Usage *UsageInfo
}

// Queue submits a skin generation job and returns it without waiting for the
// result; use WaitForJob or GetJob to follow it. A nil or invalid src fails
// with ErrInvalidInput without sending a request. opts may be nil.
func (c *Client) Queue(ctx context.Context, src Source, opts *GenerateOptions) (*QueueResponse, error) {
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	var out QueueResponse
	meta, err := c.submit(ctx, queuePath, src, opts, func(obj map[string]json.RawMessage) error {
		if v := obj[fieldJob]; len(v) > 0 {
			if err := json.Unmarshal(v, &out.Job); err != nil {
				return err
			}
		}
		out.RateLimit = decodeRateLimit(obj[fieldRateLimit])
		out.Usage = decodeUsage(obj[fieldUsage])
		return nil
	})
	if err != nil {
		return nil, err
	}
	out.ResponseMeta = meta
	out.Job.Breadcrumb = meta.Breadcrumb
	return &out, nil
}

// Generate generates a skin and waits for it in a single request. The request
// is bounded by the client timeout (see WithTimeout), which defaults to a
// value that may be too short for slow generations: raise it with WithTimeout
// or pass a context with a deadline. For long jobs prefer Queue followed by
// WaitForJob. A nil or invalid src fails with ErrInvalidInput without sending
// a request. opts may be nil.
func (c *Client) Generate(ctx context.Context, src Source, opts *GenerateOptions) (*GenerateResponse, error) {
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	var out GenerateResponse
	meta, err := c.submit(ctx, generatePath, src, opts, func(obj map[string]json.RawMessage) error {
		if err := decodeSkin(obj[fieldSkin], &out.Skin); err != nil {
			return err
		}
		out.RateLimit = decodeRateLimit(obj[fieldRateLimit])
		out.Usage = decodeUsage(obj[fieldUsage])
		return nil
	})
	if err != nil {
		return nil, err
	}
	out.ResponseMeta = meta
	out.Skin.Breadcrumb = meta.Breadcrumb
	return &out, nil
}

// submit encodes src, posts it to path and decodes the response with decode.
func (c *Client) submit(ctx context.Context, path string, src Source, opts *GenerateOptions, decode func(map[string]json.RawMessage) error) (ResponseMeta, error) {
	if src == nil {
		return ResponseMeta{}, invalidInput("source is nil")
	}
	body, err := src.requestBody(opts)
	if err != nil {
		return ResponseMeta{}, err
	}
	return c.post(ctx, path, body, func(obj map[string]json.RawMessage, _ []byte) error {
		return decode(obj)
	})
}
