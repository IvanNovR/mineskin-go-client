package mineskin

import (
	"encoding/json"
	"math"
	"time"
)

const (
	millisPerSecond = int64(time.Second / time.Millisecond)
	// maxDurationSeconds is the largest second count a time.Duration holds.
	maxDurationSeconds = maxDurationMillis / millisPerSecond
)

// RateLimit is the informational rate limit state sent with a response. It is
// decoded leniently (see UnmarshalJSON): a missing or malformed part is left
// zero, and a zero value means the API did not send it.
type RateLimit struct {
	// NextAt is the point in time the next request may be sent.
	NextAt time.Time
	// NextIn is the time from now until the next request may be sent.
	NextIn time.Duration
	// Delay is the delay requested by the API, read from delay.millis and
	// falling back to delay.seconds.
	Delay time.Duration
	// Limit and Remaining are the deprecated flat request limit and the
	// requests remaining. Prefer Minute and Hour where the API sends them.
	Limit, Remaining int
	// Minute is the per-minute window.
	Minute WindowLimit
	// Hour is the per-hour window.
	Hour WindowLimit
}

// WindowLimit is the state of one rate limit window. The zero value means the
// API sent no such window; a single zero field means that field was absent.
type WindowLimit struct {
	// Limit is the number of requests the window allows.
	Limit int
	// Remaining is the number of requests left in the window.
	Remaining int
	// Used is the number of requests used in the window.
	Used int
	// Reset is when the window resets. The API sends epoch milliseconds, like
	// next.absolute.
	Reset time.Time
}

// Usage is the usage state sent with a response. A zero field means the API
// did not send a usable value.
type Usage struct {
	// CreditsUsed is the number of credits the API key has used.
	CreditsUsed int
	// CreditsRemaining is the number of credits left.
	CreditsRemaining int
}

// rateLimitWire is the wire shape of the rateLimit field. The parts stay raw
// so that a malformed part is dropped without failing the whole response.
type rateLimitWire struct {
	Next  json.RawMessage `json:"next"`
	Delay json.RawMessage `json:"delay"`
	Limit json.RawMessage `json:"limit"`
}

type nextWire struct {
	Absolute json.RawMessage `json:"absolute"`
	Relative json.RawMessage `json:"relative"`
}

type delayWire struct {
	Millis  json.RawMessage `json:"millis"`
	Seconds json.RawMessage `json:"seconds"`
}

type limitWire struct {
	Limit     json.RawMessage `json:"limit"`
	Remaining json.RawMessage `json:"remaining"`
	Minute    json.RawMessage `json:"minute"`
	Hour      json.RawMessage `json:"hour"`
}

type windowWire struct {
	Limit     json.RawMessage `json:"limit"`
	Remaining json.RawMessage `json:"remaining"`
	Used      json.RawMessage `json:"used"`
	Reset     json.RawMessage `json:"reset"`
}

// lenientWindow decodes a window object; anything else gives the zero value.
func lenientWindow(v json.RawMessage) WindowLimit {
	var w windowWire
	if !unmarshalObject(v, &w) {
		return WindowLimit{}
	}
	return WindowLimit{
		Limit: lenientInt(w.Limit), Remaining: lenientInt(w.Remaining),
		Used: lenientInt(w.Used), Reset: lenientTime(w.Reset),
	}
}

// UnmarshalJSON decodes the rateLimit field. It never fails: a value that is
// not an object, and any malformed part, is left zero (JB-13).
func (r *RateLimit) UnmarshalJSON(b []byte) error {
	*r = RateLimit{}
	var wire rateLimitWire
	if json.Unmarshal(b, &wire) != nil {
		return nil
	}
	var next nextWire
	if unmarshalObject(wire.Next, &next) {
		r.NextAt = lenientTime(next.Absolute)
		r.NextIn = lenientDuration(next.Relative, maxDurationMillis, time.Millisecond)
	}
	var delay delayWire
	if unmarshalObject(wire.Delay, &delay) {
		r.Delay = lenientDuration(delay.Millis, maxDurationMillis, time.Millisecond)
		if r.Delay == 0 {
			r.Delay = lenientDuration(delay.Seconds, maxDurationSeconds, time.Second)
		}
	}
	var limit limitWire
	if unmarshalObject(wire.Limit, &limit) {
		r.Limit, r.Remaining = lenientInt(limit.Limit), lenientInt(limit.Remaining)
		r.Minute, r.Hour = lenientWindow(limit.Minute), lenientWindow(limit.Hour)
	}
	return nil
}

// usageWire is the wire shape of the usage field.
type usageWire struct {
	Credits json.RawMessage `json:"credits"`
}

type creditsWire struct {
	Used      json.RawMessage `json:"used"`
	Remaining json.RawMessage `json:"remaining"`
}

// UnmarshalJSON decodes the usage field with the same leniency as
// RateLimit.UnmarshalJSON: it never fails.
func (u *Usage) UnmarshalJSON(b []byte) error {
	*u = Usage{}
	var wire usageWire
	if json.Unmarshal(b, &wire) != nil {
		return nil
	}
	var credits creditsWire
	if unmarshalObject(wire.Credits, &credits) {
		u.CreditsUsed, u.CreditsRemaining = lenientInt(credits.Used), lenientInt(credits.Remaining)
	}
	return nil
}

// unmarshalObject decodes v into dst, a struct of raw fields, and reports
// whether v was a JSON object. Absent, null and non-object values report false.
func unmarshalObject(v json.RawMessage, dst any) bool {
	if len(v) == 0 || string(v) == jsonNull {
		return false
	}
	return json.Unmarshal(v, dst) == nil
}

// lenientInt parses a JSON integer, returning 0 for anything else (absent,
// null, string, fraction, out of range).
func lenientInt(v json.RawMessage) int {
	n, ok, err := optionalInt64(v)
	if err != nil || !ok || n < math.MinInt || n > math.MaxInt {
		return 0
	}
	return int(n)
}

// lenientDuration converts an integer count of unit into a Duration. Values
// that are malformed, negative or above limit yield 0.
func lenientDuration(v json.RawMessage, limit int64, unit time.Duration) time.Duration {
	n, ok, err := optionalInt64(v)
	if err != nil || !ok || n < 0 || n > limit {
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
