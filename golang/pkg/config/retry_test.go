package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	ci "github.com/megalomania428/go-lib-ci"

	"github.com/megalomania428/mega-doub/pkg/config"
	"github.com/megalomania428/mega-doub/pkg/retry"
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

// retryOf returns the values of the retry section as retry.Options.
func retryOf(r config.Retry) retry.Options {
	return retry.Options{
		MaxRetriesTime:      time.Duration(r.MaxRetriesTime),
		BackoffInitial:      time.Duration(r.BackoffInitial),
		BackoffStep:         time.Duration(r.BackoffStep),
		BackoffStepAttempts: r.BackoffStepAttempts,
		BackoffCap:          time.Duration(r.BackoffCap),
	}
}

func TestLoad_retryDefaults(t *testing.T) {
	setRetryEnv(t, nil)
	loaded, err := config.Load(writeRetryConfig(t, "---\n{}\n"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := retry.Options{MaxRetriesTime: ci.DefaultMaxRetriesTime,
		BackoffInitial: ci.DefaultBackoffInitial, BackoffStep: ci.DefaultBackoffStep,
		BackoffStepAttempts: ci.DefaultBackoffStepAttempts, BackoffCap: ci.DefaultBackoffCap}
	if got := retryOf(loaded.Config.Global.Retry); got != want {
		t.Errorf("Load() global.retry = %+v, want %+v", got, want)
	}
	if loaded.Retry != want || loaded.RetryEnv != nil {
		t.Errorf("Load() Retry = %+v, RetryEnv = %v, want %+v and nil", loaded.Retry,
			loaded.RetryEnv, want)
	}
}

func TestLoad_retryOverrides(t *testing.T) {
	setRetryEnv(t, map[string]string{"BACKOFF_CAP": "1m", "BACKOFF_STEP_ATTEMPTS": "7",
		"BACKOFF_STEP": "1s", "BACKOFF_INITIAL": "500ms", "MAX_RETRIES_TIME": "0s"})
	loaded, err := config.Load(writeRetryConfig(t, "---\nglobal:\n  retry:\n"+
		"    max_retries_time: 2m\n    backoff_initial: 2s\n    backoff_step: 3s\n"+
		"    backoff_step_attempts: 5\n    backoff_cap: 30s\n"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	file := retry.Options{MaxRetriesTime: 2 * time.Minute, BackoffInitial: 2 * time.Second,
		BackoffStep: 3 * time.Second, BackoffStepAttempts: 5, BackoffCap: 30 * time.Second}
	if got := retryOf(loaded.Config.Global.Retry); got != file {
		t.Errorf("Load() global.retry = %+v, want the values of the file %+v", got, file)
	}
	want := retry.Options{BackoffInitial: 500 * time.Millisecond, BackoffStep: time.Second,
		BackoffStepAttempts: 7, BackoffCap: time.Minute}
	if loaded.Retry != want {
		t.Errorf("Load() Retry = %+v, want %+v", loaded.Retry, want)
	}
	wantEnv := []retry.Override{{Variable: "MAX_RETRIES_TIME", Value: "0s"},
		{Variable: "BACKOFF_INITIAL", Value: "500ms"}, {Variable: "BACKOFF_STEP", Value: "1s"},
		{Variable: "BACKOFF_STEP_ATTEMPTS", Value: "7"},
		{Variable: "BACKOFF_CAP", Value: "1m"}}
	if !reflect.DeepEqual(loaded.RetryEnv, wantEnv) {
		t.Errorf("Load() RetryEnv = %v, want %v", loaded.RetryEnv, wantEnv)
	}
}

func TestLoad_retryEnvironment(t *testing.T) {
	// place is one expected problem; an empty file is the configuration file, a zero
	// line and column mean no position, msg is a part of the message.
	type place struct {
		file, path   string
		line, column int
		msg          string
	}
	const overflow = "2305843009213693952"
	retryFile := func(lines string) string { return "---\nglobal:\n  retry:\n" + lines }
	tests := []struct {
		name    string
		content string
		env     map[string]string
		// load are the problems of Load, validate the problems of Validate on the paths
		// of global, which run only when Load succeeds.
		load     []place
		validate []place
	}{
		{
			name:    "invalid variables with a problem of the file",
			content: "---\nbogus: 1\n",
			env:     map[string]string{"MAX_RETRIES_TIME": "bogus", "BACKOFF_STEP_ATTEMPTS": "x"},
			load: []place{
				{"", "bogus", 2, 1, "not found"},
				{"<env MAX_RETRIES_TIME>", "global.retry.max_retries_time", 0, 0,
					`MAX_RETRIES_TIME: invalid duration "bogus"`},
				{"<env BACKOFF_STEP_ATTEMPTS>", "global.retry.backoff_step_attempts", 0, 0,
					`BACKOFF_STEP_ATTEMPTS: invalid integer "x"`},
			},
		},
		{
			name:    "invalid variable",
			content: "---\n{}\n",
			env:     map[string]string{"BACKOFF_CAP": "1"},
			load: []place{{"<env BACKOFF_CAP>", "global.retry.backoff_cap", 0, 0,
				`BACKOFF_CAP: invalid duration "1"`}},
		},
		{
			name:    "cap below initial",
			content: "---\n{}\n",
			env:     map[string]string{"BACKOFF_INITIAL": "5s", "BACKOFF_CAP": "2s"},
			validate: []place{{"<env BACKOFF_CAP>", "global.retry.backoff_cap", 0, 0,
				`effective "2s" < "5s"`}},
		},
		{
			name:    "zero initial selects the default",
			content: "---\n{}\n",
			env:     map[string]string{"BACKOFF_INITIAL": "0s", "BACKOFF_CAP": "500ms"},
			validate: []place{{"<env BACKOFF_CAP>", "global.retry.backoff_cap", 0, 0,
				`effective "500ms" < "1s"`}},
		},
		{
			name:    "initial above the default cap",
			content: "---\n{}\n",
			env:     map[string]string{"BACKOFF_INITIAL": "10s"},
			validate: []place{{"<env BACKOFF_INITIAL>", "global.retry.backoff_cap", 0, 0,
				`effective "7s" < "10s"`}},
		},
		{
			name:    "initial above the cap of the file",
			content: retryFile("    backoff_cap: 2s\n"),
			env:     map[string]string{"BACKOFF_INITIAL": "5s"},
			validate: []place{{"<env BACKOFF_INITIAL>", "global.retry.backoff_cap", 0, 0,
				`effective "2s" < "5s"`}},
		},
		{
			name:    "overridden value of the file",
			content: retryFile("    backoff_initial: 5s\n    backoff_cap: 2s\n"),
			env:     map[string]string{"BACKOFF_CAP": "6s"},
		},
		{
			name:    "cap of the file below initial",
			content: retryFile("    backoff_initial: 5s\n    backoff_cap: 2s\n"),
			validate: []place{{"", "global.retry.backoff_cap", 5, 18,
				`effective "2s" < "5s"`}},
		},
		{
			name:    "null cap of the file",
			content: retryFile("    backoff_initial: 10s\n    backoff_cap: null\n"),
			validate: []place{{"", "global.retry.backoff_cap", 5, 18,
				`effective "7s" < "10s"`}},
		},
		{
			name:    "missing cap of the file",
			content: retryFile("    backoff_initial: 10s\n"),
			validate: []place{{"", "global.retry.backoff_cap", 3, 3,
				`effective "7s" < "10s"`}},
		},
		{
			name:    "values without limits",
			content: "---\n{}\n",
			env: map[string]string{"MAX_RETRIES_TIME": "-1s", "BACKOFF_STEP": "0s",
				"BACKOFF_STEP_ATTEMPTS": "0"},
		},
		{
			name:    "zero budget",
			content: "---\n{}\n",
			env:     map[string]string{"MAX_RETRIES_TIME": "0s"},
		},
		{
			// 8 * 2^61 is 2^64.
			name:    "cycle overflow",
			content: "---\n{}\n",
			env:     map[string]string{"BACKOFF_STEP_ATTEMPTS": overflow},
			validate: []place{{"<env BACKOFF_STEP_ATTEMPTS>",
				"global.retry.backoff_step_attempts", 0, 0, "overflows int"}},
		},
		{
			// 3 * 2^63 overflows, BACKOFF_STEP_ATTEMPTS is not set.
			name:    "cycle overflow by the cap",
			content: "---\n{}\n",
			env: map[string]string{"BACKOFF_STEP": "1ns",
				"BACKOFF_CAP": "9223372036854775807ns"},
			validate: []place{{"<env BACKOFF_CAP>", "global.retry.backoff_step_attempts",
				0, 0, "overflows int"}},
		},
		{
			name:    "largest cap",
			content: "---\n{}\n",
			env:     map[string]string{"BACKOFF_CAP": "9223372036854775807ns"},
		},
		{
			name:    "cycle overflow of the file",
			content: retryFile("    backoff_step_attempts: " + overflow + "\n"),
			validate: []place{{"", "global.retry.backoff_step_attempts", 4, 28,
				"overflows int"}},
		},
		{
			// The attempts of the file overflow with the step from the environment.
			name:    "cycle overflow with the step",
			content: retryFile("    backoff_step_attempts: " + overflow + "\n"),
			env:     map[string]string{"BACKOFF_STEP": "1s"},
			validate: []place{{"<env BACKOFF_STEP>", "global.retry.backoff_step_attempts",
				0, 0, "overflows int"}},
		},
		{
			// 8 * (2^60 - 1) is 2^63 - 8.
			name:    "largest attempts",
			content: "---\n{}\n",
			env:     map[string]string{"BACKOFF_STEP_ATTEMPTS": "1152921504606846975"},
		},
		{
			// The problems without a position go last in the order they are found.
			name:    "both rules",
			content: "---\nglobal:\n  parallel_targets: 0\n",
			env: map[string]string{"BACKOFF_INITIAL": "10s",
				"BACKOFF_STEP_ATTEMPTS": overflow},
			validate: []place{
				{"", "global.parallel_targets", 3, 21, "must be at least 1"},
				{"<env BACKOFF_INITIAL>", "global.retry.backoff_cap", 0, 0,
					`effective "7s" < "10s"`},
				{"<env BACKOFF_STEP_ATTEMPTS>", "global.retry.backoff_step_attempts", 0, 0,
					"overflows int"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRetryEnv(t, tt.env)
			path := writeRetryConfig(t, tt.content)
			compare := func(kind string, got []config.Problem, wants []place) {
				t.Helper()
				if len(got) != len(wants) {
					t.Fatalf("%s problems = %v, want %d", kind, got, len(wants))
				}
				for i, w := range wants {
					file := w.file
					if file == "" {
						file = path
					}
					p := got[i]
					if p.File != file || p.Path != w.path || p.Line != w.line ||
						p.Column != w.column || !strings.Contains(p.Message, w.msg) {
						t.Errorf("%s problem %d = %v, want %s:%d:%d: %s: …%s…", kind, i, p,
							file, w.line, w.column, w.path, w.msg)
					}
				}
			}
			loaded, err := config.Load(path)
			if tt.load != nil {
				var cfgErr *config.Error
				if loaded != nil || !errors.As(err, &cfgErr) {
					t.Fatalf("Load() = %v, %v, want nil and *config.Error", loaded, err)
				}
				compare("Load()", cfgErr.Problems, tt.load)
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			var problems []config.Problem
			for _, p := range config.Validate(loaded).Problems {
				if ownPath(p.Path) {
					problems = append(problems, p)
				}
			}
			compare("Validate()", problems, tt.validate)
		})
	}
}
