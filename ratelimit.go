package mineskin

import (
	"encoding/json"
	"time"
)

const (
	fieldNext      = "next"
	fieldAbsolute  = "absolute"
	fieldRelative  = "relative"
	fieldDelay     = "delay"
	fieldMillis    = "millis"
	fieldSeconds   = "seconds"
	fieldLimit     = "limit"
	fieldRemaining = "remaining"
	fieldCredits   = "credits"
	fieldUsed      = "used"

	millisPerSecond = int64(time.Second / time.Millisecond)
	// maxDurationSeconds is the largest second count a time.Duration holds.
	maxDurationSeconds = maxDurationMillis / millisPerSecond
)

// RateLimitInfo is the informational rate limit state sent with a response.
// It is decoded leniently: missing or malformed parts are left zero or nil.
type RateLimitInfo struct {
	// Next describes when the next request may be sent, or nil when absent.
	Next *NextRequest
	// Delay is the delay requested by the API, read from delay.millis and
	// falling back to delay.seconds; zero when absent.
	Delay time.Duration
	// Limit is the request limit state, or nil when absent.
	Limit *LimitInfo
}

// NextRequest tells when the next request may be sent.
type NextRequest struct {
	// Absolute is the point in time, zero when absent.
	Absolute time.Time
	// Relative is the time from now, zero when absent.
	Relative time.Duration
}

// LimitInfo is the request limit and the requests remaining.
type LimitInfo struct {
	Limit, Remaining int
}

// UsageInfo is the usage state sent with a response.
type UsageInfo struct {
	// Credits is the credit usage, or nil when absent.
	Credits *CreditsUsage
}

// CreditsUsage is the credit usage of the API key.
type CreditsUsage struct {
	Used, Remaining int
}

// decodeRateLimit decodes a rateLimit value. It never fails: a value that is
// absent, null or not an object yields nil, and malformed fields stay zero.
func decodeRateLimit(v json.RawMessage) *RateLimitInfo {
	obj := jsonObject(v)
	if obj == nil {
		return nil
	}
	out := &RateLimitInfo{}
	if next := jsonObject(obj[fieldNext]); next != nil {
		out.Next = &NextRequest{
			Absolute: lenientTime(next[fieldAbsolute]),
			Relative: lenientDuration(next[fieldRelative], maxDurationMillis, time.Millisecond),
		}
	}
	if delay := jsonObject(obj[fieldDelay]); delay != nil {
		out.Delay = lenientDuration(delay[fieldMillis], maxDurationMillis, time.Millisecond)
		if out.Delay == 0 {
			out.Delay = lenientDuration(delay[fieldSeconds], maxDurationSeconds, time.Second)
		}
	}
	if limit := jsonObject(obj[fieldLimit]); limit != nil {
		out.Limit = &LimitInfo{Limit: intOrZero(limit[fieldLimit]), Remaining: intOrZero(limit[fieldRemaining])}
	}
	return out
}

// decodeUsage decodes a usage value with the same leniency as decodeRateLimit.
func decodeUsage(v json.RawMessage) *UsageInfo {
	obj := jsonObject(v)
	if obj == nil {
		return nil
	}
	out := &UsageInfo{}
	if credits := jsonObject(obj[fieldCredits]); credits != nil {
		out.Credits = &CreditsUsage{Used: intOrZero(credits[fieldUsed]), Remaining: intOrZero(credits[fieldRemaining])}
	}
	return out
}

// jsonObject returns the fields of a JSON object, or nil for anything
// else (absent, null, wrong type).
func jsonObject(v json.RawMessage) map[string]json.RawMessage {
	if len(v) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(v, &obj) != nil {
		return nil
	}
	return obj
}

// intOrZero parses a JSON integer, returning 0 for anything else.
func intOrZero(v json.RawMessage) int {
	if n := integer(v); n != nil {
		return *n
	}
	return 0
}

// lenientDuration converts an integer count of unit into a Duration. Values
// that are malformed, negative or above max yield 0.
func lenientDuration(v json.RawMessage, max int64, unit time.Duration) time.Duration {
	n, ok, err := optionalInt64(v)
	if err != nil || !ok || n < 0 || n > max {
		return 0
	}
	return time.Duration(n) * unit
}

// lenientTime converts epoch milliseconds into a time, or the zero time when
// the value is absent or malformed.
func lenientTime(v json.RawMessage) time.Time {
	n, ok, err := optionalInt64(v)
	if err != nil || !ok {
		return time.Time{}
	}
	return time.UnixMilli(n)
}
