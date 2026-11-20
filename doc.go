// Package mineskin is a client for the MineSkin V2 API
// (https://api.mineskin.org). It depends only on the Go standard library.
//
// # Usage
//
// Create a [Client] with [NewClient] and functional [Option] values such as
// [WithAPIKey], [WithUserAgent], [WithTimeout], [WithTransport] and
// [WithLogger]; the Option type lists all of them. Then call [Client.GetSkin]
// to fetch a skin by its MineSkin skin id (a UUID, see [ParseUUID]), or
// [Client.GetUser] to read the user and the grants of the API key. Close the
// client when done (see "Shutting down"):
//
//	c, err := mineskin.NewClient(mineskin.WithAPIKey(key), mineskin.WithUserAgent("my-app/1.0"))
//	if err != nil {
//		// handle error
//	}
//	defer c.Close()
//	id, err := mineskin.ParseUUID("c1a1982831874868a37f5be375d38d5b")
//	if err != nil {
//		// handle error
//	}
//	res, err := c.GetSkin(ctx, id)
//
// Every response has a Meta field (a [ResponseMeta]) with the HTTP status, API
// warnings and messages, the breadcrumb and the mineskin-* headers.
//
// # Generating skins
//
// A skin is generated from a [Source]: [FromURL], [FromUser], [FromReader],
// [FromFile] or [FromImage]. [GenerateOptions] (the zero value for none) sets
// the name, variant, visibility and cape.
//
// The queue flow submits a job, waits for it and then reads the skin:
//
//	q, err := c.Queue(ctx, mineskin.FromURL("https://example.com/skin.png"), mineskin.GenerateOptions{})
//	if err != nil {
//		// handle error
//	}
//	done, err := c.WaitForJob(ctx, q.Job)
//	if err != nil {
//		// handle error, e.g. *JobFailedError or ErrMaxAttempts
//	}
//	res, err := c.GetSkin(ctx, done.Job.Result) // or done.Skin when it is not nil
//
// [Client.Queue] returns as soon as the job is accepted. [Client.WaitForJob]
// polls the job until it is completed or failed; concurrent waits share one
// batched poller that is configured with [WithJobPolling] (maximum attempts,
// intervals such as [ConstantInterval] or [Backoff], and whether to follow the
// ETA of the job). The skin of a finished job is done.Skin when the response
// embeds it, otherwise [Client.GetSkin] with done.Job.Result; a failed job is a
// [*JobFailedError]. [Client.GetJob] and [Client.ListJobs] inspect jobs without
// waiting.
//
// [Client.Generate] does the same in a single request and returns the skin
// directly. For long jobs prefer Queue and WaitForJob; see "Timeouts" for the
// limits that apply.
//
// # Errors
//
// Error text starts with "mineskin:", once, at the outermost layer. Invalid
// arguments or options match [ErrInvalidInput]. A 404 response matches
// [ErrNotFound] and a 429 response matches [ErrRateLimited], whatever the
// body. When the API reports a failure the error is an [*APIError] (use
// errors.As); a body that is not the expected JSON, such as a Cloudflare
// error page, yields an [*UnexpectedResponseError]. A failed job is a
// [*JobFailedError], and a wait that used up its attempts matches
// [ErrMaxAttempts]. Context cancellation, deadlines and network errors are
// preserved and can be matched with errors.Is. [IsRetryable] tells whether
// trying the call again may succeed. Calls on a closed client match
// [ErrClientClosed].
//
// # Rate limiting
//
// Queue and Generate responses and an [*APIError] report the rate limit state
// as [RateLimit] and [Usage], and [Client.GetUser] reports the [Grants] of the
// key. The RateLimit.Minute and RateLimit.Hour windows (see [WindowLimit]) are
// exposed for information only: the client never enforces a quota on its own,
// and a quota that is used up comes back as a 429.
//
// Requests are throttled on the client side and the throttle is on by
// default. Requests belong to one of two groups. The generate group holds
// Queue and Generate and starts at most one request per 200 ms with one in
// flight. The get group holds GetSkin, GetUser, GetJob and ListJobs, including
// the calls made while WaitForJob polls, and starts at most one request per
// 100 ms with five in flight. Queued requests start in FIFO order, and a start
// needs a free slot, the interval since the previous start and the group's
// server delay to have passed.
//
// [WithGenerateThrottle] and [WithGetThrottle] set a [Throttle] for one group;
// a zero field keeps the default and a negative field is [ErrInvalidInput].
// Throttling cannot be turned off.
//
// After every response the group also honours the delay the server asks for:
// rateLimit.next.relative from a JSON body, and the Retry-After header
// (delta-seconds or an HTTP-date, RFC 9110) of a retryable response (408, 429
// and 5xx except 501 and 505). The delay moves only the deadline of the group
// that received the response and is capped at one hour (a larger or
// overflowing positive value counts as one hour); [APIError].RetryAfter keeps
// the raw parsed value. A missing, negative or garbled value changes nothing.
//
// [WithAutoTuning] makes the generate group follow the limits granted to the
// API key instead of fixed values (the last of it and [WithGenerateThrottle]
// wins). The grants are loaded lazily by the first Queue or Generate call,
// never by NewClient, and are refreshed in the background when they are older
// than five minutes. Until the first load the group runs at one request per
// second with one in flight. A job that ends FAILED, or a retryable non-JSON
// response to a generate call, temporarily lengthens the interval, and the
// penalty decays over time.
//
// A request that the server rejects with HTTP 429 is retried automatically, at
// most 2 times; [WithRateLimitRetries] changes that and 0 turns it off. This
// applies to every request, including Generate and the poller's checks: a 429
// is never processed, so a retry cannot create a duplicate skin or cost an
// extra credit. MineSkin starts the generate cooldown when a job finishes and
// announces it nowhere in advance, so a Generate right after WaitForJob can be
// answered with "request too soon". The retry goes through the group's
// throttle again and waits for the delay the 429 carried (or the normal
// interval when it had none). If that delay ends after the context's deadline,
// the original 429 error is returned at once. If the context ends while a
// retry is waiting, the error matches both the context error (errors.Is with
// context.Canceled or context.DeadlineExceeded) and the 429 (errors.As with
// [*APIError] or [*UnexpectedResponseError]). Only 429 is retried; for other
// failures the errors carry [APIError].RetryAfter, [APIError].RateLimit and
// [IsRetryable] so that the caller can decide.
//
// # Timeouts
//
// The context of a call bounds the whole operation: the time spent in the
// throttle, every retry after a 429 and, for [Client.WaitForJob], the whole
// wait together with the poll max attempts. Pass a context with a deadline to
// limit it; passing a nil context panics, as in the standard library.
//
// [WithTimeout] (default 10 seconds) bounds each HTTP request attempt on its
// own, including reading the response body. A retry after a 429 gets a fresh
// budget, and time in the throttle does not count. A request that exceeds it
// fails with an error that matches context.DeadlineExceeded and is retryable
// by [IsRetryable]; a deadline of the caller's own context is not. Raise it
// for slow Generate calls.
//
// # Shutting down
//
// Callers should Close every Client they create, usually with defer. Unless
// [WithTransport] is given, each Client owns its own connection pool (a clone
// of http.DefaultTransport), and only [Client.Close] releases its idle
// connections and stops its background goroutines; dropping a Client without
// Close leaks them.
//
// Close is safe to call more than once and always returns nil; a concurrent or
// second call blocks until the first has finished. Calls that are queued or
// waiting, and every call made after Close, fail with [ErrClientClosed];
// requests in flight are cancelled and Close returns after the background
// goroutines have exited. A transport passed with WithTransport is left open
// and remains the caller's to close.
//
// # Security, concurrency and logging
//
// A Client is safe for concurrent use by multiple goroutines. The API key is
// sent only in the Authorization header. It is held in a redacting type, so it
// shows as [REDACTED] when a configuration is printed or logged, and it is
// never included in errors or log output. A key together with an http:// base
// URL to a remote host is refused unless [WithInsecureHTTP] is given.
//
// The Client is silent by default; [WithLogger] enables log/slog output,
// including Debug messages about auto-tuning and rate-limit retries and a Warn
// when a response carries rateLimit, usage or Retry-After data that is present
// but malformed and therefore ignored (the message lists the field paths,
// never the values).
package mineskin
