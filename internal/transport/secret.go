package transport

import (
	"encoding"
	"log/slog"
)

// redacted is what every formatted or logged Secret shows.
const redacted = "[REDACTED]"

// Secret holds a credential, such as the API key, so that it cannot be
// printed or logged by accident. Its String, GoString, LogValue and
// MarshalText methods all give "[REDACTED]", and only Reveal returns the
// value, for the one place that must send it.
//
// The value sits behind a pointer on purpose: package fmt cannot call methods
// on an unexported struct field, and prints such a field by reflection. A
// pointer is printed as an address there, so the value stays hidden even when
// a config or client struct is formatted with %v, %+v or %#v.
//
// This protects against accidental printing and logging only. A deep
// reflection dumper (for example go-spew) follows the pointer and prints the
// value, so never dump a Client or config with such a tool.
//
// A Secret is a handle: copies share the value, and comparing two Secrets
// with == compares the handles, not the values.
type Secret struct {
	value *string
}

var (
	_ slog.LogValuer         = Secret{}
	_ encoding.TextMarshaler = Secret{}
)

// NewSecret wraps value. An empty value gives the zero Secret.
func NewSecret(value string) Secret {
	if value == "" {
		return Secret{}
	}
	return Secret{value: &value}
}

// Reveal returns the secret value, or "" for an empty Secret. Use it only to
// build the request header.
func (s Secret) Reveal() string {
	if s.value == nil {
		return ""
	}
	return *s.value
}

// IsEmpty reports whether there is no secret value.
func (s Secret) IsEmpty() bool { return s.value == nil }

// String implements fmt.Stringer and never shows the value.
func (Secret) String() string { return redacted }

// GoString implements fmt.GoStringer and never shows the value.
func (Secret) GoString() string { return redacted }

// LogValue implements slog.LogValuer and never shows the value.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText implements encoding.TextMarshaler, so JSON and similar
// encoders never show the value.
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
