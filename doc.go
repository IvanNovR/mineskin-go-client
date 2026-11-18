// Package mineskin is a client for the MineSkin V2 API (https://api.mineskin.org).
//
// It depends only on the Go standard library. This release covers skin and
// user lookups, skin generation and waiting for queued jobs.
//
// # Usage
//
// Create a Client with NewClient and functional options (WithAPIKey,
// WithBaseURL, WithUserAgent, WithTimeout, WithTransport, WithLogger), then
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
// # Concurrency and logging
//
// A Client is safe for concurrent use by multiple goroutines. It is silent by
// default; WithLogger enables log/slog output. The API key is sent only in the
// Authorization header and is never logged or included in errors.
package mineskin
