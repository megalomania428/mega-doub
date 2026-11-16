// cspell:ignore doub

package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/megalomania428/mega-doub/internal/cli"
	"github.com/megalomania428/mega-doub/pkg/logging"
)

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
		invalid:     "---\ntargets:\n  - name: a\n    extra: 1\nglobal:\n  timeout: 30\n",
		logSettings: "---\nglobal:\n  log_level: warn\n  log_format: json\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	examplePath, err := filepath.Abs(example)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args args
		want int
		// json selects the JSON log format for the records in wantStderr.
		json       bool
		want1      string
		wantStderr []string
	}{
		{
			name: "backup",
			args: args{ctx: t.Context(), args: []string{"backup", "-c", example}},
			want: 3,
			wantStderr: []string{
				`level=INFO msg=command command=backup ` +
					`config=` + regexp.QuoteMeta(examplePath) + ` dry_run=false$`,
				`level=ERROR msg="command is not implemented yet" command=backup$`,
			},
		},
		{
			name: "valid configuration",
			args: args{ctx: t.Context(), args: []string{"config", "validate", "-c", example}},
			want: 0,
			wantStderr: []string{
				`level=INFO msg="configuration is valid" config=` +
					regexp.QuoteMeta(examplePath) + `$`,
			},
		},
		{
			name: "invalid configuration",
			args: args{ctx: t.Context(), args: []string{"config", "validate", "-c", invalid}},
			want: 2,
			wantStderr: []string{
				`level=ERROR msg="invalid configuration" file=` + regexp.QuoteMeta(invalid) +
					` line=4 column=5 error="field extra not found in type config\.targetItem"$`,
				`level=ERROR msg="invalid configuration" file=` + regexp.QuoteMeta(invalid) +
					` line=6 column=12 error="invalid duration: want a number with a unit, ` +
					`such as 30s"$`,
			},
		},
		{
			name: "invalid log level with the flag",
			args: args{ctx: t.Context(), args: []string{"config", "validate", "--log-level",
				"debug", "-c", "../../testdata/invalid/bad-log-level.yaml"}},
			want: 2,
			wantStderr: []string{
				`level=ERROR msg="invalid configuration" file=.*/bad-log-level\.yaml ` +
					`line=3 column=14 error="invalid log level: want one of debug, info, warn, error"$`,
			},
		},
		{
			name: "log settings from the configuration",
			args: args{ctx: t.Context(), args: []string{"backup", "-c", logSettings}},
			want: 3,
			json: true,
			wantStderr: []string{
				`"level":"ERROR","msg":"command is not implemented yet","command":"backup"\}$`,
			},
		},
		{
			name: "log flags over the configuration",
			args: args{ctx: t.Context(), args: []string{"backup", "-c", logSettings,
				"--log-level", "info", "--log-format", "text"}},
			want: 3,
			wantStderr: []string{
				`level=INFO msg=command command=backup config=` + regexp.QuoteMeta(logSettings) +
					` dry_run=false$`,
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
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in2 := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			if got := run(tt.args.ctx, tt.args.args, in2, stderr); got != tt.want {
				t.Errorf("run() = %v, want %v", got, tt.want)
			}
			if got1 := in2.String(); got1 != tt.want1 {
				t.Errorf("run() stdout = %q, want %q", got1, tt.want1)
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

func Test_handle(t *testing.T) {
	type args struct {
		ctx context.Context
		inv cli.Invocation
		env cli.Env
	}
	logSettings := filepath.Join(t.TempDir(), "logging.yaml")
	if err := os.WriteFile(logSettings, []byte("---\nglobal:\n  log_format: json\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, fromFlag := &bytes.Buffer{}, &bytes.Buffer{}
	tests := []struct {
		name   string
		args   args
		want   int
		stderr *bytes.Buffer
		// wantRecord matches the record that the caller writes with its logger after the
		// handler, as cli.Execute does on an interruption.
		wantRecord string
	}{
		{
			name: "format from the configuration",
			args: args{ctx: t.Context(), inv: cli.Invocation{Command: "config validate",
				Global: cli.Global{Config: logSettings}}, env: cli.Env{Stderr: fromFile,
				Logger: logging.New(fromFile, slog.LevelInfo, logging.FormatText)}},
			want:       0,
			stderr:     fromFile,
			wantRecord: `^\{"time":"[^"]+","level":"ERROR","msg":"interrupted"`,
		},
		{
			name: "format from the flag",
			args: args{ctx: t.Context(), inv: cli.Invocation{Command: "config validate",
				Global: cli.Global{Config: logSettings, LogFormat: logging.FormatText,
					LogFormatSet: true}}, env: cli.Env{Stderr: fromFlag,
				Logger: logging.New(fromFlag, slog.LevelInfo, logging.FormatText)}},
			want:       0,
			stderr:     fromFlag,
			wantRecord: `^time=\S+ level=ERROR msg=interrupted`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := handle(tt.args.ctx, tt.args.inv, tt.args.env); got != tt.want {
				t.Errorf("handle() = %v, want %v", got, tt.want)
			}
			tt.args.env.Logger.Error("interrupted", "error", context.Canceled)
			lines := strings.Split(strings.TrimSuffix(tt.stderr.String(), "\n"), "\n")
			if last := lines[len(lines)-1]; !regexp.MustCompile(tt.wantRecord).MatchString(last) {
				t.Errorf("handle() caller record %q, want a match for %q", last, tt.wantRecord)
			}
		})
	}
}
