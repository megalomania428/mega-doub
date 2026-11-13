// Package secret holds references to secret values.
package secret

import (
	"log/slog"
	"strconv"
)

// Ref is a reference to a secret value, such as env:NAME or vault:mount/path/key. fmt
// does not call the methods of an unexported field, so a printed structure masks only a
// reference in an exported one. encoding/json and slog.NewJSONHandler print references
// within a structure, map, or slice as they are, since Ref has no MarshalText method.
type Ref string

// masked replaces a plain value in the output.
const masked = "plain:***"

// String returns the reference with a plain value masked: a plain: reference and a
// string without a known prefix give plain:***, the env:, file: and vault: references
// are returned as they are, an empty reference gives an empty string.
func (r Ref) String() string {
	if r == "" {
		return ""
	}
	if kind, _, ok := split(string(r)); ok && kind != KindPlain {
		return string(r)
	}
	return masked
}

// LogValue implements slog.LogValuer with the masked reference.
func (r Ref) LogValue() slog.Value {
	return slog.StringValue(r.String())
}

// GoString masks the reference for the %#v verb, which does not call String.
func (r Ref) GoString() string {
	return strconv.Quote(r.String())
}
