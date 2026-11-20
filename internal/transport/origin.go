package transport

import (
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Known Origin sources.
const (
	SourceCloudflare = "cloudflare"
	SourceNginx      = "nginx"
	SourceUnknown    = "unknown"
)

const (
	// SnippetInputMaxBytes caps the body prefix examined for the snippet and
	// source detection, bounding regex work on huge bodies.
	SnippetInputMaxBytes = 64 << 10

	snippetMaxRunes = 200
	snippetEllipsis = "..."

	statusCloudflareOriginMin  = 520 // 520-527: the status is the Cloudflare error code
	statusCloudflareOriginMax  = 527
	statusCloudflareWrappedMin = 528 // 528-530: the code comes from the body
	statusCloudflareWrappedMax = 530

	serverPrefixNginx = "nginx"
)

var (
	htmlTitlePattern = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	// cloudflareCodePattern matches "error code: 1033", tolerating markup
	// between the label and the number in HTML pages.
	cloudflareCodePattern = regexp.MustCompile(`(?i)error code:?\s*(?:<[^>]*>\s*)*(\d{3,4})`)
	// nginxFooterPattern matches the footer of an nginx error page, for
	// example <hr><center>nginx/1.28.0</center>.
	nginxFooterPattern = regexp.MustCompile(`(?i)<center>\s*(nginx(?:/[\w.]+)?)\s*</center>`)
	whitespaceRun      = regexp.MustCompile(`\s+`)
)

// Origin is the detected generator of a response.
type Origin struct {
	// Source is SourceCloudflare, SourceNginx or SourceUnknown.
	Source         string
	ServerSoftware string
	CloudflareCode int
}

// DetectOrigin applies the source detection rules in their fixed order. server
// is the Server header, viaCloudflare tells that the response passed through
// Cloudflare and body is the (already capped) body text.
func DetectOrigin(status int, server string, viaCloudflare bool, body string) Origin {
	if m := nginxFooterPattern.FindStringSubmatch(body); m != nil {
		return Origin{Source: SourceNginx, ServerSoftware: m[1]}
	}
	if len(server) >= len(serverPrefixNginx) && strings.EqualFold(server[:len(serverPrefixNginx)], serverPrefixNginx) {
		return Origin{Source: SourceNginx, ServerSoftware: server}
	}
	if status >= statusCloudflareOriginMin && status <= statusCloudflareOriginMax {
		return Origin{Source: SourceCloudflare, CloudflareCode: status}
	}
	code := findCloudflareCode(body)
	if status >= statusCloudflareWrappedMin && status <= statusCloudflareWrappedMax {
		// 530 wraps a 1xxx error, for example 1033.
		return Origin{Source: SourceCloudflare, CloudflareCode: code}
	}
	if viaCloudflare && code != 0 {
		return Origin{Source: SourceCloudflare, CloudflareCode: code}
	}
	return Origin{Source: SourceUnknown}
}

// findCloudflareCode returns the error code in body, or zero.
func findCloudflareCode(body string) int {
	m := cloudflareCodePattern.FindStringSubmatch(body)
	if m == nil {
		return 0
	}
	code, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return code
}

// Summarize returns the HTML title, or the body, collapsed and shortened.
func Summarize(body string) string {
	text := body
	if m := htmlTitlePattern.FindStringSubmatch(body); m != nil {
		text = m[1]
	}
	text = strings.TrimSpace(StripControl(whitespaceRun.ReplaceAllString(text, " ")))
	if runes := []rune(text); len(runes) > snippetMaxRunes {
		return string(runes[:snippetMaxRunes]) + snippetEllipsis
	}
	return text
}

// StripControl removes control characters, such as ANSI escape sequences, so
// that attacker-controlled response text cannot manipulate a terminal or log.
func StripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// ParseRetryAfter parses a Retry-After value per RFC 9110: delta-seconds or an
// HTTP-date in any of the formats http.ParseTime accepts, measured from now. A
// negative value or a date in the past gives (0, true). An empty, malformed or
// overflowing value gives (0, false). Deliberate deviation from the Java
// client, which ignores the date form (JB-18, D13).
func ParseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if digits, negative := strings.CutPrefix(value, "-"); negative {
		if isDigits(digits) {
			return 0, true
		}
		return 0, false
	}
	if isDigits(value) {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil || seconds > int64(math.MaxInt64/time.Second) {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	return max(date.Sub(now), 0), true
}

// isDigits reports whether s is a non-empty run of ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// IsRetryableStatus reports whether an HTTP status is likely temporary: 408,
// 429, or 500 and above except 501 and 505.
func IsRetryableStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusTooManyRequests ||
		(status >= http.StatusInternalServerError &&
			status != http.StatusNotImplemented &&
			status != http.StatusHTTPVersionNotSupported)
}
