// cspell:ignore doub

package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megalomania428/mega-doub/pkg/config"
)

// ownPath tells whether the path belongs to the rules of global and global.retry, the
// problems of the other sections are not compared here.
func ownPath(path string) bool {
	return path == "global" || strings.HasPrefix(path, "global.")
}

func TestValidate(t *testing.T) {
	// want is one expected problem: the path, the position and a part of the message.
	type want struct {
		path         string
		line, column int
		msg          string
	}
	const invalid = "../../testdata/invalid/"
	tests := []struct {
		name string
		// path is a file of testdata, content is written to a temporary file instead.
		path    string
		content string
		want    []want
	}{
		{name: "example", path: "../../testdata/example.yaml"},
		{name: "full", path: "../../testdata/full.yaml"},
		{
			name: "global values",
			path: invalid + "global-values.yaml",
			want: []want{
				{"global.instance", 8, 13, `"a/b" must match ^[A-Za-z0-9_-]+$`},
				{"global.parallel_targets", 9, 21, "must be at least 1"},
				{"global.parallel_sources", 10, 21, "must be at least 1"},
				{"global.progress", 11, 13, `"foo" must be one of auto, always, never`},
				{"global.timeout", 12, 12, "must be greater than zero"},
			},
		},
		{
			name: "global directories",
			path: invalid + "global-dirs.yaml",
			want: []want{{"global.copy_dir", 9, 13, "must not be inside global.state_dir"}},
		},
		{
			name: "retry values",
			path: invalid + "retry-values.yaml",
			want: []want{{"global.retry.backoff_cap", 9, 3, "less than backoff_initial"}},
		},
		{
			// An empty value is left out of the message.
			name: "empty strings",
			content: "---\nglobal:\n  instance: ''\n  copy_dir: ''\n  state_dir: ''\n" +
				"  progress: ''\n",
			want: []want{
				{"global.instance", 3, 13, "must match"},
				{"global.copy_dir", 4, 13, "must be an absolute path"},
				{"global.state_dir", 5, 14, "must be an absolute path"},
				{"global.progress", 6, 13, "must be one of"},
			},
		},
		{
			name: "explicit null keeps the defaults",
			content: "---\nglobal:\n  instance: null\n  copy_dir: null\n" +
				"  parallel_targets: null\n  progress: null\n  timeout: null\n" +
				"  retry:\n    backoff_cap: null\n",
		},
		{
			// The computed directories of the instance are equal after cleaning; the
			// missing state_dir is reported at the key global and goes first.
			name:    "invalid instance with computed directories",
			content: "---\nglobal:\n  instance: ..\n",
			want: []want{
				{"global.state_dir", 2, 1, "must differ from global.copy_dir"},
				{"global.instance", 3, 13, `".." must match`},
			},
		},
		{
			// Both values are the anchor, the same position keeps the order of the fields.
			name:    "same position",
			content: "---\nglobal:\n  parallel_targets: &zero 0\n  parallel_sources: *zero\n",
			want: []want{
				{"global.parallel_targets", 3, 21, "must be at least 1"},
				{"global.parallel_sources", 3, 21, "must be at least 1"},
			},
		},
		{
			name: "value from a template",
			content: "---\ntemplates:\n  base: &base\n    parallel_sources: 0\nglobal:\n" +
				"  <<: *base\n",
			want: []want{{"global.parallel_sources", 4, 23, "must be at least 1"}},
		},
		{
			name: "cap below initial",
			content: "---\nglobal:\n  retry:\n    backoff_initial: 5s\n" +
				"    backoff_cap: 2s\n",
			want: []want{{"global.retry.backoff_cap", 5, 18, `effective "2s" < "5s"`}},
		},
		{
			name:    "initial above the default cap",
			content: "---\nglobal:\n  retry:\n    backoff_initial: 10s\n",
			want:    []want{{"global.retry.backoff_cap", 3, 3, `effective "7s" < "10s"`}},
		},
		{
			name: "zero cap selects the default",
			content: "---\nglobal:\n  retry:\n    backoff_initial: 10s\n" +
				"    backoff_cap: 0s\n",
			want: []want{{"global.retry.backoff_cap", 5, 18, `effective "7s" < "10s"`}},
		},
		{
			name: "null cap selects the default",
			content: "---\nglobal:\n  retry:\n    backoff_initial: 10s\n" +
				"    backoff_cap: null\n",
			want: []want{{"global.retry.backoff_cap", 5, 18, `effective "7s" < "10s"`}},
		},
		{
			name: "negative values select the defaults",
			content: "---\nglobal:\n  retry:\n    max_retries_time: -1s\n" +
				"    backoff_initial: -1s\n    backoff_step: 0s\n" +
				"    backoff_step_attempts: -1\n    backoff_cap: 0s\n",
		},
		{
			name: "zero values select the defaults",
			content: "---\nglobal:\n  retry:\n    max_retries_time: 0\n" +
				"    backoff_initial: 0s\n    backoff_step: -2s\n" +
				"    backoff_step_attempts: 0\n    backoff_cap: -1s\n",
		},
		{
			// 8 * 2^61 overflows uint64.
			name: "cycle overflow with the defaults",
			content: "---\nglobal:\n  retry:\n" +
				"    backoff_step_attempts: 2305843009213693952\n",
			want: []want{{"global.retry.backoff_step_attempts", 4, 28, "overflows int"}},
		},
		{
			// 2 * (2^62 - 1) is 2^63 - 2.
			name: "largest cycle",
			content: "---\nglobal:\n  retry:\n    backoff_step: 4s\n" +
				"    backoff_step_attempts: 4611686018427387903\n",
		},
		{
			// 2 * 2^62 is 2^63.
			name: "smallest overflowing cycle",
			content: "---\nglobal:\n  retry:\n    backoff_step: 4s\n" +
				"    backoff_step_attempts: 4611686018427387904\n",
			want: []want{{"global.retry.backoff_step_attempts", 5, 28, "overflows int"}},
		},
		{
			// The step above the cap leaves one block per cycle.
			name: "one block per cycle",
			content: "---\nglobal:\n  retry:\n    backoff_step: 10s\n" +
				"    backoff_step_attempts: 9223372036854775807\n",
		},
		{
			name:    "equal directories",
			content: "---\nglobal:\n  copy_dir: /srv/same\n  state_dir: /srv/same/\n",
			want:    []want{{"global.state_dir", 4, 14, "must differ from global.copy_dir"}},
		},
		{
			name: "state directory inside the copy directory after cleaning",
			content: "---\nglobal:\n  copy_dir: /srv/copy\n" +
				"  state_dir: /srv/other/../copy/state\n",
			want: []want{{"global.state_dir", 4, 14, "must not be inside global.copy_dir"}},
		},
		{
			name:    "common prefix",
			content: "---\nglobal:\n  copy_dir: /a/copy\n  state_dir: /a/copy2\n",
		},
		{
			name:    "letter case",
			content: "---\nglobal:\n  copy_dir: /A\n  state_dir: /a\n",
		},
		{
			name:    "root copy directory",
			content: "---\nglobal:\n  copy_dir: /\n  state_dir: /srv/state\n",
			want:    []want{{"global.state_dir", 4, 14, "must not be inside global.copy_dir"}},
		},
		{
			name:    "relative directory",
			content: "---\nglobal:\n  copy_dir: srv/copy\n",
			want:    []want{{"global.copy_dir", 3, 13, `"srv/copy" must be an absolute path`}},
		},
		{
			name:    "null copy directory inside the state directory",
			content: "---\nglobal:\n  copy_dir: null\n  state_dir: /var/lib/mega-doub\n",
			want:    []want{{"global.copy_dir", 3, 13, "must not be inside global.state_dir"}},
		},
		{
			name:    "missing copy directory inside the state directory",
			content: "---\nglobal:\n  state_dir: /var/lib/mega-doub\n",
			want:    []want{{"global.copy_dir", 2, 1, "must not be inside global.state_dir"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.path
			if tt.content != "" {
				path = filepath.Join(t.TempDir(), "main.yaml")
				if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			file, err := filepath.Abs(path)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := config.Load(path)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			result := config.Validate(loaded)
			checkErr(t, result)
			for _, w := range result.Warnings {
				if ownPath(w.Path) {
					t.Errorf("Validate() warning %v, want none", w)
				}
			}
			var got []config.Problem
			for _, p := range result.Problems {
				if ownPath(p.Path) {
					got = append(got, p)
				}
			}
			if len(got) != len(tt.want) {
				t.Fatalf("Validate() problems = %v, want %d", got, len(tt.want))
			}
			for i, w := range tt.want {
				p := got[i]
				if p.File != file || p.Path != w.path || p.Line != w.line ||
					p.Column != w.column || !strings.Contains(p.Message, w.msg) ||
					strings.Contains(p.Message, `""`) {
					t.Errorf("Validate() problem %d = %v, want %s:%d:%d: %s: …%s…", i, p,
						file, w.line, w.column, w.path, w.msg)
				}
			}
		})
	}
}

// checkErr checks that Err returns a true nil without problems and *Error with the
// same problems otherwise.
func checkErr(t *testing.T, result config.Result) {
	t.Helper()
	err := result.Err()
	if len(result.Problems) == 0 {
		if err != nil {
			t.Errorf("Result.Err() = %v, want nil", err)
		}
		return
	}
	var cfgErr *config.Error
	if !errors.As(err, &cfgErr) || len(cfgErr.Problems) != len(result.Problems) {
		t.Errorf("Result.Err() = %v, want *config.Error with %d problems", err,
			len(result.Problems))
	}
}
