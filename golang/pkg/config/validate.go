// Package config is documented in config.go.
package config

import (
	"fmt"
	"math/bits"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Result holds the problems and the warnings that Validate found, each sorted by
// position.
type Result struct {
	Problems []Problem
	Warnings []Problem
}

// Err returns nil without problems and *Error with the problems otherwise.
func (r Result) Err() error {
	if len(r.Problems) == 0 {
		return nil
	}
	return &Error{Problems: r.Problems}
}

// Validate checks the loaded configuration by the rules of its sections in the order
// of the schema. It does not touch the file system.
func Validate(l *Loaded) Result {
	v := &validator{l: l}
	v.global()
	v.retry()
	sortProblems(v.problems)
	sortProblems(v.warnings)
	return Result{Problems: v.problems, Warnings: v.warnings}
}

// validator collects the problems and the warnings of one configuration.
type validator struct {
	l        *Loaded
	problems []Problem
	warnings []Problem
}

// problem records a problem of the field at path at the position of the field.
func (v *validator) problem(path, msg string) {
	line, column := v.position(path)
	v.problems = append(v.problems, Problem{File: v.l.Path, Line: line, Column: column,
		Path: path, Message: msg})
}

// position returns where a problem of the field at path is reported: its value when
// the key is present, an explicit null included, otherwise the key of the nearest
// present ancestor, or the start of the document when there is none.
func (v *validator) position(path string) (line, column int) {
	if v.l.Has(path) {
		line, column, _ = v.l.ValuePos(path)
		return line, column
	}
	if parent := v.l.ancestor(path, ""); parent != "" {
		line, column, _ = v.l.Pos(parent)
		return line, column
	}
	return 1, 1
}

// required reports an empty value of a required field, an explicit null included, as
// required without the checks of its format, and tells whether the value is set.
func (v *validator) required(path, value string) bool {
	if value != "" {
		return true
	}
	v.problem(path, "required")
	return false
}

// quoted returns the value quoted and followed by a space to start a message, or
// nothing for an empty value.
func quoted(value string) string {
	if value == "" {
		return ""
	}
	return fmt.Sprintf("%q ", value)
}

// instanceName is the pattern of global.instance.
var instanceName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// progressModes are the values of global.progress.
var progressModes = []string{"auto", "always", "never"}

// global checks the global section.
func (v *validator) global() {
	g := v.l.Config.Global
	if !instanceName.MatchString(g.Instance) {
		v.problem("global.instance", quoted(g.Instance)+"must match "+instanceName.String())
	}
	copyDir := v.absolute("global.copy_dir", g.CopyDir)
	stateDir := v.absolute("global.state_dir", g.StateDir)
	if copyDir && stateDir {
		v.directories(g.CopyDir, g.StateDir)
	}
	if g.ParallelTargets < 1 {
		v.problem("global.parallel_targets", "must be at least 1")
	}
	if g.ParallelSources < 1 {
		v.problem("global.parallel_sources", "must be at least 1")
	}
	if !slices.Contains(progressModes, g.Progress) {
		v.problem("global.progress",
			quoted(g.Progress)+"must be one of "+strings.Join(progressModes, ", "))
	}
	if g.Timeout <= 0 {
		v.problem("global.timeout", "must be greater than zero")
	}
}

// absolute reports a directory that is not an absolute path and tells whether it is.
func (v *validator) absolute(path, dir string) bool {
	if filepath.IsAbs(dir) {
		return true
	}
	v.problem(path, quoted(dir)+"must be an absolute path")
	return false
}

// directories reports equal or nested global.copy_dir and global.state_dir. The paths
// are compared as written after cleaning, links and letter case are not resolved.
func (v *validator) directories(copyDir, stateDir string) {
	switch {
	case filepath.Clean(copyDir) == filepath.Clean(stateDir):
		v.problem("global.state_dir", "must differ from global.copy_dir")
	case within(copyDir, stateDir):
		v.problem("global.state_dir", "must not be inside global.copy_dir")
	case within(stateDir, copyDir):
		v.problem("global.copy_dir", "must not be inside global.state_dir")
	}
}

// within tells whether the path child is inside the path parent or equal to it.
func within(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, "../")))
}

// retry checks global.retry by the effective values as go-lib-ci uses them: a zero or
// negative backoff value selects its default, max_retries_time is not checked.
func (v *validator) retry() {
	r := v.l.Config.Global.Retry
	initial := effective(r.BackoffInitial, defaultBackoffInitial)
	step := effective(r.BackoffStep, defaultBackoffStep)
	limit := effective(r.BackoffCap, defaultBackoffCap)
	attempts := r.BackoffStepAttempts
	if attempts <= 0 {
		attempts = defaultBackoffStepAttempts
	}
	if limit < initial {
		v.problem("global.retry.backoff_cap", fmt.Sprintf(
			"must not be less than backoff_initial: effective %q < %q",
			limit.String(), initial.String()))
	}
	// go-lib-ci computes the backoff cycle (cap/step + 1) * attempts in int, a cycle
	// that overflows to zero divides by zero there.
	hi, lo := bits.Mul64(uint64(limit/step)+1, uint64(attempts))
	if hi != 0 || lo >= 1<<63 {
		v.problem("global.retry.backoff_step_attempts",
			"is too large: the backoff cycle overflows int")
	}
}

// effective returns the duration, or def when it is zero or negative.
func effective(d Duration, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return time.Duration(d)
}
