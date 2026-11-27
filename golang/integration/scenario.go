//go:build integration

// Package integration is documented in manifest.go.
package integration

// cspell:ignore clis doub duplicacy sprune subtest subtests

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// maxDiff limits the differences that a restore reports one by one.
const maxDiff = 50

// Option sets a part of the storage of a scenario.
type Option func(*scenarioOptions)

// scenarioOptions are the storage of a scenario and the removal of the storage.
type scenarioOptions struct {
	url     string
	keys    map[string]string
	cleanup func()
}

// WithStorageURL sets storage.url of the scenario; it is required.
func WithStorageURL(url string) Option {
	return func(o *scenarioOptions) { o.url = url }
}

// WithKeys sets storage.keys of the scenario: a key name, such as s3_id, maps to its
// value, which mega-doub gets through an env: reference and duplicacy through the
// variable DUPLICACY_<NAME>.
func WithKeys(keys map[string]string) Option {
	return func(o *scenarioOptions) { o.keys = keys }
}

// WithCleanup sets a function that removes the storage after the scenario, also after a
// failure or a skip.
func WithCleanup(cleanup func()) Option {
	return func(o *scenarioOptions) { o.cleanup = cleanup }
}

// scenario backs up the revisions r1 to r4 of a tree into one storage with mega-doub,
// restores every revision with both duplicacy CLIs and checks both kinds of prune.
type scenario struct {
	name     string // storage in the summary, such as local
	access   access
	mega     *megaDoub
	clis     []duplicacyCLI
	patched  duplicacyCLI
	tree     *tree
	expected map[int]manifest        // manifests of the revisions
	backups  map[int]backupCompleted // records of the backups
	removed  map[int]bool            // revisions removed by the exclusive prune
	last     int                     // newest revision
	phase    string                  // prefix of the names of the subtests
	summary  string                  // file of MEGA_DOUB_IT_SUMMARY, may be empty
}

// restoreCase is the restore of a revision with a CLI.
type restoreCase struct {
	cli      duplicacyCLI
	revision int
}

// runScenario runs the scenario on the storage that opts set. Every check is a subtest
// with a readable name, such as r2/restore/upstream-3.3.1. A failed init, backup or prune
// stops the scenario, since the later steps depend on it, while the restores and the
// checks of the two CLIs are independent.
func runScenario(t *testing.T, opts ...Option) {
	t.Helper()
	var o scenarioOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.cleanup != nil {
		t.Cleanup(o.cleanup)
	}
	if o.url == "" {
		t.Fatal("scenario: WithStorageURL is required")
	}
	s := newScenario(t, o)
	if !t.Run("init", s.initStorage) {
		t.FailNow()
	}
	for n := 1; n <= 3; n++ {
		s.backup(t, n)
	}
	t.Run("r2/new-chunks", s.newChunks)
	if !t.Run("prune-1/mega-doub", s.freshPrune) {
		t.FailNow()
	}
	s.restoreAll(t)
	s.checkAll(t)
	s.phase = "prune-2/"
	if !t.Run("prune-2/exclusive", s.exclusivePrune) {
		t.FailNow()
	}
	s.backup(t, 4)
	if !t.Run("prune-2/mega-doub", s.prune) {
		t.FailNow()
	}
	s.restoreAll(t)
	s.checkAll(t)
}

