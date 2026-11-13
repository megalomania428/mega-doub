package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/megalomania428/mega-doub/pkg/config"
)

// indexed is the document of the tests of the index, one line of the text per line.
const indexed = "---\n" +
	"templates:\n" +
	"  base: &base\n" +
	"    transport: local\n" +
	"    port: 2200\n" +
	"  lvl: &lvl warn\n" +
	"  dirs: &dirs {copy_dir: /srv/copy}\n" +
	"global:\n" +
	"  <<: *dirs\n" +
	"  log_level: *lvl\n" +
	"  log_format: null\n" +
	"storage:\n" +
	"  keys:\n" +
	"    \"a.b\": 'plain:fake-dot'\n" +
	"    \"\": 'plain:fake-empty'\n" +
	"targets:\n" +
	"  - <<: *base\n" +
	"    name: one\n" +
	"    auth: [agent, key]\n" +
	"    sources:\n" +
	"      - type: test\n" +
	"        path: relative\n"

// merged has a key and a value from a merge key and an alias key with an alias value.
const merged = "---\n" +
	"templates:\n" +
	"  base: &base\n" +
	"    port: 22\n" +
	"  key: &key user\n" +
	"  val: &val root\n" +
	"targets:\n" +
	"  - <<: *base\n" +
	"    *key : *val\n" +
	"    name: a\n"

// elements has an element of a list that is an alias.
const elements = "---\n" +
	"templates:\n" +
	"  method: &method agent\n" +
	"targets:\n" +
	"  - name: a\n" +
	"    auth: [key, *method]\n"

