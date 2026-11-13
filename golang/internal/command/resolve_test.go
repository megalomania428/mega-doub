// cspell:ignore doub

package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/megalomania428/mega-doub/internal/cli"
	"github.com/megalomania428/mega-doub/pkg/config"
	"github.com/megalomania428/mega-doub/pkg/secret"
)

// resolvedOne is the pattern of the record of a run with one secret reference.
const resolvedOne = `level=INFO msg="secrets resolved" count=1`

// resolverFunc is a secret.Resolver made of a function.
type resolverFunc func(ctx context.Context, ref secret.Ref) (secret.Value, error)

// Resolve calls f.
func (f resolverFunc) Resolve(ctx context.Context, ref secret.Ref) (secret.Value, error) {
	return f(ctx, ref)
}

// depsOf returns the dependencies with f as the resolver of every run.
func depsOf(f resolverFunc) Deps {
	return Deps{NewResolver: func(secret.VaultOptions) secret.Resolver { return f }}
}

// stubDeps returns the dependencies with a resolver that gives every reference the
// value fake-stub-value.
func stubDeps() Deps {
	return depsOf(func(context.Context, secret.Ref) (secret.Value, error) {
		return secret.NewValue("fake-stub-value"), nil
	})
}

// execute runs cli.Execute on args with the handler of deps and returns the exit code,
// stdout and stderr.
func execute(t *testing.T, deps Deps, args ...string) (int, string, string) {
	t.Helper()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := cli.Execute(t.Context(), args, stdout, stderr, cli.Build{}, New(deps))
	return code, stdout.String(), stderr.String()
}

// unsetEnv removes the variables for the test and restores them after it.
func unsetEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

// resolveConfig is a valid configuration without warnings with three references in
// storage, two of them through one alias, which gives them the position of the anchor.
const resolveConfig = "---\nstorage:\n  url: 'b2://resolve-bucket/prefix'\n" +
	"  snapshot_id: resolve-main\n  password: 'env:MEGA_DOUB_RESOLVE_A'\n  keys:\n" +
	"    b2_key: &unset 'env:MEGA_DOUB_RESOLVE_B'\n    b2_id: *unset\n" +
	"targets:\n  - name: local\n    transport: local\n" +
	"    sources: [{type: test, id: data, path: /srv/data}]\n"

func Test_handle_resolveCanary(t *testing.T) {
	const (
		envCanary  = "fake-canary-0c1d2e"
		fileCanary = "fake-canary-5f6a7b"
	)
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	empty := filepath.Join(dir, "empty")
	resolved := filepath.Join(dir, "resolved.yaml")
	failed := filepath.Join(dir, "failed.yaml")
	// canaryConfig returns a configuration with the password in the storage URL, the
	// value of the password from the environment and the value of a key from the file.
	canaryConfig := func(file string) string {
		return "---\nstorage:\n  url: 'sftp://u:p%40ss@h.example.invalid/x'\n" +
			"  snapshot_id: canary-main\n  password: 'env:MEGA_DOUB_CANARY_ENV'\n" +
			"  keys:\n    b2_key: 'file:" + file + "'\ntargets:\n  - name: local\n" +
			"    transport: local\n    sources: [{type: test, id: data, path: /srv/data}]\n"
	}
	writeFiles(t, map[string]string{
		key:      " " + fileCanary + "\r\n",
		empty:    "",
		resolved: canaryConfig(key),
		failed:   canaryConfig(empty),
	})
	t.Setenv("MEGA_DOUB_CANARY_ENV", envCanary)
	tests := []struct {
		name string
		path string
		want int
		// wantStderr is the text that stderr holds.
		wantStderr []string
	}{
		{name: "resolved", path: resolved, want: 0, wantStderr: []string{"secrets resolved"}},
		{
			name:       "empty file",
			path:       failed,
			want:       2,
			wantStderr: []string{"secret resolution failed", "storage.keys.b2_key", empty},
		},
	}
	canaries := []string{envCanary, fileCanary, "p%40ss", "p@ss"}
	commands := [][]string{{"config", "validate", "--resolve"}, {"backup", "--dry-run"}}
	for _, tt := range tests {
		for _, format := range []string{"text", "json"} {
			for _, command := range commands {
				name := tt.name + "/" + format + "/" + strings.Join(command, " ")
				t.Run(name, func(t *testing.T) {
					args := append([]string{"--log-format", format, "-c", tt.path}, command...)
					got, stdout, stderr := execute(t, Deps{}, args...)
					if got != tt.want {
						t.Errorf("cli.Execute() = %v, want %v: %s", got, tt.want, stderr)
					}
					if tt.want != 0 && stdout != "" {
						t.Errorf("cli.Execute() stdout = %q, want none", stdout)
					}
					for _, text := range tt.wantStderr {
						if !strings.Contains(stderr, text) {
							t.Errorf("cli.Execute() stderr = %q, want %q in it", stderr, text)
						}
					}
					for _, canary := range canaries {
						if strings.Contains(stdout, canary) || strings.Contains(stderr, canary) {
							t.Errorf("cli.Execute() output contains %q", canary)
						}
					}
				})
			}
		}
	}
}

