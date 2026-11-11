// Package secret is documented in ref.go.
package secret

import (
	"fmt"
	"io"
	"log/slog"
)

// hidden replaces a resolved value in every output.
const hidden = "***"

// Value is a resolved secret value: every output form of it is ***, only Reveal gives the
// value. The value is kept behind a pointer, since fmt prints an unexported field of a
// structure by reflection without calling its methods: a string there would show the
// value, a pointer shows only an address.
type Value struct {
	value *string
}

// NewValue returns a Value that holds s.
func NewValue(s string) Value {
	return Value{value: &s}
}

// Reveal returns the value, an empty string for the zero Value.
func (v Value) Reveal() string {
	if v.value == nil {
		return ""
	}
	return *v.value
}

// String returns ***.
func (v Value) String() string {
	return hidden
}

// GoString returns *** for the %#v verb.
func (v Value) GoString() string {
	return hidden
}

// Format writes *** for every verb and flag of fmt, %x and %q included.
func (v Value) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, hidden)
}

// LogValue implements slog.LogValuer with ***.
func (v Value) LogValue() slog.Value {
	return slog.StringValue(hidden)
}

// MarshalText returns *** for the encoding packages.
func (v Value) MarshalText() ([]byte, error) {
	return []byte(hidden), nil
}

// MarshalJSON returns *** as a JSON string.
func (v Value) MarshalJSON() ([]byte, error) {
	return []byte(`"` + hidden + `"`), nil
}
