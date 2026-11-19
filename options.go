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
	// generateThrottle is the generate group's setting; generateThrottleSet
	// records that WithGenerateThrottle was used, even with a zero value.
	generateThrottle    Throttle
	generateThrottleSet bool
	getThrottle         Throttle
	autoTuning          bool
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
// reading the response body. It must be greater than zero. It covers only
// the HTTP exchange: time spent queued in a request group's throttle is
// bounded only by the caller's context.
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

// WithGenerateThrottle sets the pacing of the generate group, which holds
// Queue and Generate. Zero fields select DefaultGenerateInterval (200 ms) and
// DefaultGenerateConcurrency (1); a negative field makes NewClient return an
// error matching ErrInvalidInput. Queue time is bounded only by the caller's
// context, and throttling cannot be disabled. It cannot be combined with
// WithAutoTuning.
func WithGenerateThrottle(t Throttle) Option {
	return func(c *config) {
		c.generateThrottle = t
		c.generateThrottleSet = true
	}
}

// WithGetThrottle sets the pacing of the get group, which holds GetSkin,
// GetUser, GetJob, ListJobs and the job poller's requests. Zero fields select
// DefaultGetInterval (100 ms) and DefaultGetConcurrency (5); a negative field
// makes NewClient return an error matching ErrInvalidInput. Queue time is
// bounded only by the caller's context, and throttling cannot be disabled.
func WithGetThrottle(t Throttle) Option {
	return func(c *config) { c.getThrottle = t }
}

// WithAutoTuning makes the generate group follow the limits granted to the
// API key. The first Queue or Generate call loads GET /v2/me (without
// blocking longer than its context allows); until that succeeds the group
// runs on a baseline of 1000 ms and 1 request in flight. Grants are refreshed
// in the background when older than 5 minutes. Concurrency is clamped to 1 to
// 30 and the interval to 100 ms to 1000 ms. A job ending FAILED, or a
// retryable unexpected response on the generate group, temporarily widens the
// interval, and the penalty decays linearly.
//
// Auto-tuning and WithGenerateThrottle are mutually exclusive: using both, in
// either order, makes NewClient return an error matching ErrInvalidInput.
func WithAutoTuning() Option {
	return func(c *config) { c.autoTuning = true }
}