func Test_handle_resolveErrors(t *testing.T) {
	unsetEnv(t, "MEGA_DOUB_RESOLVE_A", "MEGA_DOUB_RESOLVE_B", "MEGA_DOUB_RESOLVE_C")
	// The target comes first in the file, while its reference is the last one walked.
	file := filepath.Join(t.TempDir(), "errors.yaml")
	writeFiles(t, map[string]string{file: "---\ntargets:\n  - name: local\n" +
		"    transport: local\n    sources:\n      - type: test\n        id: data\n" +
		"        path: /srv/data\n        token: 'env:MEGA_DOUB_RESOLVE_C'\nstorage:\n" +
		"  url: 'b2://resolve-bucket/prefix'\n  snapshot_id: resolve-main\n" +
		"  password: 'env:MEGA_DOUB_RESOLVE_A'\n  keys:\n" +
		"    b2_key: &unset 'env:MEGA_DOUB_RESOLVE_B'\n    b2_id: *unset\n"})
	abs, err := filepath.Abs(file)
	if err != nil {
		t.Fatal(err)
	}
	// failed is the pattern of the record of a failed reference of an unset variable.
	failed := func(path string, line, column int, name string) string {
		return `level=ERROR msg="secret resolution failed" path=` + path +
			` kind=env file=` + slogValue(t, abs) + ` line=` + strconv.Itoa(line) +
			` column=` + strconv.Itoa(column) +
			` error="env reference: variable ` + name + ` is not set"`
	}
	// The records go by the position of the values, the alias keeps the order of walking.
	want := []string{
		failed(`targets\[0\]\.sources\[0\]\.token`, 9, 16, "MEGA_DOUB_RESOLVE_C"),
		failed(`storage\.password`, 13, 13, "MEGA_DOUB_RESOLVE_A"),
		failed(`storage\.keys\.b2_id`, 15, 13, "MEGA_DOUB_RESOLVE_B"),
		failed(`storage\.keys\.b2_key`, 15, 13, "MEGA_DOUB_RESOLVE_B"),
	}
	for _, command := range [][]string{{"config", "validate", "--resolve"},
		{"backup", "--dry-run"}, {"prune"}, {"check"}, {"verify"}, {"list"}} {
		t.Run(strings.Join(command, " "), func(t *testing.T) {
			got, stdout, stderr := execute(t, Deps{}, append([]string{"-c", file}, command...)...)
			if got != 2 || stdout != "" {
				t.Errorf("cli.Execute() = %v with stdout %q, want 2 and none", got, stdout)
			}
			matchRecords(t, textRecords(stderr), want)
		})
	}
}

