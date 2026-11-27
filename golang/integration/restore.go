//go:build integration

// Package integration is documented in manifest.go.
package integration

// cspell:ignore duplicacy subtests

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// upstreamName is the name of the upstream release in the subtests.
const upstreamName = "upstream-3.3.1"

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
	vars       []string    // NAME=value of the variables that the scenario adds to env
	commands   *commandLog // runs since the last pause
}

// cliRepo is a repository of a duplicacy CLI: a directory with the settings .duplicacy
// that are connected to the storage of a scenario.
type cliRepo struct {
	bin      string
	dir      string
	env      []string
	vars     []string    // NAME=value of the variables that the scenario adds to env
	commands *commandLog // runs since the last pause
}

// tabular is the output of duplicacy check -tabular.
type tabular []byte

// storageTotal is the storage as the table of check -a -tabular shows it: the chunks and
// the bytes of the whole storage and the revisions in it.
type storageTotal struct {
	chunks    int64
	bytes     int64
	revisions []int
}

// duplicacyCLIs returns the upstream release and the patched build of update-graves from
// the variables that scripts/tests/integration-tools.sh prints.
func duplicacyCLIs(t *testing.T) []duplicacyCLI {
	t.Helper()
	return []duplicacyCLI{
		{name: upstreamName, bin: requireEnv(t, "MEGA_DOUB_IT_UPSTREAM")},
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
	r := cliRepo{bin: c.bin, dir: t.TempDir(), env: a.env, vars: a.vars,
		commands: a.commands}
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
	r.commands.add(shellLine{dir: r.dir, vars: r.vars, bin: r.bin, args: args}.String())
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

// total returns the totals of the storage: the columns chunks and bytes of the row all,
// which counts every chunk of the snapshot once, and the revisions of the other rows. The
// table shows the bytes in KiB, rounded down: 512 or 30,546K.
func (tb tabular) total() (storageTotal, error) {
	var total storageTotal
	found := false
	for line := range strings.Lines(string(tb)) {
		cells := strings.Split(line, "|")
		if len(cells) < 7 {
			continue
		}
		rev := strings.TrimSpace(cells[1])
		if n, err := strconv.Atoi(rev); err == nil {
			total.revisions = append(total.revisions, n)
		}
		if rev != "all" {
			continue
		}
		chunks, err := strconv.ParseInt(strings.TrimSpace(cells[5]), 10, 64)
		if err != nil {
			return total, err
		}
		size := strings.ReplaceAll(strings.TrimSpace(cells[6]), ",", "")
		size, kib := strings.CutSuffix(size, "K")
		volume, err := strconv.ParseInt(size, 10, 64)
		if err != nil {
			return total, err
		}
		if kib {
			volume <<= 10
		}
		total.chunks, total.bytes, found = chunks, volume, true
	}
	if !found {
		return total, errors.New("no row all in the output of check -tabular")
	}
	return total, nil
}

// storage runs check -a -tabular and returns the totals of the storage; a failure stops
// the test.
func (r cliRepo) storage(t *testing.T) storageTotal {
	t.Helper()
	out, err := r.run("check", "-a", "-tabular")
	must(t, err)
	total, err := tabular(out).total()
	must(t, err)
	return total
}

// String returns the totals in a line.
func (st storageTotal) String() string {
	revisions := make([]string, len(st.revisions))
	for i, n := range st.revisions {
		revisions[i] = strconv.Itoa(n)
	}
	return fmt.Sprintf("%d chunks, %d bytes, revisions %s", st.chunks, st.bytes,
		strings.Join(revisions, " "))
}
