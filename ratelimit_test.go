package mineskin

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRateLimitUnmarshalJSON(t *testing.T) {
	const bigInt = "9223372036854775807"
	tests := []struct {
		name string
		raw  string
		want RateLimit
	}{
		{
			name: "full object",
			raw: `{"next":{"absolute":1700000000123,"relative":2500},"delay":{"millis":1500,"seconds":9},` +
				`"limit":{"limit":10,"remaining":4,` +
				`"minute":{"limit":10,"remaining":4,"used":6,"reset":1700000060000},` +
				`"hour":{"limit":100,"remaining":90,"used":10,"reset":1700003600000}}}`,
			want: RateLimit{
				NextAt: time.UnixMilli(1700000000123), NextIn: 2500 * time.Millisecond,
				Delay: 1500 * time.Millisecond, Limit: 10, Remaining: 4,
				Minute: WindowLimit{Limit: 10, Remaining: 4, Used: 6, Reset: time.UnixMilli(1700000060000)},
				Hour:   WindowLimit{Limit: 100, Remaining: 90, Used: 10, Reset: time.UnixMilli(1700003600000)},
			},
		},
		{name: "only delay seconds", raw: `{"delay":{"seconds":3}}`, want: RateLimit{Delay: 3 * time.Second}},
		{name: "absent", raw: ``, want: RateLimit{}},
		{name: "null", raw: `null`, want: RateLimit{}},
		{name: "not an object", raw: `"x"`, want: RateLimit{}},
		{name: "empty object", raw: `{}`, want: RateLimit{}},
		{name: "next without relative", raw: `{"next":{"absolute":1000}}`, want: RateLimit{NextAt: time.UnixMilli(1000)}},
		{name: "next without absolute", raw: `{"next":{"relative":50}}`, want: RateLimit{NextIn: 50 * time.Millisecond}},
		{name: "wrong typed next", raw: `{"next":"soon"}`, want: RateLimit{}},
		{name: "null sub objects", raw: `{"next":null,"delay":null,"limit":null}`, want: RateLimit{}},
		{name: "wrong typed values", raw: `{"next":{"absolute":"1","relative":"2"},"delay":{"millis":"5","seconds":1.5},"limit":{"limit":"x","remaining":true}}`, want: RateLimit{}},
		{name: "out of range relative", raw: `{"next":{"relative":` + bigInt + `}}`, want: RateLimit{}},
		{name: "overflowing int", raw: `{"limit":{"limit":99999999999999999999,"remaining":3}}`, want: RateLimit{Remaining: 3}},
		{name: "out of range delay seconds", raw: `{"delay":{"seconds":` + bigInt + `}}`, want: RateLimit{}},
		{name: "negative delay", raw: `{"delay":{"millis":-5}}`, want: RateLimit{}},
		{name: "wrong typed delay", raw: `{"delay":[1]}`, want: RateLimit{}},
		{name: "deprecated flat limit only", raw: `{"limit":{"limit":10,"remaining":4}}`, want: RateLimit{Limit: 10, Remaining: 4}},
		{name: "windows without flat fields", raw: `{"limit":{"hour":{"limit":50,"remaining":7,"used":43}}}`, want: RateLimit{Hour: WindowLimit{Limit: 50, Remaining: 7, Used: 43}}},
		{name: "wrong typed window", raw: `{"limit":{"minute":"x","hour":[1]}}`, want: RateLimit{}},
		{name: "null window", raw: `{"limit":{"minute":null}}`, want: RateLimit{}},
		{name: "wrong typed window values", raw: `{"limit":{"hour":{"limit":"x","remaining":3,"used":1.5,"reset":"soon"}}}`, want: RateLimit{Hour: WindowLimit{Remaining: 3}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rateLimitField(t, tc.raw)
			assertRateLimit(t, got, tc.want)
		})
	}
}

func assertRateLimit(t *testing.T, got, want RateLimit) {
	t.Helper()
	if !got.NextAt.Equal(want.NextAt) || got.NextIn != want.NextIn || got.Delay != want.Delay ||
		got.Limit != want.Limit || got.Remaining != want.Remaining {
		t.Errorf("RateLimit = %+v, want %+v", got, want)
	}
	assertWindow(t, "Minute", got.Minute, want.Minute)
	assertWindow(t, "Hour", got.Hour, want.Hour)
}

func assertWindow(t *testing.T, name string, got, want WindowLimit) {
	t.Helper()
	if got.Limit != want.Limit || got.Remaining != want.Remaining || got.Used != want.Used || !got.Reset.Equal(want.Reset) {
		t.Errorf("%s = %+v, want %+v", name, got, want)
	}
}

func TestUsageUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Usage
	}{
		{name: "full object", raw: `{"credits":{"used":7,"remaining":93}}`, want: Usage{CreditsUsed: 7, CreditsRemaining: 93}},
		{name: "absent", raw: ``, want: Usage{}},
		{name: "null", raw: `null`, want: Usage{}},
		{name: "not an object", raw: `[]`, want: Usage{}},
		{name: "empty object", raw: `{}`, want: Usage{}},
		{name: "null credits", raw: `{"credits":null}`, want: Usage{}},
		{name: "wrong typed credits", raw: `{"credits":5}`, want: Usage{}},
		{name: "missing remaining", raw: `{"credits":{"used":2}}`, want: Usage{CreditsUsed: 2}},
		{name: "wrong typed values", raw: `{"credits":{"used":"1","remaining":1.5}}`, want: Usage{}},
		{name: "overflowing int", raw: `{"credits":{"used":99999999999999999999,"remaining":1}}`, want: Usage{CreditsRemaining: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := usageField(t, tc.raw); got != tc.want {
				t.Errorf("Usage = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// rateLimitField decodes raw as the rateLimit field of a response body, the way
// the wire structs do. Decoding must never fail.
func rateLimitField(t *testing.T, raw string) RateLimit {
	t.Helper()
	var wire struct {
		RateLimit RateLimit `json:"rateLimit"`
	}
	if err := json.Unmarshal([]byte(fieldBody("rateLimit", raw)), &wire); err != nil {
		t.Fatalf("Unmarshal() error = %v, RateLimit decoding must never fail", err)
	}
	return wire.RateLimit
}

// usageField is rateLimitField for the usage field.
func usageField(t *testing.T, raw string) Usage {
	t.Helper()
	var wire struct {
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(fieldBody("usage", raw)), &wire); err != nil {
		t.Fatalf("Unmarshal() error = %v, Usage decoding must never fail", err)
	}
	return wire.Usage
}

// fieldBody builds a JSON object with one field; an empty raw omits it.
func fieldBody(name, raw string) string {
	if raw == "" {
		return `{}`
	}
	return `{"` + name + `":` + raw + `}`
}
