// cspell:ignore doub

package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/megalomania428/mega-doub/internal/cli"
	"github.com/megalomania428/mega-doub/pkg/config"
	"github.com/megalomania428/mega-doub/pkg/logging"
)

// slogValue returns the pattern of the value v of a field as the text handler of slog
// writes it, with the quotes and the escapes that it adds when needed.
func slogValue(t *testing.T, v string) string {
	t.Helper()
	buf := &bytes.Buffer{}
	drop := func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == slog.LevelKey ||
			a.Key == slog.MessageKey) {
			return slog.Attr{}
		}
		return a
	}
	slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{ReplaceAttr: drop})).Info("",
		"k", v)
	got, ok := strings.CutPrefix(strings.TrimSuffix(buf.String(), "\n"), "k=")
	if !ok {
		t.Fatalf("slog wrote %q, want a k= field", buf.String())
	}
	return regexp.QuoteMeta(got)
}

// timeField is the leading time field of a record of the text format.
var timeField = regexp.MustCompile(`^time=\S+ `)

// textRecords returns the records of the text format written to s without their time
// field.
func textRecords(s string) []string {
	var records []string
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		if line != "" {
			records = append(records, timeField.ReplaceAllString(line, ""))
		}
	}
	return records
}

// matchRecords reports the records that do not match the patterns in their order.
func matchRecords(t *testing.T, records, patterns []string) {
	t.Helper()
	if len(records) != len(patterns) {
		t.Fatalf("cli.Execute() wrote %d records, want %d: %q", len(records), len(patterns),
			records)
	}
	for i, pattern := range patterns {
		if re := regexp.MustCompile("^" + pattern + "$"); !re.MatchString(records[i]) {
			t.Errorf("cli.Execute() record %q, want a match for %q", records[i], re)
		}
	}
}

// commandConfig returns a valid configuration without warnings with the lines of
// global, prune.enabled and check.enabled set to enabled and check.files set to true.
func commandConfig(global string, enabled bool) string {
	return fmt.Sprintf("---\nglobal:\n%s"+
		"storage:\n  url: 'b2://command-bucket/prefix'\n  snapshot_id: command-main\n"+
		"  password: 'env:MEGA_DOUB_COMMAND_PASSWORD'\n"+
		"prune:\n  enabled: %[2]t\n  keep: ['1:7']\n"+
		"check:\n  enabled: %[2]t\n  files: true\n  chunks: false\n"+
		"  test_restore_bytes: 100\n"+
		"targets:\n  - name: local\n    transport: local\n"+
		"    sources: [{type: test, id: data, path: /srv/data}]\n", global, enabled)
}

// writeFiles writes the files with their content.
func writeFiles(t *testing.T, files map[string]string) {
	t.Helper()
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// stripMarkers drops the jscpd marker lines from a golden file.
func stripMarkers(golden []byte) []byte {
	var lines [][]byte
	for _, line := range bytes.Split(golden, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("# jscpd:")) {
			lines = append(lines, line)
		}
	}
	return bytes.Join(lines, []byte("\n"))
}

