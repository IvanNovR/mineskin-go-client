package mineskin

import "context"

const (
	queuePath    = "/v2/queue"
	generatePath = "/v2/generate"
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
	wire, meta, err := submit[queueWire](ctx, c, queuePath, src, opts)
	if err != nil {
		return nil, err
	}
	wire.Job.Breadcrumb = meta.Breadcrumb
	return &QueueResponse{ResponseMeta: meta, Job: wire.Job, RateLimit: wire.RateLimit, Usage: wire.Usage}, nil
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
	wire, meta, err := submit[generateWire](ctx, c, generatePath, src, opts)
	if err != nil {
		return nil, err
	}
	wire.Skin.Breadcrumb = meta.Breadcrumb
	return &GenerateResponse{ResponseMeta: meta, Skin: wire.Skin, RateLimit: wire.RateLimit, Usage: wire.Usage}, nil
}

// queueWire is the wire shape of a queued generation response.
type queueWire struct {
	Job       Job            `json:"job"`
	RateLimit *RateLimitInfo `json:"rateLimit"`
	Usage     *UsageInfo     `json:"usage"`
}

// generateWire is the wire shape of a synchronous generation response.
type generateWire struct {
	Skin      SkinInfo       `json:"skin"`
	RateLimit *RateLimitInfo `json:"rateLimit"`
	Usage     *UsageInfo     `json:"usage"`
}

// submit encodes src, posts it to path and decodes the response into W.
func submit[W any](ctx context.Context, c *Client, path string, src Source, opts *GenerateOptions) (W, ResponseMeta, error) {
	var zero W
	if src == nil {
		return zero, ResponseMeta{}, invalidInput("source is nil")
	}
	body, err := src.requestBody(opts)
	if err != nil {
		return zero, ResponseMeta{}, err
	}
	return postResponse[W](ctx, c, path, body)
}
