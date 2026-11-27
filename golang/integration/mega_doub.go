//go:build integration

// Package integration is documented in manifest.go.
package integration

// cspell:ignore doub duplicacy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// passwordVariable holds the storage password for the env: reference of the
// configuration.
const passwordVariable = "MEGA_DOUB_IT_PASSWORD"

// keyVariable returns the variable that holds the key name of storage.keys for its env:
// reference.
func keyVariable(name string) string {
	return "MEGA_DOUB_IT_KEY_" + strings.ToUpper(name)
}

// demo reports the demo mode MEGA_DOUB_IT_DEMO=1, in which the terminal shows the output
// of mega-doub with its progress bars.
func demo() bool {
	return os.Getenv("MEGA_DOUB_IT_DEMO") == "1"
}

// storageConfig is the configuration file of mega-doub for a scenario.
type storageConfig struct {
	dir        string   // work directory that holds copy and state
	url        string   // storage.url
	snapshotID string   // storage.snapshot_id
	keys       []string // sorted names of storage.keys
	demo       bool     // keep the progress bars of backup
}

// yaml returns the configuration as YAML. The password and the keys are env:
// references to passwordVariable and keyVariable.
func (c storageConfig) yaml() string {
	var b strings.Builder
	b.WriteString("---\nglobal:\n")
	fmt.Fprintf(&b, "  copy_dir: %s\n", strconv.Quote(filepath.Join(c.dir, "copy")))
	fmt.Fprintf(&b, "  state_dir: %s\n", strconv.Quote(filepath.Join(c.dir, "state")))
	if !c.demo {
		b.WriteString("  progress: []\n")
	}
	b.WriteString("storage:\n")
	fmt.Fprintf(&b, "  url: %s\n", strconv.Quote(c.url))
	fmt.Fprintf(&b, "  snapshot_id: %s\n", strconv.Quote(c.snapshotID))
	fmt.Fprintf(&b, "  password: %s\n", strconv.Quote("env:"+passwordVariable))
	if len(c.keys) > 0 {
		b.WriteString("  keys:\n")
		for _, name := range c.keys {
			fmt.Fprintf(&b, "    %s: %s\n", name, strconv.Quote("env:"+keyVariable(name)))
		}
	}
	b.WriteString("prune:\n  keep: ['1:7']\n")
	return b.String()
}

// record is a JSON log record of mega-doub.
type record struct {
	msg string
	raw json.RawMessage
}

// records are the JSON log records of a run in the order of the output.
type records []record

// backupCompleted is the record "backup completed".
type backupCompleted struct {
	Revision  int    `json:"revision"`
	Files     int64  `json:"files"`
	Bytes     int64  `json:"bytes"`
	NewChunks int64  `json:"new_chunks"`
	Duration  string `json:"duration"`
}

// pruneCompleted is the record "prune completed".
type pruneCompleted struct {
	NothingToDelete bool  `json:"nothing_to_delete"`
	Deleted         int64 `json:"deleted"`
}

// parseRecords finds the JSON records among the lines of out; the other lines, such as
// the progress bars of the demo mode, are left out.
func parseRecords(out []byte) records {
	var recs records
	for line := range bytes.Lines(out) {
		line = bytes.TrimSpace(line)
		var head struct {
			Msg string `json:"msg"`
		}
		if len(line) == 0 || line[0] != '{' || json.Unmarshal(line, &head) != nil {
			continue
		}
		recs = append(recs, record{msg: head.Msg, raw: bytes.Clone(line)})
	}
	return recs
}

// decode puts the last record with the message msg into v.
func (r records) decode(msg string, v any) error {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i].msg == msg {
			return json.Unmarshal(r[i].raw, v)
		}
	}
	return fmt.Errorf("no record %q", msg)
}

// megaDoub runs the mega-doub binary with the configuration of a scenario.
type megaDoub struct {
	bin      string
	config   string
	env      []string
	vars     []string    // NAME=value of the variables that the scenario adds to env
	tty      io.Writer   // terminal of the demo mode, nil without it
	commands *commandLog // runs since the last pause
}

// run runs the command with JSON log records and returns the records of its stderr. A
// failed run gives an error with the whole stderr.
func (m *megaDoub) run(command string) (records, error) {
	args := []string{"--config", m.config, "--log-format", "json", command}
	cmd := exec.Command(m.bin, args...)
	cmd.Env = m.env
	dir, _ := os.Getwd()
	m.commands.add(shellLine{dir: dir, vars: m.vars, bin: m.bin, args: args}.String())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if m.tty != nil {
		_, _ = fmt.Fprintf(m.tty, "\n$ mega-doub %s\n", command)
		cmd.Stderr = io.MultiWriter(&stderr, quiet{w: m.tty})
	}
	err := cmd.Run()
	recs := parseRecords(stderr.Bytes())
	if err != nil {
		out := bytes.TrimSpace(stderr.Bytes())
		return recs, fmt.Errorf("mega-doub %s: %w: %s", command, err, out)
	}
	return recs, nil
}

// quiet writes to w and drops the errors, so that a lost terminal does not fail a run.
type quiet struct {
	w io.Writer
}

// Write writes p to the terminal and reports success.
func (q quiet) Write(p []byte) (int, error) {
	_, _ = q.w.Write(p)
	return len(p), nil
}

// megaDoubBinary returns the binary of MEGA_DOUB_BIN, an absolute path, or builds the
// binary of the module into a temporary directory of the test.
func megaDoubBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("MEGA_DOUB_BIN"); bin != "" {
		if !filepath.IsAbs(bin) {
			t.Fatalf("MEGA_DOUB_BIN=%s is not an absolute path", bin)
		}
		return bin
	}
	bin := filepath.Join(t.TempDir(), "mega-doub")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/mega-doub")
	// The tests run in golang/integration, the module is its parent.
	cmd.Dir = ".."
	output(t, cmd)
	return bin
}

// environ returns the environment of the test without the variables of duplicacy and
// of the retries of mega-doub, which change what a run does, followed by add.
func environ(add ...string) []string {
	var env []string
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if strings.HasPrefix(name, "DUPLICACY_") || strings.HasPrefix(name, "BACKOFF_") ||
			name == "MAX_RETRIES_TIME" {
			continue
		}
		env = append(env, variable)
	}
	return append(env, add...)
}

// demoTerminal returns the terminal for the output of mega-doub when the demo mode is on,
// or nil outside the demo mode and without a terminal.
func demoTerminal(t *testing.T, on bool) io.Writer {
	t.Helper()
	if !on {
		return nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		t.Logf("demo mode without a terminal: %v", err)
		return nil
	}
	t.Cleanup(func() { _ = tty.Close() })
	return tty
}
