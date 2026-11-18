// Package mineskin is a client for the MineSkin V2 API (https://api.mineskin.org).
//
// It depends only on the Go standard library. This release covers skin and
// user lookups; skin generation is not available yet.
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
