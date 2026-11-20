package ratelimit

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

// maxRelativeMillis is the largest millisecond count a time.Duration holds.
const maxRelativeMillis = math.MaxInt64 / int64(time.Millisecond)

// maxServerDelay caps the delay a server can impose on a group, so a hostile or
// broken Retry-After or rateLimit.next.relative cannot stall the group for
// days or years. APIError.RetryAfter keeps the raw server value.
const maxServerDelay = time.Hour

// ServerDelay returns how long the server asks the group to wait: the later
// of the body's rateLimit.next.relative (any JSON object body) and the
// Retry-After header (retryable statuses only). Missing, negative or garbled
// values give 0; a positive numeric value above maxServerDelay, including one
// too large for an int64, gives maxServerDelay. It never fails, so the
// original result or error is never replaced.
func ServerDelay(r transport.Response) time.Duration {
	delay := bodyRelative(r.Body)
	if transport.IsRetryableStatus(r.Status) {
		value := r.Header.Get(transport.HeaderRetryAfter)
		if ra, ok := transport.ParseRetryAfter(value, r.Received); ok {
			delay = max(delay, ra)
		} else if isPositiveDigits(value) {
			// Only an overflowing delta-seconds count is digits yet unparsed.
			delay = maxServerDelay
		}
	}
	return min(delay, maxServerDelay)
}

// isPositiveDigits reports whether the trimmed value is a non-empty run of
// ASCII digits.
func isPositiveDigits(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
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
// A positive count too large for a Duration gives maxServerDelay; a negative,
// fractional, non-numeric or absent one gives 0.
func lenientMillis(v json.RawMessage) time.Duration {
	if len(v) == 0 || bytes.Equal(v, []byte("null")) || v[0] == '"' {
		return 0
	}
	var num json.Number
	if json.Unmarshal(v, &num) != nil {
		return 0
	}
	text := num.String()
	n, err := strconv.ParseInt(text, 10, 64)
	if errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(text, "-") {
		return maxServerDelay // a positive integer too large for an int64
	}
	if err != nil || n < 0 {
		return 0
	}
	if n > maxRelativeMillis {
		return maxServerDelay // too large for a Duration
	}
	return time.Duration(n) * time.Millisecond
}
