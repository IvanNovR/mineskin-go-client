package transport

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

func mhdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func TestParseRetryAfter(t *testing.T) {
	at := testNow.Add(30 * time.Second)
	tests := []struct {
		name    string
		value   string
		want    time.Duration
		present bool
	}{
		{"seconds", "120", 2 * time.Minute, true},
		{"whitespace", " 5 ", 5 * time.Second, true},
		{"zero", "0", 0, true},
		{"negative", "-3", 0, true},
		{"IMF-fixdate", at.Format(http.TimeFormat), 30 * time.Second, true},
		{"RFC 850", at.Format(time.RFC850), 30 * time.Second, true},
		{"asctime", at.Format(time.ANSIC), 30 * time.Second, true},
		{"past date", testNow.Add(-time.Hour).Format(http.TimeFormat), 0, true},
		{"empty", "", 0, false},
		{"garbage", "soon", 0, false},
		{"fraction", "1.5", 0, false},
		{"plus sign", "+5", 0, false},
		{"overflow", "99999999999999999999", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseRetryAfter(tc.value, testNow)
			if got != tc.want || ok != tc.present {
				t.Errorf("ParseRetryAfter(%q) = (%v, %v), want (%v, %v)", tc.value, got, ok, tc.want, tc.present)
			}
		})
	}
}

func TestIsRetryableStatus(t *testing.T) {
	for status, want := range map[int]bool{
		408: true, 429: true, 500: true, 502: true, 503: true, 599: true,
		501: false, 505: false, 400: false, 404: false, 409: false, 200: false,
	} {
		if got := IsRetryableStatus(status); got != want {
			t.Errorf("IsRetryableStatus(%d) = %v, want %v", status, got, want)
		}
	}
}

func TestFilterHeaders(t *testing.T) {
	tests := []struct {
		name string
		in   http.Header
		want map[string]string
	}{
		{"nil", nil, map[string]string{}},
		{"empty", http.Header{}, map[string]string{}},
		{"unrelated dropped", mhdr("Content-Type", "x", "Server", "nginx"), map[string]string{}},
		{"lowercased", mhdr("Mineskin-Server", "a"), map[string]string{"mineskin-server": "a"}},
		{"x prefix", mhdr("X-MineSkin-Id", "1"), map[string]string{"x-mineskin-id": "1"}},
		{"duplicates joined (JB-08)", mhdr("Mineskin-Tag", "a", "mineskin-tag", "b", "MINESKIN-TAG", "c"), map[string]string{"mineskin-tag": "a,b,c"}},
		{"non-canonical map keys merged", http.Header{"mineskin-x": {"1"}, "Mineskin-X": {"2"}}, map[string]string{"mineskin-x": "2,1"}},
		{"prefix must be followed by dash", mhdr("Mineskinfoo", "a", "X-Mineskinfoo", "b"), map[string]string{}},
		{"empty value kept", http.Header{"Mineskin-Empty": {""}}, map[string]string{"mineskin-empty": ""}},
		{"no values", http.Header{"Mineskin-None": nil}, map[string]string{"mineskin-none": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FilterHeaders(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTrimBOMAndIsJSONObject(t *testing.T) {
	if got := string(TrimBOM([]byte("\xef\xbb\xbf{}"))); got != "{}" {
		t.Errorf("TrimBOM = %q, want {}", got)
	}
	if got := string(TrimBOM([]byte("{}"))); got != "{}" {
		t.Errorf("TrimBOM without BOM = %q", got)
	}
	for body, want := range map[string]bool{"{}": true, " \r\n\t\f\v{": true, "": false, "  ": false, "[": false, "x{": false} {
		if got := IsJSONObject([]byte(body)); got != want {
			t.Errorf("IsJSONObject(%q) = %v, want %v", body, got, want)
		}
	}
}

func TestDetectOrigin(t *testing.T) {
	tests := []struct {
		name   string
		status int
		server string
		via    bool
		body   string
		want   Origin
	}{
		{"nginx footer", 502, "", false, "<hr><center>nginx/1.28.0</center>", Origin{Source: SourceNginx, ServerSoftware: "nginx/1.28.0"}},
		{"nginx server header", 502, "NGINX/1.2", false, "", Origin{Source: SourceNginx, ServerSoftware: "NGINX/1.2"}},
		{"cloudflare origin status", 521, "", false, "", Origin{Source: SourceCloudflare, CloudflareCode: 521}},
		{"cloudflare wrapped status", 530, "", false, "error code: 1033", Origin{Source: SourceCloudflare, CloudflareCode: 1033}},
		{"cloudflare wrapped without code", 529, "", false, "", Origin{Source: SourceCloudflare}},
		{"via cloudflare with code", 502, "cloudflare", true, "Error code 502", Origin{Source: SourceCloudflare, CloudflareCode: 502}},
		{"via cloudflare without code", 502, "cloudflare", true, "oops", Origin{Source: SourceUnknown}},
		{"code without cloudflare", 502, "", false, "error code: 502", Origin{Source: SourceUnknown}},
		{"short server header", 500, "ng", false, "", Origin{Source: SourceUnknown}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectOrigin(tt.status, tt.server, tt.via, tt.body); got != tt.want {
				t.Errorf("DetectOrigin() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	long := strings.Repeat("a", snippetMaxRunes+5)
	tests := map[string]string{
		"<html><title> 502 \n Bad  </title></html>": "502 Bad",
		"  plain   text\n here ":                    "plain text here",
		"":                                          "",
		"a\x1b[31mred":                              "a[31mred",
		long:                                        strings.Repeat("a", snippetMaxRunes) + snippetEllipsis,
		strings.Repeat("é", snippetMaxRunes):        strings.Repeat("é", snippetMaxRunes),
	}
	for in, want := range tests {
		if got := Summarize(in); got != want {
			t.Errorf("Summarize(%.30q) = %.30q, want %.30q", in, got, want)
		}
	}
}

func TestStripControlAndHasControlChar(t *testing.T) {
	if got := StripControl("a\x1b[0m\r\nb\x00"); got != "a[0mb" {
		t.Errorf("StripControl = %q", got)
	}
	if got := StripControl("héllo 世界 😀"); got != "héllo 世界 😀" {
		t.Errorf("StripControl changed printable text: %q", got)
	}
	for in, want := range map[string]bool{"": false, "plain": false, "tab\there": true, "nul\x00": true, "esc\x1b": true, "世界": false} {
		if got := HasControlChar(in); got != want {
			t.Errorf("HasControlChar(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestOriginSourceValues(t *testing.T) {
	if SourceCloudflare != "cloudflare" || SourceNginx != "nginx" || SourceUnknown != "unknown" {
		t.Error("source values are part of the public ResponseSource contract")
	}
}
