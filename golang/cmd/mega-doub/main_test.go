// cspell:ignore doub

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func textHandler(w io.Writer, o *slog.HandlerOptions) slog.Handler {
	return slog.NewTextHandler(w, o)
}

func jsonHandler(w io.Writer, o *slog.HandlerOptions) slog.Handler {
	return slog.NewJSONHandler(w, o)
}

// slogPath returns the pattern of the value v of a path field as the text handler of
// slog writes it, with the quotes and the escapes that it adds when needed.
func slogPath(t *testing.T, v string) string {
	t.Helper()
	return slogValue(t, v, "k=", "\n", textHandler)
}

// slogJSONPath returns the pattern of the value v of a path field as the JSON handler of
// slog writes it, with the quotes.
func slogJSONPath(t *testing.T, v string) string {
	t.Helper()
	return slogValue(t, v, `{"k":`, "}\n", jsonHandler)
}

// slogValue returns the pattern of the field k=v written by the handler that newHandler
// makes, without prefix and suffix.
func slogValue(t *testing.T, v, prefix, suffix string,
	newHandler func(io.Writer, *slog.HandlerOptions) slog.Handler) string {
	t.Helper()
	buf := &bytes.Buffer{}
	drop := func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == slog.LevelKey ||
			a.Key == slog.MessageKey) {
			return slog.Attr{}
		}
		return a
	}
	slog.New(newHandler(buf, &slog.HandlerOptions{ReplaceAttr: drop})).Info("", "k", v)
	got, ok := strings.CutPrefix(buf.String(), prefix)
	if !ok {
		t.Fatalf("slog wrote %q, want the prefix %q", buf.String(), prefix)
	}
	if got, ok = strings.CutSuffix(got, suffix); !ok {
		t.Fatalf("slog wrote %q, want the suffix %q", buf.String(), suffix)
	}
	return regexp.QuoteMeta(got)
}

