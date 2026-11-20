# Security policy

## Supported versions

Only the latest v0.x release receives fixes.

## Reporting a vulnerability

Please report vulnerabilities through GitHub private vulnerability reporting:
open the repository's **Security** tab and choose **Report a vulnerability**.
Do not open a public issue for a security problem.

## API key handling

- The API key is sent only in the `Authorization` header.
- It is held in a redacting type, so it shows as `[REDACTED]` when a configuration is printed or logged, and it never appears in errors or log output.
- A key together with an `http://` base URL to a remote host is refused unless `WithInsecureHTTP` is given.
- `.env` files are gitignored; never commit a key.
