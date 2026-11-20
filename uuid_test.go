package mineskin

import (
	"errors"
	"strings"
	"testing"
)

const (
	uuidDashed   = "0123abcd-4567-89ef-0123-456789abcdef"
	uuidUndashed = "0123abcd456789ef0123456789abcdef"
)

func TestParseUUIDValid(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, in string }{
		{"dashed", uuidDashed},
		{"undashed", uuidUndashed},
		{"upper-case dashed", strings.ToUpper(uuidDashed)},
		{"upper-case undashed", strings.ToUpper(uuidUndashed)},
		{"mixed case", "0123ABCD-4567-89ef-0123-456789AbCdEf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u, err := ParseUUID(tt.in)
			if err != nil {
				t.Fatalf("ParseUUID(%q) error: %v", tt.in, err)
			}
			if got := u.String(); got != uuidDashed {
				t.Fatalf("String() = %q, want %q", got, uuidDashed)
			}
		})
	}
}

func TestParseUUIDBytes(t *testing.T) {
	t.Parallel()
	u, err := ParseUUID(uuidUndashed)
	if err != nil {
		t.Fatal(err)
	}
	if u[0] != 0x01 || u[1] != 0x23 || u[2] != 0xab || u[15] != 0xef {
		t.Fatalf("unexpected bytes: %x", [16]byte(u))
	}
}

func TestParseUUIDInvalid(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, in string }{
		{"empty", ""},
		{"too short", "0123abcd"},
		{"too long undashed", uuidUndashed + "0"},
		{"too long dashed", uuidDashed + "0"},
		{"misplaced dash", "0123abcd4-567-89ef-0123-456789abcdef"},
		{"missing dash", "0123abcd-456789ef-0123-456789abcdef"},
		{"dashes in undashed length", "0123abcd456789ef0123456789abcde-"},
		{"non-hex dashed", "0123abcd-4567-89ef-0123-456789abcdeg"},
		{"non-hex undashed", "0123abcd456789ef0123456789abcdeg"},
		{"whitespace", " " + uuidDashed},
		{"braces", "{" + uuidDashed[:34] + "}"},
		{"unicode", "0123abcd-4567-89ef-0123-456789abcdéé"},
		{"emoji", strings.Repeat("\U0001F600", 8)},
		{"NUL", uuidUndashed[:31] + "\x00"},
		{"dot", "."},
		{"dot dot", ".."},
		{"special chars", "a/b?c#d e"},
		{"valid with trailing newline", uuidDashed + "\n"},
		{"path segment", "../v2/me"},
		{"path segment with slash", "skins/" + uuidDashed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u, err := ParseUUID(tt.in)
			if err == nil {
				t.Fatalf("ParseUUID(%q) = %v, want error", tt.in, u)
			}
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error %v must match ErrInvalidInput", err)
			}
			if errors.Is(err, ErrNotFound) {
				t.Fatalf("error must not match ErrNotFound")
			}
			if msg := err.Error(); !strings.HasPrefix(msg, "mineskin: invalid input: invalid UUID: ") || strings.Count(msg, "mineskin:") != 1 {
				t.Fatalf("error %q must have a single mineskin: prefix and the invalid-UUID text", msg)
			}
			if u != (UUID{}) {
				t.Fatalf("expected zero UUID on error, got %v", u)
			}
		})
	}
}

func TestParseUUIDErrorDoesNotEchoInput(t *testing.T) {
	t.Parallel()
	huge := strings.Repeat("secretinput", 100000)
	_, err := ParseUUID(huge)
	if err == nil {
		t.Fatal("expected error")
	}
	if len(err.Error()) > 200 || strings.Contains(err.Error(), "secretinput") {
		t.Fatalf("error echoes input: %.80q (len %d)", err.Error(), len(err.Error()))
	}
}

func TestUUIDStringRoundTrip(t *testing.T) {
	t.Parallel()
	var zero UUID
	if got := zero.String(); got != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("zero String() = %q", got)
	}
	u, _ := ParseUUID(uuidDashed)
	back, err := ParseUUID(u.String())
	if err != nil || back != u {
		t.Fatalf("round trip failed: %v %v", back, err)
	}
}

func TestUUIDTextRoundTrip(t *testing.T) {
	t.Parallel()
	const dashed = "744c993e-0e8a-4f0b-9b6a-2f3c7a1b5d11"
	var u UUID
	if err := u.UnmarshalText([]byte("744C993E0E8A4F0B9B6A2F3C7A1B5D11")); err != nil {
		t.Fatal(err)
	}
	b, err := u.MarshalText()
	if err != nil || string(b) != dashed {
		t.Errorf("MarshalText() = %q, %v", b, err)
	}
	if err := u.UnmarshalText([]byte("nope")); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("UnmarshalText(nope) err = %v", err)
	}
}

func TestUUIDIsZero(t *testing.T) {
	t.Parallel()
	parsed, err := ParseUUID(uuidDashed)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		in   UUID
		want bool
	}{
		{"zero value", UUID{}, true},
		{"parsed nil UUID", mustParseUUID(t, "00000000-0000-0000-0000-000000000000"), true},
		{"non-zero", parsed, false},
		{"last byte set", UUID{15: 1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.in.IsZero(); got != tt.want {
				t.Errorf("IsZero() = %v, want %v", got, tt.want)
			}
		})
	}
}

func mustParseUUID(t *testing.T, s string) UUID {
	t.Helper()
	u, err := ParseUUID(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func FuzzParseUUID(f *testing.F) {
	for _, seed := range []string{
		uuidDashed, uuidUndashed, strings.ToUpper(uuidDashed), "", ".", "..", "../v2/me",
		uuidDashed + "\n", "\x00", strings.Repeat("\U0001F600", 8), "0123abcd-4567-89ef-0123-456789abcdeg",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		u, err := ParseUUID(s)
		if err != nil {
			if !errors.Is(err, ErrInvalidInput) || u != (UUID{}) {
				t.Fatalf("ParseUUID(%q) = (%v, %v), want zero UUID and ErrInvalidInput", s, u, err)
			}
			return
		}
		if len(s) != 32 && len(s) != 36 {
			t.Fatalf("ParseUUID(%q) accepted input of length %d", s, len(s))
		}
		dashed := u.String()
		if len(dashed) != 36 || dashed != strings.ToLower(dashed) {
			t.Fatalf("String() = %q, want 36 lower-case characters", dashed)
		}
		again, err := ParseUUID(dashed)
		if err != nil || again != u {
			t.Fatalf("round trip of %q through %q = (%v, %v)", s, dashed, again, err)
		}
		text, err := u.MarshalText()
		if err != nil {
			t.Fatalf("MarshalText() error = %v", err)
		}
		var viaText UUID
		if err := viaText.UnmarshalText(text); err != nil || viaText != u {
			t.Fatalf("text round trip = (%v, %v), want %v", viaText, err, u)
		}
	})
}
