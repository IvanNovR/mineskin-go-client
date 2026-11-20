package mineskin

import (
	"encoding/json"
	"math"
	"time"
)

const (
	pathRateLimit = "rateLimit"
	pathUsage     = "usage"

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
	// Reset is when the window resets. The API sends it as a UNIX epoch
	// timestamp in seconds (the MineSkin V2 OpenAPI LimitInfo.reset), unlike
	// next.absolute, which is in milliseconds.
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

// dropped collects the paths of the fields a lenient decoder ignored because
// they were present but malformed. It never holds a value.
type dropped struct{ paths []string }

func (d *dropped) add(path string) { d.paths = append(d.paths, path) }

// isPresent reports whether v is a JSON value other than absent or null.
func isPresent(v json.RawMessage) bool { return len(v) > 0 && string(v) != jsonNull }

// object decodes v into dst, a struct of raw fields, and reports whether v was
// a JSON object. A present value that is not an object is recorded as dropped
// under path; an absent or null value is not.
func (d *dropped) object(path string, v json.RawMessage, dst any) bool {
	if !isPresent(v) {
		return false
	}
	if json.Unmarshal(v, dst) != nil {
		d.add(path)
		return false
	}
	return true
}

// int is lenientInt that records a present value it had to drop.
func (d *dropped) int(path string, v json.RawMessage) int {
	n, ok := lenientIntOK(v)
	if !ok && isPresent(v) {
		d.add(path)
	}
	return n
}

// duration is lenientDuration that records a present value it had to drop.
func (d *dropped) duration(path string, v json.RawMessage, limit int64, unit time.Duration) time.Duration {
	n, ok, err := optionalInt64(v)
	if err != nil || !ok || n < 0 || n > limit {
		if isPresent(v) {
			d.add(path)
		}
		return 0
	}
	return time.Duration(n) * unit
}

// time is lenientTime that records a present value it had to drop.
func (d *dropped) time(path string, v json.RawMessage) time.Time {
	n, ok, err := optionalInt64(v)
	if err != nil || !ok {
		if isPresent(v) {
			d.add(path)
		}
		return time.Time{}
	}
	return time.UnixMilli(n)
}

// timeSeconds is like time, for a value in epoch seconds.
func (d *dropped) timeSeconds(path string, v json.RawMessage) time.Time {
	n, ok, err := optionalInt64(v)
	if err != nil || !ok {
		if isPresent(v) {
			d.add(path)
		}
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// window decodes a window object; anything else gives the zero value.
func (d *dropped) window(path string, v json.RawMessage) WindowLimit {
	var w windowWire
	if !d.object(path, v, &w) {
		return WindowLimit{}
	}
	return WindowLimit{
		Limit: d.int(path+".limit", w.Limit), Remaining: d.int(path+".remaining", w.Remaining),
		Used: d.int(path+".used", w.Used), Reset: d.timeSeconds(path+".reset", w.Reset),
	}
}

// parseRateLimit decodes a rateLimit value leniently. It never fails; it
// returns the paths of the present but malformed parts it dropped.
func parseRateLimit(b []byte) (RateLimit, []string) {
	var d dropped
	var r RateLimit
	var wire rateLimitWire
	if json.Unmarshal(b, &wire) != nil {
		d.add(pathRateLimit)
		return r, d.paths
	}
	var next nextWire
	if d.object(pathRateLimit+".next", wire.Next, &next) {
		r.NextAt = d.time(pathRateLimit+".next.absolute", next.Absolute)
		r.NextIn = d.duration(pathRateLimit+".next.relative", next.Relative, maxDurationMillis, time.Millisecond)
	}
	var delay delayWire
	if d.object(pathRateLimit+".delay", wire.Delay, &delay) {
		r.Delay = d.duration(pathRateLimit+".delay.millis", delay.Millis, maxDurationMillis, time.Millisecond)
		if r.Delay == 0 {
			r.Delay = d.duration(pathRateLimit+".delay.seconds", delay.Seconds, maxDurationSeconds, time.Second)
		}
	}
	var limit limitWire
	if d.object(pathRateLimit+".limit", wire.Limit, &limit) {
		r.Limit = d.int(pathRateLimit+".limit.limit", limit.Limit)
		r.Remaining = d.int(pathRateLimit+".limit.remaining", limit.Remaining)
		r.Minute = d.window(pathRateLimit+".limit.minute", limit.Minute)
		r.Hour = d.window(pathRateLimit+".limit.hour", limit.Hour)
	}
	return r, d.paths
}

// UnmarshalJSON decodes the rateLimit field. It never fails: a value that is
// not an object, and any malformed part, is left zero (JB-13).
func (r *RateLimit) UnmarshalJSON(b []byte) error {
	*r, _ = parseRateLimit(b)
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

// parseUsage decodes a usage value with the same leniency as parseRateLimit.
func parseUsage(b []byte) (Usage, []string) {
	var d dropped
	var u Usage
	var wire usageWire
	if json.Unmarshal(b, &wire) != nil {
		d.add(pathUsage)
		return u, d.paths
	}
	var credits creditsWire
	if d.object(pathUsage+".credits", wire.Credits, &credits) {
		u.CreditsUsed = d.int(pathUsage+".credits.used", credits.Used)
		u.CreditsRemaining = d.int(pathUsage+".credits.remaining", credits.Remaining)
	}
	return u, d.paths
}

// UnmarshalJSON decodes the usage field with the same leniency as
// RateLimit.UnmarshalJSON: it never fails.
func (u *Usage) UnmarshalJSON(b []byte) error {
	*u, _ = parseUsage(b)
	return nil
}

// trackedRateLimit is a RateLimit that also remembers which parts of the
// response were dropped as malformed. The wire structs use it so that
// decodeResponse can log them; RateLimit itself stays comparable.
type trackedRateLimit struct {
	RateLimit
	dropped []string
}

// UnmarshalJSON implements json.Unmarshaler.
func (t *trackedRateLimit) UnmarshalJSON(b []byte) error {
	t.RateLimit, t.dropped = parseRateLimit(b)
	return nil
}

// trackedUsage is the Usage counterpart of trackedRateLimit.
type trackedUsage struct {
	Usage
	dropped []string
}

// UnmarshalJSON implements json.Unmarshaler.
func (t *trackedUsage) UnmarshalJSON(b []byte) error {
	t.Usage, t.dropped = parseUsage(b)
	return nil
}

// lenientIntOK parses a JSON integer. ok is false for anything else (absent,
// null, string, fraction, out of range), and the result is then 0.
func lenientIntOK(v json.RawMessage) (n int, ok bool) {
	n64, ok, err := optionalInt64(v)
	if err != nil || !ok || n64 < math.MinInt || n64 > math.MaxInt {
		return 0, false
	}
	return int(n64), true
}

// lenientInt is lenientIntOK that returns only the value.
func lenientInt(v json.RawMessage) int {
	n, _ := lenientIntOK(v)
	return n
}
