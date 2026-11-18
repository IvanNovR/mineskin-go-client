package mineskin

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
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
// Go's HTTP client already strips the Authorization header when a redirect
// leads to a different host, and this client refuses redirects from https to
// http and stops after 10 redirects.
type Client struct {
	baseURL    string
	userAgent  string
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger
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

	baseURL, err := normalizeBaseURL(cfg.baseURL)
	if err != nil {
		return nil, err
	}
	if cfg.timeout <= 0 {
		return nil, newInvalidInputError("timeout must be greater than zero")
	}
	if cfg.transport == nil {
		cfg.transport = http.DefaultTransport
	}
	if cfg.logger == nil {
		cfg.logger = slog.New(slog.DiscardHandler)
	}
	userAgent := strings.TrimSpace(cfg.userAgent)
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	apiKey := strings.TrimSpace(cfg.apiKey)

	logConfig(cfg.logger, userAgent, apiKey)

	return &Client{
		baseURL:   baseURL,
		userAgent: userAgent,
		apiKey:    apiKey,
		httpClient: &http.Client{
			Transport:     cfg.transport,
			Timeout:       cfg.timeout,
			CheckRedirect: checkRedirect,
		},
		logger: cfg.logger,
	}, nil
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
func logConfig(l *slog.Logger, userAgent, apiKey string) {
	if userAgent == DefaultUserAgent {
		l.Warn("Using the default User-Agent, please set a custom one (e.g. AppName/Version)")
	}
	if apiKey == "" {
		l.Warn("Creating MineSkin client without API key, get one at https://account.mineskin.org/keys")
		return
	}
	if strings.HasPrefix(apiKey, apiKeyPrefix) {
		if parts := strings.SplitN(apiKey, "_", 3); len(parts) == 3 {
			l.Debug("Using MineSkin API key", "key_id", parts[1])
		}
	}
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
