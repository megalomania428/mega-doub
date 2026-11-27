//go:build integration

// Package integration is documented in manifest.go.
package integration

// cspell:ignore clis duplicacy

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// pauseVariable is the variable MEGA_DOUB_IT_PAUSE=1 that stops the scenario at the
// pauses of a demonstration.
const pauseVariable = "MEGA_DOUB_IT_PAUSE"

// The pauses of the scenario in the order of their numbers.
const (
	pauseInit = iota + 1
	pauseR1
	pauseR3
	pauseExclusive
	pauseRestore
)

// pauseTitles are the names of the pauses; the title of the pause n is the element n-1.
var pauseTitles = [...]string{"init done", "r1 backup done", "r3 backup done",
	"exclusive prune done", "r4 upstream restore done"}

// pauseHint is printed before the pause waits for next or q.
const pauseHint = "next and Enter – continue, q and Enter – stop"

// pauser shows the pauses on a terminal and reads the key of the user there.
type pauser struct {
	in  *bufio.Reader
	out io.Writer
}

// pauseBlock is the text that a pause prints before it waits for a key.
type pauseBlock struct {
	number   int      // number of the pause from 1 to len(pauseTitles)
	done     []string // what has been done, the lines indented in the text
	paths    []string // directories and files as name: value
	commands []string // runs since the last pause
}

// commandLog keeps the command lines of the runs since the last pause.
type commandLog struct {
	lines []string
}

// shellLine is a run of a program in the form for copying: cd <dir> && NAME=*** <bin>
// <args>. The values of the variables are never shown.
type shellLine struct {
	dir  string
	vars []string // NAME=value of the variables that the run adds to the environment
	bin  string
	args []string
}

// restored is the restore of a revision that a pause shows.
type restored struct {
	revision    int
	cli         string
	dir         string // directory of the restore
	differences int    // lines of the difference from the manifest
}

// pauseRequested reports whether the value of MEGA_DOUB_IT_PAUSE asks for the pauses:
// the empty value does not and 1 does, any other value is an error.
func pauseRequested(value string) (bool, error) {
	switch value {
	case "":
		return false, nil
	case "1":
		return true, nil
	}
	return false, fmt.Errorf("%s=%q, want 1 or an empty value", pauseVariable, value)
}

// pauseOff returns why the requested pauses are off, or the empty string when they run:
// GitHub Actions has no user to press a key and the terminal must open.
func pauseOff(githubActions string, ttyErr error) string {
	if githubActions == "true" {
		return "GITHUB_ACTIONS=true"
	}
	if ttyErr != nil {
		return ttyErr.Error()
	}
	return ""
}

