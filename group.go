package mineskin

import (
	"context"
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

// exchange waits for a start slot in g, sends the request with do and frees
// the slot. Time spent queued is bounded only by ctx; the client timeout
// covers the HTTP exchange alone.
func (c *Client) exchange(ctx context.Context, g *requestGroup, method, path string, body *requestBody) (rawResponse, error) {
	if ctx == nil {
		return rawResponse{}, newInvalidInputError("context is nil")
	}
	release, err := c.acquire(ctx, g, method+" "+path)
	if err != nil {
		return rawResponse{}, err
	}
	defer release()
	return c.do(ctx, method, path, body)
}

// acquire takes a slot in g's throttle. A done ctx or a closed throttle
// (ErrClientClosed arrives with Close) yields an error matching ErrClient; the ctx error stays in the chain.
func (c *Client) acquire(ctx context.Context, g *requestGroup, label string) (func(), error) {
	release, err := g.throttle.Acquire(ctx)
	if err != nil {
		return nil, wrapClientError(label, err)
	}
	return release, nil
}