func Test_run(t *testing.T) {
	type args struct {
		ctx  context.Context
		args []string
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	const (
		stamp     = `^time=\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}(Z|[+-]\d{2}:\d{2}) `
		jsonStamp = `^\{"time":"[^"]+",`
		example   = "../../testdata/example.yaml"
	)
	dir := t.TempDir()
	invalid := filepath.Join(dir, "invalid.yaml")
	logSettings := filepath.Join(dir, "logging.yaml")
	for path, content := range map[string]string{
		invalid: "---\ntargets:\n  - name: a\n    extra: 1\nglobal:\n  timeout: 30\n",
		logSettings: "---\nglobal:\n  log_level: warn\n  log_format: json\nstorage:\n" +
			"  url: b2://bucket/prefix\n  snapshot_id: main\n  password: env:PASSWORD\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	examplePath, err := filepath.Abs(example)
	if err != nil {
		t.Fatal(err)
	}
	// exampleWarnings are the warning records of the targets of example.yaml.
	exampleWarnings := []string{
		`level=WARN msg="configuration warning" file=` + slogPath(t, examplePath) +
			` line=26 column=5 path=targets\[0\] warning="target \\"db01\\" has no sources"$`,
		`level=WARN msg="configuration warning" file=` + slogPath(t, examplePath) +
			` line=32 column=5 path=targets\[1\] warning="target \\"local\\" has no sources"$`,
	}
	// backup is the pattern of the command record of backup on example.yaml.
	backup := func(dryRun bool) string {
		return `level=INFO msg=command command=backup config=` + slogPath(t, examplePath) +
			` dry_run=` + strconv.FormatBool(dryRun) +
			` log_level=info log_format=text no_progress=false$`
	}
	// shown returns the stdout of config show on the configuration file.
	shown := func(t *testing.T, path string) string {
		t.Helper()
		stdout := &bytes.Buffer{}
		if got := run(t.Context(), []string{"config", "show", "-c", path}, stdout,
			io.Discard); got != 0 {
			t.Fatalf("run() config show = %v, want 0", got)
		}
		return stdout.String()
	}
	type test struct {
		name string
		args args
		want int
		// json selects the JSON log format for the records in wantStderr.
		json bool
		// show is the configuration file whose config show output is the expected
		// stdout, empty for no output.
		show       string
		wantStderr []string
	}
	tests := []test{
		{
			name: "backup",
			args: args{ctx: t.Context(), args: []string{"backup", "-c", example}},
			want: 3,
			show: example,
			wantStderr: []string{
				exampleWarnings[0],
				exampleWarnings[1],
				backup(false),
				`level=ERROR msg="command is not implemented yet" command=backup$`,
			},
		},
		{
			name: "backup dry run",
			args: args{ctx: t.Context(), args: []string{"backup", "--dry-run", "-c", example}},
			want: 0,
			show: example,
			wantStderr: []string{
				exampleWarnings[0],
				exampleWarnings[1],
				backup(true),
				`level=INFO msg="dry run, nothing to do" command=backup$`,
			},
		},
		{
			name: "valid configuration",
			args: args{ctx: t.Context(), args: []string{"config", "validate", "-c", example}},
			want: 0,
			wantStderr: []string{
				exampleWarnings[0],
				exampleWarnings[1],
				`level=INFO msg="configuration is valid" config=` +
					slogPath(t, examplePath) + `$`,
			},
		},
		{
			name: "invalid configuration",
			args: args{ctx: t.Context(), args: []string{"config", "validate", "-c", invalid}},
			want: 2,
			wantStderr: []string{
				`level=ERROR msg="invalid configuration" file=` + slogPath(t, invalid) +
					` line=4 column=5 path=targets\[0\]\.extra ` +
					`error="field extra not found in type config\.targetItem"$`,
				`level=ERROR msg="invalid configuration" file=` + slogPath(t, invalid) +
					` line=6 column=12 path=global\.timeout error="invalid duration: want a ` +
					`number with a unit, such as 30s"$`,
			},
		},
		{
			name: "empty configuration path",
			args: args{ctx: t.Context(), args: []string{"config", "validate", "-c", ""}},
			want: 2,
			wantStderr: []string{
				`level=ERROR msg="invalid configuration" error="configuration path is empty"$`,
			},
		},
		{
			name: "unknown keys on many levels",
			args: args{ctx: t.Context(), args: []string{"config", "validate", "-c",
				"../../testdata/invalid/unknown-keys-many.yaml"}},
			want: 2,
			wantStderr: []string{
				`level=ERROR msg="invalid configuration" file=.*/unknown-keys-many\.yaml ` +
					`line=2 column=1 path=bogus error="field bogus not found in type ` +
					`config\.configFile"$`,
				`level=ERROR msg="invalid configuration" file=.*/unknown-keys-many\.yaml ` +
					`line=4 column=3 path=storage\.extra error="field extra not found in type ` +
					`config\.Storage"$`,
				`level=ERROR msg="invalid configuration" file=.*/unknown-keys-many\.yaml ` +
					`line=8 column=5 path=targets\[0\]\.surplus error="field surplus not found ` +
					`in type config\.targetItem"$`,
			},
		},
		{
			name: "invalid log level with the flag",
			args: args{ctx: t.Context(), args: []string{"config", "validate", "--log-level",
				"debug", "-c", "../../testdata/invalid/bad-log-level.yaml"}},
			want: 2,
			wantStderr: []string{
				`level=ERROR msg="invalid configuration" file=.*/bad-log-level\.yaml ` +
					`line=3 column=14 path=global\.log_level error="invalid log level: want one of ` +
					`debug, info, warn, error"$`,
			},
		},
		{
			name: "log settings from the configuration",
			args: args{ctx: t.Context(), args: []string{"backup", "-c", logSettings}},
			want: 3,
			json: true,
			show: logSettings,
			wantStderr: []string{
				`"level":"WARN","msg":"configuration warning","file":` +
					slogJSONPath(t, logSettings) + `,"line":1,"column":1,"path":"targets",` +
					`"warning":"no targets configured"\}$`,
				`"level":"ERROR","msg":"command is not implemented yet","command":"backup"\}$`,
			},
		},
		{
			name: "log flags over the configuration",
			args: args{ctx: t.Context(), args: []string{"backup", "-c", logSettings,
				"--log-level", "info", "--log-format", "text"}},
			want: 3,
			show: logSettings,
			wantStderr: []string{
				`level=WARN msg="configuration warning" file=` + slogPath(t, logSettings) +
					` line=1 column=1 path=targets warning="no targets configured"$`,
				`level=INFO msg=command command=backup config=` + slogPath(t, logSettings) +
					` dry_run=false log_level=info log_format=text no_progress=false$`,
				`level=ERROR msg="command is not implemented yet" command=backup$`,
			},
		},
		{
			name: "canceled context",
			args: args{ctx: canceled, args: []string{"backup"}},
			want: 4,
			wantStderr: []string{
				`level=ERROR msg=interrupted error="context canceled"$`,
			},
		},
	}
	// A canceled context gives 4 and one interrupted record for every command, a dry run
	// that succeeds included.
	for _, command := range [][]string{{"backup", "--dry-run"}, {"prune"}, {"check"},
		{"verify"}, {"list"}, {"config", "validate"}, {"config", "show"}} {
		tests = append(tests, test{
			name: "canceled context of " + strings.Join(command, " "),
			args: args{ctx: canceled, args: append(command, "-c", example)},
			want: 4,
			wantStderr: []string{
				`level=ERROR msg=interrupted error="context canceled"$`,
			},
		})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in2 := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			if got := run(tt.args.ctx, tt.args.args, in2, stderr); got != tt.want {
				t.Errorf("run() = %v, want %v", got, tt.want)
			}
			want1 := ""
			if tt.show != "" {
				want1 = shown(t, tt.show)
			}
			if got1 := in2.String(); got1 != want1 {
				t.Errorf("run() stdout = %q, want %q", got1, want1)
			}
			gotStderr := stderr.String()
			lines := strings.Split(strings.TrimSuffix(gotStderr, "\n"), "\n")
			if len(lines) != len(tt.wantStderr) {
				t.Fatalf("run() stderr has %d records, want %d: %q", len(lines),
					len(tt.wantStderr), gotStderr)
			}
			prefix := stamp
			if tt.json {
				prefix = jsonStamp
			}
			for i, pattern := range tt.wantStderr {
				if record := regexp.MustCompile(prefix + pattern); !record.MatchString(lines[i]) {
					t.Errorf("run() stderr record %q, want a match for %q", lines[i], record)
				}
			}
		})
	}
}