// newScenario writes the configuration of mega-doub into a temporary directory of the
// test, which holds the copy and the state directories too.
func newScenario(t *testing.T, o scenarioOptions) *scenario {
	t.Helper()
	clis := duplicacyCLIs(t)
	name := strings.ToLower(strings.TrimPrefix(t.Name(), "Test"))
	id := fmt.Sprintf("it-%s-%d-%d", name, time.Now().UnixNano(), os.Getpid())
	password := "fake-" + id + "-password"
	work := t.TempDir()
	keys := slices.Sorted(maps.Keys(o.keys))
	cfg := storageConfig{dir: work, url: o.url, snapshotID: id, keys: keys, demo: demo()}
	config := filepath.Join(work, "mega-doub.yaml")
	must(t, os.WriteFile(config, []byte(cfg.yaml()), 0o600))
	// An empty variable of global.retry counts as unset.
	megaEnv := []string{passwordVariable + "=" + password, "MAX_RETRIES_TIME=",
		"BACKOFF_INITIAL=", "BACKOFF_STEP=", "BACKOFF_STEP_ATTEMPTS=", "BACKOFF_CAP="}
	cliEnv := []string{"DUPLICACY_PASSWORD=" + password}
	for _, key := range keys {
		megaEnv = append(megaEnv, keyVariable(key)+"="+o.keys[key])
		cliEnv = append(cliEnv, "DUPLICACY_"+strings.ToUpper(key)+"="+o.keys[key])
	}
	mega := &megaDoub{bin: megaDoubBinary(t), config: config, env: environ(megaEnv...),
		tty: demoTerminal(t)}
	return &scenario{
		name:     name,
		access:   access{snapshotID: id, url: o.url, env: environ(cliEnv...)},
		mega:     mega,
		clis:     clis,
		patched:  clis[len(clis)-1],
		tree:     newTree(t, filepath.Join(work, "copy")),
		expected: map[int]manifest{},
		backups:  map[int]backupCompleted{},
		removed:  map[int]bool{},
		summary:  os.Getenv("MEGA_DOUB_IT_SUMMARY"),
	}
}

// initStorage initializes the storage with mega-doub.
func (s *scenario) initStorage(t *testing.T) {
	_, err := s.mega.run("init")
	must(t, err)
}

// backup builds revision n, backs it up with mega-doub and keeps the manifest that the
// restores of the revision must reproduce. A failure stops the scenario.
func (s *scenario) backup(t *testing.T, n int) {
	t.Helper()
	ok := t.Run(fmt.Sprintf("%sr%d/backup", s.phase, n), func(t *testing.T) {
		s.tree.revision(t, n)
		before, err := scanTree(s.tree.root, skipPartial)
		must(t, err)
		recs, err := s.mega.run("backup")
		must(t, err)
		// The backup removes the partial files, which changes the times of their
		// directories, so the tree after the backup is what the revision holds.
		want, err := scanTree(s.tree.root, nil)
		must(t, err)
		for _, line := range before.diff(want) {
			t.Errorf("backup changed more than the partial files: %s", line)
		}
		var got backupCompleted
		must(t, recs.decode("backup completed", &got))
		if got.Revision != n {
			t.Fatalf("backup created revision %d, want %d", got.Revision, n)
		}
		total := want.totals()
		if got.Files != total.files {
			t.Errorf("backup counted %d files, the tree has %d", got.Files, total.files)
		}
		if got.Bytes != total.bytes {
			t.Errorf("backup counted %d bytes, the tree has %d", got.Bytes, total.bytes)
		}
		if got.Bytes < 60<<20 || got.Bytes > 100<<20 {
			t.Errorf("revision of %d bytes, want from 60 to 100 MiB", got.Bytes)
		}
		s.expected[n] = want
		s.backups[n] = got
		s.last = n
		layers := ""
		if len(s.tree.notes) > 0 {
			layers = strings.Join(s.tree.notes, ", ") + "; "
		}
		s.note(t, fmt.Sprintf("r%d backup: %s%d files, %d bytes, %d new chunks, %s",
			n, layers, got.Files, got.Bytes, got.NewChunks, got.Duration))
	})
	if !ok {
		t.FailNow()
	}
}

// newChunks checks the deduplication of r2 against r1: its backup uploaded fewer chunks
// than r2 references.
func (s *scenario) newChunks(t *testing.T) {
	repo := s.patched.open(t, s.access)
	out, err := repo.run("check", "-a", "-tabular")
	must(t, err)
	chunks, err := tabular(out).chunks(2)
	must(t, err)
	got := s.backups[2].NewChunks
	if got < 1 || got >= chunks {
		t.Errorf("r2 uploaded %d new chunks, want from 1 to %d", got, chunks-1)
	}
	s.note(t, fmt.Sprintf("r2 new chunks: %d of the %d chunks of r2", got, chunks))
}

// freshPrune checks that prune of mega-doub finds nothing to delete among fresh
// revisions.
func (s *scenario) freshPrune(t *testing.T) {
	got := s.pruneRecord(t)
	if !got.NothingToDelete {
		t.Errorf("prune deleted %d chunks of fresh revisions, want nothing", got.Deleted)
	}
}

