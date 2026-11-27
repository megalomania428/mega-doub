//go:build integration

// Package integration is documented in manifest.go.
package integration

// cspell:ignore duplicacy subtests

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// duplicacyCLI is a build of the duplicacy CLI that restores and checks the backups.
type duplicacyCLI struct {
	name string // name in the subtests
	bin  string
}

// access is what a duplicacy CLI needs to open the storage of a scenario: the snapshot
// ID, the URL and the environment with the password and the keys.
type access struct {
	snapshotID string
	url        string
	env        []string
}

// cliRepo is a repository of a duplicacy CLI: a directory with the settings .duplicacy
// that are connected to the storage of a scenario.
type cliRepo struct {
	bin string
	dir string
	env []string
}

// tabular is the output of duplicacy check -tabular.
type tabular []byte

// duplicacyCLIs returns the upstream release and the patched build of update-graves from
// the variables that scripts/tests/integration-tools.sh prints.
func duplicacyCLIs(t *testing.T) []duplicacyCLI {
	t.Helper()
	return []duplicacyCLI{
		{name: "upstream-3.3.1", bin: requireEnv(t, "MEGA_DOUB_IT_UPSTREAM")},
		{name: "patched-3.3.0-p003", bin: requireEnv(t, "MEGA_DOUB_IT_PATCHED")},
	}
}

// requireEnv returns the value of the variable name or skips the test without it. In
// GitHub Actions a missing variable fails the test, since the workflow runs every test.
func requireEnv(t *testing.T, name string) string {
	t.Helper()
	if value := os.Getenv(name); value != "" {
		return value
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Fatalf("%s is not set", name)
	}
	t.Skipf("%s is not set, scripts/tests/integration.sh sets it", name)
	return ""
}

// open initializes a repository of c in a new empty directory of the test with
// duplicacy init -e and the storage name default.
func (c duplicacyCLI) open(t *testing.T, a access) cliRepo {
	t.Helper()
	r := cliRepo{bin: c.bin, dir: t.TempDir(), env: a.env}
	_, err := r.run("init", "-e", a.snapshotID, a.url)
	must(t, err)
	return r
}

// run runs the CLI with args in the directory of the repository and returns its output.
// A failed run gives an error with the whole output.
func (r cliRepo) run(args ...string) ([]byte, error) {
	cmd := exec.Command(r.bin, args...)
	cmd.Dir = r.dir
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		command := filepath.Base(r.bin) + " " + strings.Join(args, " ")
		return out, fmt.Errorf("%s: %w: %s", command, err, bytes.TrimSpace(out))
	}
	return out, nil
}

// chunks returns the number of the chunks that revision rev references: the column
// chunks of the row of the revision in the table.
func (tb tabular) chunks(rev int) (int64, error) {
	want := strconv.Itoa(rev)
	for line := range strings.Lines(string(tb)) {
		cells := strings.Split(line, "|")
		if len(cells) > 5 && strings.TrimSpace(cells[1]) == want {
			return strconv.ParseInt(strings.TrimSpace(cells[5]), 10, 64)
		}
	}
	return 0, fmt.Errorf("no row of revision %d in the output of check -tabular", rev)
}
