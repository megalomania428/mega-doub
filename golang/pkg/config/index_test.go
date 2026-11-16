package config

import (
	"reflect"
	"strconv"
	"testing"

	"go.yaml.in/yaml/v4"
)

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
