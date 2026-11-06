// Package logging builds log/slog loggers for the command line tool.
package logging

import (
	"fmt"
	"io"
	"log/slog"
)

// Format is a log output format.
type Format string

// Supported log formats.
const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// Field names for log record context.
const (
	KeyTarget   = "target"
	KeySource   = "source"
	KeyArtifact = "artifact"
	KeyAttempt  = "attempt"
)

const (
	levelNames  = "debug, info, warn, error"
	formatNames = "text, json"
)

// ParseLevel converts a lower case level name to a slog level.
func ParseLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("invalid log level %q: want one of %s", s, levelNames)
}

// ParseFormat converts a lower case format name to a Format.
func ParseFormat(s string) (Format, error) {
	switch f := Format(s); f {
	case FormatText, FormatJSON:
		return f, nil
	}
	return "", fmt.Errorf("invalid log format %q: want one of %s", s, formatNames)
}

// New returns a logger that writes records at or above level to w in the given format.
// An unknown format falls back to text.
func New(w io.Writer, level slog.Level, format Format) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	if format == FormatJSON {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}
