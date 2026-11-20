package ratelimit

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// maxRelativeMillis is the largest millisecond count a time.Duration holds.
const maxRelativeMillis = math.MaxInt64 / int64(time.Millisecond)

// ServerDelay returns how long the server asks the group to wait: the later
// of the body's rateLimit.next.relative (any JSON object body) and the
// Retry-After header (retryable statuses only). Missing or invalid values
// give 0; it never fails, so the original result or error is never replaced.
func ServerDelay(r transport.Response) time.Duration {
	delay := bodyRelative(r.Body)
	if transport.IsRetryableStatus(r.Status) {
		if ra, ok := transport.ParseRetryAfter(r.Header.Get(transport.HeaderRetryAfter), r.Received); ok {
			delay = max(delay, ra)
		}
	}
	return delay
}

// bodyRelative leniently reads rateLimit.next.relative (milliseconds) from a
// JSON object body. Anything absent, malformed, negative or too large gives 0.
func bodyRelative(body []byte) time.Duration {
	body = transport.TrimBOM(body)
	if !transport.IsJSONObject(body) {
		return 0
	}
	var env struct {
		RateLimit struct {
			Next struct {
				Relative json.RawMessage `json:"relative"`
			} `json:"next"`
		} `json:"rateLimit"`
	}
	if json.Unmarshal(body, &env) != nil {
		return 0
	}
	return lenientMillis(env.RateLimit.Next.Relative)
}

// lenientMillis converts a JSON integer count of milliseconds into a Duration.
func lenientMillis(v json.RawMessage) time.Duration {
	if len(v) == 0 || bytes.Equal(v, []byte("null")) || v[0] == '"' {
		return 0
	}
	var num json.Number
	if json.Unmarshal(v, &num) != nil {
		return 0
	}
	n, err := strconv.ParseInt(num.String(), 10, 64)
	if err != nil || n < 0 || n > maxRelativeMillis {
		return 0
	}
	return time.Duration(n) * time.Millisecond
}
