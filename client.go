package mineskin

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

const (
	// DefaultBaseURL is the base URL of the MineSkin API.
	DefaultBaseURL = "https://api.mineskin.org"
	// DefaultUserAgent is the User-Agent used when none is configured. Set a
	// custom one such as "AppName/1.0" instead.
	DefaultUserAgent = "MineSkinClient"
	// DefaultTimeout is the default timeout of a single HTTP request.
	DefaultTimeout = 10 * time.Second

	maxRedirects = 10
	apiKeyPrefix = "msk_"
)

// Client is a MineSkin API client. It is immutable after NewClient returns and
// safe for concurrent use by multiple goroutines.
//
// The only background goroutine is the job poller used by WaitForJob. It is
// started lazily by the first wait and exits by itself once no wait is
// pending; a later wait starts it again. There is no Close method.
//
// Go's HTTP client already strips the Authorization header when a redirect
// leads to a different host, and this client refuses redirects from https to
// http and stops after 10 redirects.
type Client struct {
	baseURL    string
	userAgent  string
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger
	polling    PollOptions
	poller     *jobPoller
	clock      clock.Clock
	genGroup   *requestGroup
	getGroup   *requestGroup
}

// NewClient creates a Client from the given options. Invalid configuration
// yields an error matching ErrInvalidInput and ErrClient; the error never
// contains the API key.
func NewClient(opts ...Option) (*Client, error) {
	cfg := &config{
		baseURL:   DefaultBaseURL,
		userAgent: DefaultUserAgent,
		timeout:   DefaultTimeout,
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
		baseURL:   r.baseURL,
		userAgent: r.userAgent,
		apiKey:    r.apiKey,
		httpClient: &http.Client{
			Transport:     r.transport,
			Timeout:       r.timeout,
			CheckRedirect: checkRedirect,
		},
		logger:  r.logger,
		polling: r.polling,
		clock:   r.clock,
	}
	c.genGroup, c.getGroup = c.newRequestGroups(r)
	c.poller = newJobPoller(c, r.polling, r.logger)
	return c, nil
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

	generateThrottle Throttle
	getThrottle      Throttle
	autoTuning       bool
}

// resolve validates cfg and fills in defaults. Errors match ErrInvalidInput
// and never contain the API key.
func (cfg *config) resolve() (*resolvedConfig, error) {
	baseURL, err := normalizeBaseURL(cfg.baseURL)
	if err != nil {
		return nil, err
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
		generateThrottle: gen, getThrottle: get, autoTuning: cfg.autoTuning,
		baseURL: baseURL, timeout: cfg.timeout, polling: polling,
		transport: cfg.transport, logger: cfg.logger,
		userAgent: strings.TrimSpace(cfg.userAgent),
		apiKey:    strings.TrimSpace(cfg.apiKey),
	}
	if r.transport == nil {
		r.transport = http.DefaultTransport
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
	if hasControlChar(userAgent) {
		return newInvalidInputError("User-Agent must not contain control characters")
	}
	if hasControlChar(apiKey) {
		return newInvalidInputError("API key must not contain control characters")
	}
	return nil
}

// hasControlChar reports whether s contains a control character.
func hasControlChar(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
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

// checkRedirect refuses redirects from https to http and stops after
// maxRedirects redirects.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return wrapClientError("redirect", errors.New("stopped after 10 redirects"))
	}
	if req.URL.Scheme == "http" {
		for _, prev := range via {
			if prev.URL.Scheme == "https" {
				return wrapClientError("redirect", errors.New("refusing redirect from https to http"))
			}
		}
	}
	return nil
}
