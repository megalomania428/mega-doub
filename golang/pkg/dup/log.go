// cspell:ignore abcd abcde duplicacy

// Package dup runs duplicacy inside the process of mega-doub. For now it holds the bridge
// from the log of duplicacy to log/slog.
package dup

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"strings"

	duplicacy "duplicacy.invalid/v3/src"
)

// The msg and the fields of the records of the bridge.
const (
	logMessage = "duplicacy"
	keyID      = "id"
	keyText    = "text"
)

// masked replaces the masks in the text of a record.
const masked = "***"

// Option is an option of LogFunction.
type Option func(*options)

// options are the settings of LogFunction.
type options struct {
	masks []string
}

// WithMasks adds strings, such as the values of secrets, that LogFunction replaces with
// *** in the text of a record; an empty string masks nothing. The option keeps its own
// copy of values, and a repeated WithMasks adds its strings to the earlier ones.
func WithMasks(values ...string) Option {
	masks := slices.Clone(values)
	return func(o *options) {
		o.masks = append(o.masks, masks...)
	}
}

// LogFunction returns a value for duplicacy.LogFunction that writes every record of
// duplicacy to logger as a record with the msg duplicacy and the fields id, the log ID,
// left out when it is empty, and text, the message with the masks of WithMasks replaced.
// The levels DEBUG and TRACE of duplicacy become slog.LevelDebug, INFO becomes
// slog.LevelInfo, WARN becomes slog.LevelWarn and the others, the unknown ones included,
// become slog.LevelError. A nil logger discards the records and a nil option is skipped.
// The returned function is safe for concurrent use and does not panic; duplicacy itself
// panics with an Exception after it returns from a record above WARN.
func LogFunction(logger *slog.Logger, opts ...Option) func(int, string, string) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	masks := o.masks
	return func(level int, logID, message string) {
		ctx := context.Background()
		l := slogLevel(level)
		if !logger.Enabled(ctx, l) {
			return
		}
		attrs := make([]slog.Attr, 0, 2)
		if logID != "" {
			attrs = append(attrs, slog.String(keyID, logID))
		}
		attrs = append(attrs, slog.String(keyText, mask(message, masks)))
		logger.LogAttrs(ctx, l, logMessage, attrs...)
	}
}

// slogLevel converts a level of duplicacy to a level of slog.
func slogLevel(level int) slog.Level {
	switch level {
	case duplicacy.DEBUG, duplicacy.TRACE:
		return slog.LevelDebug
	case duplicacy.INFO:
		return slog.LevelInfo
	case duplicacy.WARN:
		return slog.LevelWarn
	}
	return slog.LevelError
}

// mask replaces every occurrence of any of the non-empty masks in s with ***. The
// occurrences are searched in s as it is, so overlapping ones, such as abcd and cde in
// abcde, are replaced together with one ***, while adjacent ones get one *** each, and
// the stars of a replacement never match a mask.
func mask(s string, masks []string) string {
	type span struct{ start, end int }
	var spans []span
	for _, m := range masks {
		if m == "" {
			continue
		}
		for from := 0; ; {
			i := strings.Index(s[from:], m)
			if i < 0 {
				break
			}
			spans = append(spans, span{start: from + i, end: from + i + len(m)})
			from += i + 1
		}
	}
	if len(spans) == 0 {
		return s
	}
	slices.SortFunc(spans, func(a, b span) int { return cmp.Compare(a.start, b.start) })
	var b strings.Builder
	last, cur := 0, spans[0]
	for _, sp := range spans[1:] {
		if sp.start < cur.end {
			cur.end = max(cur.end, sp.end)
			continue
		}
		b.WriteString(s[last:cur.start])
		b.WriteString(masked)
		last, cur = cur.end, sp
	}
	b.WriteString(s[last:cur.start])
	b.WriteString(masked)
	b.WriteString(s[cur.end:])
	return b.String()
}