func Test_handle(t *testing.T) {
	type args struct {
		ctx context.Context
		inv cli.Invocation
		env cli.Env
	}
	logSettings := filepath.Join(t.TempDir(), "logging.yaml")
	if err := os.WriteFile(logSettings, []byte("---\nglobal:\n  log_format: json\n"+
		"storage:\n  url: b2://bucket/prefix\n  snapshot_id: main\n"+
		"  password: env:PASSWORD\n"), 0o600); err != nil {
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
			if got := New(Deps{})(tt.args.ctx, tt.args.inv, tt.args.env); got != tt.want {
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

func Test_handle_commands(t *testing.T) {
	dir := t.TempDir()
	// The configuration is given by a relative path, the records carry the absolute one.
	t.Chdir(dir)
	writeFiles(t, map[string]string{
		"auto.yaml":    commandConfig("  progress: auto\n", false),
		"never.yaml":   commandConfig("  progress: never\n", true),
		"debug.yaml":   commandConfig("  log_level: debug\n", false),
		"load.yaml":    "---\nglobal:\n  timeout: 30\n",
		"problem.yaml": commandConfig("  parallel_targets: 0\n", false),
	})
	path := func(file string) string {
		abs, err := filepath.Abs(file)
		if err != nil {
			t.Fatal(err)
		}
		return slogValue(t, abs)
	}
	const (
		run    = `dry_run=false log_level=info log_format=text no_progress=false`
		dryRun = `dry_run=true log_level=info log_format=text no_progress=false`
	)
	// command is the pattern of the command record of a stub on the file with the fields
	// after config.
	command := func(name, file, fields string) string {
		return `level=INFO msg=command command=` + name + ` config=` + path(file) + ` ` +
			fields
	}
	// stopped is the pattern of the last record of a stub.
	stopped := func(name string, dryRun bool) string {
		if dryRun {
			return `level=INFO msg="dry run, nothing to do" command=` + name
		}
		return `level=ERROR msg="command is not implemented yet" command=` + name
	}
	tests := []struct {
		name string
		file string
		args []string
		// cancel cancels the context after the handler, as a signal during the command.
		cancel bool
		want   int
		// show is true when stdout holds the safe configuration of the file.
		show        bool
		wantRecords []string
	}{
		{name: "backup dry run", file: "auto.yaml", args: []string{"backup", "--dry-run"},
			want: 0, show: true,
			wantRecords: []string{command("backup", "auto.yaml", dryRun),
				stopped("backup", true)}},
		{name: "backup", file: "auto.yaml", args: []string{"backup"}, want: 3, show: true,
			wantRecords: []string{command("backup", "auto.yaml", run),
				stopped("backup", false)}},
		{name: "prune dry run", file: "auto.yaml", args: []string{"prune", "--dry-run"},
			want: 0, show: true,
			wantRecords: []string{command("prune", "auto.yaml", dryRun+` enabled=false`),
				stopped("prune", true)}},
		{name: "prune not enabled", file: "auto.yaml", args: []string{"prune"}, want: 3,
			show: true,
			wantRecords: []string{command("prune", "auto.yaml", run+` enabled=false`),
				stopped("prune", false)}},
		{name: "prune enabled", file: "never.yaml",
			args: []string{"prune", "--no-progress=false"}, want: 3, show: true,
			wantRecords: []string{command("prune", "never.yaml", run+` enabled=true`),
				stopped("prune", false)}},
		{name: "check from the configuration", file: "auto.yaml", args: []string{"check"},
			want: 3, show: true,
			wantRecords: []string{command("check", "auto.yaml",
				run+` files=true chunks=false enabled=false`), stopped("check", false)}},
		{name: "check flags over the configuration", file: "auto.yaml",
			args: []string{"check", "--files=false", "--chunks"}, want: 3, show: true,
			wantRecords: []string{command("check", "auto.yaml",
				run+` files=false chunks=true enabled=false`), stopped("check", false)}},
		{name: "check enabled dry run", file: "never.yaml",
			args: []string{"check", "--dry-run", "--no-progress=false"}, want: 0, show: true,
			wantRecords: []string{command("check", "never.yaml",
				dryRun+` files=true chunks=false enabled=true`), stopped("check", true)}},
		{name: "verify bytes from the configuration", file: "auto.yaml",
			args: []string{"verify"}, want: 3, show: true,
			wantRecords: []string{command("verify", "auto.yaml", run+` revision=0 bytes=100`),
				stopped("verify", false)}},
		{name: "verify zero bytes", file: "auto.yaml",
			args: []string{"verify", "--dry-run", "--revision", "2", "--bytes", "0"}, want: 0,
			show: true,
			wantRecords: []string{command("verify", "auto.yaml", dryRun+` revision=2 bytes=0`),
				stopped("verify", true)}},
		{name: "list", file: "auto.yaml", args: []string{"list", "--revision", "3"}, want: 3,
			show: true,
			wantRecords: []string{command("list", "auto.yaml", run+` revision=3`),
				stopped("list", false)}},
		{name: "list dry run", file: "auto.yaml", args: []string{"list", "--dry-run"},
			want: 0, show: true,
			wantRecords: []string{command("list", "auto.yaml", dryRun+` revision=0`),
				stopped("list", true)}},
		{name: "no progress by the flag", file: "auto.yaml",
			args: []string{"backup", "--dry-run", "--no-progress"}, want: 0, show: true,
			wantRecords: []string{command("backup", "auto.yaml",
				`dry_run=true log_level=info log_format=text no_progress=true`),
				stopped("backup", true)}},
		{name: "no progress by the configuration", file: "never.yaml",
			args: []string{"backup", "--dry-run"}, want: 0, show: true,
			wantRecords: []string{command("backup", "never.yaml",
				`dry_run=true log_level=info log_format=text no_progress=true`),
				stopped("backup", true)}},
		{name: "log level from the configuration", file: "debug.yaml",
			args: []string{"backup", "--dry-run"}, want: 0, show: true,
			wantRecords: []string{command("backup", "debug.yaml",
				`dry_run=true log_level=debug log_format=text no_progress=false`),
				stopped("backup", true)}},
		{name: "config validate", file: "auto.yaml", args: []string{"config", "validate"},
			want: 0,
			wantRecords: []string{`level=INFO msg="configuration is valid" config=` +
				path("auto.yaml")}},
		{name: "config show", file: "auto.yaml", args: []string{"config", "show"}, want: 0,
			show: true},
		{name: "config show of a file that fails to load", file: "load.yaml",
			args: []string{"config", "show"}, want: 2,
			wantRecords: []string{`level=ERROR msg="invalid configuration" file=` +
				path("load.yaml") + ` line=3 column=12 path=global\.timeout ` +
				`error="invalid duration: want a number with a unit, such as 30s"`}},
		{name: "config show of an invalid file", file: "problem.yaml",
			args: []string{"config", "show"}, want: 2,
			wantRecords: []string{`level=ERROR msg="invalid configuration" file=` +
				path("problem.yaml") + ` line=3 column=21 path=global\.parallel_targets ` +
				`error=.+`}},
		{name: "backup of an invalid file", file: "problem.yaml",
			args: []string{"backup", "--dry-run"}, want: 2,
			wantRecords: []string{`level=ERROR msg="invalid configuration" file=` +
				path("problem.yaml") + ` line=3 column=21 path=global\.parallel_targets ` +
				`error=.+`}},
		{name: "backup dry run and then canceled", file: "auto.yaml",
			args: []string{"backup", "--dry-run"}, cancel: true, want: 4, show: true,
			wantRecords: []string{command("backup", "auto.yaml", dryRun),
				stopped("backup", true), `level=ERROR msg=interrupted error="context canceled"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			handler := func(ctx context.Context, inv cli.Invocation, env cli.Env) int {
				code := New(Deps{})(ctx, inv, env)
				if tt.cancel {
					stop()
				}
				return code
			}
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			args := append([]string{"-c", tt.file}, tt.args...)
			got := cli.Execute(ctx, args, stdout, stderr, cli.Build{}, handler)
			if got != tt.want {
				t.Errorf("cli.Execute() = %v, want %v", got, tt.want)
			}
			var want []byte
			if tt.show {
				loaded, err := config.Load(tt.file)
				if err != nil {
					t.Fatal(err)
				}
				if want, err = config.Show(loaded.Config); err != nil {
					t.Fatal(err)
				}
			}
			if got := stdout.Bytes(); !bytes.Equal(got, want) {
				t.Errorf("cli.Execute() stdout = %q, want %q", got, want)
			}
			matchRecords(t, textRecords(stderr.String()), tt.wantRecords)
		})
	}
}

func Test_handle_show(t *testing.T) {
	const full = "../../testdata/full.yaml"
	golden, err := os.ReadFile("../../testdata/golden/show-full.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := stripMarkers(golden)
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "config show", args: []string{"config", "show"}, want: 0},
		{name: "backup dry run", args: []string{"backup", "--dry-run"}, want: 0},
		{name: "backup", args: []string{"backup"}, want: 3},
		{name: "prune", args: []string{"prune"}, want: 3},
		{name: "check", args: []string{"check"}, want: 3},
		{name: "verify", args: []string{"verify"}, want: 3},
		{name: "list", args: []string{"list"}, want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			args := append([]string{"-c", full}, tt.args...)
			if got := cli.Execute(t.Context(), args, stdout, stderr, cli.Build{},
				New(Deps{})); got != tt.want {
				t.Errorf("cli.Execute() = %v, want %v", got, tt.want)
			}
			if got := stdout.Bytes(); !bytes.Equal(got, want) {
				t.Errorf("cli.Execute() stdout = %s, want %s", got, want)
			}
		})
	}
}

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("stdout is closed")
}

func Test_handle_output(t *testing.T) {
	file := filepath.Join(t.TempDir(), "output.yaml")
	writeFiles(t, map[string]string{file: commandConfig("  progress: auto\n", false)})
	const failed = `level=ERROR msg="output write failed" error="stdout is closed"`
	tests := []struct {
		name        string
		args        []string
		wantRecords []string
	}{
		{name: "config show", args: []string{"config", "show"},
			wantRecords: []string{failed}},
		{name: "backup", args: []string{"backup"},
			wantRecords: []string{`level=INFO msg=command command=backup .+`, failed}},
		{name: "backup dry run", args: []string{"backup", "--dry-run"},
			wantRecords: []string{`level=INFO msg=command command=backup .+`, failed}},
		{name: "verify", args: []string{"verify"},
			wantRecords: []string{`level=INFO msg=command command=verify .+`, failed}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stderr := &bytes.Buffer{}
			args := append([]string{"-c", file}, tt.args...)
			if got := cli.Execute(t.Context(), args, failingWriter{}, stderr, cli.Build{},
				New(Deps{})); got != 2 {
				t.Errorf("cli.Execute() = %v, want 2", got)
			}
			matchRecords(t, textRecords(stderr.String()), tt.wantRecords)
		})
	}
}

func Test_handle_canary(t *testing.T) {
	const full = "../../testdata/full.yaml"
	dir := t.TempDir()
	escaped := filepath.Join(dir, "escaped.yaml")
	vault := filepath.Join(dir, "vault.yaml")
	unparsed := filepath.Join(dir, "unparsed.yaml")
	// valid holds the lines after storage.url of a configuration that passes the
	// validation.
	const valid = "  snapshot_id: canary-main\n" +
		"  password: 'env:MEGA_DOUB_CANARY_PASSWORD'\ntargets:\n  - name: local\n" +
		"    transport: local\n    sources: [{type: test, id: data, path: /srv/data}]\n"
	vaultConfig := "---\nvault:\n" +
		"  address: 'https://u:fake-canary-vault@vault.example.invalid'\n" +
		"  token: 'env:MEGA_DOUB_CANARY_TOKEN'\n" +
		"storage:\n  url: 'b2://canary-bucket/prefix'\n" + valid
	writeFiles(t, map[string]string{
		escaped:  "---\nstorage:\n  url: 'sftp://u:p%40ss@h.example.invalid/x'\n" + valid,
		vault:    vaultConfig,
		unparsed: "---\nstorage:\n  url: 'sftp://u:fake-canary-url@h:bad/'\n" + valid,
	})
	commands := [][]string{{"backup"}, {"backup", "--dry-run"}, {"prune"}, {"check"},
		{"verify"}, {"list"}, {"config", "validate"}, {"config", "show"}}
	tests := []struct {
		name string
		path string
		// invalid is true when the file fails the validation.
		invalid bool
		// canaries are the secret values that never appear in the output.
		canaries []string
	}{
		{name: "full", path: full,
			canaries: []string{"fake-b2-key", "fake-password", "fake-url-password",
				"fake-source-token"}},
		{name: "escaped password in the storage URL", path: escaped,
			canaries: []string{"p%40ss", "p@ss"}},
		{name: "password in the vault address", path: vault, invalid: true,
			canaries: []string{"fake-canary-vault"}},
		{name: "storage URL that fails to parse", path: unparsed, invalid: true,
			canaries: []string{"fake-canary-url"}},
	}
	for _, tt := range tests {
		for _, format := range []string{"text", "json"} {
			for _, command := range commands {
				name := tt.name + "/" + format + "/" + strings.Join(command, " ")
				t.Run(name, func(t *testing.T) {
					stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
					args := append([]string{"--log-format", format, "-c", tt.path}, command...)
					got := cli.Execute(t.Context(), args, stdout, stderr, cli.Build{},
						New(Deps{}))
					if tt.invalid && (got != 2 || stdout.Len() > 0) {
						t.Errorf("cli.Execute() = %v with stdout %q, want 2 and no output", got,
							stdout)
					}
					shows := strings.Join(command, " ") != "config validate"
					if !tt.invalid && shows && stdout.Len() == 0 {
						t.Errorf("cli.Execute() = %v without the configuration in stdout", got)
					}
					for _, canary := range tt.canaries {
						if strings.Contains(stdout.String(), canary) ||
							strings.Contains(stderr.String(), canary) {
							t.Errorf("cli.Execute() output contains %q", canary)
						}
					}
				})
			}
		}
	}
}

func Test_handle_outputTooLarge(t *testing.T) {
	// The file is below the limit of 1048576 bytes, the defaults filled in make the
	// output larger.
	var b strings.Builder
	b.WriteString("---\nstorage:\n  url: b2://bucket/prefix\n  snapshot_id: main\n" +
		"  password: env:PASSWORD\ntargets:\n")
	for i := range 5000 {
		fmt.Fprintf(&b, "  - {name: host%d, transport: ssh, host: h%d.example.invalid, "+
			"user: backup, known_hosts: /etc/ssh/ssh_known_hosts}\n", i, i)
	}
	file := filepath.Join(t.TempDir(), "large.yaml")
	writeFiles(t, map[string]string{file: b.String()})
	if b.Len() >= 1<<20 {
		t.Fatalf("file is %d bytes, want less than the limit", b.Len())
	}
	loaded, err := config.Load(file)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if out, err := config.Show(loaded.Config); err == nil {
		t.Fatalf("config.Show() = %d bytes, want an error", len(out))
	}
	const failed = `level=ERROR msg="output write failed" error="output is larger ` +
		`than 1048576 bytes"`
	command := `level=INFO msg=command command=backup .+`
	tests := []struct {
		name        string
		args        []string
		wantRecords []string
	}{
		{name: "config show", args: []string{"config", "show"},
			wantRecords: []string{failed}},
		{name: "backup dry run", args: []string{"backup", "--dry-run"},
			wantRecords: []string{command, failed}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			args := append([]string{"-c", file}, tt.args...)
			if got := cli.Execute(t.Context(), args, stdout, stderr, cli.Build{},
				New(Deps{})); got != 2 {
				t.Errorf("cli.Execute() = %v, want 2", got)
			}
			if stdout.Len() != 0 {
				t.Errorf("cli.Execute() stdout = %d bytes, want none", stdout.Len())
			}
			// Every target has no sources: the warnings are not of interest here.
			var records []string
			for _, record := range textRecords(stderr.String()) {
				if !strings.HasPrefix(record, "level=WARN ") {
					records = append(records, record)
				}
			}
			matchRecords(t, records, tt.wantRecords)
		})
	}
}
