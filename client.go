package mineskin

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
	"github.com/IvanNovR/mineskin-go-client/internal/poll"
	"github.com/IvanNovR/mineskin-go-client/internal/ratelimit"
	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

const (
	// DefaultBaseURL is the base URL of the MineSkin API.
	DefaultBaseURL = "https://api.mineskin.org"
	// DefaultUserAgent is the User-Agent used when none is configured. Set a
	// custom one such as "AppName/1.0" instead.
	DefaultUserAgent = "MineSkinClient"
	// DefaultTimeout is the default timeout of a single HTTP request.
	DefaultTimeout = 10 * time.Second

	apiKeyPrefix = "msk_"
)

// DefaultRateLimitRetries is the default number of automatic retries of a
// request rejected with HTTP 429 (see WithRateLimitRetries).
const DefaultRateLimitRetries = 2

// Client is a MineSkin API client. It is immutable after NewClient returns and
// safe for concurrent use by multiple goroutines.
//
// The configuration is immutable; Close only changes lifecycle state. The only
// background goroutine is the job poller used by WaitForJob. It is started
// lazily by the first wait and exits by itself once no wait is pending; a
// later wait starts it again. Close releases the client's resources: it fails
// queued and waiting calls, cancels requests in flight and waits for the
// poller to exit (see Close). The client creates its own HTTP transport
// unless WithTransport is used.
//
// Go's HTTP client already strips the Authorization header when a redirect
// leads to a different host, and this client refuses redirects from https to
// http and stops after 10 redirects.
type Client struct {
	// doer holds the base URL, User-Agent, API key and HTTP client.
	doer     *transport.Doer
	logger   *slog.Logger
	polling  PollOptions
	poller   *poll.Poller[*JobResponse]
	clock    clock.Clock
	genGroup *ratelimit.Group
	getGroup *ratelimit.Group

	// baseCtx is cancelled by Close and parents every request context.
	baseCtx    context.Context
	cancelBase context.CancelCauseFunc
	closed     atomic.Bool
	closeOnce  sync.Once
	// bg tracks the grants load goroutine, which Close waits for.
	bg sync.WaitGroup
	// idleCloser closes idle connections of a transport the client created;
	// nil when the caller supplied the transport.
	idleCloser func()
}

var _ io.Closer = (*Client)(nil)

