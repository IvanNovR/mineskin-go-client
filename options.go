package mineskin

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

// config collects the settings applied by Option values before NewClient
// validates them and builds an immutable Client.
type config struct {
	baseURL   string
	userAgent string
	apiKey    string
	timeout   time.Duration
	transport http.RoundTripper
	logger    *slog.Logger
	polling   PollOptions
	// clock is set only by tests; nil means the real clock.
	clock clock.Clock
}

// Option configures a Client. Options are applied in order by NewClient.
type Option func(*config)

// WithAPIKey sets the MineSkin API key. Surrounding whitespace is trimmed. A
// missing or blank key is allowed, but logs a warning and sends no
// Authorization header. The key is never logged.
func WithAPIKey(key string) Option {
	return func(c *config) { c.apiKey = key }
}

// WithUserAgent sets the User-Agent header, for example "AppName/1.2.3". An
// empty value selects DefaultUserAgent, which logs a warning.
func WithUserAgent(userAgent string) Option {
	return func(c *config) { c.userAgent = userAgent }
}

// WithBaseURL sets the API base URL. It must be an absolute http or https URL
// with a host and without credentials, query or fragment; trailing slashes are trimmed.
// A path prefix such as "http://localhost:8080/prefix" is allowed.
func WithBaseURL(baseURL string) Option {
	return func(c *config) { c.baseURL = baseURL }
}

// WithTimeout sets the total timeout of a single HTTP request, including
// reading the response body. It must be greater than zero.
func WithTimeout(d time.Duration) Option {
	return func(c *config) { c.timeout = d }
}

// WithTransport sets the http.RoundTripper used to send requests. A nil value
// selects http.DefaultTransport.
//
// It is also the interception point: wrap another RoundTripper to add headers,
// metrics or tracing.
//
//	type headerAdder struct{ next http.RoundTripper }
//
//	func (h headerAdder) RoundTrip(r *http.Request) (*http.Response, error) {
//		r = r.Clone(r.Context())
//		r.Header.Set("X-Trace", "1")
//		return h.next.RoundTrip(r)
//	}
//
//	client, err := mineskin.NewClient(mineskin.WithTransport(headerAdder{http.DefaultTransport}))
func WithTransport(rt http.RoundTripper) Option {
	return func(c *config) { c.transport = rt }
}

// WithLogger sets the logger for diagnostics. A nil value keeps the default,
// which discards all output. Credentials and request headers are never logged.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

// WithJobPolling sets how the client waits for jobs. Zero fields use the
// defaults (see PollOptions); a negative InitialDelay or MaxAttempts makes
// NewClient return an error matching ErrInvalidInput.
func WithJobPolling(p PollOptions) Option {
	return func(c *config) { c.polling = p }
}
