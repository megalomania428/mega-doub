// cspell:ignore doub

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/megalomania428/mega-doub/internal/cli"
	"github.com/megalomania428/mega-doub/internal/testsource"
	"github.com/megalomania428/mega-doub/pkg/config"
	"github.com/megalomania428/mega-doub/pkg/logging"
)

func init() {
	testsource.Register()
}

// record is one decoded JSON log record.
type record map[string]any

// decodeRecords decodes the JSON log records written to b.
func decodeRecords(t *testing.T, b *bytes.Buffer) []record {
	t.Helper()
	var records []record
	for dec := json.NewDecoder(b); dec.More(); {
		var r record
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("decode log record: %v", err)
		}
		records = append(records, r)
	}
	return records
}

// place is the path and the position of a problem record.
type place struct {
	Path         string
	Line, Column float64
}

// globalPlaces returns the places of the problem records of global and global.retry,
// the records of the other sections are left out.
func globalPlaces(records []record) []place {
	var places []place
	for _, r := range records {
		path, _ := r["path"].(string)
		if r["msg"] != "invalid configuration" ||
			path != "global" && !strings.HasPrefix(path, "global.") {
			continue
		}
		line, _ := r["line"].(float64)
		column, _ := r["column"].(float64)
		places = append(places, place{Path: path, Line: line, Column: column})
	}
	return places
}

// warningPlaces returns the places of the warning records of vault and storage.
func warningPlaces(records []record) []place {
	var places []place
	for _, r := range records {
		path, _ := r["path"].(string)
		if r["msg"] != "configuration warning" ||
			!strings.HasPrefix(path, "vault.") && !strings.HasPrefix(path, "storage.") {
			continue
		}
		line, _ := r["line"].(float64)
		column, _ := r["column"].(float64)
		places = append(places, place{Path: path, Line: line, Column: column})
	}
	return places
}

// values are the places of global-values.yaml.
var values = []place{
	{"global.instance", 8, 13},
	{"global.parallel_targets", 9, 21},
	{"global.parallel_sources", 10, 21},
	{"global.progress", 11, 13},
	{"global.timeout", 12, 12},
}

// secrets are the URLs and the secret references of the files of the tests, they never
// appear in the records.
var secrets = []string{"b2://values-bucket", "MEGA_DOUB_VALUES_STORAGE_PASSWORD",
	"b2://example-bucket", "MEGA_DOUB_EXAMPLE", "vault.example.invalid",
	"fake-warn-password", "MEGA_DOUB_WARN"}

