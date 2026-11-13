package cli

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/megalomania428/mega-doub/internal/project"
	"github.com/megalomania428/mega-doub/pkg/logging"
	"github.com/megalomania428/mega-doub/pkg/model"
)

// lateCancel reports cancellation from the second Err call on, as if the context
// was canceled while the arguments were being parsed.
type lateCancel struct {
	context.Context
	calls int
}

func (c *lateCancel) Err() error {
	c.calls++
	if c.calls > 1 {
		return context.Canceled
	}
	return nil
}

func TestExecute(t *testing.T) {
	type args struct {
		ctx  context.Context
		args []string
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	defaults := Global{
		Config:    project.DefaultConfigPath,
		LogLevel:  slog.LevelInfo,
		LogFormat: logging.FormatText,
	}
	invocation := func(command string) *Invocation {
		return &Invocation{Command: command, Global: defaults}
	}
	moved := &Invocation{Command: "backup", Global: Global{
		Config:      "/srv/main.yaml",
		LogLevel:    slog.LevelDebug,
		LogFormat:   logging.FormatText,
		DryRun:      true,
		LogLevelSet: true,
		DryRunSet:   true,
	}}
	invalid := func(text string) []string {
		return []string{`level=ERROR msg="invalid invocation"`, text,
			`hint="run 'mega-doub --help'"`}
	}
	rootHelp := []string{"Usage:", "mega-doub [command]", "Available Commands:"}
	tests := []struct {
		name       string
		args       args
		cancel     bool
		want       int
		wantInv    *Invocation
		wantStdout []string
		wantStderr []string
		records    int
	}{
		{name: "long help flag", args: args{args: []string{"--help"}}, wantStdout: rootHelp},
		{name: "short help flag", args: args{args: []string{"-h"}}, wantStdout: rootHelp},
		{name: "help command", args: args{args: []string{"help"}}, wantStdout: rootHelp},
		{
			name:       "help for a command",
			args:       args{args: []string{"help", "config", "validate"}},
			wantStdout: []string{"Usage:", "mega-doub config validate [flags]"},
		},
		{
			name: "help config",
			args: args{args: []string{"help", "config"}},
			wantStdout: []string{
				"Usage:", "mega-doub config [command]",
				"Show the effective configuration of the file without the retry overrides",
			},
		},
		{
			name:       "help flag before a command",
			args:       args{args: []string{"--help", "backup"}},
			wantStdout: []string{"Usage:", "mega-doub backup [flags]"},
		},
		{
			name:       "help flag on the help command",
			args:       args{args: []string{"help", "--help", "backup"}},
			wantStdout: []string{"Usage:", "mega-doub backup [flags]"},
		},
		{
			name:       "help flag after a help topic",
			args:       args{args: []string{"help", "backup", "-h"}},
			wantStdout: []string{"Usage:", "mega-doub backup [flags]"},
		},
		{
			name:       "help before a flag with a value",
			args:       args{args: []string{"--help", "-c", "/srv/main.yaml", "backup"}},
			wantStdout: []string{"Usage:", "mega-doub backup [flags]"},
		},
		{
			name:       "version command",
			args:       args{args: []string{"version"}},
			wantStdout: []string{"mega-doub 1.2.3 (abc123)\n"},
		},
		{
			name:       "version flag",
			args:       args{args: []string{"--version", "-c", "/srv/main.yaml"}},
			wantStdout: []string{"mega-doub 1.2.3 (abc123)\n"},
		},
		{
			name:       "backup",
			args:       args{args: []string{"backup"}},
			want:       model.ExitStorage,
			wantInv:    invocation("backup"),
			wantStderr: []string{"level=INFO msg=handled command=backup"},
			records:    1,
		},
		{
			name:    "prune",
			args:    args{args: []string{"prune"}},
			want:    model.ExitStorage,
			wantInv: invocation("prune"),
			records: 1,
		},
		{
			name: "check with flags",
			args: args{args: []string{"check", "--files", "--chunks"}},
			want: model.ExitStorage,
			wantInv: &Invocation{
				Command: "check",
				Global:  defaults,
				Check:   Check{Files: true, FilesSet: true, Chunks: true, ChunksSet: true},
			},
			records: 1,
		},
		{
			name: "verify with flags",
			args: args{args: []string{"verify", "--revision", "7", "--bytes", "256M"}},
			want: model.ExitStorage,
			wantInv: &Invocation{
				Command: "verify",
				Global:  defaults,
				Verify:  Verify{Revision: 7, Bytes: 256 << 20, BytesSet: true},
			},
			records: 1,
		},
		{
			name:    "list with revision",
			args:    args{args: []string{"list", "--revision=3"}},
			want:    model.ExitStorage,
			wantInv: &Invocation{Command: "list", Global: defaults, List: List{Revision: 3}},
			records: 1,
		},
		{
			name:    "config validate",
			args:    args{args: []string{"config", "validate"}},
			want:    model.ExitStorage,
			wantInv: invocation("config validate"),
			records: 1,
		},
		{
			name:    "config show",
			args:    args{args: []string{"config", "show"}},
			want:    model.ExitStorage,
			wantInv: invocation("config show"),
			records: 1,
		},
		{
			name: "global flags before the command",
			args: args{args: []string{
				"--dry-run", "--log-level", "debug", "-c", "/srv/main.yaml", "backup",
			}},
			want:    model.ExitStorage,
			wantInv: moved,
			records: 2,
		},
		{
			name: "global flags after the command",
			args: args{args: []string{
				"backup", "--dry-run", "--log-level=debug", "--config", "/srv/main.yaml",
			}},
			want:    model.ExitStorage,
			wantInv: moved,
			records: 2,
		},
		{
			name: "explicit default values",
			args: args{args: []string{
				"--log-level", "info", "--log-format", "text", "--no-progress", "backup",
			}},
			want: model.ExitStorage,
			wantInv: &Invocation{Command: "backup", Global: Global{
				Config:        project.DefaultConfigPath,
				LogLevel:      slog.LevelInfo,
				LogFormat:     logging.FormatText,
				NoProgress:    true,
				LogLevelSet:   true,
				LogFormatSet:  true,
				NoProgressSet: true,
			}},
			records: 1,
		},
		{
			name: "json log format",
			args: args{args: []string{"--log-format", "json", "prune"}},
			want: model.ExitStorage,
			wantInv: &Invocation{Command: "prune", Global: Global{
				Config:       project.DefaultConfigPath,
				LogLevel:     slog.LevelInfo,
				LogFormat:    logging.FormatJSON,
				LogFormatSet: true,
			}},
			wantStderr: []string{`"level":"INFO","msg":"handled","command":"prune"}`},
			records:    1,
		},
		{
			name:       "negative verify revision",
			args:       args{args: []string{"verify", "--revision", "-1"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`invalid argument \"-1\" for \"--revision\" flag`),
			records:    1,
		},
		{
			name:       "negative list revision",
			args:       args{args: []string{"list", "--revision=-2"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`invalid argument \"-2\" for \"--revision\" flag`),
			records:    1,
		},
		{
			name:       "invalid size",
			args:       args{args: []string{"verify", "--bytes", "1.5G"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`invalid argument \"1.5G\" for \"--bytes\" flag`),
			records:    1,
		},
		{
			name:       "unknown command",
			args:       args{args: []string{"bogus"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`error="unknown command \"bogus\" for \"mega-doub\""`),
			records:    1,
		},
		{
			name:       "unknown help topic",
			args:       args{args: []string{"help", "bogus"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`error="unknown command \"bogus\" for \"mega-doub\""`),
			records:    1,
		},
		{
			name:       "unknown help topic with a help flag",
			args:       args{args: []string{"help", "-h", "bogus"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`error="unknown command \"bogus\" for \"mega-doub\""`),
			records:    1,
		},
		{
			name: "extra help topic argument with a help flag",
			args: args{args: []string{"help", "backup", "--help", "extra"}},
			want: model.ExitConfig,
			wantStderr: invalid(
				`error="unknown command \"extra\" for \"mega-doub backup\""`),
			records: 1,
		},
		{
			name:       "unknown config command",
			args:       args{args: []string{"config", "bogus"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`error="unknown command \"bogus\" for \"mega-doub config\""`),
			records:    1,
		},
		{
			name:       "unknown flag",
			args:       args{args: []string{"backup", "--bogus"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`error="unknown flag: --bogus"`),
			records:    1,
		},
		{
			name:       "extra argument after help",
			args:       args{args: []string{"--help", "extra"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`error="unknown command \"extra\" for \"mega-doub\""`),
			records:    1,
		},
		{
			name: "extra argument after command help",
			args: args{args: []string{"backup", "-h", "extra"}},
			want: model.ExitConfig,
			wantStderr: invalid(
				`error="unknown command \"extra\" for \"mega-doub backup\""`),
			records: 1,
		},
		{
			name:       "extra argument after version",
			args:       args{args: []string{"--version", "extra"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`error="unknown command \"extra\" for \"mega-doub\""`),
			records:    1,
		},
		{
			name:       "help with an invalid flag value",
			args:       args{args: []string{"--help", "--log-level", "trace"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`invalid argument \"trace\" for \"--log-level\" flag`),
			records:    1,
		},
		{
			name:       "version with an unknown flag",
			args:       args{args: []string{"--version", "--bogus"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`error="unknown flag: --bogus"`),
			records:    1,
		},
		{
			name:       "version flag after a command",
			args:       args{args: []string{"backup", "--version"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`error="unknown flag: --version"`),
			records:    1,
		},
		{
			name:       "version flag before a command",
			args:       args{args: []string{"--version", "backup"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`error="unknown flag: --version"`),
			records:    1,
		},
		{
			name:       "short version flag",
			args:       args{args: []string{"-v"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`unknown shorthand flag: 'v' in -v`),
			records:    1,
		},
		{
			name: "extra argument",
			args: args{args: []string{"backup", "extra"}},
			want: model.ExitConfig,
			wantStderr: invalid(
				`error="unknown command \"extra\" for \"mega-doub backup\""`),
			records: 1,
		},
		{
			name:       "invalid log level",
			args:       args{args: []string{"--log-level", "trace", "backup"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`invalid argument \"trace\" for \"--log-level\" flag`),
			records:    1,
		},
		{
			name:       "invalid log format",
			args:       args{args: []string{"backup", "--log-format", "yaml"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`invalid argument \"yaml\" for \"--log-format\" flag`),
			records:    1,
		},
		{
			name:       "no command",
			args:       args{args: []string{}},
			want:       model.ExitConfig,
			wantStderr: invalid(`error="command is required"`),
			records:    1,
		},
		{
			name:       "config without a command",
			args:       args{args: []string{"config"}},
			want:       model.ExitConfig,
			wantStderr: invalid(`error="command is required"`),
			records:    1,
		},
		{
			name:       "canceled before a command",
			args:       args{ctx: canceled, args: []string{"backup"}},
			want:       model.ExitInterrupted,
			wantStderr: []string{`level=ERROR msg=interrupted error="context canceled"`},
			records:    1,
		},
		{
			name:       "canceled before help",
			args:       args{ctx: canceled, args: []string{"--help"}},
			want:       model.ExitInterrupted,
			wantStderr: []string{`level=ERROR msg=interrupted error="context canceled"`},
			records:    1,
		},
		{
			name:       "canceled before an invalid flag",
			args:       args{ctx: canceled, args: []string{"backup", "--bogus"}},
			want:       model.ExitInterrupted,
			wantStderr: []string{`level=ERROR msg=interrupted error="context canceled"`},
			records:    1,
		},
		{
			name:       "canceled while parsing an invalid invocation",
			args:       args{ctx: &lateCancel{Context: t.Context()}, args: []string{"bogus"}},
			want:       model.ExitInterrupted,
			wantStderr: []string{`level=ERROR msg=interrupted error="context canceled"`},
			records:    1,
		},
		{
			name:    "canceled during a command",
			args:    args{args: []string{"backup"}},
			cancel:  true,
			want:    model.ExitInterrupted,
			wantInv: invocation("backup"),
			wantStderr: []string{
				"level=INFO msg=handled command=backup",
				`level=ERROR msg=interrupted error="context canceled"`,
			},
			records: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			if tt.args.ctx != nil {
				ctx = tt.args.ctx
			}
			var gotInv *Invocation
			handler := func(_ context.Context, inv Invocation, env Env) int {
				gotInv = &inv
				env.Logger.Debug("handled at debug level")
				env.Logger.Info("handled", "command", inv.Command)
				if tt.cancel {
					stop()
				}
				return model.ExitStorage
			}
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			build := Build{Version: "1.2.3", Commit: "abc123"}
			if got := Execute(ctx, tt.args.args, stdout, stderr, build, handler); got != tt.want {
				t.Errorf("Execute() = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(gotInv, tt.wantInv) {
				t.Errorf("Execute() invocation = %+v, want %+v", gotInv, tt.wantInv)
			}
			gotStdout, gotStderr := stdout.String(), stderr.String()
			if tt.wantStdout == nil && gotStdout != "" {
				t.Errorf("Execute() stdout = %q, want empty", gotStdout)
			}
			for _, sub := range tt.wantStdout {
				if !strings.Contains(gotStdout, sub) {
					t.Errorf("Execute() stdout %q does not contain %q", gotStdout, sub)
				}
			}
			for _, sub := range tt.wantStderr {
				if !strings.Contains(gotStderr, sub) {
					t.Errorf("Execute() stderr %q does not contain %q", gotStderr, sub)
				}
			}
			if strings.Contains(gotStderr, "Usage:") {
				t.Errorf("Execute() stderr %q contains usage", gotStderr)
			}
			if n := strings.Count(gotStderr, "\n"); n != tt.records {
				t.Errorf("Execute() stderr has %d records, want %d: %q", n, tt.records, gotStderr)
			}
		})
	}
}
