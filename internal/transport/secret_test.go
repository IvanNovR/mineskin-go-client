package transport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

const secretValue = "msk_id_SECRETPART" //nolint:gosec // fake key for redaction tests

func TestSecretNeverFormatsItsValue(t *testing.T) {
	s := NewSecret(secretValue)
	for name, got := range map[string]string{
		"%v":  fmt.Sprintf("%v", s),
		"%+v": fmt.Sprintf("%+v", s),
		"%#v": fmt.Sprintf("%#v", s),
		"%s":  fmt.Sprintf("%s ", s) + fmt.Sprint(s),
		"%q":  fmt.Sprintf("%q", s),
		"%x":  fmt.Sprintf("%x", s),
		"ptr": fmt.Sprintf("%v %+v %#v", &s, &s, &s),
		"str": s.String(),
		"go":  s.GoString(),
	} {
		if strings.Contains(got, "SECRETPART") || strings.Contains(got, "msk_id") {
			t.Errorf("%s leaks the secret: %q", name, got)
		}
	}
	if got := s.String(); got != "[REDACTED]" {
		t.Errorf("String() = %q, want [REDACTED]", got)
	}
	if got := s.GoString(); got != "[REDACTED]" {
		t.Errorf("GoString() = %q, want [REDACTED]", got)
	}
}

func TestSecretReveal(t *testing.T) {
	if got := NewSecret(secretValue).Reveal(); got != secretValue {
		t.Errorf("Reveal() = %q, want %q", got, secretValue)
	}
	var zero Secret
	if zero.Reveal() != "" || !zero.IsEmpty() || !NewSecret("").IsEmpty() || NewSecret("x").IsEmpty() {
		t.Error("zero or empty secret must reveal \"\" and report IsEmpty; a set one must not")
	}
}

func TestSecretRedactsInStructsByReflection(t *testing.T) {
	type holder struct {
		key  Secret // unexported: fmt cannot call methods on it
		Key  Secret
		Name string
	}
	h := holder{key: NewSecret(secretValue), Key: NewSecret(secretValue), Name: "n"}
	for _, got := range []string{
		fmt.Sprintf("%v %+v %#v", h, h, h),
		fmt.Sprintf("%v %+v %#v", &h, &h, &h),
	} {
		if strings.Contains(got, "SECRETPART") || strings.Contains(got, "msk_id") {
			t.Errorf("formatted struct leaks the secret: %q", got)
		}
	}
}

func TestSecretSlogAndJSON(t *testing.T) {
	s := NewSecret(secretValue)
	for name, h := range map[string]func(*bytes.Buffer) slog.Handler{
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
	} {
		var buf bytes.Buffer
		slog.New(h(&buf)).Info("x", "key", s, "ptr", &s, slog.Any("any", s), slog.Group("g", "key", s))
		out := buf.String()
		if strings.Contains(out, "SECRETPART") || strings.Contains(out, "msk_id") {
			t.Errorf("%s output leaks the secret: %s", name, out)
		}
		if !strings.Contains(out, "[REDACTED]") {
			t.Errorf("%s output = %s, want [REDACTED]", name, out)
		}
	}
	if got := s.LogValue(); got.Kind() != slog.KindString || got.String() != "[REDACTED]" {
		t.Errorf("LogValue() = %v, want the string [REDACTED]", got)
	}
	b, err := json.Marshal(map[string]any{"k": s, "p": &s})
	if err != nil || strings.Contains(string(b), "SECRETPART") || string(b) != `{"k":"[REDACTED]","p":"[REDACTED]"}` {
		t.Errorf("json.Marshal = %s, %v", b, err)
	}
}