func Test_handle_validate(t *testing.T) {
	const (
		invalid = "../../testdata/invalid/global-values.yaml"
		example = "../../testdata/example.yaml"
	)
	mixed := filepath.Join(t.TempDir(), "mixed.yaml")
	if err := os.WriteFile(mixed, []byte("---\nbogus: 1\nglobal:\n  parallel_targets: 0\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	// warned has the warnings of vault.address at 3:12 and of storage.url at 5:8.
	warned := filepath.Join(t.TempDir(), "warned.yaml")
	if err := os.WriteFile(warned, []byte("---\nvault:\n"+
		"  address: 'http://vault.example.invalid'\nstorage:\n"+
		"  url: 'sftp://u:fake-warn-password@h.example.invalid/x'\n"+
		"  snapshot_id: warn-main\n  password: 'env:MEGA_DOUB_WARN_STORAGE_PASSWORD'\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
		// cancel cancels the context after the handler, as a signal during the command.
		cancel bool
		want   int
		// wantPlaces are the problem records of global and global.retry in their order.
		wantPlaces []place
		// wantWarnings are the warning records of vault and storage in their order.
		wantWarnings []place
		// wantTail are the msg of the last records in their order.
		wantTail []string
	}{
		{name: "problems", path: invalid, want: 2, wantPlaces: values,
			wantTail: []string{"invalid configuration"}},
		{name: "valid", path: example, want: 0,
			wantTail: []string{"configuration is valid"}},
		{name: "warnings", path: warned, want: 0,
			wantWarnings: []place{{"vault.address", 3, 12}, {"storage.url", 5, 8}},
			wantTail:     []string{"configuration is valid"}},
		{name: "loading error first", path: mixed, want: 2,
			wantTail: []string{"invalid configuration"}},
		{name: "problems and then canceled", path: invalid, cancel: true, want: 4,
			wantPlaces: values, wantTail: []string{"invalid configuration", "interrupted"}},
		{name: "valid and then canceled", path: example, cancel: true, want: 4,
			wantTail: []string{"configuration is valid", "interrupted"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			handler := func(ctx context.Context, inv cli.Invocation, env cli.Env) int {
				code := handle(ctx, inv, env)
				if tt.cancel {
					stop()
				}
				return code
			}
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			args := []string{"config", "validate", "--log-level", "debug", "--log-format",
				"json", "-c", tt.path}
			got := cli.Execute(ctx, args, stdout, stderr, cli.Build{}, handler)
			if got != tt.want {
				t.Errorf("cli.Execute() = %v, want %v", got, tt.want)
			}
			text := stderr.String()
			for _, s := range secrets {
				if strings.Contains(text, s) {
					t.Errorf("cli.Execute() stderr contains %q: %s", s, text)
				}
			}
			records := decodeRecords(t, stderr)
			if len(records) == 0 {
				t.Fatal("cli.Execute() wrote no records")
			}
			if got := globalPlaces(records); !reflect.DeepEqual(got, tt.wantPlaces) {
				t.Errorf("cli.Execute() problem records = %v, want %v", got, tt.wantPlaces)
			}
			if got := warningPlaces(records); !reflect.DeepEqual(got, tt.wantWarnings) {
				t.Errorf("cli.Execute() warning records = %v, want %v", got, tt.wantWarnings)
			}
			file, err := filepath.Abs(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			valid, interrupted := 0, 0
			for _, r := range records {
				switch r["msg"] {
				case "invalid configuration", "configuration warning":
					if r["file"] != file {
						t.Errorf("cli.Execute() record file = %v, want %v", r["file"], file)
					}
				case "configuration is valid":
					if r["config"] != file {
						t.Errorf("cli.Execute() record config = %v, want %v", r["config"], file)
					}
					valid++
				case "interrupted":
					interrupted++
				case "context canceled":
					t.Errorf("cli.Execute() wrote a record %v", r)
				}
			}
			wantValid, wantInterrupted := 0, 0
			if slices.Contains(tt.wantTail, "configuration is valid") {
				wantValid = 1
			}
			if tt.cancel {
				wantInterrupted = 1
			}
			if valid != wantValid || interrupted != wantInterrupted {
				t.Errorf("cli.Execute() wrote %d valid and %d interrupted records, "+
					"want %d and %d", valid, interrupted, wantValid, wantInterrupted)
			}
			var tail []string
			if n := len(records) - len(tt.wantTail); n >= 0 {
				for _, r := range records[n:] {
					msg, _ := r["msg"].(string)
					tail = append(tail, msg)
				}
			}
			if !reflect.DeepEqual(tail, tt.wantTail) {
				t.Errorf("cli.Execute() last records msg = %v, want %v", tail, tt.wantTail)
			}
		})
	}
}

func Test_handle_validate_warnings(t *testing.T) {
	const full = "../../testdata/full.yaml"
	dir := t.TempDir()
	// unprefixed has a value without a prefix at 5:13 and a target without sources at 7:5.
	unprefixed := filepath.Join(dir, "unprefixed.yaml")
	// empty has no targets, the warning is at 1:1.
	empty := filepath.Join(dir, "empty.yaml")
	for path, content := range map[string]string{
		unprefixed: "---\nstorage:\n  url: 'b2://unprefixed-bucket/prefix'\n" +
			"  snapshot_id: unprefixed-main\n  password: 'fake-unprefixed-password'\n" +
			"targets:\n  - name: db01\n    transport: local\n    sources: []\n",
		empty: "---\nstorage:\n  url: 'b2://empty-bucket/prefix'\n  snapshot_id: empty-main\n" +
			"  password: 'env:MEGA_DOUB_EMPTY_STORAGE_PASSWORD'\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// warning is a warning record: the path, the position and the message.
	type warning struct {
		Path         string
		Line, Column float64
		Warning      string
	}
	tests := []struct {
		name string
		path string
		want []warning
	}{
		{name: "full", path: full, want: []warning{
			{"storage.url", 29, 8, "url contains a password"},
			{"targets[0].password", 60, 15, "value without prefix is treated as plain"},
		}},
		{name: "value without prefix and no sources", path: unprefixed, want: []warning{
			{"storage.password", 5, 13, "value without prefix is treated as plain"},
			{"targets[0]", 7, 5, `target "db01" has no sources`},
		}},
		{name: "no targets", path: empty, want: []warning{
			{"targets", 1, 1, "no targets configured"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			args := []string{"config", "validate", "--log-level", "debug", "--log-format",
				"json", "-c", tt.path}
			if got := cli.Execute(t.Context(), args, stdout, stderr, cli.Build{},
				handle); got != 0 {
				t.Errorf("cli.Execute() = %v, want 0", got)
			}
			text := stderr.String()
			for _, s := range []string{"fake-", "MEGA_DOUB", "b2://"} {
				if strings.Contains(text, s) {
					t.Errorf("cli.Execute() stderr contains %q: %s", s, text)
				}
			}
			file, err := filepath.Abs(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			var got []warning
			for _, r := range decodeRecords(t, stderr) {
				if r["msg"] != "configuration warning" {
					continue
				}
				if r["level"] != "WARN" || r["file"] != file {
					t.Errorf("cli.Execute() warning record %v, want level WARN and file %v",
						r, file)
				}
				path, _ := r["path"].(string)
				line, _ := r["line"].(float64)
				column, _ := r["column"].(float64)
				msg, _ := r["warning"].(string)
				got = append(got, warning{Path: path, Line: line, Column: column, Warning: msg})
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("cli.Execute() warning records = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_logResult(t *testing.T) {
	const file = "/etc/mega-doub/main.yaml"
	result := config.Result{
		Problems: []config.Problem{
			{File: file, Line: 4, Column: 3, Path: "global.timeout", Message: "bad"},
		},
		Warnings: []config.Problem{
			{File: file, Line: 7, Column: 5, Path: "storage.url", Message: "first"},
			{File: file, Message: "second"},
		},
	}
	stderr := &bytes.Buffer{}
	logResult(logging.New(stderr, slog.LevelDebug, logging.FormatJSON), result)
	records := decodeRecords(t, stderr)
	for _, r := range records {
		delete(r, "time")
	}
	want := []record{
		{"level": "WARN", "msg": "configuration warning", "file": file, "line": 7.0,
			"column": 5.0, "path": "storage.url", "warning": "first"},
		{"level": "WARN", "msg": "configuration warning", "file": file, "warning": "second"},
		{"level": "ERROR", "msg": "invalid configuration", "file": file, "line": 4.0,
			"column": 3.0, "path": "global.timeout", "error": "bad"},
	}
	if !reflect.DeepEqual(records, want) {
		t.Errorf("logResult() records = %v, want %v", records, want)
	}
}
