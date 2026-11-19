package ratelimit

import (
	"net/http"
	"testing"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

func TestServerDelay(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	rel := func(v string) string { return `{"success":true,"rateLimit":{"next":{"relative":` + v + `}}}` }
	tests := []struct {
		name       string
		status     int
		retryAfter string
		body       string
		want       time.Duration
	}{
		{"relative from body", 200, "", rel("5000"), 5 * time.Second},
		{"BOM body", 200, "", "\xef\xbb\xbf" + rel("7000"), 7 * time.Second},
		{"zero relative", 200, "", rel("0"), 0},
		{"negative relative", 200, "", rel("-5000"), 0},
		{"string relative", 200, "", rel(`"5000"`), 0},
		{"fraction relative", 200, "", rel("1.5"), 0},
		{"null relative", 200, "", rel("null"), 0},
		{"too large relative", 200, "", rel("9223372036854775807"), 0},
		{"no rateLimit", 200, "", `{"success":true}`, 0},
		{"next null", 200, "", `{"rateLimit":{"next":null}}`, 0},
		{"rateLimit is a string", 200, "", `{"rateLimit":"x"}`, 0},
		{"truncated JSON", 200, "", `{"rateLimit":`, 0},
		{"not an object", 200, "", `[]`, 0},
		{"html", 200, "", "<html>", 0},
		{"empty body", 200, "", "", 0},
		{"html 503 with Retry-After", 503, "4", "<html>down</html>", 4 * time.Second},
		{"429 takes the later of body and header", 429, "2", rel("3000"), 3 * time.Second},
		{"503 takes the later Retry-After", 503, "8", rel("3000"), 8 * time.Second},
		{"409 ignores Retry-After", 409, "9", `{"success":false}`, 0},
		{"200 ignores Retry-After", 200, "5", `{"success":true}`, 0},
		{"garbage Retry-After", 503, "soon", "<html>down</html>", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.retryAfter != "" {
				h.Set(transport.HeaderRetryAfter, tt.retryAfter)
			}
			r := transport.Response{Status: tt.status, Header: h, Body: []byte(tt.body), Received: at}
			if got := ServerDelay(r); got != tt.want {
				t.Errorf("ServerDelay() = %v, want %v", got, tt.want)
			}
		})
	}
}
