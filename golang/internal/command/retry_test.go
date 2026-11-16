// cspell:ignore doub

package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// retryVariables are the variables that override global.retry.
var retryVariables = []string{"MAX_RETRIES_TIME", "BACKOFF_INITIAL", "BACKOFF_STEP",
	"BACKOFF_STEP_ATTEMPTS", "BACKOFF_CAP"}

// setRetryEnv sets the variables of global.retry to their values in env, the others to
// the empty string, which counts as unset.
func setRetryEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, name := range retryVariables {
		t.Setenv(name, env[name])
	}
}

// writeRetryConfig writes content to main.yaml in a temporary directory and returns
// its absolute path.
func writeRetryConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// retryStorage is a valid storage section; without targets the configuration has the
// one warning "no targets configured".
const retryStorage = "storage:\n  url: 'b2://bucket'\n  snapshot_id: main\n" +
	"  password: 'env:MEGA_DOUB_X'\n"

func Test_handle_retryRecords(t *testing.T) {
	file := writeRetryConfig(t, "---\nglobal:\n  log_level: debug\n"+retryStorage)
	// tail are the records of Validate and of the command after the overrides.
	tail := []string{
		`level=WARN msg="configuration warning" file=` + slogValue(t, file) +
			` line=1 column=1 path=targets warning="no targets configured"`,
		`level=INFO msg="configuration is valid" config=` + slogValue(t, file),
	}
	tests := []struct {
		name string
		env  map[string]string
		code int
		want []string
	}{
		{name: "no variables", want: tail},
		{
			// The value is logged as written, the records follow the order of the
			// variables and go before the records of Validate.
			name: "variables",
			env: map[string]string{"BACKOFF_STEP": "2s", "MAX_RETRIES_TIME": "0s",
				"BACKOFF_CAP": "1m"},
			want: append([]string{
				`level=DEBUG msg="retry option from environment" ` +
					`variable=MAX_RETRIES_TIME value=0s`,
				`level=DEBUG msg="retry option from environment" ` +
					`variable=BACKOFF_STEP value=2s`,
				`level=DEBUG msg="retry option from environment" ` +
					`variable=BACKOFF_CAP value=1m`,
			}, tail...),
		},
		{
			// The file is not validated and the valid override is not logged: the
			// only record is the problem of the variable, without a position.
			name: "invalid variable",
			env:  map[string]string{"MAX_RETRIES_TIME": "bogus", "BACKOFF_STEP": "2s"},
			code: 2,
			want: []string{`level=ERROR msg="invalid configuration" ` +
				`file="<env MAX_RETRIES_TIME>" path=global\.retry\.max_retries_time ` +
				`error="MAX_RETRIES_TIME: invalid duration .+"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRetryEnv(t, tt.env)
			code, _, stderr := execute(t, Deps{}, "config", "validate", "-c", file)
			if code != tt.code {
				t.Errorf("cli.Execute() = %d, want %d, stderr %q", code, tt.code, stderr)
			}
			matchRecords(t, textRecords(stderr), tt.want)
		})
	}
}

func Test_handle_retryShow(t *testing.T) {
	file := writeRetryConfig(t, "---\nglobal:\n  retry:\n    max_retries_time: 2m\n"+
		"    backoff_initial: 2s\n    backoff_step: 3s\n    backoff_step_attempts: 5\n"+
		"    backoff_cap: 30s\n"+retryStorage)
	show := func(path string) string {
		t.Helper()
		code, stdout, stderr := execute(t, Deps{}, "config", "show", "-c", path)
		if code != 0 {
			t.Fatalf("cli.Execute() = %d, want 0, stderr %q", code, stderr)
		}
		return stdout
	}
	setRetryEnv(t, nil)
	plain := show(file)
	if !strings.Contains(plain, "max_retries_time: 2m0s") {
		t.Fatalf("config show = %q, want max_retries_time: 2m0s", plain)
	}
	// The output shows the values of the file, not the overrides, and loads again with
	// the same variables set.
	setRetryEnv(t, map[string]string{"MAX_RETRIES_TIME": "5m", "BACKOFF_INITIAL": "500ms",
		"BACKOFF_STEP": "1s", "BACKOFF_STEP_ATTEMPTS": "7", "BACKOFF_CAP": "1m"})
	overridden := show(file)
	if overridden != plain {
		t.Errorf("config show with the variables = %q, want %q", overridden, plain)
	}
	again := show(writeRetryConfig(t, overridden))
	if again != overridden {
		t.Errorf("config show of its output = %q, want %q", again, overridden)
	}
}
