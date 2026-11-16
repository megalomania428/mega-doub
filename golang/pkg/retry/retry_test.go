package retry

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	ci "github.com/megalomania428/go-lib-ci"
)

// setEnv sets every variable of the overrides to its value in env, or to the empty
// string when env has none. With unset the variables missing from env are removed.
func setEnv(t *testing.T, env map[string]string, unset bool) {
	t.Helper()
	for _, v := range variables {
		value, ok := env[v.name]
		t.Setenv(v.name, value)
		if unset && !ok {
			if err := os.Unsetenv(v.name); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestFromEnv(t *testing.T) {
	base := Options{MaxRetriesTime: 2 * time.Minute, BackoffInitial: 2 * time.Second,
		BackoffStep: 3 * time.Second, BackoffStepAttempts: 5, BackoffCap: 30 * time.Second}
	// with returns base changed by f.
	with := func(f func(o *Options)) Options {
		o := base
		f(&o)
		return o
	}
	type args struct {
		base Options
	}
	tests := []struct {
		name string
		env  map[string]string
		// unset removes the variables missing from env instead of setting them empty.
		unset bool
		args  args
		want  Options
		want1 []Override
		// want2 holds the expected errors, Err is a part of the message.
		want2 []EnvError
	}{
		{name: "unset variables", unset: true, args: args{base}, want: base},
		{name: "empty variables", args: args{base}, want: base},
		{
			name:  "max retries time",
			env:   map[string]string{"MAX_RETRIES_TIME": "5m"},
			args:  args{base},
			want:  with(func(o *Options) { o.MaxRetriesTime = 5 * time.Minute }),
			want1: []Override{{"MAX_RETRIES_TIME", "5m"}},
		},
		{
			name:  "backoff initial",
			env:   map[string]string{"BACKOFF_INITIAL": "500ms"},
			args:  args{base},
			want:  with(func(o *Options) { o.BackoffInitial = 500 * time.Millisecond }),
			want1: []Override{{"BACKOFF_INITIAL", "500ms"}},
		},
		{
			name:  "backoff step",
			env:   map[string]string{"BACKOFF_STEP": "2s"},
			args:  args{base},
			want:  with(func(o *Options) { o.BackoffStep = 2 * time.Second }),
			want1: []Override{{"BACKOFF_STEP", "2s"}},
		},
		{
			name:  "backoff step attempts",
			env:   map[string]string{"BACKOFF_STEP_ATTEMPTS": "7"},
			args:  args{base},
			want:  with(func(o *Options) { o.BackoffStepAttempts = 7 }),
			want1: []Override{{"BACKOFF_STEP_ATTEMPTS", "7"}},
		},
		{
			name:  "backoff cap",
			env:   map[string]string{"BACKOFF_CAP": "1m"},
			args:  args{base},
			want:  with(func(o *Options) { o.BackoffCap = time.Minute }),
			want1: []Override{{"BACKOFF_CAP", "1m"}},
		},
		{
			name: "all variables",
			env: map[string]string{"BACKOFF_CAP": "1m", "BACKOFF_STEP_ATTEMPTS": "7",
				"BACKOFF_STEP": "2s", "BACKOFF_INITIAL": "500ms", "MAX_RETRIES_TIME": "5m"},
			args: args{base},
			want: Options{MaxRetriesTime: 5 * time.Minute,
				BackoffInitial: 500 * time.Millisecond, BackoffStep: 2 * time.Second,
				BackoffStepAttempts: 7, BackoffCap: time.Minute},
			want1: []Override{{"MAX_RETRIES_TIME", "5m"}, {"BACKOFF_INITIAL", "500ms"},
				{"BACKOFF_STEP", "2s"}, {"BACKOFF_STEP_ATTEMPTS", "7"},
				{"BACKOFF_CAP", "1m"}},
		},
		{
			name:  "zero budget",
			env:   map[string]string{"MAX_RETRIES_TIME": "0s"},
			args:  args{base},
			want:  with(func(o *Options) { o.MaxRetriesTime = 0 }),
			want1: []Override{{"MAX_RETRIES_TIME", "0s"}},
		},
		{
			name:  "negative budget",
			env:   map[string]string{"MAX_RETRIES_TIME": "-1s"},
			args:  args{base},
			want:  with(func(o *Options) { o.MaxRetriesTime = -time.Second }),
			want1: []Override{{"MAX_RETRIES_TIME", "-1s"}},
		},
		{
			// The invalid values keep base, the valid one between them is applied.
			name: "invalid values",
			env: map[string]string{"MAX_RETRIES_TIME": "bogus", "BACKOFF_STEP": "2s",
				"BACKOFF_STEP_ATTEMPTS": "x"},
			args:  args{base},
			want:  with(func(o *Options) { o.BackoffStep = 2 * time.Second }),
			want1: []Override{{"BACKOFF_STEP", "2s"}},
			want2: []EnvError{
				{"MAX_RETRIES_TIME", "max_retries_time",
					errors.New(`MAX_RETRIES_TIME: invalid duration "bogus"`)},
				{"BACKOFF_STEP_ATTEMPTS", "backoff_step_attempts",
					errors.New(`BACKOFF_STEP_ATTEMPTS: invalid integer "x"`)},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env, tt.unset)
			got, got1, got2 := FromEnv(tt.args.base)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FromEnv() got = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(got1, tt.want1) {
				t.Errorf("FromEnv() got1 = %v, want %v", got1, tt.want1)
			}
			if len(got2) != len(tt.want2) {
				t.Fatalf("FromEnv() got2 = %v, want %v", got2, tt.want2)
			}
			for i, w := range tt.want2 {
				e := got2[i]
				if e.Variable != w.Variable || e.Field != w.Field ||
					!strings.Contains(e.Error(), w.Err.Error()) ||
					!errors.Is(errors.Unwrap(e), e.Err) {
					t.Errorf("FromEnv() got2[%d] = %+v, want %+v", i, e, w)
				}
			}
		})
	}
}

func TestOptions_CI(t *testing.T) {
	type fields struct {
		MaxRetriesTime      time.Duration
		BackoffInitial      time.Duration
		BackoffStep         time.Duration
		BackoffStepAttempts int
		BackoffCap          time.Duration
	}
	type args struct {
		name string
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   ci.RetryOptions
	}{
		{
			// Zero values stay zero, go-lib-ci selects its defaults for them.
			name: "zero values",
			args: args{"vault"},
			want: ci.RetryOptions{Name: "vault"},
		},
		{
			name: "negative values",
			fields: fields{MaxRetriesTime: -time.Second, BackoffInitial: -time.Second,
				BackoffStep: -2 * time.Second, BackoffStepAttempts: -1,
				BackoffCap: -3 * time.Second},
			args: args{"vault"},
			want: ci.RetryOptions{Name: "vault", BackoffOptions: ci.BackoffOptions{
				MaxRetriesTime: -time.Second, BackoffInitial: -time.Second,
				BackoffStep: -2 * time.Second, BackoffStepAttempts: -1,
				BackoffCap: -3 * time.Second}},
		},
		{
			name: "positive values",
			fields: fields{MaxRetriesTime: 2 * time.Minute, BackoffInitial: 2 * time.Second,
				BackoffStep: 3 * time.Second, BackoffStepAttempts: 5,
				BackoffCap: 30 * time.Second},
			args: args{"storage"},
			want: ci.RetryOptions{Name: "storage", BackoffOptions: ci.BackoffOptions{
				MaxRetriesTime: 2 * time.Minute, BackoffInitial: 2 * time.Second,
				BackoffStep: 3 * time.Second, BackoffStepAttempts: 5,
				BackoffCap: 30 * time.Second}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := Options{
				MaxRetriesTime:      tt.fields.MaxRetriesTime,
				BackoffInitial:      tt.fields.BackoffInitial,
				BackoffStep:         tt.fields.BackoffStep,
				BackoffStepAttempts: tt.fields.BackoffStepAttempts,
				BackoffCap:          tt.fields.BackoffCap,
			}
			if got := o.CI(tt.args.name); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Options.CI() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestOptions_CI_negativeBudget runs the converted options with ci.Retry: a negative
// budget makes exactly one attempt.
func TestOptions_CI_negativeBudget(t *testing.T) {
	failed := errors.New("failed")
	opts := Options{MaxRetriesTime: -time.Second}.CI("test")
	opts.Stderr = io.Discard
	attempts := 0
	err := ci.Retry(t.Context(), opts, func(context.Context) error {
		attempts++
		return failed
	})
	if !errors.Is(err, failed) || attempts != 1 {
		t.Errorf("ci.Retry() error = %v, attempts = %d, want %v and 1", err, attempts,
			failed)
	}
}
