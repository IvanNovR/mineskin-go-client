// Package mineskin is a client for the MineSkin V2 API (https://api.mineskin.org).
//
// It depends only on the Go standard library. This release covers skin and
// user lookups, skin generation and waiting for queued jobs.
//
// # Usage
//
// Create a Client with NewClient and functional options (WithAPIKey,
// WithBaseURL, WithUserAgent, WithTimeout, WithTransport, WithLogger,
// WithJobPolling, WithGenerateThrottle, WithGetThrottle, WithAutoTuning), then
// call GetSkin or GetSkinByUUID to fetch a skin by its MineSkin skin id, or
// GetUser to read the user and grants of the API key:
//
//	c, err := mineskin.NewClient(mineskin.WithAPIKey(key), mineskin.WithUserAgent("my-app/1.0"))
//	if err != nil {
//		// handle error
//	}
//	res, err := c.GetSkin(ctx, "c1a1982831874868a37f5be375d38d5b")
//
// Every response embeds ResponseMeta with the HTTP status, API messages and
// the mineskin-* headers.
//
// # Generating skins
//
// A skin is generated from a Source: FromURL, FromUser, FromReader, FromFile
// or FromImage. GenerateOptions (nil for none) sets the name, variant,
// visibility and cape.
//
// The queue flow submits a job, waits for it and then reads the skin:
//
//	q, err := c.Queue(ctx, mineskin.FromURL("https://example.com/skin.png"), nil)
//	if err != nil {
//		// handle error
//	}
//	done, err := c.WaitForJob(ctx, q.Job)
//	if err != nil {
//		// handle error, e.g. *JobFailedError or ErrMaxAttempts
//	}
//	skin, err := c.JobSkin(ctx, done)
//
// Queue returns as soon as the job is accepted. WaitForJob polls the job
// until it is completed or failed; concurrent waits share one batched poller
// that is configured with WithJobPolling (maximum attempts, intervals and
// whether to follow the ETA of the job). JobSkin returns the skin of the
// finished job, or a *JobFailedError. GetJob and ListJobs inspect jobs
// without waiting.
//
// Generate does the same in a single request and returns the skin directly.
// The request is bounded by the client timeout, so raise it with WithTimeout
// (or pass a context with a deadline) for slow generations; for long jobs
// prefer Queue and WaitForJob.
//
// # Errors
//
// Every error returned by the package matches ErrClient with errors.Is.
// Invalid arguments or options also match ErrInvalidInput. A 404 response
// matches ErrNotFound. When the API reports a failure the error is an
// *APIError (use errors.As); a body that is not the expected JSON, such as a
// Cloudflare error page, yields an *UnexpectedResponseError. Context
// cancellation, deadlines and network errors are preserved and can be matched
// with errors.Is.
//
// # Rate limiting
//
// Requests are throttled on the client side and the throttle is on by
// default. Requests belong to one of two groups. The generate group holds
// Queue and Generate and starts at most one request per DefaultGenerateInterval
// (200 ms) with DefaultGenerateConcurrency (1) in flight. The get group holds
// GetSkin, GetSkinByUUID, GetUser, GetJob and ListJobs, including the calls
// made while WaitForJob polls, and uses DefaultGetInterval (100 ms) and
// DefaultGetConcurrency (5). Queued requests start in FIFO order, and a start
// needs a free slot, the interval since the previous start and the group's
// server delay to have passed.
//
// WithGenerateThrottle and WithGetThrottle set a Throttle for one group; a
// zero field keeps the default and a negative field is ErrInvalidInput.
// Throttling cannot be turned off.
//
// After every response the group also honours the delay the server asks for:
// rateLimit.next.relative from a JSON body, and the Retry-After header
// (delta-seconds or an HTTP-date, RFC 9110) of a retryable response (408, 429
// and 5xx except 501 and 505). The delay moves only the deadline of the group
// that received the response and is not capped. A missing or invalid value
// changes nothing.
//
// WithAutoTuning makes the generate group follow the limits granted to the
// API key instead of fixed values (it cannot be combined with
// WithGenerateThrottle). The grants are loaded lazily by the first Queue or
// Generate call, never by NewClient, and are refreshed in the background when
// they are older than five minutes. Until the first load the group runs at one
// request per second with one in flight. A job that ends FAILED, or a
// retryable non-JSON response to a generate call, temporarily lengthens the
// interval, and the penalty decays over time.
//
// The client never retries a request by itself; the errors carry
// APIError.RetryAfter, APIError.RateLimit and APIError.IsRetryable so that the
// caller can decide. Time spent waiting in the throttle is bounded only by the
// context of the call: WithTimeout covers the HTTP exchange alone.
//
// # Shutting down
//
// Close releases the resources of a Client. It is safe to call more than
// once and returns nil. Calls that are queued or waiting, and every call made
// after Close, fail with ErrClientClosed; requests in flight are cancelled and
// Close returns after the background goroutines have exited. A transport
// passed with WithTransport is left open.
//
// # Concurrency and logging
//
// A Client is safe for concurrent use by multiple goroutines. It is silent by
// default; WithLogger enables log/slog output, including Debug messages about
// auto-tuning. The API key is sent only in the Authorization header and is
// never logged or included in errors.
package mineskin
