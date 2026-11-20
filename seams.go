package mineskin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
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

// discardLogger is for decoding a response that was already decoded and logged.
var discardLogger = slog.New(slog.DiscardHandler)

// errCheckSkipped is returned by a request whose start gate declined it. The
// poller treats it as "nothing to do", never as a job failure.
var errCheckSkipped = ratelimit.ErrSkipped

const (
	generateGroupName = "generate"
	getGroupName      = "get"
)

// newRequestGroups builds the generate and get groups from the resolved
// pacing settings of cfg.
func (c *Client) newRequestGroups(cfg *config) (gen, get *ratelimit.Group) {
	get = &ratelimit.Group{
		Name:     getGroupName,
		Throttle: newFixedThrottle(c.clock, cfg.getThrottle),
		Retries:  cfg.rateLimitRetries, Logger: cfg.logger, Clock: c.clock,
	}
	gen = &ratelimit.Group{
		Name:    generateGroupName,
		Retries: cfg.rateLimitRetries, Logger: cfg.logger, Clock: c.clock,
	}
	if !cfg.generate.auto {
		gen.Throttle = newFixedThrottle(c.clock, cfg.generate.throttle)
		return gen, get
	}
	// The tuner loads its grants through GetUser, which uses the get group (D8).
	gen.Tuner = ratelimit.NewTuner(ratelimit.TunerConfig{
		Clock: c.clock, Logger: cfg.logger, Load: c.loadGrants, BaseCtx: c.baseCtx, BG: &c.bg,
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
		return transport.Response{}, invalidInput("context is nil")
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
// closed throttle or tuner is ErrClientClosed, an unbuildable request is
// ErrInvalidInput, and anything else (a ctx error of the queue, a transport or
// read failure) is wrapped with the label and keeps the cause in its chain.
func mapGroupErr(label string, err error) error {
	var retry *ratelimit.RetryCancelledError
	if errors.As(err, &retry) {
		return newRetryCancelledError(mapGroupErr(label, retry.Err), retry.Last)
	}
	var build *transport.BuildError
	switch {
	case errors.Is(err, errCheckSkipped):
		return err
	case errors.Is(err, throttle.ErrClosed), errors.Is(err, ratelimit.ErrClosed):
		return closedError(label)
	case errors.As(err, &build):
		return fmt.Errorf("%w: %s: %w", ErrInvalidInput, label, build.Err)
	default:
		return fmt.Errorf("mineskin: %s: %w", label, err)
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
	_, _, rateLimited := decodeResponse[struct{}](last, discardLogger, "")
	if rateLimited == nil {
		return ctxErr
	}
	return &retryCancelledError{ctxErr: ctxErr, rateLimited: rateLimited}
}

// Error implements the error interface and names both causes.
func (e *retryCancelledError) Error() string {
	return e.ctxErr.Error() + " (while retrying after " + strings.TrimPrefix(e.rateLimited.Error(), errorPrefix) + ")"
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
	return poll.State{ID: j.ID, ETA: j.ETA, Done: j.IsTerminal(), Failed: j.Status == JobStatusFailed}
}

// toPollOptions maps the resolved public polling options to the neutral ones.
func toPollOptions(o PollOptions) poll.Options {
	return poll.Options{InitialDelay: o.InitialDelay, Interval: o.Interval.Wait, MaxAttempts: o.MaxAttempts, UseETA: o.UseETA}
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
		return panickedIntervalError(interval.Panic)
	case errors.As(err, &interval):
		return invalidInput("poll interval must be greater than zero")
	default:
		return err
	}
}

// panickedIntervalError reports a recovered poll-interval panic. When the
// panic value is an error it stays in the chain.
func panickedIntervalError(r any) error {
	if err, ok := r.(error); ok {
		return fmt.Errorf("mineskin: poll interval panicked: %w", err)
	}
	return fmt.Errorf("mineskin: poll interval panicked: %v", r)
}

// do sends a request through the transport and returns its raw errors: a
// *transport.BuildError for a nil context or an unbuildable request, otherwise
// the HTTP client or body read error. mapGroupErr adds the method and path and
// the public classification. ctx must not be nil.
func (c *Client) do(ctx context.Context, method, path string, body *transport.Body) (transport.Response, error) {
	return c.doer.Do(ctx, method, path, body)
}

// getResponse sends a GET request through the get group (GetSkin, GetUser,
// GetJob, ListJobs and the poller) and decodes the response with
// decodeResponse. A non-nil proceed is the start gate (see exchange).
func getResponse[W any](ctx context.Context, c *Client, path string, proceed func() bool) (W, ResponseMeta, error) {
	raw, err := c.exchange(ctx, c.getGroup, http.MethodGet, path, nil, proceed)
	if err != nil {
		var zero W
		return zero, ResponseMeta{}, err
	}
	return decodeResponse[W](raw, c.logger, path)
}

// postResponse sends a POST request with body through the generate group
// (Queue and Generate) and decodes the response with decodeResponse.
func postResponse[W any](ctx context.Context, c *Client, path string, body *transport.Body) (W, ResponseMeta, error) {
	raw, err := c.exchange(ctx, c.genGroup, http.MethodPost, path, body, nil)
	if err != nil {
		var zero W
		return zero, ResponseMeta{}, err
	}
	return decodeResponse[W](raw, c.logger, path)
}