// prune checks that prune of mega-doub works after the exclusive prune.
func (s *scenario) prune(t *testing.T) {
	s.pruneRecord(t)
}

// pruneRecord runs prune of mega-doub and returns its record "prune completed".
func (s *scenario) pruneRecord(t *testing.T) pruneCompleted {
	t.Helper()
	recs, err := s.mega.run("prune")
	must(t, err)
	var got pruneCompleted
	must(t, recs.decode("prune completed", &got))
	s.note(t, fmt.Sprintf("%sprune: nothing_to_delete=%t, deleted=%d", s.phase,
		got.NothingToDelete, got.Deleted))
	return got
}

// exclusivePrune removes r2 with prune -r 2 -exclusive of the patched CLI.
func (s *scenario) exclusivePrune(t *testing.T) {
	repo := s.patched.open(t, s.access)
	_, err := repo.run("prune", "-r", "2", "-exclusive")
	must(t, err)
	s.removed[2] = true
}

// restoreAll restores every revision with every CLI, each in its own subtest.
func (s *scenario) restoreAll(t *testing.T) {
	t.Helper()
	for n := 1; n <= s.last; n++ {
		for _, cli := range s.clis {
			c := restoreCase{cli: cli, revision: n}
			name := fmt.Sprintf("%sr%d/restore/%s", s.phase, n, cli.name)
			t.Run(name, func(t *testing.T) { s.restore(t, c) })
		}
	}
}

// restore restores a revision into a new empty directory and compares the tree with the
// manifest of the revision; a removed revision must fail to restore.
func (s *scenario) restore(t *testing.T, c restoreCase) {
	repo := c.cli.open(t, s.access)
	start := time.Now()
	_, err := repo.run("restore", "-r", strconv.Itoa(c.revision), "-ignore-owner")
	elapsed := time.Since(start).Round(time.Millisecond)
	if s.removed[c.revision] {
		want := fmt.Sprintf("Snapshot %s at revision %d does not exist",
			s.access.snapshotID, c.revision)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("restore of the removed revision %d: %v, want %q", c.revision,
				err, want)
		}
		return
	}
	must(t, err)
	got, err := scanTree(repo.dir, skipSettings)
	must(t, err)
	want := s.expected[c.revision]
	diff := want.diff(got)
	for i, line := range diff {
		if i == maxDiff {
			t.Errorf("and %d more differences", len(diff)-i)
			break
		}
		t.Error(line)
	}
	s.note(t, fmt.Sprintf("%sr%d restore %s: %s, %d directories with another mtime",
		s.phase, c.revision, c.cli.name, elapsed, want.dirTimes(got)))
}

// checkAll runs duplicacy check -a with every CLI, each in its own subtest.
func (s *scenario) checkAll(t *testing.T) {
	t.Helper()
	for _, cli := range s.clis {
		t.Run(s.phase+"check/"+cli.name, func(t *testing.T) {
			repo := cli.open(t, s.access)
			_, err := repo.run("check", "-a")
			must(t, err)
		})
	}
}

// note writes a line of the summary into the log of the test and into the file that
// scripts/tests/integration.sh prints after the tests.
func (s *scenario) note(t *testing.T, line string) {
	t.Helper()
	line = s.name + " " + line
	t.Log(line)
	if s.summary == "" {
		return
	}
	f, err := os.OpenFile(s.summary, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err == nil {
		_, err = fmt.Fprintln(f, line)
		err = errors.Join(err, f.Close())
	}
	if err != nil {
		t.Errorf("summary: %v", err)
	}
}

// must stops the test at the error of a step that the next steps need.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// output runs cmd and returns its stdout; a failure stops the test with both outputs.
func output(t *testing.T, cmd *exec.Cmd) []byte {
	t.Helper()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v: %s %s", strings.Join(cmd.Args, " "), err, bytes.TrimSpace(out),
			bytes.TrimSpace(stderr.Bytes()))
	}
	return out
}

// text runs cmd and returns its stdout without the surrounding white space.
func text(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	return strings.TrimSpace(string(output(t, cmd)))
}
