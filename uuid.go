package mineskin

import (
	"encoding/hex"
	"fmt"
)

const (
	uuidBytes         = 16
	uuidUndashedLen   = 2 * uuidBytes
	uuidDashedLen     = uuidUndashedLen + 4
	uuidDashSeparator = '-'
)

// uuidDashPositions are the indexes of the dashes in the canonical form.
var uuidDashPositions = [...]int{8, 13, 18, 23}

// UUID is a 128-bit MineSkin skin identifier (not a player UUID).
type UUID [16]byte

// ParseUUID parses a UUID in dashed (8-4-4-4-12) or undashed (32 hex digits)
// form. Hex digits may be upper or lower case. A failure matches
// ErrInvalidInput and ErrClient; the error never contains the input.
func ParseUUID(s string) (UUID, error) {
	var u UUID
	hexDigits, ok := uuidHexDigits(s)
	if !ok {
		return u, newInvalidInputError(fmt.Sprintf("invalid UUID: unrecognised format (length %d)", len(s)))
	}
	if _, err := hex.Decode(u[:], []byte(hexDigits)); err != nil {
		return UUID{}, newInvalidInputError("invalid UUID: contains a non-hexadecimal character")
	}
	return u, nil
}

// uuidHexDigits strips the dashes of a well-formed dashed UUID, or returns an
// undashed one unchanged. ok is false when the shape is wrong.
func uuidHexDigits(s string) (digits string, ok bool) {
	switch len(s) {
	case uuidUndashedLen:
		return s, true
	case uuidDashedLen:
		b := make([]byte, 0, uuidUndashedLen)
		next := 0
		for i := 0; i < len(s); i++ {
			if next < len(uuidDashPositions) && i == uuidDashPositions[next] {
				if s[i] != uuidDashSeparator {
					return "", false
				}
				next++
				continue
			}
			b = append(b, s[i])
		}
		return string(b), true
	default:
		return "", false
	}
}

// String returns the lower-case dashed form, for example
// "0123abcd-4567-89ef-0123-456789abcdef".
func (u UUID) String() string {
	var buf [uuidDashedLen]byte
	hex.Encode(buf[0:8], u[0:4])
	buf[8] = uuidDashSeparator
	hex.Encode(buf[9:13], u[4:6])
	buf[13] = uuidDashSeparator
	hex.Encode(buf[14:18], u[6:8])
	buf[18] = uuidDashSeparator
	hex.Encode(buf[19:23], u[8:10])
	buf[23] = uuidDashSeparator
	hex.Encode(buf[24:], u[10:])
	return string(buf[:])
}

// MarshalText implements encoding.TextMarshaler using the dashed form.
func (u UUID) MarshalText() ([]byte, error) { return []byte(u.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler. It accepts the forms
// ParseUUID accepts and leaves u unchanged on error.
func (u *UUID) UnmarshalText(text []byte) error {
	parsed, err := ParseUUID(string(text))
	if err != nil {
		return err
	}
	*u = parsed
	return nil
}
