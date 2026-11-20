package mineskin

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

const redactKey = "msk_id_SECRETPART"

// assertNoKey fails when out holds the key or any distinctive part of it.
func assertNoKey(t *testing.T, what, out string) {
	t.Helper()
	for _, part := range []string{redactKey, "SECRETPART", "msk_id"} {
		if strings.Contains(out, part) {
			t.Errorf("%s leaks %q: %s", what, part, out)
		}
	}
}

func TestClientAndConfigRedactAPIKeyWhenFormatted(t *testing.T) {
	c, err := NewClient(WithAPIKey("  "+redactKey+"\n"), WithUserAgent("A/1"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	WithAPIKey(redactKey)(cfg)
	assertNoKey(t, "Client", fmt.Sprintf("%v %+v %#v", c, c, c))
	assertNoKey(t, "config pointer", fmt.Sprintf("%v %+v %#v", cfg, cfg, cfg))
	assertNoKey(t, "config value", fmt.Sprintf("%v %+v %#v", *cfg, *cfg, *cfg))
	assertNoKey(t, "Doer pointer", fmt.Sprintf("%v %+v %#v", c.doer, c.doer, c.doer))
	assertNoKey(t, "Doer value", fmt.Sprintf("%v %+v %#v", *c.doer, *c.doer, *c.doer))
	if got := c.doer.APIKey.Reveal(); got != redactKey {
		t.Errorf("Reveal() = %q, want the trimmed key %q (the header still needs it)", got, redactKey)
	}
}

func TestClientRedactsAPIKeyInSlogOutput(t *testing.T) {
	c, err := NewClient(WithAPIKey(redactKey), WithUserAgent("A/1"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	WithAPIKey(redactKey)(cfg)
	for name, newHandler := range map[string]func(*bytes.Buffer) slog.Handler{
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
	} {
		var buf bytes.Buffer
		slog.New(newHandler(&buf)).Info("dump", "client", c, "doer", c.doer, "cfg", cfg, "key", c.doer.APIKey)
		assertNoKey(t, name+" handler", buf.String())
		if !strings.Contains(buf.String(), "[REDACTED]") {
			t.Errorf("%s handler output = %s, want [REDACTED] for the key", name, buf.String())
		}
	}
}