// newPauser returns the pauser of /dev/tty when MEGA_DOUB_IT_PAUSE=1 asks for the pauses,
// or nil. The pauses that are off give one warning, to the terminal or to the test log.
func newPauser(t *testing.T) *pauser {
	t.Helper()
	requested, err := pauseRequested(os.Getenv(pauseVariable))
	must(t, err)
	if !requested {
		return nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err == nil {
		t.Cleanup(func() { _ = tty.Close() })
	}
	reason := pauseOff(os.Getenv("GITHUB_ACTIONS"), err)
	if reason == "" {
		return &pauser{in: bufio.NewReader(tty), out: tty}
	}
	warning := pauseVariable + "=1: the pauses are off: " + reason
	if err == nil {
		_, _ = fmt.Fprintln(tty, warning)
	} else {
		t.Log(warning)
	}
	return nil
}

// wait prints the hint and reads lines: next goes on and q stops. An empty line is
// ignored because Enter pressed earlier waits in the terminal buffer; another word
// repeats the hint.
func (p *pauser) wait() (bool, error) {
	showHint := true
	for {
		if showHint {
			if _, err := fmt.Fprintln(p.out, pauseHint); err != nil {
				return false, err
			}
		}
		line, err := p.in.ReadString('\n')
		key := strings.TrimSpace(line)
		switch {
		case key == "q":
			return true, nil
		case err != nil:
			return false, err
		case key == "next":
			return false, nil
		}
		showHint = key != ""
	}
}

// add keeps the command line of a run.
func (l *commandLog) add(line string) {
	l.lines = append(l.lines, line)
}

// take returns the lines and empties the log.
func (l *commandLog) take() []string {
	lines := l.lines
	l.lines = nil
	return lines
}

// String returns the command line with every value of the variables masked as ***.
func (l shellLine) String() string {
	parts := []string{"cd", l.dir, "&&"}
	for _, variable := range l.vars {
		name, _, _ := strings.Cut(variable, "=")
		parts = append(parts, name+"=***")
	}
	parts = append(parts, l.bin)
	return strings.Join(append(parts, l.args...), " ")
}

// String returns the text of the pause without the hint. Done comes last, next to the
// hint: the list of commands is long and would push it off the screen.
func (b pauseBlock) String() string {
	var text strings.Builder
	fmt.Fprintf(&text, "\n== %d/%d %s ==\n", b.number, len(pauseTitles),
		pauseTitles[b.number-1])
	for _, section := range []struct {
		name  string
		lines []string
	}{
		{name: "Commands since the last pause:", lines: b.commands},
		{name: "Paths:", lines: b.paths},
		{name: "Done:", lines: b.done},
	} {
		text.WriteString(section.name + "\n")
		for _, line := range section.lines {
			text.WriteString("  " + line + "\n")
		}
	}
	return text.String()
}

// pause prints the block, waits for a key and, at q, stops the scenario: the later steps
// do not run, see halt.
func (s *scenario) pause(t *testing.T, b pauseBlock) {
	t.Helper()
	_, err := io.WriteString(s.pauser.out, b.String())
	must(t, err)
	stop, err := s.pauser.wait()
	must(t, err)
	if stop {
		s.stopped = fmt.Sprintf("stopped at pause %d/%d %s", b.number, len(pauseTitles),
			pauseTitles[b.number-1])
		t.Fatal(s.stopped)
	}
}

// halt ends the scenario after the step in which the user chose q at a pause; the pause
// stops only its own subtest, and the parent would go on with the next steps.
func (s *scenario) halt(t *testing.T) {
	t.Helper()
	if s.stopped != "" {
		t.FailNow()
	}
}

// block returns the pause number with the paths and the commands that every pause shows.
func (s *scenario) block(number int) pauseBlock {
	return pauseBlock{
		number: number,
		paths: []string{
			"work: " + s.cfg.dir,
			"copy_dir: " + s.tree.root,
			"state_dir: " + filepath.Join(s.cfg.dir, "state"),
			"config: " + s.mega.config,
			"storage.url: " + s.cfg.url,
		},
		commands: s.commands.take(),
	}
}

// initBlock is the pause after init: the preparation of the scenario and the result of
// init, the last record of mega-doub.
func (s *scenario) initBlock(recs records) pauseBlock {
	result := "no log records"
	if len(recs) > 0 {
		result = recs[len(recs)-1].msg
	}
	b := s.block(pauseInit)
	b.done = []string{"snapshot_id: " + s.access.snapshotID, "mega-doub: " + s.mega.bin}
	for _, cli := range s.clis {
		b.done = append(b.done, cli.name+": "+cli.bin)
	}
	b.done = append(b.done, "init: "+result, "config:")
	config := strings.TrimSuffix(s.cfg.yaml(), "\n")
	for _, line := range strings.Split(config, "\n") {
		b.done = append(b.done, "  "+line)
	}
	return b
}

// backupBlock is the pause after the backup of r1 or r3: the edits of the revisions since
// the last pause, which are r2 and r3 for r3, and the result of the backup.
func (s *scenario) backupBlock(n int) pauseBlock {
	number := pauseR1
	if n == 3 {
		number = pauseR3
	}
	got := s.backups[n]
	b := s.block(number)
	b.done = []string{"edits:"}
	for _, line := range s.tree.takeJournal() {
		b.done = append(b.done, "  "+line)
	}
	b.done = append(b.done, fmt.Sprintf("backup: revision %d, %d files, %d bytes, "+
		"%d new chunks, %s", got.Revision, got.Files, got.Bytes, got.NewChunks, got.Duration))
	return b
}

// pruneBlock is the pause after the exclusive prune: the removed revisions and the
// storage before and after it.
func (s *scenario) pruneBlock(before, after storageTotal) pauseBlock {
	var removed []string
	for _, n := range before.revisions {
		if !slices.Contains(after.revisions, n) {
			removed = append(removed, strconv.Itoa(n))
		}
	}
	b := s.block(pauseExclusive)
	b.done = []string{
		"removed revisions: " + strings.Join(removed, " "),
		"storage before: " + before.String(),
		"storage after: " + after.String(),
		fmt.Sprintf("difference: %+d chunks, %+d bytes", after.chunks-before.chunks,
			after.bytes-before.bytes),
	}
	return b
}

// restoreBlock is the pause after the restore of r4 by the upstream CLI: the directory
// of the restore and the result of the comparison with the manifest.
func (s *scenario) restoreBlock(r restored) pauseBlock {
	result := "the tree equals the manifest"
	if r.differences > 0 {
		result = fmt.Sprintf("%d differences from the manifest", r.differences)
	}
	b := s.block(pauseRestore)
	b.paths = append(b.paths, "restore dir: "+r.dir)
	b.done = []string{
		fmt.Sprintf("restored revision %d with %s", r.revision, r.cli),
		"comparison: " + result,
	}
	return b
}
