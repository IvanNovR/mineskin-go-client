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
	tests := []struct{ name, in string }{
		{"dashed", uuidDashed},
		{"undashed", uuidUndashed},
		{"upper-case dashed", strings.ToUpper(uuidDashed)},
		{"upper-case undashed", strings.ToUpper(uuidUndashed)},
		{"mixed case", "0123ABCD-4567-89ef-0123-456789AbCdEf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
	u, err := ParseUUID(uuidUndashed)
	if err != nil {
		t.Fatal(err)
	}
	if u[0] != 0x01 || u[1] != 0x23 || u[2] != 0xab || u[15] != 0xef {
		t.Fatalf("unexpected bytes: %x", [16]byte(u))
	}
}

func TestParseUUIDInvalid(t *testing.T) {
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := ParseUUID(tt.in)
			if err == nil {
				t.Fatalf("ParseUUID(%q) = %v, want error", tt.in, u)
			}
			if !errors.Is(err, ErrInvalidInput) || !errors.Is(err, ErrClient) {
				t.Fatalf("error %v must match ErrInvalidInput and ErrClient", err)
			}
			if errors.Is(err, ErrNotFound) {
				t.Fatalf("error must not match ErrNotFound")
			}
			if u != (UUID{}) {
				t.Fatalf("expected zero UUID on error, got %v", u)
			}
		})
	}
}

func TestParseUUIDErrorDoesNotEchoInput(t *testing.T) {
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
