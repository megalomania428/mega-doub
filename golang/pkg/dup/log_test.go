// cspell:ignore abab abcd abcde duplicacy xabcdex

package dup

import (
	"bytes"
	"encoding/json"
	"log"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	duplicacy "duplicacy.invalid/v3/src"
)

// newLogger returns a JSON logger that writes the records at or above level to buf.
func newLogger(buf *bytes.Buffer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level}))
}

// records decodes the records written to buf without their time.
func records(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var got []map[string]any
	for line := range strings.Lines(buf.String()) {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("record %q: %v", line, err)
		}
		delete(record, slog.TimeKey)
		got = append(got, record)
	}
	return got
}

func TestWithMasks(t *testing.T) {
	type args struct {
		values []string
	}
	tests := []struct {
		name string
		args args
		want []string
	}{
		{name: "none", args: args{}, want: nil},
		{name: "one", args: args{values: []string{"fake-a"}}, want: []string{"fake-a"}},
		{
			name: "several with an empty one",
			args: args{values: []string{"fake-a", "", "fake-b"}},
			want: []string{"fake-a", "", "fake-b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := slices.Clone(tt.args.values)
			opt := WithMasks(values...)
			// A change of the slice after the call does not reach the option.
			for i := range values {
				values[i] = "fake-changed"
			}
			var o options
			opt(&o)
			if !slices.Equal(o.masks, tt.want) {
				t.Errorf("WithMasks() masks = %q, want %q", o.masks, tt.want)
			}
		})
	}
	t.Run("repeated", func(t *testing.T) {
		var o options
		WithMasks("fake-a")(&o)
		WithMasks("fake-b", "fake-c")(&o)
		want := []string{"fake-a", "fake-b", "fake-c"}
		if !slices.Equal(o.masks, want) {
			t.Errorf("WithMasks() masks = %q, want %q", o.masks, want)
		}
	})
}

func TestLogFunction(t *testing.T) {
	type args struct {
		level slog.Level
		opts  []Option
	}
	type call struct {
		level          int
		logID, message string
	}
	record := func(level, id, text string) map[string]any {
		r := map[string]any{"level": level, "msg": "duplicacy", "text": text}
		if id != "" {
			r["id"] = id
		}
		return r
	}
	debug := args{level: slog.LevelDebug}
	tests := []struct {
		name string
		args args
		call call
		want map[string]any
	}{
		{
			name: "debug",
			args: debug,
			call: call{level: duplicacy.DEBUG, logID: "TEST_DEBUG", message: "debug"},
			want: record("DEBUG", "TEST_DEBUG", "debug"),
		},
		{
			name: "trace",
			args: debug,
			call: call{level: duplicacy.TRACE, logID: "TEST_TRACE", message: "trace"},
			want: record("DEBUG", "TEST_TRACE", "trace"),
		},
		{
			name: "info",
			args: debug,
			call: call{level: duplicacy.INFO, logID: "TEST_INFO", message: "info"},
			want: record("INFO", "TEST_INFO", "info"),
		},
		{
			name: "warn",
			args: debug,
			call: call{level: duplicacy.WARN, logID: "TEST_WARN", message: "warn"},
			want: record("WARN", "TEST_WARN", "warn"),
		},
		{
			name: "error",
			args: debug,
			call: call{level: duplicacy.ERROR, logID: "TEST_ERROR", message: "error"},
			want: record("ERROR", "TEST_ERROR", "error"),
		},
		{
			name: "fatal",
			args: debug,
			call: call{level: duplicacy.FATAL, logID: "TEST_FATAL", message: "fatal"},
			want: record("ERROR", "TEST_FATAL", "fatal"),
		},
		{
			name: "assert",
			args: debug,
			call: call{level: duplicacy.ASSERT, logID: "TEST_ASSERT", message: "assert"},
			want: record("ERROR", "TEST_ASSERT", "assert"),
		},
		{
			name: "unknown level",
			args: debug,
			call: call{level: 5, logID: "TEST_UNKNOWN", message: "unknown"},
			want: record("ERROR", "TEST_UNKNOWN", "unknown"),
		},
		{
			name: "empty id is left out",
			args: debug,
			call: call{level: duplicacy.INFO, message: "info"},
			want: record("INFO", "", "info"),
		},
		{
			name: "empty text is kept",
			args: debug,
			call: call{level: duplicacy.INFO, logID: "TEST_INFO"},
			want: record("INFO", "TEST_INFO", ""),
		},
		{
			name: "one mask",
			args: args{level: slog.LevelDebug, opts: []Option{WithMasks("fake-a")}},
			call: call{level: duplicacy.INFO, logID: "TEST_INFO", message: "key fake-a"},
			want: record("INFO", "TEST_INFO", "key ***"),
		},
		{
			name: "several masks",
			args: args{
				level: slog.LevelDebug,
				opts:  []Option{WithMasks("fake-a"), WithMasks("fake-b")},
			},
			call: call{level: duplicacy.WARN, logID: "TEST_WARN", message: "fake-a fake-b"},
			want: record("WARN", "TEST_WARN", "*** ***"),
		},
		{
			name: "empty mask masks nothing",
			args: args{level: slog.LevelDebug, opts: []Option{WithMasks("")}},
			call: call{level: duplicacy.INFO, logID: "TEST_INFO", message: "fake-a"},
			want: record("INFO", "TEST_INFO", "fake-a"),
		},
		{
			name: "nil option is skipped",
			args: args{level: slog.LevelDebug, opts: []Option{nil, WithMasks("fake-a")}},
			call: call{level: duplicacy.INFO, logID: "TEST_INFO", message: "fake-a"},
			want: record("INFO", "TEST_INFO", "***"),
		},
		{
			name: "below the level of the logger",
			args: args{level: slog.LevelInfo},
			call: call{level: duplicacy.DEBUG, logID: "TEST_DEBUG", message: "debug"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			f := LogFunction(newLogger(&buf, tt.args.level), tt.args.opts...)
			f(tt.call.level, tt.call.logID, tt.call.message)
			got := records(t, &buf)
			if tt.want == nil {
				if len(got) != 0 {
					t.Errorf("LogFunction() records = %v, want none", got)
				}
				return
			}
			if len(got) != 1 || !maps.Equal(got[0], tt.want) {
				t.Errorf("LogFunction() records = %v, want [%v]", got, tt.want)
			}
		})
	}
	t.Run("nil logger", func(*testing.T) {
		LogFunction(nil)(duplicacy.ERROR, "TEST_ERROR", "error")
	})
}

