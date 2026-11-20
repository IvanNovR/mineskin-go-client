package mineskin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
	"github.com/IvanNovR/mineskin-go-client/internal/poll"
	"github.com/IvanNovR/mineskin-go-client/internal/ratelimit"
	"github.com/IvanNovR/mineskin-go-client/internal/throttle"
	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// This file is the root's translation layer between the public API and the
// neutral internal packages: it wires the request groups, adapts the job
// endpoints to internal/poll, and maps the raw errors of internal/ratelimit
// and internal/poll to the public error contract.

// errCheckSkipped is returned by a request whose start gate declined it. The
// poller treats it as "nothing to do", never as a job failure.
var errCheckSkipped = ratelimit.ErrSkipped

const (
	generateGroupName = "generate"
	getGroupName      = "get"
)

// acceptAnyBody is a payload decoder that never fails, so handleResponse only
// reports envelope and API errors.
func acceptAnyBody(map[string]json.RawMessage, []byte) error { return nil }

// newRequestGroups builds the generate and get groups from the resolved
// throttle settings.
func (c *Client) newRequestGroups(r *resolvedConfig) (gen, get *ratelimit.Group) {
	get = &ratelimit.Group{
		Name:     getGroupName,
		Throttle: newFixedThrottle(c.clock, r.getThrottle),
		Retries:  r.rateLimitRetries, Logger: r.logger, Clock: c.clock,
	}
	gen = &ratelimit.Group{
		Name:    generateGroupName,
		Retries: r.rateLimitRetries, Logger: r.logger, Clock: c.clock,
	}
	if !r.autoTuning {
		gen.Throttle = newFixedThrottle(c.clock, r.generateThrottle)
		return gen, get
	}
	// The tuner loads its grants through GetUser, which uses the get group (D8).
	gen.Tuner = ratelimit.NewTuner(ratelimit.TunerConfig{
		Clock: c.clock, Logger: r.logger, Load: c.loadGrants, BaseCtx: c.baseCtx, BG: &c.bg,
	})
	gen.Throttle = throttle.New(c.clock, gen.Tuner.Limits)
	gen.Tuner.SetKick(gen.Throttle.Kick)
	return gen, get
}

// newFixedThrottle builds a throttle with fixed limits.
func newFixedThrottle(clk clock.Clock, t Throttle) *throttle.Throttle {
	limits := throttle.Limits{Interval: t.Interval, Concurrency: t.Concurrency}
	return throttle.New(clk, func(time.Time) throttle.Limits { return limits })
}

// loadGrants fetches the grants of the configured API key through GetUser.
func (c *Client) loadGrants(ctx context.Context) (ratelimit.Grants, error) {
	resp, err := c.GetUser(ctx)
	if err != nil {
		return ratelimit.Grants{}, err
	}
	g := resp.User.Grants
	return ratelimit.Grants{PerMinute: g.PerMinute, Concurrency: g.Concurrency}, nil
}

// exchange waits for a start slot in g, sends the request and maps the raw
// errors of the group to the public contract. Time spent queued is bounded
// only by ctx; the client timeout covers the HTTP exchange alone. A non-nil
// proceed is the start gate (see ratelimit.Group.Do); when it declines,
// errCheckSkipped is returned.
func (c *Client) exchange(ctx context.Context, g *ratelimit.Group, method, path string, body *transport.Body, proceed func() bool) (transport.Response, error) {
	if ctx == nil {
		return transport.Response{}, newInvalidInputError("context is nil")
	}
	label := method + " " + path
	merged, cancel := c.requestContext(ctx)
	defer cancel()
	send := func(ctx context.Context) (transport.Response, error) { return c.do(ctx, method, path, body) }
	raw, err := g.Do(merged, send, proceed)
	if err != nil {
		return transport.Response{}, closedOrSame(ctx, merged, label, mapGroupErr(label, err))
	}
	return raw, nil
}

