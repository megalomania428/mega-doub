package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"go.yaml.in/yaml/v4"
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

// named has keys of every kind of name.
const named = "---\n" +
	"templates:\n" +
	"  k: &k name\n" +
	"a:\n" +
	"  *k : 1\n" +
	"b:\n" +
	"  !!binary bmFtZQ==: 2\n" +
	"c:\n" +
	"  [x]: 3\n" +
	"  {x: y}: 4\n" +
	"  '<<': 5\n" +
	"d:\n" +
	"  null: 6\n" +
	"  ~: 7\n"

// loadText loads the text from a temporary file.
func loadText(t *testing.T, text string) *Loaded {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

// parseText returns the root node of the text.
func parseText(t *testing.T, text string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Load([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Content[0]
}

func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func mapping(content ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: content}
}

func alias(target *yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.AliasNode, Alias: target}
}

func mergeKey() *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!merge", Value: "<<"}
}

// fanOut returns a mapping of width keys whose values are aliases of the mapping one
// level lower, depth levels over a mapping of scalars.
func fanOut(width, depth int) *yaml.Node {
	level := mapping()
	for i := range width {
		level.Content = append(level.Content, scalar("k"+strconv.Itoa(i)), scalar("v"))
	}
	for range depth {
		next := mapping()
		for i := range width {
			next.Content = append(next.Content, scalar("k"+strconv.Itoa(i)), alias(level))
		}
		level = next
	}
	return level
}

// mergeFanOut returns a mapping that merges the mapping one level lower width times,
// depth levels over a mapping of one key.
func mergeFanOut(width, depth int) *yaml.Node {
	level := mapping(scalar("port"), scalar("1"))
	for range depth {
		list := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for range width {
			list.Content = append(list.Content, alias(level))
		}
		level = mapping(mergeKey(), list)
	}
	return level
}

// mergeMany returns a mapping of the local keys and values that merges base n times.
func mergeMany(base *yaml.Node, n int, local ...*yaml.Node) *yaml.Node {
	list := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for range n {
		list.Content = append(list.Content, alias(base))
	}
	return mapping(append(local, mergeKey(), list)...)
}

// keys returns a mapping of n keys named by prefix and their index, a null key for an
// empty prefix.
func keys(prefix string, n int) *yaml.Node {
	m := mapping()
	for i := range n {
		key := scalar(prefix + strconv.Itoa(i))
		if prefix == "" {
			key = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
		}
		m.Content = append(m.Content, key, scalar("v"))
	}
	return m
}

