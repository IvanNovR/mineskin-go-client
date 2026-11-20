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

	apiKeyPrefix = "msk_"
)

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
	cfg := defaultConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	logConfig(cfg.logger, cfg.baseURL, cfg.userAgent, cfg.apiKey)

	// A transport the caller did not supply is created here and owned by the
	// client, which closes its idle connections.
	rt, idleCloser := cfg.transport, (func())(nil)
	if rt == nil {
		owned := transport.DefaultTransport()
		rt, idleCloser = owned, owned.CloseIdleConnections
	}
	c := &Client{
		doer: &transport.Doer{
			BaseURL:   cfg.baseURL,
			UserAgent: cfg.userAgent,
			APIKey:    cfg.apiKey,
			HTTP: &http.Client{
				Transport:     rt,
				Timeout:       cfg.timeout,
				CheckRedirect: transport.CheckRedirect,
			},
			Clock:  cfg.clock,
			Logger: cfg.logger,
		},
		logger:     cfg.logger,
		polling:    cfg.polling,
		clock:      cfg.clock,
		idleCloser: idleCloser,
	}
	c.baseCtx, c.cancelBase = context.WithCancelCause(context.Background())
	c.genGroup, c.getGroup = c.newRequestGroups(cfg)
	c.poller = poll.New(poll.Config[*JobResponse]{
		Ctx: c.baseCtx, Clock: c.clock, Fetcher: jobSource{c}, Options: toPollOptions(cfg.polling),
		Logger: cfg.logger, OnJobFailure: c.failureHook(),
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

// normalizeBaseURL validates raw and returns it without trailing slashes.
func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", invalidInput("base URL is not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", invalidInput("base URL scheme must be http or https")
	}
	if u.User != nil {
		return "", invalidInput("base URL must not contain credentials")
	}
	if u.Host == "" {
		return "", invalidInput("base URL must include a host")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", invalidInput("base URL must not contain a query or fragment")
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
		return invalidInput("User-Agent must not contain control characters")
	}
	if transport.HasControlChar(apiKey) {
		return invalidInput("API key must not contain control characters")
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
