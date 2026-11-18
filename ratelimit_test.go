package mineskin

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDecodeRateLimit(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want *RateLimitInfo
	}{
		{
			name: "full object",
			raw:  `{"next":{"absolute":1700000000123,"relative":2500},"delay":{"millis":1500,"seconds":9},"limit":{"limit":10,"remaining":4}}`,
			want: &RateLimitInfo{
				Next:  &NextRequest{Absolute: time.UnixMilli(1700000000123), Relative: 2500 * time.Millisecond},
				Delay: 1500 * time.Millisecond,
				Limit: &LimitInfo{Limit: 10, Remaining: 4},
			},
		},
		{name: "only delay seconds", raw: `{"delay":{"seconds":3}}`, want: &RateLimitInfo{Delay: 3 * time.Second}},
		{name: "absent", raw: ``, want: nil},
		{name: "null", raw: `null`, want: nil},
		{name: "not an object", raw: `"x"`, want: nil},
		{name: "empty object", raw: `{}`, want: &RateLimitInfo{}},
		{name: "next without relative", raw: `{"next":{"absolute":1000}}`, want: &RateLimitInfo{Next: &NextRequest{Absolute: time.UnixMilli(1000)}}},
		{name: "next without absolute", raw: `{"next":{"relative":50}}`, want: &RateLimitInfo{Next: &NextRequest{Relative: 50 * time.Millisecond}}},
		{name: "empty next", raw: `{"next":{}}`, want: &RateLimitInfo{Next: &NextRequest{}}},
		{name: "wrong typed next", raw: `{"next":"soon"}`, want: &RateLimitInfo{}},
		{name: "null sub objects", raw: `{"next":null,"delay":null,"limit":null}`, want: &RateLimitInfo{}},
		{name: "wrong typed values", raw: `{"next":{"absolute":"1","relative":"2"},"delay":{"millis":"5","seconds":1.5},"limit":{"limit":"x","remaining":true}}`, want: &RateLimitInfo{Next: &NextRequest{}, Limit: &LimitInfo{}}},
		{name: "out of range relative", raw: `{"next":{"relative":9223372036854775807}}`, want: &RateLimitInfo{Next: &NextRequest{}}},
		{name: "overflowing int", raw: `{"limit":{"limit":99999999999999999999,"remaining":3}}`, want: &RateLimitInfo{Limit: &LimitInfo{Remaining: 3}}},
		{name: "out of range delay seconds", raw: `{"delay":{"seconds":9223372036854775807}}`, want: &RateLimitInfo{}},
		{name: "negative delay", raw: `{"delay":{"millis":-5}}`, want: &RateLimitInfo{}},
		{name: "wrong typed delay", raw: `{"delay":[1]}`, want: &RateLimitInfo{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeRateLimit(json.RawMessage(tc.raw))
			assertRateLimit(t, got, tc.want)
		})
	}
}

func assertRateLimit(t *testing.T, got, want *RateLimitInfo) {
	t.Helper()
	if got == nil || want == nil {
		if got != want {
			t.Fatalf("decodeRateLimit() = %+v, want %+v", got, want)
		}
		return
	}
	if got.Delay != want.Delay {
		t.Errorf("Delay = %v, want %v", got.Delay, want.Delay)
	}
	if (got.Next == nil) != (want.Next == nil) {
		t.Fatalf("Next = %+v, want %+v", got.Next, want.Next)
	}
	if want.Next != nil && (!got.Next.Absolute.Equal(want.Next.Absolute) || got.Next.Relative != want.Next.Relative) {
		t.Errorf("Next = %+v, want %+v", *got.Next, *want.Next)
	}
	if (got.Limit == nil) != (want.Limit == nil) {
		t.Fatalf("Limit = %+v, want %+v", got.Limit, want.Limit)
	}
	if want.Limit != nil && *got.Limit != *want.Limit {
		t.Errorf("Limit = %+v, want %+v", *got.Limit, *want.Limit)
	}
}

func TestDecodeUsage(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want *UsageInfo
	}{
		{name: "full object", raw: `{"credits":{"used":7,"remaining":93}}`, want: &UsageInfo{Credits: &CreditsUsage{Used: 7, Remaining: 93}}},
		{name: "absent", raw: ``, want: nil},
		{name: "null", raw: `null`, want: nil},
		{name: "not an object", raw: `[]`, want: nil},
		{name: "empty object", raw: `{}`, want: &UsageInfo{}},
		{name: "null credits", raw: `{"credits":null}`, want: &UsageInfo{}},
		{name: "wrong typed credits", raw: `{"credits":5}`, want: &UsageInfo{}},
		{name: "empty credits", raw: `{"credits":{}}`, want: &UsageInfo{Credits: &CreditsUsage{}}},
		{name: "missing remaining", raw: `{"credits":{"used":2}}`, want: &UsageInfo{Credits: &CreditsUsage{Used: 2}}},
		{name: "wrong typed values", raw: `{"credits":{"used":"1","remaining":1.5}}`, want: &UsageInfo{Credits: &CreditsUsage{}}},
		{name: "overflowing int", raw: `{"credits":{"used":99999999999999999999,"remaining":1}}`, want: &UsageInfo{Credits: &CreditsUsage{Remaining: 1}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeUsage(json.RawMessage(tc.raw))
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("decodeUsage() = %+v, want %+v", got, tc.want)
			}
			if got == nil {
				return
			}
			if (got.Credits == nil) != (tc.want.Credits == nil) {
				t.Fatalf("Credits = %+v, want %+v", got.Credits, tc.want.Credits)
			}
			if got.Credits != nil && *got.Credits != *tc.want.Credits {
				t.Errorf("Credits = %+v, want %+v", *got.Credits, *tc.want.Credits)
			}
		})
	}
}
