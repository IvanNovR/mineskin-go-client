package mineskin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
	"github.com/IvanNovR/mineskin-go-client/internal/throttle"
)

const (
	generateGroupName = "generate"
	getGroupName      = "get"
)

// requestGroup is one throttled class of requests (D5). The generate group
// holds Queue and Generate; the get group holds every other request,
// including the job poller's.
type requestGroup struct {
	name     string
	throttle *throttle.Throttle
}

// newRequestGroups builds the generate and get groups from the resolved
// throttle settings.
func (c *Client) newRequestGroups(r *resolvedConfig) (gen, get *requestGroup) {
	return newRequestGroup(generateGroupName, c.clock, r.generateThrottle),
		newRequestGroup(getGroupName, c.clock, r.getThrottle)
}

// newRequestGroup builds a group with fixed limits.
func newRequestGroup(name string, clk clock.Clock, t Throttle) *requestGroup {
	limits := throttle.Limits{Interval: t.Interval, Concurrency: t.Concurrency}
	return &requestGroup{
		name:     name,
		throttle: throttle.New(clk, func(_ time.Time) throttle.Limits { return limits }),
	}
}

// exchange waits for a start slot in g, sends the request with do, applies any
// server delay and frees the slot. Time spent queued is bounded only by ctx; the client timeout
// covers the HTTP exchange alone.
func (c *Client) exchange(ctx context.Context, g *requestGroup, method, path string, body *requestBody) (rawResponse, error) {
	if ctx == nil {
		return rawResponse{}, newInvalidInputError("context is nil")
	}
	label := method + " " + path
	merged, cancel := c.requestContext(ctx)
	defer cancel()
	release, err := c.acquire(merged, g, label)
	if err != nil {
		return rawResponse{}, closedOrSame(ctx, merged, label, err)
	}
	defer release()
	raw, err := c.do(merged, method, path, body)
	if err != nil {
		return rawResponse{}, closedOrSame(ctx, merged, label, err)
	}
	// The delay must be in place before release, or the next queued waiter
	// could be dispatched ahead of it.
	if d := serverDelay(raw); d > 0 {
		g.throttle.Delay(raw.Received.Add(d))
	}
	return raw, nil
}

// serverDelay returns how long the server asks the group to wait: the later
// of the body's rateLimit.next.relative (any JSON object body) and the
// Retry-After header (retryable statuses only). Missing or invalid values
// give 0; it never fails, so the original result or error is never replaced.
func serverDelay(raw rawResponse) time.Duration {
	var delay time.Duration
	if rl := bodyRateLimit(raw.Body); rl != nil && rl.Next != nil {
		delay = rl.Next.Relative
	}
	if isRetryableStatus(raw.Status) {
		if ra, ok := parseRetryAfter(raw.Header.Get(headerRetryAfter), raw.Received); ok {
			delay = max(delay, ra)
		}
	}
	return delay
}

// bodyRateLimit leniently reads the rateLimit field of a JSON object body. It
// returns nil for a body that is not a JSON object or has no usable rateLimit.
func bodyRateLimit(body []byte) *RateLimitInfo {
	body = bytes.TrimPrefix(body, utf8BOM)
	if !looksLikeJSONObject(body) {
		return nil
	}
	var env struct {
		RateLimit json.RawMessage `json:"rateLimit"`
	}
	if json.Unmarshal(body, &env) != nil {
		return nil
	}
	return decodeRateLimit(env.RateLimit)
}

// closedOrSame returns err, or an ErrClientClosed error when err happened
// because Close cancelled merged while the caller's own ctx is still live.
func closedOrSame(ctx, merged context.Context, label string, err error) error {
	if ctx.Err() == nil && errors.Is(context.Cause(merged), ErrClientClosed) {
		return wrapClientError(label, ErrClientClosed)
	}
	return err
}

// acquire takes a slot in g's throttle. A done ctx yields an error matching
// ErrClient with the ctx error in its chain; a closed throttle yields
// ErrClientClosed.
func (c *Client) acquire(ctx context.Context, g *requestGroup, label string) (func(), error) {
	release, err := g.throttle.Acquire(ctx)
	if errors.Is(err, throttle.ErrClosed) {
		return nil, wrapClientError(label, ErrClientClosed)
	}
	if err != nil {
		return nil, wrapClientError(label, err)
	}
	return release, nil
}