// loadText loads the text from a temporary file.
func loadText(t *testing.T, text string) *config.Loaded {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestLoaded_Has(t *testing.T) {
	l := loadText(t, indexed)
	type args struct {
		path string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{name: "root", args: args{path: ""}, want: true},
		{name: "explicit key", args: args{path: "targets[0].name"}, want: true},
		{name: "key from a merge key", args: args{path: "targets[0].port"}, want: true},
		{name: "key through an alias", args: args{path: "global.log_level"}, want: true},
		{name: "explicit null", args: args{path: "global.log_format"}, want: true},
		{name: "missing key", args: args{path: "global.state_dir"}, want: false},
		{name: "element of a list", args: args{path: "targets[0].auth[1]"}, want: true},
		{name: "element out of a list", args: args{path: "targets[0].auth[2]"}, want: false},
		{name: "key with a dot", args: args{path: `storage.keys["a.b"]`}, want: true},
		{name: "empty key", args: args{path: `storage.keys[""]`}, want: true},
		{name: "plain key in quotes", args: args{path: `storage["keys"]`}, want: true},
		{name: "templates", args: args{path: "templates"}, want: true},
		{name: "content of templates", args: args{path: "templates.base"}, want: false},
		{name: "space before", args: args{path: " storage"}},
		{name: "space after", args: args{path: "storage "}},
		{name: "leading dot", args: args{path: ".storage"}},
		{name: "trailing dot", args: args{path: "storage."}},
		{name: "double dot", args: args{path: "storage..keys"}},
		{name: "plus index", args: args{path: "targets[+1]"}},
		{name: "minus index", args: args{path: "targets[-1]"}},
		{name: "leading zero", args: args{path: "targets[01]"}},
		{name: "overflow", args: args{path: "targets[99999999999999999999]"}},
		{name: "unclosed index", args: args{path: "targets[0"}},
		{name: "rest after a bracket", args: args{path: "targets[0]x"}},
		{name: "name of an element", args: args{path: "targets.0"}},
		{name: "index of a mapping", args: args{path: "storage[0]"}},
		{name: "name in a list", args: args{path: "targets.name"}},
		{name: "unclosed quote", args: args{path: `storage["keys]`}},
		{name: "rest after a quote", args: args{path: `storage["keys"x]`}},
		{name: "empty brackets", args: args{path: "storage[]"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := l.Has(tt.args.path); got != tt.want {
				t.Errorf("Loaded.Has() = %v, want %v", got, tt.want)
			}
			// The positions are found for the same paths.
			if _, _, ok := l.Pos(tt.args.path); ok != tt.want {
				t.Errorf("Loaded.Pos() ok = %v, want %v", ok, tt.want)
			}
			if _, _, ok := l.ValuePos(tt.args.path); ok != tt.want {
				t.Errorf("Loaded.ValuePos() ok = %v, want %v", ok, tt.want)
			}
		})
	}
}

func TestLoaded_HasValue(t *testing.T) {
	l := loadText(t, indexed)
	type args struct {
		path string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{name: "root", args: args{path: ""}, want: true},
		{name: "explicit key", args: args{path: "targets[0].name"}, want: true},
		{name: "key from a merge key", args: args{path: "global.copy_dir"}, want: true},
		{name: "key through an alias", args: args{path: "global.log_level"}, want: true},
		{name: "explicit null", args: args{path: "global.log_format"}, want: false},
		{name: "missing key", args: args{path: "global.state_dir"}, want: false},
		{name: "element of a list", args: args{path: "targets[0].auth[1]"}, want: true},
		{name: "key with a dot", args: args{path: `storage.keys["a.b"]`}, want: true},
		{name: "invalid path", args: args{path: "storage..keys"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := l.HasValue(tt.args.path); got != tt.want {
				t.Errorf("Loaded.HasValue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoaded_Pos(t *testing.T) {
	l := loadText(t, indexed)
	m := loadText(t, merged)
	e := loadText(t, elements)
	type args struct {
		path string
	}
	tests := []struct {
		name       string
		l          *config.Loaded
		args       args
		wantLine   int
		wantColumn int
		wantOk     bool
	}{
		{name: "root", l: l, args: args{path: ""}, wantLine: 1, wantColumn: 1,
			wantOk: true},
		{name: "explicit key", l: l, args: args{path: "targets[0].name"}, wantLine: 18,
			wantColumn: 5, wantOk: true},
		{name: "key from a template", l: l, args: args{path: "targets[0].port"},
			wantLine: 5, wantColumn: 5, wantOk: true},
		{name: "key from a merge key", l: l, args: args{path: "global.copy_dir"},
			wantLine: 7, wantColumn: 16, wantOk: true},
		{name: "key through an alias", l: l, args: args{path: "global.log_level"},
			wantLine: 10, wantColumn: 3, wantOk: true},
		{name: "explicit null", l: l, args: args{path: "global.log_format"},
			wantLine: 11, wantColumn: 3, wantOk: true},
		{name: "element of a list", l: l, args: args{path: "targets[0].auth[1]"},
			wantLine: 19, wantColumn: 19, wantOk: true},
		{name: "key with a dot", l: l, args: args{path: `storage.keys["a.b"]`},
			wantLine: 14, wantColumn: 5, wantOk: true},
		{name: "missing key", l: l, args: args{path: "global.state_dir"}},
		{name: "merged key", l: m, args: args{path: "targets[0].port"}, wantLine: 4,
			wantColumn: 5, wantOk: true},
		{name: "alias key", l: m, args: args{path: "targets[0].user"}, wantLine: 9,
			wantColumn: 5, wantOk: true},
		{name: "key after an alias key", l: m, args: args{path: "targets[0].name"},
			wantLine: 10, wantColumn: 5, wantOk: true},
		{name: "alias element", l: e, args: args{path: "targets[0].auth[1]"}, wantLine: 6,
			wantColumn: 17, wantOk: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLine, gotColumn, gotOk := tt.l.Pos(tt.args.path)
			if gotLine != tt.wantLine {
				t.Errorf("Loaded.Pos() gotLine = %v, want %v", gotLine, tt.wantLine)
			}
			if gotColumn != tt.wantColumn {
				t.Errorf("Loaded.Pos() gotColumn = %v, want %v", gotColumn, tt.wantColumn)
			}
			if gotOk != tt.wantOk {
				t.Errorf("Loaded.Pos() gotOk = %v, want %v", gotOk, tt.wantOk)
			}
		})
	}
}

func TestLoaded_ValuePos(t *testing.T) {
	l := loadText(t, indexed)
	m := loadText(t, merged)
	e := loadText(t, elements)
	type args struct {
		path string
	}
	tests := []struct {
		name       string
		l          *config.Loaded
		args       args
		wantLine   int
		wantColumn int
		wantOk     bool
	}{
		{name: "root", l: l, args: args{path: ""}, wantLine: 1, wantColumn: 1,
			wantOk: true},
		{name: "explicit key", l: l, args: args{path: "targets[0].name"}, wantLine: 18,
			wantColumn: 11, wantOk: true},
		{name: "key from a template", l: l, args: args{path: "targets[0].port"},
			wantLine: 5, wantColumn: 11, wantOk: true},
		{name: "key from a merge key", l: l, args: args{path: "global.copy_dir"},
			wantLine: 7, wantColumn: 26, wantOk: true},
		{name: "key through an alias", l: l, args: args{path: "global.log_level"},
			wantLine: 6, wantColumn: 8, wantOk: true},
		{name: "explicit null", l: l, args: args{path: "global.log_format"},
			wantLine: 11, wantColumn: 15, wantOk: true},
		{name: "element of a list", l: l, args: args{path: "targets[0].auth[1]"},
			wantLine: 19, wantColumn: 19, wantOk: true},
		{name: "mapping", l: l, args: args{path: "storage.keys"}, wantLine: 14,
			wantColumn: 5, wantOk: true},
		{name: "element mapping", l: l, args: args{path: "targets[0]"}, wantLine: 17,
			wantColumn: 5, wantOk: true},
		{name: "missing key", l: l, args: args{path: "global.state_dir"}},
		{name: "merged value", l: m, args: args{path: "targets[0].port"}, wantLine: 4,
			wantColumn: 11, wantOk: true},
		{name: "alias value", l: m, args: args{path: "targets[0].user"}, wantLine: 6,
			wantColumn: 8, wantOk: true},
		{name: "value after an alias key", l: m, args: args{path: "targets[0].name"},
			wantLine: 10, wantColumn: 11, wantOk: true},
		{name: "alias element", l: e, args: args{path: "targets[0].auth[1]"}, wantLine: 3,
			wantColumn: 11, wantOk: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLine, gotColumn, gotOk := tt.l.ValuePos(tt.args.path)
			if gotLine != tt.wantLine {
				t.Errorf("Loaded.ValuePos() gotLine = %v, want %v", gotLine, tt.wantLine)
			}
			if gotColumn != tt.wantColumn {
				t.Errorf("Loaded.ValuePos() gotColumn = %v, want %v", gotColumn, tt.wantColumn)
			}
			if gotOk != tt.wantOk {
				t.Errorf("Loaded.ValuePos() gotOk = %v, want %v", gotOk, tt.wantOk)
			}
		})
	}
}

func TestLoaded_sourceProblems(t *testing.T) {
	l := loadText(t, indexed)
	type args struct {
		i        int
		j        int
		problems []config.Problem
	}
	tests := []struct {
		name string
		args args
		want []config.Problem
	}{
		{
			name: "problems of the source type",
			args: args{problems: l.Config.Targets[0].Sources[0].Validate()},
			want: []config.Problem{
				{Line: 21, Column: 9, Path: "targets[0].sources[0]", Message: "id is required"},
				{Line: 22, Column: 15, Path: "targets[0].sources[0].path",
					Message: "path must be absolute"},
			},
		},
		{
			name: "missing path",
			args: args{problems: []config.Problem{{Path: "extra.deep",
				Message: "deep is wrong"}}},
			want: []config.Problem{{Line: 21, Column: 9,
				Path: "targets[0].sources[0].extra.deep", Message: "deep is wrong"}},
		},
		{
			name: "missing key of a present one",
			args: args{problems: []config.Problem{{Path: "path.deep",
				Message: "deep is wrong"}}},
			want: []config.Problem{{Line: 22, Column: 9,
				Path: "targets[0].sources[0].path.deep", Message: "deep is wrong"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := l.SourceProblems(tt.args.i, tt.args.j, tt.args.problems)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Loaded.sourceProblems() = %v, want %v", got, tt.want)
			}
		})
	}
}
