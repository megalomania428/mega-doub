// Package retry holds the options of global.retry with the overrides from the
// environment and converts them to the options of go-lib-ci.
package retry

import (
	"os"
	"time"

	ci "github.com/megalomania428/go-lib-ci"
)

// Options are the values of global.retry. They are passed to go-lib-ci as they are:
// a zero MaxRetriesTime selects its default, a negative one disables the retries, a
// zero or negative backoff value selects its default.
type Options struct {
	MaxRetriesTime      time.Duration
	BackoffInitial      time.Duration
	BackoffStep         time.Duration
	BackoffStepAttempts int
	BackoffCap          time.Duration
}

// Override is an option taken from the environment: the variable and its value as it
// is written.
type Override struct {
	Variable, Value string
}

// EnvError is a variable of the environment that does not parse. Field is the key of
// global.retry that the variable overrides.
type EnvError struct {
	Variable, Field string
	Err             error
}

// Error returns the error of the parser, it names the variable and the value.
func (e EnvError) Error() string {
	return e.Err.Error()
}

// Unwrap returns the error of the parser.
func (e EnvError) Unwrap() error {
	return e.Err
}

// variable is an environment variable that overrides one field of Options.
type variable struct {
	name, field string
	// parse reads the variable name into its field of o, the field is the default.
	parse func(name string, o *Options) error
}

// variables are the overrides in the order of the records and the errors.
var variables = []variable{
	{"MAX_RETRIES_TIME", "max_retries_time", func(name string, o *Options) (err error) {
		o.MaxRetriesTime, err = ci.ParseDurationEnv(name, o.MaxRetriesTime)
		return err
	}},
	{"BACKOFF_INITIAL", "backoff_initial", func(name string, o *Options) (err error) {
		o.BackoffInitial, err = ci.ParseDurationEnv(name, o.BackoffInitial)
		return err
	}},
	{"BACKOFF_STEP", "backoff_step", func(name string, o *Options) (err error) {
		o.BackoffStep, err = ci.ParseDurationEnv(name, o.BackoffStep)
		return err
	}},
	{"BACKOFF_STEP_ATTEMPTS", "backoff_step_attempts",
		func(name string, o *Options) (err error) {
			o.BackoffStepAttempts, err = ci.ParseIntEnv(name, o.BackoffStepAttempts)
			return err
		}},
	{"BACKOFF_CAP", "backoff_cap", func(name string, o *Options) (err error) {
		o.BackoffCap, err = ci.ParseDurationEnv(name, o.BackoffCap)
		return err
	}},
}

// FromEnv returns base with the overrides from the environment, the applied overrides
// and the errors, both in the order of the variables. An empty variable counts as
// unset. Every variable is parsed, a variable that does not parse keeps the field of
// base and is not an override.
func FromEnv(base Options) (Options, []Override, []EnvError) {
	var overrides []Override
	var errs []EnvError
	for _, v := range variables {
		value, ok := os.LookupEnv(v.name)
		if !ok || value == "" {
			continue
		}
		// The parsers of go-lib-ci return zero on an error, the copy keeps base.
		next := base
		if err := v.parse(v.name, &next); err != nil {
			errs = append(errs, EnvError{Variable: v.name, Field: v.field, Err: err})
			continue
		}
		base = next
		overrides = append(overrides, Override{Variable: v.name, Value: value})
	}
	return base, overrides, errs
}

// Field returns the key of global.retry that the variable name overrides, or "" for a
// name that is not one of the variables.
func Field(name string) string {
	for _, v := range variables {
		if v.name == name {
			return v.field
		}
	}
	return ""
}

// CI returns the options of ci.Retry for the operation name with every value as it is;
// the caller sets Stderr and IdleCloser.
func (o Options) CI(name string) ci.RetryOptions {
	return ci.RetryOptions{
		Name: name,
		BackoffOptions: ci.BackoffOptions{
			MaxRetriesTime:      o.MaxRetriesTime,
			BackoffInitial:      o.BackoffInitial,
			BackoffStep:         o.BackoffStep,
			BackoffStepAttempts: o.BackoffStepAttempts,
			BackoffCap:          o.BackoffCap,
		},
	}
}