// mapGroupErr turns a raw error of ratelimit.Group.Do into a public one: a
// closed throttle or tuner is ErrClientClosed, errors already built by c.do
// pass through, and anything else (a ctx error of the queue or of the grants
// load) is wrapped as a client error that keeps the cause in its chain.
func mapGroupErr(label string, err error) error {
	var retry *ratelimit.RetryCancelledError
	if errors.As(err, &retry) {
		return newRetryCancelledError(mapGroupErr(label, retry.Err), retry.Last)
	}
	var ce *clientError
	switch {
	case errors.Is(err, errCheckSkipped), errors.As(err, &ce):
		return err
	case errors.Is(err, throttle.ErrClosed), errors.Is(err, ratelimit.ErrClosed):
		return wrapClientError(label, ErrClientClosed)
	default:
		return wrapClientError(label, err)
	}
}

// retryCancelledError is returned when the caller's ctx ends while a retry of
// a 429 is waiting or in flight. It matches both the ctx error and the 429
// error.
type retryCancelledError struct {
	// ctxErr is the wrapped ctx error of the failed retry attempt.
	ctxErr error
	// rateLimited is the *APIError or *UnexpectedResponseError of the 429.
	rateLimited error
}

// newRetryCancelledError combines the ctx error of a failed retry attempt with
// the error of the 429 that caused the retry; without a 429 error it returns
// ctxErr alone.
func newRetryCancelledError(ctxErr error, last transport.Response) error {
	_, rateLimited := handleResponse(last, acceptAnyBody)
	if rateLimited == nil {
		return ctxErr
	}
	return &retryCancelledError{ctxErr: ctxErr, rateLimited: rateLimited}
}

// Error implements the error interface and names both causes.
func (e *retryCancelledError) Error() string {
	return e.ctxErr.Error() + " (while retrying after " + e.rateLimited.Error() + ")"
}

// Unwrap lets errors.Is and errors.As reach both causes.
func (e *retryCancelledError) Unwrap() []error { return []error{e.ctxErr, e.rateLimited} }

// jobSource adapts the job endpoints of a Client to poll.Fetcher.
type jobSource struct{ c *Client }

var _ poll.Fetcher[*JobResponse] = jobSource{}

// Get fetches one job through the gated GET, mapping a declined start gate to
// poll.ErrSkipped.
func (s jobSource) Get(ctx context.Context, id string, proceed func() bool) (*JobResponse, poll.State, error) {
	resp, err := s.c.getJobGated(ctx, id, proceed)
	if errors.Is(err, errCheckSkipped) {
		return nil, poll.State{}, poll.ErrSkipped
	}
	if err != nil {
		return nil, poll.State{}, err
	}
	return resp, jobState(resp.Job), nil
}

// List fetches the states of the listed jobs with one request.
func (s jobSource) List(ctx context.Context) ([]poll.State, error) {
	list, err := s.c.ListJobs(ctx)
	if err != nil {
		return nil, err
	}
	states := make([]poll.State, len(list.Jobs))
	for i, j := range list.Jobs {
		states[i] = jobState(j)
	}
	return states, nil
}

// jobState derives the neutral poll state of j.
func jobState(j Job) poll.State {
	return poll.State{ID: j.ID, ETA: j.ETA, Done: isPollDone(j), Failed: j.Status == JobStatusFailed}
}

// toPollOptions maps the resolved public polling options to the neutral ones.
func toPollOptions(o PollOptions) poll.Options {
	return poll.Options{InitialDelay: o.InitialDelay, Interval: o.Interval, MaxAttempts: o.MaxAttempts, UseETA: o.UseETA}
}

// mapPollErr turns the errors of internal/poll into the public contract: an
// exhausted job is ErrMaxAttempts, a bad interval is ErrInvalidInput and a
// panicking one a wrapped error. Other errors (a Fetcher error, the error
// passed to Close) pass through.
func mapPollErr(err error) error {
	var exhausted *poll.ExhaustedError
	var interval *poll.IntervalError
	switch {
	case errors.As(err, &exhausted):
		return fmt.Errorf("%w: job %s after %d attempts", ErrMaxAttempts, exhausted.ID, exhausted.Attempts)
	case errors.As(err, &interval) && interval.Panic != nil:
		return wrapClientError("poll interval panicked", fmt.Errorf("%v", interval.Panic))
	case errors.As(err, &interval):
		return newInvalidInputError("poll interval must be greater than zero")
	default:
		return err
	}
}

// do sends a request through the transport and wraps its raw errors. ctx must
// not be nil; a nil context or an unbuildable request yields an error matching
// ErrInvalidInput. Transport and read failures keep the
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
	return handleResponse(raw, decodeBody)
}