func Test_newIndex(t *testing.T) {
	aliasCycle := mapping(scalar("k"))
	aliasCycle.Content = append(aliasCycle.Content, alias(aliasCycle))
	mergeCycle := mapping(mergeKey())
	mergeCycle.Content = append(mergeCycle.Content, alias(mergeCycle), scalar("a"),
		scalar("1"))
	shared := mapping(scalar("k"), scalar("v"))
	// mergeLoop merges itself through many aliases.
	mergeLoop := mapping()
	mergeLoop.Content = mergeMany(mergeLoop, indexBudget).Content
	// loops are many uses of a mapping that merges itself through many aliases.
	loop := mapping()
	loop.Content = mergeMany(loop, 1000).Content
	loops := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	// valueLoops are many uses of a mapping that holds many aliases of itself.
	valueLoop := mapping(scalar("k"), &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
	valueLoops := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for range 1000 {
		loops.Content = append(loops.Content, alias(loop))
		valueLoop.Content[1].Content = append(valueLoop.Content[1].Content, alias(valueLoop))
		valueLoops.Content = append(valueLoops.Content, alias(valueLoop))
	}
	// The root and every key with a value cost a visit, as does a key that is a list,
	// while the value of such a key costs none.
	list := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	edge := keys("k", indexBudget/2-1)
	edge.Content = append(edge.Content, list, scalar("v"))
	over := keys("k", indexBudget/2-1)
	over.Content = append(over.Content, list, scalar("v"), list, scalar("w"))
	type args struct {
		root *yaml.Node
	}
	tests := []struct {
		name string
		args args
		// wantComplete reports that the walk stays in the budget.
		wantComplete bool
		// wantPaths are present, wantMissing are not.
		wantPaths   []string
		wantMissing []string
		// wantPlaces and wantVisits are the numbers of places and visits when not 0.
		wantPlaces int
		wantVisits int
		wantNulls  []Problem
	}{
		{
			name:      "aliases over the budget",
			args:      args{root: fanOut(10, 6)},
			wantPaths: []string{"k0", "k0.k0.k0.k0.k0.k0"},
		},
		{
			name: "merge keys over the budget",
			args: args{root: mergeFanOut(9, 8)},
		},
		{
			name: "null keys over the budget",
			args: args{root: mergeMany(keys("", 300), 400)},
		},
		{
			name: "overridden keys over the budget",
			args: args{root: mergeMany(keys("k", 300), 400, keys("k", 300).Content...)},
		},
		{
			// An alias and the mapping it gives are two visits.
			name: "merged aliases over the budget",
			args: args{root: mergeMany(mapping(), indexBudget/2+1)},
		},
		{
			name:         "merged aliases in the budget",
			args:         args{root: mergeMany(mapping(), 3)},
			wantComplete: true,
			// The root, the merge key, the list and two visits for each alias.
			wantVisits: 9,
		},
		{
			name:         "aliases in the budget",
			args:         args{root: fanOut(3, 3)},
			wantComplete: true,
			wantPaths:    []string{"k2.k1.k0.k2"},
			wantMissing:  []string{"k3", "k2.k1.k0.k2.k0"},
		},
		{
			name:         "alias cycle",
			args:         args{root: aliasCycle},
			wantComplete: true,
			wantPaths:    []string{"k"},
			wantMissing:  []string{"k.k"},
			// The root and the key, the alias already on the way costs no visit.
			wantVisits: 2,
		},
		{
			name:         "visits at the budget",
			args:         args{root: edge},
			wantComplete: true,
			wantVisits:   indexBudget,
		},
		{
			name:       "visits over the budget",
			args:       args{root: over},
			wantVisits: indexBudget + 1,
		},
		{
			// The aliases of the mapping on the way in its merge list cost no visit.
			name:         "merge cycle of many aliases",
			args:         args{root: mergeLoop},
			wantComplete: true,
			// The root, the merge key and the list.
			wantVisits: 3,
		},
		{
			// The aliases on the way cost no visit, but their marks are bounded.
			name: "merge cycles over the budget",
			args: args{root: mapping(scalar("t"), loops)},
		},
		{
			name: "value cycles over the budget",
			args: args{root: mapping(scalar("t"), valueLoops)},
		},
		{
			name:         "merge cycle",
			args:         args{root: mergeCycle},
			wantComplete: true,
			wantPaths:    []string{"a"},
		},
		{
			name: "template used twice",
			args: args{root: mapping(scalar("x"), alias(shared), scalar("y"),
				alias(shared))},
			wantComplete: true,
			wantPaths:    []string{"x.k", "y.k"},
		},
		{
			name:         "key names",
			args:         args{root: parseText(t, named)},
			wantComplete: true,
			wantPaths:    []string{"a.name", "b.name", `c["<<"]`, "d"},
			wantMissing:  []string{"templates.k", "c.x", `c["{x: y}"]`, "d.null", `d["~"]`},
			wantPlaces:   9,
			wantNulls: []Problem{
				{Line: 13, Column: 3, Path: "d", Message: "null key is not allowed"},
				{Line: 14, Column: 3, Path: "d", Message: "null key is not allowed"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newIndex(tt.args.root)
			if got.complete != tt.wantComplete {
				t.Errorf("newIndex() complete = %v, want %v", got.complete, tt.wantComplete)
			}
			if got.visits > indexBudget+1 || len(got.nulls) > indexBudget ||
				len(got.marks) > 2*indexBudget+2 {
				t.Errorf("newIndex() visits = %d, nulls = %d, marks = %d, over the budget",
					got.visits, len(got.nulls), len(got.marks))
			}
			for _, path := range tt.wantPaths {
				if _, ok := got.places[path]; !ok {
					t.Errorf("newIndex() has no %q", path)
				}
			}
			for _, path := range tt.wantMissing {
				if _, ok := got.places[path]; ok {
					t.Errorf("newIndex() has %q", path)
				}
			}
			if tt.wantVisits > 0 && got.visits != tt.wantVisits {
				t.Errorf("newIndex() visits = %d, want %d", got.visits, tt.wantVisits)
			}
			if tt.wantPlaces > 0 && len(got.places) != tt.wantPlaces {
				t.Errorf("newIndex() places = %v, want %d", got.places, tt.wantPlaces)
			}
			var nulls []Problem
			for _, n := range got.nulls {
				nulls = append(nulls, n.Problem)
			}
			// Over the budget the null keys are only bounded.
			if tt.wantComplete && !reflect.DeepEqual(nulls, tt.wantNulls) {
				t.Errorf("newIndex() nulls = %v, want %v", nulls, tt.wantNulls)
			}
		})
	}
}

func Test_joinKey(t *testing.T) {
	type args struct {
		path string
		name string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{name: "plain", args: args{path: "storage.keys", name: "b2_id-2"},
			want: "storage.keys.b2_id-2"},
		{name: "first", args: args{path: "", name: "storage"}, want: "storage"},
		{name: "dot", args: args{path: "storage.keys", name: "a.b"},
			want: `storage.keys["a.b"]`},
		{name: "space", args: args{path: "storage.keys", name: "a b"},
			want: `storage.keys["a b"]`},
		{name: "quote", args: args{path: "storage.keys", name: `a"]b`},
			want: `storage.keys["a\"]b"]`},
		{name: "unicode", args: args{path: "storage.keys", name: "ключ"},
			want: `storage.keys["ключ"]`},
		{name: "empty", args: args{path: "storage.keys", name: ""},
			want: `storage.keys[""]`},
		{name: "first quoted", args: args{path: "", name: "a.b"}, want: `["a.b"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinKey(tt.args.path, tt.args.name)
			if got != tt.want {
				t.Errorf("joinKey() = %v, want %v", got, tt.want)
			}
			// The path is parsed back to the same name.
			segments, ok := parsePath(got)
			if !ok || segments[len(segments)-1].name != tt.args.name {
				t.Errorf("parsePath(%q) = %v, %v, want the name %q", got, segments, ok,
					tt.args.name)
			}
		})
	}
}

func Test_joinPath(t *testing.T) {
	type args struct {
		path string
		rel  string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{name: "key", args: args{path: "targets[0].sources[1]", rel: "port"},
			want: "targets[0].sources[1].port"},
		{name: "nested key", args: args{path: "targets[0].sources[1]", rel: "a.b"},
			want: "targets[0].sources[1].a.b"},
		{name: "element", args: args{path: "targets[0].sources[1]", rel: "[2]"},
			want: "targets[0].sources[1][2]"},
		{name: "quoted key", args: args{path: "targets[0].sources[1]", rel: `["a.b"]`},
			want: `targets[0].sources[1]["a.b"]`},
		{name: "empty", args: args{path: "targets[0].sources[1]", rel: ""},
			want: "targets[0].sources[1]"},
		{name: "root", args: args{path: "", rel: "global"}, want: "global"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := joinPath(tt.args.path, tt.args.rel); got != tt.want {
				t.Errorf("joinPath() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_parsePath(t *testing.T) {
	type args struct {
		path string
	}
	tests := []struct {
		name  string
		args  args
		want  []segment
		want1 bool
	}{
		{name: "root", args: args{path: ""}, want1: true},
		{name: "keys and elements", args: args{path: "targets[10].sources[0].port"},
			want: []segment{{name: "targets"}, {index: 10, isIndex: true},
				{name: "sources"}, {isIndex: true}, {name: "port"}}, want1: true},
		{name: "quoted key", args: args{path: `storage["a\"b"].c`},
			want: []segment{{name: "storage"}, {name: `a"b`}, {name: "c"}}, want1: true},
		{name: "quoted key first", args: args{path: `["a b"]`},
			want: []segment{{name: "a b"}}, want1: true},
		{name: "list in a list", args: args{path: "a[1][2]"},
			want: []segment{{name: "a"}, {index: 1, isIndex: true},
				{index: 2, isIndex: true}}, want1: true},
		{name: "dot before a bracket", args: args{path: "a.[1]"}},
		{name: "leading zero", args: args{path: "a[01]"}},
		{name: "overflow", args: args{path: "a[99999999999999999999]"}},
		{name: "unicode name", args: args{path: "ключ"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1 := parsePath(tt.args.path)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parsePath() got = %v, want %v", got, tt.want)
			}
			if got1 != tt.want1 {
				t.Errorf("parsePath() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
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
		l          *Loaded
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
		l          *Loaded
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
		problems []Problem
	}
	tests := []struct {
		name string
		args args
		want []Problem
	}{
		{
			name: "problems of the source type",
			args: args{problems: l.Config.Targets[0].Sources[0].Validate()},
			want: []Problem{
				{Line: 21, Column: 9, Path: "targets[0].sources[0]", Message: "id is required"},
				{Line: 22, Column: 15, Path: "targets[0].sources[0].path",
					Message: "path must be absolute"},
			},
		},
		{
			name: "missing path",
			args: args{problems: []Problem{{Path: "extra.deep", Message: "deep is wrong"}}},
			want: []Problem{{Line: 21, Column: 9, Path: "targets[0].sources[0].extra.deep",
				Message: "deep is wrong"}},
		},
		{
			name: "missing key of a present one",
			args: args{problems: []Problem{{Path: "path.deep", Message: "deep is wrong"}}},
			want: []Problem{{Line: 22, Column: 9, Path: "targets[0].sources[0].path.deep",
				Message: "deep is wrong"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := l.sourceProblems(tt.args.i, tt.args.j, tt.args.problems)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Loaded.sourceProblems() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_underAny(t *testing.T) {
	paths := map[string]bool{"targets[1]": true, `storage.keys["a.b"]`: true}
	type args struct {
		path  string
		paths map[string]bool
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{name: "no paths", args: args{path: "global"}},
		{name: "the root holds all", args: args{path: "global",
			paths: map[string]bool{"": true}}, want: true},
		{name: "same path", args: args{path: "targets[1]", paths: paths}, want: true},
		{name: "inside", args: args{path: "targets[1].sources[0].url", paths: paths},
			want: true},
		{name: "quoted key", args: args{path: `storage.keys["a.b"].x`, paths: paths},
			want: true},
		{name: "same prefix of text", args: args{path: "targets[10]", paths: paths}},
		{name: "ancestor", args: args{path: "targets", paths: paths}},
		{name: "not a path", args: args{path: "targets[1].", paths: paths}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := underAny(tt.args.path, tt.args.paths); got != tt.want {
				t.Errorf("underAny() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_index_assign(t *testing.T) {
	const (
		twice   = "---\nglobal:\n  timeout: &t x\nvault:\n  timeout: *t\n"
		skipped = "---\nglobal:\n  timeout: &t x\n  bogus: *t\n"
		repeat  = "---\nglobal:\n  timeout: &t x\nvault:\n  timeout: *t\n  timeout: 1s\n"
	)
	value := func(n int) []issue {
		issues := make([]issue, n)
		for i := range issues {
			issues[i] = issue{Problem: Problem{Line: 3, Column: 12, Message: "bad value"},
				lookup: true}
		}
		return issues
	}
	unknown := issue{Problem: Problem{Line: 4, Column: 3,
		Message: "field bogus not found in type config.Global"}, lookup: true,
		kind: unknownKey, field: "bogus"}
	bad := Problem{Line: 3, Column: 12, Message: "bad value"}
	type args struct {
		issues []issue
	}
	tests := []struct {
		name string
		text string
		args args
		want []Problem
	}{
		{
			name: "as many problems as candidates",
			text: twice,
			args: args{issues: value(2)},
			want: []Problem{
				{Line: 3, Column: 12, Path: "global.timeout", Message: "bad value"},
				{Line: 3, Column: 12, Path: "vault.timeout", Message: "bad value"},
			},
		},
		{
			name: "fewer problems than candidates",
			text: twice,
			args: args{issues: value(1)},
			want: []Problem{bad},
		},
		{
			name: "more problems than candidates",
			text: twice,
			args: args{issues: value(3)},
			want: []Problem{bad, bad, bad},
		},
		{
			name: "value of an unknown key",
			text: skipped,
			args: args{issues: append(value(1), unknown)},
			want: []Problem{
				{Line: 3, Column: 12, Path: "global.timeout", Message: "bad value"},
				{Line: 4, Column: 3, Path: "global.bogus", Message: unknown.Message},
			},
		},
		{
			name: "value in a mapping with a repeated key",
			text: repeat,
			args: args{issues: append(value(1), issue{Problem: Problem{Line: 6, Column: 3,
				Message: `mapping key "timeout" already defined at line 5`}, lookup: true,
				kind: repeatedKey})},
			want: []Problem{
				{Line: 3, Column: 12, Path: "global.timeout", Message: "bad value"},
				{Line: 6, Column: 3, Path: "vault.timeout",
					Message: `mapping key "timeout" already defined at line 5`},
			},
		},
		{
			name: "field of another name",
			text: skipped,
			args: args{issues: []issue{{Problem: unknown.Problem, lookup: true,
				kind: unknownKey, field: "other"}}},
			want: []Problem{unknown.Problem},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newIndex(parseText(t, tt.text))
			if got := x.assign(tt.args.issues); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("index.assign() = %v, want %v", got, tt.want)
			}
		})
	}
}
