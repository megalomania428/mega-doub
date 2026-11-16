// cspell:ignore duplicacy

// Package config is documented in config.go.
package config

import (
	"fmt"
	"maps"
	"math/bits"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/megalomania428/mega-doub/pkg/secret"
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
	v.vault()
	v.storage()
	v.prune()
	v.check()
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

// warning records a warning about the field at path at the position of the field.
func (v *validator) warning(path, msg string) {
	line, column := v.position(path)
	v.warnings = append(v.warnings, Problem{File: v.l.Path, Line: line, Column: column,
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

// vault checks the vault section. vault.address and vault.token are required when a
// reference uses Vault, vault.token itself and the fields forbidden for local targets
// left out.
func (v *validator) vault() {
	c := v.l.Config.Vault
	if v.usesVault() {
		v.required("vault.address", c.Address)
		v.required("vault.token", string(c.Token))
	}
	if c.Address != "" {
		scheme, msg := vaultAddress(c.Address)
		switch {
		case msg != "":
			v.problem("vault.address", msg)
		case scheme == "http":
			v.warning("vault.address", "vault token is sent in clear text")
		}
	}
	if strings.HasPrefix(string(c.Token), "vault:") {
		v.problem("vault.token", "must not be a vault: reference")
	}
	if c.Timeout <= 0 {
		v.problem("vault.timeout", "must be greater than zero")
	}
	if c.CAFile != "" {
		v.absolute("vault.ca_file", c.CAFile)
	}
}

// usesVault tells whether a reference uses Vault, vault.token and the fields forbidden
// for local targets left out. The prefix is checked as written, so a malformed vault:
// reference counts too.
func (v *validator) usesVault() bool {
	skip := map[string]bool{"vault.token": true}
	for i, t := range v.l.Config.Targets {
		if t.Transport == "local" {
			for _, field := range []string{"key", "key_passphrase", "password"} {
				skip[fmt.Sprintf("targets[%d].%s", i, field)] = true
			}
		}
	}
	uses := false
	WalkRefs(v.l.Config, func(path string, ref *secret.Ref) {
		if !skip[path] && strings.HasPrefix(string(*ref), "vault:") {
			uses = true
		}
	})
	return uses
}

// vaultAddress checks the Vault address and returns its scheme, or the message of the
// first failed check. The messages leave out the address and the parse error.
func vaultAddress(s string) (scheme, msg string) {
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return "", "must be a valid URL"
	case u.Scheme != "http" && u.Scheme != "https":
		return "", "must use http or https"
	case u.Opaque != "":
		return "", "must have // before the host"
	case u.Hostname() == "":
		return "", "must have a host"
	case u.User != nil:
		return "", "must not contain user information"
	case u.EscapedPath() != "" && u.EscapedPath() != "/":
		return "", "must not contain a path"
	case strings.ContainsAny(s, "?#"):
		// url.URL.ForceQuery hides an empty query, so the string is checked.
		return "", "must not contain a query or a fragment"
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "", "must have a port from 1 to 65535"
		}
	}
	return u.Scheme, ""
}

// snapshotID is the pattern of storage.snapshot_id, the one of duplicacy.
var snapshotID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// storage checks the storage section. The messages leave out the URL and the parse
// error, they may hold a password.
func (v *validator) storage() {
	s := v.l.Config.Storage
	switch {
	case s.Name == "":
		v.problem("storage.name", "must not be empty")
	case strings.EqualFold(s.Name, "ssh"):
		v.problem("storage.name", quoted(s.Name)+"is reserved by duplicacy")
	}
	if v.required("storage.url", s.URL) {
		u, err := url.Parse(s.URL)
		if err != nil || u.Scheme == "" || u.Opaque != "" || u.Host == "" {
			v.problem("storage.url", "must be a URL with a scheme and a host")
		}
		if err == nil && u.User != nil {
			// An empty password is a password too.
			if _, ok := u.User.Password(); ok {
				v.warning("storage.url", "url contains a password")
			}
		}
	}
	if v.required("storage.snapshot_id", s.SnapshotID) &&
		!snapshotID.MatchString(s.SnapshotID) {
		v.problem("storage.snapshot_id",
			quoted(s.SnapshotID)+"must match "+snapshotID.String())
	}
	v.required("storage.password", string(s.Password))
	if s.Threads < 1 {
		v.problem("storage.threads", "must be at least 1")
	}
	for _, name := range slices.Sorted(maps.Keys(s.Keys)) {
		path := joinKey("storage.keys", name)
		if name == "" {
			v.problem(path, "required")
			continue
		}
		v.required(path, string(s.Keys[name]))
	}
}

// retention is the pattern of an element n:m of prune.keep.
var retention = regexp.MustCompile(`^[0-9]+:[0-9]+$`)

// prune checks the prune section. An element of prune.keep gets at most one problem:
// its format, a number out of the range of int, its age m below 1 or its age not below
// the age of the nearest previous element with valid format and numbers in int range.
func (v *validator) prune() {
	p := v.l.Config.Prune
	if p.Enabled && len(p.Keep) == 0 {
		v.problem("prune.keep", "must not be empty when prune is enabled")
	}
	previous, hasPrevious := 0, false
	for i, keep := range p.Keep {
		path := fmt.Sprintf("prune.keep[%d]", i)
		if !retention.MatchString(keep) {
			v.problem(path, quoted(keep)+"must have the form n:m")
			continue
		}
		n, m, _ := strings.Cut(keep, ":")
		// duplicacy ignores the error of strconv.Atoi and takes the limit of int.
		_, errN := strconv.Atoi(n)
		age, errM := strconv.Atoi(m)
		switch {
		case errN != nil || errM != nil:
			v.problem(path, quoted(keep)+"is out of range")
			continue
		case age < 1:
			v.problem(path, quoted(keep)+"must have an age m of at least 1")
		case hasPrevious && age >= previous:
			v.problem(path, quoted(keep)+"must have an age m below the previous one")
		}
		previous, hasPrevious = age, true
	}
	if p.Threads < 1 {
		v.problem("prune.threads", "must be at least 1")
	}
}

// check checks the check section.
func (v *validator) check() {
	c := v.l.Config.Check
	switch {
	case c.Every < 0:
		v.problem("check.every", "must not be negative")
	case c.Enabled && c.Every == 0:
		v.problem("check.every", "must be greater than zero when check is enabled")
	}
	if c.TestRestoreBytes < 0 {
		v.problem("check.test_restore_bytes", "must not be negative")
	}
}