func TestLogFunction_concurrent(t *testing.T) {
	const goroutines, calls = 8, 100
	var buf bytes.Buffer
	f := LogFunction(newLogger(&buf, slog.LevelDebug), WithMasks("fake-a"))
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range calls {
				f(duplicacy.INFO, "TEST_CONCURRENT", "key fake-a")
			}
		})
	}
	wg.Wait()
	got := records(t, &buf)
	if len(got) != goroutines*calls {
		t.Fatalf("LogFunction() wrote %d records, want %d", len(got), goroutines*calls)
	}
	for _, r := range got {
		if r["id"] != "TEST_CONCURRENT" || r["text"] != "key ***" {
			t.Fatalf("LogFunction() record = %v", r)
		}
	}
}

func TestLogFunction_duplicacy(t *testing.T) {
	saved := duplicacy.LogFunction
	t.Cleanup(func() { duplicacy.LogFunction = saved })
	tests := []struct {
		name      string
		log       func()
		want      map[string]any
		wantPanic bool
	}{
		{
			name: "info",
			log:  func() { duplicacy.LOG_INFO("TEST_INFO", "info %s", "fake-a") },
			want: map[string]any{
				"level": "INFO", "msg": "duplicacy", "id": "TEST_INFO", "text": "info ***",
			},
		},
		{
			name: "error",
			log:  func() { duplicacy.LOG_ERROR("TEST_ERROR", "error %s", "fake-a") },
			want: map[string]any{
				"level": "ERROR", "msg": "duplicacy", "id": "TEST_ERROR", "text": "error ***",
			},
			wantPanic: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			duplicacy.LogFunction = LogFunction(newLogger(&buf, slog.LevelDebug),
				WithMasks("fake-a"))
			recovered := func() (r any) {
				defer func() { r = recover() }()
				tt.log()
				return nil
			}()
			if tt.wantPanic {
				e, ok := recovered.(duplicacy.Exception)
				if !ok {
					t.Fatalf("panic = %#v, want a duplicacy.Exception", recovered)
				}
				if e.LogID != tt.want["id"] {
					t.Errorf("Exception.LogID = %q, want %q", e.LogID, tt.want["id"])
				}
			} else if recovered != nil {
				t.Fatalf("panic = %#v, want none", recovered)
			}
			got := records(t, &buf)
			if len(got) != 1 || !maps.Equal(got[0], tt.want) {
				t.Errorf("records = %v, want [%v]", got, tt.want)
			}
		})
	}
}

