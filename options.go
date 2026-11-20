package mineskin

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// config collects the settings applied by Option values. defaultConfig
// prefills every default, so a config that went through the options is
// complete; NewClient validates it once and builds the Client from it.
type config struct {
	baseURL   string
	userAgent string
	apiKey    transport.Secret
	timeout   time.Duration
	// transport is nil until WithTransport sets it; NewClient then creates and
	// owns a transport.
	transport http.RoundTripper
	logger    *slog.Logger
	polling   PollOptions
	// generate is the single pacing setting of the generate group; the last
	// of WithGenerateThrottle and WithAutoTuning wins.
	generate         generatePacing
	getThrottle      Throttle
	rateLimitRetries int
	// clock is the real clock except in tests.
	clock clock.Clock
}

// generatePacing is how the generate group is paced: by the fixed throttle,
// or, when auto is set, by the limits granted to the API key.
type generatePacing struct {
	throttle Throttle
	auto     bool
}

// Option configures a Client. Options are applied in order by NewClient.
type Option func(*config)

// WithAPIKey sets the MineSkin API key. Surrounding whitespace is trimmed. A
// missing or blank key is allowed, but logs a warning and sends no
// Authorization header. The key is never logged.
func WithAPIKey(key string) Option {
	return func(c *config) { c.apiKey = transport.NewSecret(strings.TrimSpace(key)) }
}

// WithUserAgent sets the User-Agent header, for example "AppName/1.2.3". An
// empty value selects DefaultUserAgent, which logs a warning.
func WithUserAgent(userAgent string) Option {
	return func(c *config) {
		if userAgent = strings.TrimSpace(userAgent); userAgent == "" {
			userAgent = DefaultUserAgent
		}
		c.userAgent = userAgent
	}
}

// WithBaseURL sets the API base URL. It must be an absolute http or https URL
// with a host and without credentials, query or fragment; trailing slashes are trimmed.
// A path prefix such as "http://localhost:8080/prefix" is allowed.
func WithBaseURL(baseURL string) Option {
	return func(c *config) { c.baseURL = baseURL }
}

// WithTimeout sets the timeout of each HTTP request attempt, including reading
// the response body; the default is 10 seconds. It must be greater than zero.
// It is applied per attempt with a context deadline, so every automatic retry
// after a 429 gets its own budget, and time spent queued in a request group's
// throttle does not count. It is not a limit on a whole call: the total
// duration of Generate, WaitForJob or a retried request is bounded by the
// caller's context, and WaitForJob also by the poll max attempts. A request
// that exceeds it fails with an error that matches context.DeadlineExceeded,
// is a net.Error with Timeout() true and is retryable (see IsRetryable); a
// deadline of the caller's own context is not.
func WithTimeout(d time.Duration) Option {
	return func(c *config) { c.timeout = d }
}

// WithTransport sets the http.RoundTripper used to send requests. A nil value
// selects a private clone of http.DefaultTransport, which the client owns:
// Client.Close closes its idle connections. A transport passed here is never
// closed by the client; closing it is the caller's job.
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
	return func(c *config) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithJobPolling sets how the client waits for jobs. Zero fields use the
// defaults (see PollOptions); a negative InitialDelay or MaxAttempts makes
// NewClient return an error matching ErrInvalidInput.
func WithJobPolling(p PollOptions) Option {
	return func(c *config) { c.polling = p.withDefaults() }
}

// WithGenerateThrottle sets the pacing of the generate group, which holds
// Queue and Generate. Zero fields select the defaults, an interval of 200 ms
// and 1 request in flight; a negative field makes NewClient return an error
// matching ErrInvalidInput. Queue time is bounded only by the caller's
// context, and throttling cannot be disabled. WithGenerateThrottle and
// WithAutoTuning set the same pacing, so the one given last wins.
func WithGenerateThrottle(t Throttle) Option {
	return func(c *config) { c.generate = generatePacing{throttle: t.orDefault(defaultGenerateThrottle())} }
}

// WithGetThrottle sets the pacing of the get group, which holds GetSkin,
// GetUser, GetJob, ListJobs and the job poller's requests. Zero fields select
// the defaults, an interval of 100 ms and 5 requests in flight; a negative
// field makes NewClient return an error matching ErrInvalidInput. Queue time
// is bounded only by the caller's context, and throttling cannot be disabled.
func WithGetThrottle(t Throttle) Option {
	return func(c *config) { c.getThrottle = t.orDefault(defaultGetThrottle()) }
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
// WithAutoTuning replaces any WithGenerateThrottle given before it, and a
// WithGenerateThrottle given after it replaces auto-tuning: the last one wins.
func WithAutoTuning() Option {
	return func(c *config) { c.generate = generatePacing{auto: true} }
}

// WithRateLimitRetries sets how many times a request rejected with HTTP 429
// is retried automatically; the default is 2. Zero
// disables retries and a negative value makes NewClient return an error
// matching ErrInvalidInput.
//
// A 429 is never processed by the server, so retrying is safe for every
// request, including Generate: it cannot create a duplicate skin or cost an
// extra credit. A retry goes through the group's throttle again, so it waits
// for the delay the 429 announced (rateLimit.next or Retry-After), or for the
// normal interval when there is none. When that delay would end after the
// context's deadline, the original 429 error is returned at once instead of
// waiting. If the context ends while a retry is waiting, the error matches
// both the context error and the 429: errors.Is finds
// context.Canceled or context.DeadlineExceeded, and errors.As finds the
// *APIError (or *UnexpectedResponseError) with its RateLimit and RetryAfter.
// Only 429 is retried; other failures are returned as they are, and
// IsRetryable(err) lets callers decide about those. Each retry logs one Debug line.
func WithRateLimitRetries(n int) Option {
	return func(c *config) { c.rateLimitRetries = n }
}