// NewClient creates a Client from the given options. Invalid configuration
// yields an error matching ErrInvalidInput; the error never
// contains the API key.
func NewClient(opts ...Option) (*Client, error) {
	cfg := &config{
		baseURL:   DefaultBaseURL,
		userAgent: DefaultUserAgent,
		timeout:   DefaultTimeout,

		rateLimitRetries: DefaultRateLimitRetries,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	r, err := cfg.resolve()
	if err != nil {
		return nil, err
	}
	logConfig(r.logger, r.baseURL, r.userAgent, r.apiKey)
	c := &Client{
		doer: &transport.Doer{
			BaseURL:   r.baseURL,
			UserAgent: r.userAgent,
			APIKey:    r.apiKey,
			HTTP: &http.Client{
				Transport:     r.transport,
				Timeout:       r.timeout,
				CheckRedirect: checkRedirect,
			},
			Clock:  r.clock,
			Logger: r.logger,
		},
		logger:  r.logger,
		polling: r.polling,
		clock:   r.clock,
	}
	c.baseCtx, c.cancelBase = context.WithCancelCause(context.Background())
	c.idleCloser = r.idleCloser
	c.genGroup, c.getGroup = c.newRequestGroups(r)
	c.poller = poll.New(poll.Config[*JobResponse]{
		Ctx: c.baseCtx, Clock: c.clock, Fetcher: jobSource{c}, Options: toPollOptions(r.polling),
		Logger: r.logger, OnJobFailure: c.failureHook(),
		GetLogAttrs: []any{"path", queuePath + "/{id}"}, ListLogAttrs: []any{"path", queuePath},
	})
	return c, nil
}

// failureHook returns the poller's FAILED-job hook: the penalty with
// auto-tuning, nil (a no-op) otherwise.
func (c *Client) failureHook() func() {
	if c.genGroup.Tuner == nil {
		return nil
	}
	return c.genGroup.Tuner.ReportFailure
}

// resolvedConfig is a validated config with defaults applied.
type resolvedConfig struct {
	baseURL   string
	userAgent string
	apiKey    string
	timeout   time.Duration
	transport http.RoundTripper
	logger    *slog.Logger
	polling   PollOptions
	clock     clock.Clock
	// idleCloser is set only when the client created the transport itself.
	idleCloser func()

	generateThrottle Throttle
	getThrottle      Throttle
	autoTuning       bool
	// rateLimitRetries is never negative.
	rateLimitRetries int
}

// resolve validates cfg and fills in defaults. Errors match ErrInvalidInput
// and never contain the API key.
func (cfg *config) resolve() (*resolvedConfig, error) {
	baseURL, err := normalizeBaseURL(cfg.baseURL)
	if err != nil {
		return nil, err
	}
	if cfg.rateLimitRetries < 0 {
		return nil, newInvalidInputError("rate limit retries must not be negative")
	}
	if cfg.timeout <= 0 {
		return nil, newInvalidInputError("timeout must be greater than zero")
	}
	polling, err := cfg.polling.withDefaults()
	if err != nil {
		return nil, err
	}
	gen, get, err := cfg.resolveThrottles()
	if err != nil {
		return nil, err
	}
	r := &resolvedConfig{
		generateThrottle: gen, getThrottle: get,
		autoTuning:       cfg.autoTuning,
		rateLimitRetries: cfg.rateLimitRetries,
		baseURL:          baseURL, timeout: cfg.timeout, polling: polling,
		transport: cfg.transport, logger: cfg.logger,
		userAgent: strings.TrimSpace(cfg.userAgent),
		apiKey:    strings.TrimSpace(cfg.apiKey),
	}
	if r.transport == nil {
		owned := transport.DefaultTransport()
		r.transport, r.idleCloser = owned, owned.CloseIdleConnections
	}
	r.clock = cfg.clock
	if r.clock == nil {
		r.clock = clock.Real()
	}
	if r.logger == nil {
		r.logger = slog.New(slog.DiscardHandler)
	}
	if r.userAgent == "" {
		r.userAgent = DefaultUserAgent
	}
	if err := validateHeaderValues(r.userAgent, r.apiKey); err != nil {
		return nil, err
	}
	return r, nil
}

// resolveThrottles applies the group defaults and rejects auto-tuning
// combined with an explicit generate throttle.
func (cfg *config) resolveThrottles() (gen, get Throttle, err error) {
	if cfg.autoTuning && cfg.generateThrottleSet {
		return gen, get, newInvalidInputError("auto-tuning and a generate throttle are mutually exclusive")
	}
	gen, err = cfg.generateThrottle.withDefaults(
		Throttle{Interval: DefaultGenerateInterval, Concurrency: DefaultGenerateConcurrency}, "generate")
	if err != nil {
		return gen, get, err
	}
	get, err = cfg.getThrottle.withDefaults(
		Throttle{Interval: DefaultGetInterval, Concurrency: DefaultGetConcurrency}, "get")
	return gen, get, err
}

// normalizeBaseURL validates raw and returns it without trailing slashes.
func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", newInvalidInputError("base URL is not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", newInvalidInputError("base URL scheme must be http or https")
	}
	if u.User != nil {
		return "", newInvalidInputError("base URL must not contain credentials")
	}
	if u.Host == "" {
		return "", newInvalidInputError("base URL must include a host")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", newInvalidInputError("base URL must not contain a query or fragment")
	}
	return strings.TrimRight(raw, "/"), nil
}

// logConfig emits the construction warnings and the debug key id. It never
// logs the key or its secret part.
func logConfig(l *slog.Logger, baseURL, userAgent, apiKey string) {
	if userAgent == DefaultUserAgent {
		l.Warn("Using the default User-Agent, please set a custom one (e.g. AppName/Version)")
	}
	if apiKey == "" {
		l.Warn("Creating MineSkin client without API key, get one at https://account.mineskin.org/keys")
		return
	}
	if sendsKeyOverPlainHTTP(baseURL) {
		l.Warn("Sending the API key over plain HTTP to a non-loopback host; use https")
	}
	if strings.HasPrefix(apiKey, apiKeyPrefix) {
		if parts := strings.SplitN(apiKey, "_", 3); len(parts) == 3 {
			l.Debug("Using MineSkin API key", "key_id", parts[1])
		}
	}
}

// validateHeaderValues rejects a User-Agent or API key with control
// characters, which could inject headers. The messages never echo the values.
func validateHeaderValues(userAgent, apiKey string) error {
	if transport.HasControlChar(userAgent) {
		return newInvalidInputError("User-Agent must not contain control characters")
	}
	if transport.HasControlChar(apiKey) {
		return newInvalidInputError("API key must not contain control characters")
	}
	return nil
}

// sendsKeyOverPlainHTTP reports whether baseURL uses http with a host that is
// neither "localhost" nor a loopback IP address.
func sendsKeyOverPlainHTTP(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// checkRedirect applies the transport's redirect policy and marks its refusals
// as client errors.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if err := transport.CheckRedirect(req, via); err != nil {
		return wrapClientError("redirect", err)
	}
	return nil
}
