// Package config is documented in config.go.
package config

import (
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/megalomania428/mega-doub/pkg/logging"
	"github.com/megalomania428/mega-doub/pkg/units"
)

// Duration is a time.Duration written with units, such as 30s or 12h.
type Duration time.Duration

// UnmarshalYAML parses the duration with time.ParseDuration, so a bare number other
// than 0 is rejected.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Load(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return errors.New("invalid duration: want a number with a unit, such as 30s")
	}
	*d = Duration(v)
	return nil
}

// MarshalYAML writes the duration as time.Duration.String does.
func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

// Size is a number of bytes written with an optional K, M, G or T suffix.
type Size int64

// UnmarshalYAML parses the size with units.ParseSize, a bare integer is bytes.
func (s *Size) UnmarshalYAML(node *yaml.Node) error {
	var text string
	if err := node.Load(&text); err != nil {
		return err
	}
	v, err := units.ParseSize(text)
	if err != nil {
		return valueError(err, text)
	}
	*s = Size(v)
	return nil
}

// MarshalYAML writes the size as the string of units.FormatSize, so a size without a
// suffix, such as '100', is quoted and is read back the same way.
func (s Size) MarshalYAML() (any, error) {
	return units.FormatSize(int64(s)), nil
}

// LogLevel is a log level name accepted by logging.ParseLevel.
type LogLevel string

// UnmarshalYAML accepts only the names known to logging.ParseLevel.
func (l *LogLevel) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Load(&s); err != nil {
		return err
	}
	if _, err := logging.ParseLevel(s); err != nil {
		return valueError(err, s)
	}
	*l = LogLevel(s)
	return nil
}

// Level returns the slog level of the name.
func (l LogLevel) Level() slog.Level {
	level, _ := logging.ParseLevel(string(l))
	return level
}

// LogFormat is a log format name accepted by logging.ParseFormat.
type LogFormat string

// UnmarshalYAML accepts only the names known to logging.ParseFormat.
func (f *LogFormat) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Load(&s); err != nil {
		return err
	}
	if _, err := logging.ParseFormat(s); err != nil {
		return valueError(err, s)
	}
	*f = LogFormat(s)
	return nil
}

// Format returns the logging format of the name.
func (f LogFormat) Format() logging.Format {
	return logging.Format(f)
}

// valueError drops the value quoted with %q from an error of a parser: values of the
// configuration stay out of messages.
func valueError(err error, value string) error {
	return errors.New(strings.Replace(err.Error(), " "+strconv.Quote(value), "", 1))
}