func Test_handle_resolveVault(t *testing.T) {
	unsetEnv(t, "MEGA_DOUB_RESOLVE_TOKEN")
	dir := t.TempDir()
	unused := filepath.Join(dir, "unused.yaml")
	used := filepath.Join(dir, "used.yaml")
	// vaultConfig returns a configuration with an unset vault.token and the password.
	vaultConfig := func(password string) string {
		return "---\nvault:\n  address: 'https://vault.example.invalid'\n" +
			"  token: 'env:MEGA_DOUB_RESOLVE_TOKEN'\nstorage:\n" +
			"  url: 'b2://resolve-bucket/prefix'\n  snapshot_id: resolve-main\n" +
			"  password: '" + password + "'\ntargets:\n  - name: local\n" +
			"    transport: local\n    sources: [{type: test, id: data, path: /srv/data}]\n"
	}
	writeFiles(t, map[string]string{
		unused: vaultConfig("plain:fake-resolve-password"),
		used:   vaultConfig("vault:depot/mega-doub/resolve/password"),
	})
	path := func(file string) string {
		abs, err := filepath.Abs(file)
		if err != nil {
			t.Fatal(err)
		}
		return slogValue(t, abs)
	}
	tests := []struct {
		name        string
		file        string
		want        int
		wantRecords []string
	}{
		{name: "token without vault references", file: unused, want: 0,
			wantRecords: []string{resolvedOne,
				`level=INFO msg="configuration is valid" config=` + path(unused)}},
		{name: "vault reference", file: used, want: 2,
			wantRecords: []string{`level=ERROR msg="secret resolution failed" ` +
				`path=vault\.token kind=env file=` + path(used) + ` line=4 column=10 ` +
				`error="env reference: variable MEGA_DOUB_RESOLVE_TOKEN is not set"`,
				`level=ERROR msg="secret resolution failed" ` +
					`path=storage\.password kind=vault file=` + path(used) + ` line=8 column=13 ` +
					`error="vault token is unresolved"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, stderr := execute(t, Deps{}, "config", "validate", "--resolve", "-c",
				tt.file)
			if got != tt.want {
				t.Errorf("cli.Execute() = %v, want %v", got, tt.want)
			}
			matchRecords(t, textRecords(stderr), tt.wantRecords)
		})
	}
}

func Test_handle_resolveCount(t *testing.T) {
	t.Setenv("MEGA_DOUB_RESOLVE_A", "fake-resolve-a")
	t.Setenv("MEGA_DOUB_RESOLVE_B", "fake-resolve-b")
	file := filepath.Join(t.TempDir(), "count.yaml")
	writeFiles(t, map[string]string{file: resolveConfig})
	// Two keys share one reference through the alias, the count holds both.
	const (
		resolved = `level=INFO msg="secrets resolved" count=3`
		valid    = `level=INFO msg="configuration is valid" config=.+`
	)
	tests := []struct {
		name        string
		args        []string
		want        int
		wantRecords []string
	}{
		{name: "validate without resolve", args: []string{"config", "validate"},
			wantRecords: []string{valid}},
		{name: "validate with resolve", args: []string{"config", "validate", "--resolve"},
			wantRecords: []string{resolved, valid}},
		{name: "config show", args: []string{"config", "show"}},
		{name: "backup dry run", args: []string{"backup", "--dry-run"},
			wantRecords: []string{resolved, `level=INFO msg=command command=backup .+`,
				`level=INFO msg="dry run, nothing to do" command=backup`}},
		{name: "backup", args: []string{"backup"}, want: 3,
			wantRecords: []string{resolved, `level=INFO msg=command command=backup .+`,
				`level=ERROR msg="command is not implemented yet" command=backup`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, stderr := execute(t, Deps{}, append([]string{"-c", file}, tt.args...)...)
			if got != tt.want {
				t.Errorf("cli.Execute() = %v, want %v", got, tt.want)
			}
			matchRecords(t, textRecords(stderr), tt.wantRecords)
		})
	}
}

func Test_handle_resolveCanceled(t *testing.T) {
	file := filepath.Join(t.TempDir(), "canceled.yaml")
	writeFiles(t, map[string]string{file: resolveConfig})
	const interrupted = `level=ERROR msg=interrupted error="context canceled"`
	// resolveCall is the resolver of the call with the number n from 1, stop cancels the
	// run as a signal does.
	type resolveCall func(ctx context.Context, n int, stop func()) (secret.Value, error)
	tests := []struct {
		name        string
		resolve     resolveCall
		wantRefs    []secret.Ref
		wantRecords []string
	}{
		{
			name: "failure before the canceled reference",
			resolve: func(ctx context.Context, n int, stop func()) (secret.Value, error) {
				if n == 1 {
					return secret.Value{}, errors.New("fake failure")
				}
				stop()
				return secret.Value{}, ctx.Err()
			},
			wantRefs: []secret.Ref{"env:MEGA_DOUB_RESOLVE_A", "env:MEGA_DOUB_RESOLVE_B"},
			wantRecords: []string{`level=ERROR msg="secret resolution failed" ` +
				`path=storage\.password kind=env file=.+ line=5 column=13 ` +
				`error="fake failure"`, interrupted},
		},
		{
			name: "canceled during the last reference that resolves",
			resolve: func(_ context.Context, n int, stop func()) (secret.Value, error) {
				if n == 3 {
					stop()
				}
				return secret.NewValue("fake-stub-value"), nil
			},
			wantRefs: []secret.Ref{"env:MEGA_DOUB_RESOLVE_A", "env:MEGA_DOUB_RESOLVE_B",
				"env:MEGA_DOUB_RESOLVE_B"},
			wantRecords: []string{interrupted},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			var refs []secret.Ref
			deps := depsOf(func(runCtx context.Context, ref secret.Ref) (secret.Value, error) {
				refs = append(refs, ref)
				return tt.resolve(runCtx, len(refs), stop)
			})
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			args := []string{"backup", "--dry-run", "-c", file}
			if got := cli.Execute(ctx, args, stdout, stderr, cli.Build{}, New(deps)); got != 4 {
				t.Errorf("cli.Execute() = %v, want 4", got)
			}
			if stdout.Len() > 0 {
				t.Errorf("cli.Execute() stdout = %q, want none", stdout)
			}
			if !reflect.DeepEqual(refs, tt.wantRefs) {
				t.Errorf("resolver got %q, want %q", refs, tt.wantRefs)
			}
			matchRecords(t, textRecords(stderr.String()), tt.wantRecords)
		})
	}
}

func Test_handle_resolveExample(t *testing.T) {
	const example = "../../testdata/example.yaml"
	loaded, err := config.Load(example)
	if err != nil {
		t.Fatal(err)
	}
	// The variables are taken from the references of the file by the same walk.
	for _, ref := range config.Refs(loaded.Config) {
		if parsed, err := secret.Parse(ref.Ref); err == nil && parsed.Kind == secret.KindEnv {
			value := strings.ReplaceAll(strings.ToLower(parsed.Body), "_", "-")
			t.Setenv(parsed.Body, "fake-"+value)
		}
	}
	for _, args := range [][]string{{"config", "validate", "--resolve"},
		{"backup", "--dry-run"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			got, _, stderr := execute(t, Deps{}, append([]string{"-c", example}, args...)...)
			if got != 0 {
				t.Errorf("cli.Execute() = %v, want 0: %s", got, stderr)
			}
			if !strings.Contains(stderr, `msg="secrets resolved" count=4`) {
				t.Errorf("cli.Execute() stderr = %q, want secrets resolved with count=4", stderr)
			}
		})
	}
}

func Test_sortFailures(t *testing.T) {
	type args struct {
		failures []failure
	}
	tests := []struct {
		name string
		args args
		want []string
	}{
		{
			name: "by line and column, without a position last",
			args: args{failures: []failure{
				{path: "a", line: 0},
				{path: "b", line: 7, column: 13},
				{path: "c", line: 3, column: 5},
				{path: "d", line: 0},
				{path: "e", line: 7, column: 13},
				{path: "f", line: 7, column: 2},
			}},
			want: []string{"c", "f", "b", "e", "a", "d"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sortFailures(tt.args.failures)
			var got []string
			for _, f := range tt.args.failures {
				got = append(got, f.path)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("sortFailures() = %v, want %v", got, tt.want)
			}
		})
	}
}