// The import of duplicacy by this package keeps the standard logger as it is.
func TestStandardLog(t *testing.T) {
	if got := log.Writer(); got != os.Stderr {
		t.Errorf("log.Writer() = %v, want os.Stderr", got)
	}
	if got := log.Flags(); got != log.LstdFlags {
		t.Errorf("log.Flags() = %v, want log.LstdFlags", got)
	}
}

func Test_slogLevel(t *testing.T) {
	type args struct {
		level int
	}
	tests := []struct {
		name string
		args args
		want slog.Level
	}{
		{name: "debug", args: args{level: duplicacy.DEBUG}, want: slog.LevelDebug},
		{name: "trace", args: args{level: duplicacy.TRACE}, want: slog.LevelDebug},
		{name: "info", args: args{level: duplicacy.INFO}, want: slog.LevelInfo},
		{name: "warn", args: args{level: duplicacy.WARN}, want: slog.LevelWarn},
		{name: "error", args: args{level: duplicacy.ERROR}, want: slog.LevelError},
		{name: "fatal", args: args{level: duplicacy.FATAL}, want: slog.LevelError},
		{name: "assert", args: args{level: duplicacy.ASSERT}, want: slog.LevelError},
		{name: "above assert", args: args{level: 5}, want: slog.LevelError},
		{name: "below debug", args: args{level: -3}, want: slog.LevelError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := slogLevel(tt.args.level); got != tt.want {
				t.Errorf("slogLevel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_mask(t *testing.T) {
	type args struct {
		s     string
		masks []string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{name: "no masks", args: args{s: "fake-a"}, want: "fake-a"},
		{name: "no match", args: args{s: "fake-a", masks: []string{"fake-b"}}, want: "fake-a"},
		{name: "one", args: args{s: "x fake-a y", masks: []string{"fake-a"}}, want: "x *** y"},
		{
			name: "every occurrence",
			args: args{s: "fake-a fake-a", masks: []string{"fake-a"}},
			want: "*** ***",
		},
		{
			name: "several",
			args: args{s: "fake-a:fake-b", masks: []string{"fake-a", "fake-b"}},
			want: "***:***",
		},
		{name: "empty", args: args{s: "fake-a", masks: []string{""}}, want: "fake-a"},
		{
			name: "empty among others",
			args: args{s: "fake-a", masks: []string{"", "fake-a"}},
			want: "***",
		},
		{
			name: "repeated",
			args: args{s: "fake-a", masks: []string{"fake-a", "fake-a"}},
			want: "***",
		},
		{
			name: "overlapping",
			args: args{s: "abcde", masks: []string{"abcd", "cde"}},
			want: "***",
		},
		{
			name: "overlapping in reverse order",
			args: args{s: "abcde", masks: []string{"cde", "abcd"}},
			want: "***",
		},
		{
			name: "inside another",
			args: args{s: "xabcdex", masks: []string{"bcd", "abcde"}},
			want: "x***x",
		},
		{
			name: "overlapping occurrences",
			args: args{s: "aaa", masks: []string{"aa"}},
			want: "***",
		},
		{
			name: "adjacent occurrences",
			args: args{s: "abab", masks: []string{"ab"}},
			want: "******",
		},
		{
			name: "stars",
			args: args{s: "fake-a *", masks: []string{"fake-a", "*"}},
			want: "*** ***",
		},
		{
			name: "unicode",
			args: args{s: "ключ fake-ё", masks: []string{"fake-ё"}},
			want: "ключ ***",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mask(tt.args.s, tt.args.masks); got != tt.want {
				t.Errorf("mask() = %q, want %q", got, tt.want)
			}
		})
	}
}
