# mineskin-go-client

[![Go Reference](https://pkg.go.dev/badge/github.com/IvanNovR/mineskin-go-client.svg)](https://pkg.go.dev/github.com/IvanNovR/mineskin-go-client)
[![CI](https://github.com/IvanNovR/mineskin-go-client/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/IvanNovR/mineskin-go-client/actions/workflows/ci.yml)
[![Go version](https://img.shields.io/github/go-mod/go-version/IvanNovR/mineskin-go-client)](go.mod)
[![License](https://img.shields.io/github/license/IvanNovR/mineskin-go-client)](LICENSE)

A Go client for the [MineSkin](https://mineskin.org) V2 API. It depends only on
the Go standard library.

## Install

```sh
go get github.com/IvanNovR/mineskin-go-client@latest
```

Go 1.26 or newer is required.

## Quick start

<!-- quickstart:start -->
```go
c, err := mineskin.NewClient(
	mineskin.WithAPIKey(os.Getenv("MINESKIN_API_KEY")),
	mineskin.WithUserAgent("my-app/1.0"),
)
if err != nil {
	fmt.Println(err)
	return
}
defer func() { _ = c.Close() }()

res, err := c.Generate(context.Background(),
	mineskin.FromURL("https://example.com/skin.png"), mineskin.GenerateOptions{})
if err != nil {
	fmt.Println(err)
	return
}
fmt.Println(res.Skin.Texture.URL.Skin)
```
<!-- quickstart:end -->

The snippet is compiled and run as `Example_quickStart` in
[example_test.go](example_test.go), so it cannot drift from the code. Always
set a descriptive User-Agent and close the client when you are done.

## Features

- **Configuration**: API key, User-Agent, base URL, timeout, your own HTTP transport and `log/slog` logging, all through functional options.
- **Queue generation**: `Queue` submits a job from a URL, a user UUID, a file, a reader or an in-memory image, with name, variant, visibility and cape options.
- **Jobs**: `GetJob`, `ListJobs` and `WaitForJob`, with configurable polling (intervals, backoff, ETA) and batched checks for many pending jobs.
- **Synchronous generation**: `Generate` submits and waits in one call, from the same sources.
- **Skins**: `GetSkin` returns texture value, signature, URL and hash, and the generator info.
- **Account**: `GetUser` returns the user and the grants of the API key.
- **Rate limiting**: client-side throttling per request group, the server's requested delay and `Retry-After` are honoured, and `WithAutoTuning` follows the grants of the key.
- **Errors**: typed errors (`*APIError`, `*UnexpectedResponseError`, `*JobFailedError`) with sentinels such as `ErrNotFound` and `ErrRateLimited`.
- **Response metadata**: every response carries `Meta` (status, warnings, messages, headers), and generate responses and API errors report `RateLimit` and `Usage`, including the per-minute and per-hour windows.

## Rate limits & errors

Requests are throttled on the client side, and the throttle is on by default,
so you rarely trigger the server limits. A request that the server rejects
with HTTP 429 is retried automatically (at most 2 times, after the delay the
server asked for); `WithRateLimitRetries` changes that and 0 turns it off. A
429 that remains matches `ErrRateLimited`, and `IsRetryable` tells whether
trying again may succeed. The client never enforces a quota on its own. See
the [Rate limiting](https://pkg.go.dev/github.com/IvanNovR/mineskin-go-client#hdr-Rate_limiting)
and [Errors](https://pkg.go.dev/github.com/IvanNovR/mineskin-go-client#hdr-Errors)
sections of the package documentation.

## Documentation

- [API reference on pkg.go.dev](https://pkg.go.dev/github.com/IvanNovR/mineskin-go-client)
- [Contributing](CONTRIBUTING.md), [Security policy](SECURITY.md), [Releasing](RELEASING.md)
- Release notes are on the [GitHub Releases](https://github.com/IvanNovR/mineskin-go-client/releases) page.

## License

[MIT](LICENSE)
