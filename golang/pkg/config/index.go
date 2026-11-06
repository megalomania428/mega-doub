// Package config is documented in config.go.
package config

import (
	"cmp"
	"encoding/base64"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"
)

// indexBudget is the largest number of node visits of the index, a node used through
// an alias or a merge key is visited at every use.
const indexBudget = 100000

// lookupPasses is the largest number of passes that look up the paths of the problems.
const lookupPasses = 8

var (
	// plainKey matches a mapping key written in a path without brackets.
	plainKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	// listIndex matches an index of a list without a sign and leading zeros.
	listIndex = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	// elementPath matches the element of targets or of their sources a path starts with.
	elementPath = regexp.MustCompile(`^targets\[(\d+)\](?:\.sources\[(\d+)\])?`)
)

// place is a key, a sequence element or the root at the path of its use.
type place struct {
	// key is the key node as written, an alias for an alias key, or the element node
	// as written for a sequence element. It is nil for the root.
	key *yaml.Node
	// value is the value node after the alias is resolved.
	value *yaml.Node
	// order is the position of the place in the walk.
	order int
}

// mark is a node position the library may report a problem at.
type mark struct {
	path string
	// owner is the path of the mapping that holds a key, name is the name of the key.
	owner, name  string
	line, column int
	key          bool
	// anchor marks the anchored node of an alias key, a candidate only for a value
	// problem without candidates of its own.
	anchor bool
	// source is the mapping as written that holds a key, flat marks a key the library
	// constructs while it flattens a merging mapping, before the values.
	source *yaml.Node
	flat   bool
	// bare marks a key without a name, a position where a fatal error stops and not a
	// candidate for a path. badKey marks such a key that is not null: the library
	// reports a problem of its value at it.
	bare, badKey bool
	// end is the index after the marks of the value of a key.
	end int
}

// nullKey is a null key the library drops silently.
type nullKey struct {
	Problem
	// order is the position in the walk the library sees the key at.
	order int
}

// index describes the nodes of the document by the path of their use. A node that
// comes from an alias or a merge key keeps the position of its declaration.
type index struct {
	places map[string]place
	// marks are in the order of the walk, which is the order the library constructs
	// the values in: the local keys first, then the merged ones.
	marks []mark
	nulls []nullKey
	// repeated holds the paths of the mappings with a repeated key, which the library
	// does not construct, with the marks of their uses.
	repeated map[string][]int
	// last is what the pass of the lookup before found the library does not construct,
	// the current pass takes it as known and finds it anew in next.
	last, next skips
	// active holds the collections on the way to the current node.
	active map[*yaml.Node]bool
	visits int
	// loops counts the marks of the aliases already on the way, which cost no visit.
	loops int
	// complete is false when the walk ran out of the budget.
	complete bool
}

// newIndex walks the root node of a document.
func newIndex(root *yaml.Node) *index {
	x := &index{places: map[string]place{}, repeated: map[string][]int{},
		active: map[*yaml.Node]bool{}, complete: true}
	x.visit("", nil, root)
	return x
}

// spend counts one visit and reports whether the budget allows it.
func (x *index) spend() bool {
	if x.complete {
		x.visits++
		x.complete = x.visits <= indexBudget
	}
	return x.complete
}

// loop records the marks of an alias that is already on the way: they cost no visit,
// but their number is bounded by the budget as well.
func (x *index) loop(marks ...mark) bool {
	if x.complete {
		x.loops += len(marks)
		x.complete = x.loops <= indexBudget
	}
	if x.complete {
		x.marks = append(x.marks, marks...)
	}
	return x.complete
}

// add records a mark if the budget allows one more visit.
func (x *index) add(m mark) bool {
	if !x.spend() {
		return false
	}
	x.marks = append(x.marks, m)
	return true
}

// at returns a mark of the node n at path.
func at(path string, n *yaml.Node) mark {
	return mark{path: path, line: n.Line, column: n.Column}
}

// visit indexes the value node n at path, the key is n itself for an element and nil
// for the root.
// An alias that is already on the way to the node is not expanded and costs no visit,
// its marks are bounded in loop.
func (x *index) visit(path string, key, n *yaml.Node) {
	value := resolve(n)
	cycle := x.active[value]
	if cycle {
		if !x.loop(at(path, n), at(path, value)) {
			return
		}
	} else if !x.add(at(path, n)) || value != n && !x.add(at(path, value)) {
		return
	}
	if _, ok := x.places[path]; !ok {
		x.places[path] = place{key: key, value: value, order: len(x.marks) - 1}
	}
	// The templates are checked where they are used, so their content has no paths.
	if cycle || path == "templates" {
		return
	}
	x.active[value] = true
	defer delete(x.active, value)
	switch value.Kind {
	case yaml.MappingNode:
		if repeats(value) {
			x.repeated[path] = append(x.repeated[path], len(x.marks)-1)
		}
		x.mapping(path, value, map[string]bool{}, hasMergeKey(value), len(x.marks)-1)
	case yaml.SequenceNode:
		for i, item := range value.Content {
			x.visit(path+"["+strconv.Itoa(i)+"]", item, item)
		}
	}
}

// mapping indexes the effective keys of the mapping m used at path as the library
// merges them: the local keys first, then the keys of each merged mapping in order
// that are not set yet. The keys of a higher priority are in set. A merging mapping is
// flattened before its values are constructed, so its null keys are seen at start, the
// position of its use in the walk.
func (x *index) mapping(path string, m *yaml.Node, set map[string]bool, merging bool,
	start int) {
	var merged []*yaml.Node
	var local []string
	for i := 0; i+1 < len(m.Content); i += 2 {
		key, value := m.Content[i], m.Content[i+1]
		if isMergeKey(key) {
			if !x.add(mark{path: path, owner: path, line: key.Line, column: key.Column,
				key: true, source: m, flat: merging}) {
				return
			}
			merged = append(merged, x.sources(path, value)...)
			continue
		}
		// A key that is not indexed or is overridden still costs a visit.
		name, ok := keyName(key)
		null := resolve(key).ShortTag() == "!!null"
		if !ok && !x.add(mark{path: path, owner: path, line: key.Line, column: key.Column,
			key: true, source: m, flat: merging, bare: true, badKey: !null}) ||
			ok && set[name] && !x.spend() {
			return
		}
		if !ok {
			if null {
				seen := len(x.marks) - 1
				if merging {
					seen = start
				}
				x.nulls = append(x.nulls, nullKey{Problem: Problem{Line: key.Line,
					Column: key.Column, Path: path, Message: "null key is not allowed"},
					order: seen})
			}
			continue
		}
		if set[name] {
			continue
		}
		child := joinKey(path, name)
		first := len(x.marks)
		if !x.add(mark{path: child, owner: path, name: name, line: key.Line,
			column: key.Column, key: true, source: m, flat: merging}) {
			return
		}
		if anchored := resolve(key); anchored != key && !x.add(mark{path: child, owner: path,
			name: name, line: anchored.Line, column: anchored.Column, key: true,
			anchor: true, source: m, flat: merging}) {
			return
		}
		x.visit(child, key, value)
		x.marks[first].end = len(x.marks)
		local = append(local, name)
	}
	for _, name := range local {
		set[name] = true
	}
	for _, source := range merged {
		if x.active[source] {
			continue
		}
		if repeats(source) {
			if !x.dropped(path, source) {
				return
			}
			continue
		}
		x.active[source] = true
		x.mapping(path, source, set, true, start)
		delete(x.active, source)
	}
}

// dropped marks the keys of the merged mapping m used at path that has a repeated key:
// the library drops it, so its keys have positions for the problems but set no values.
func (x *index) dropped(path string, m *yaml.Node) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		key := m.Content[i]
		// A repeated merge key is a problem of the mapping that holds it.
		if isMergeKey(key) {
			if !x.add(mark{path: path, owner: path, line: key.Line, column: key.Column,
				key: true, source: m, flat: true}) {
				return false
			}
			continue
		}
		name, ok := keyName(key)
		if !ok {
			if !x.spend() {
				return false
			}
			continue
		}
		if !x.add(mark{path: joinKey(path, name), owner: path, name: name, line: key.Line,
			column: key.Column, key: true, source: m, flat: true}) {
			return false
		}
	}
	return true
}

// repeats reports a mapping the library does not construct, as the owner or as a merged
// one: two of its keys have the same kind and value as written.
func repeats(m *yaml.Node) bool {
	type written struct {
		kind  yaml.Kind
		value string
	}
	seen := map[written]bool{}
	for i := 0; i < len(m.Content); i += 2 {
		k := written{m.Content[i].Kind, m.Content[i].Value}
		if seen[k] {
			return true
		}
		seen[k] = true
	}
	return false
}

// sources marks the value of a merge key used at path and returns the mappings it
// merges, a value that is not a mapping is left to the library to report.
func (x *index) sources(path string, value *yaml.Node) []*yaml.Node {
	items := []*yaml.Node{value}
	if value.Kind == yaml.SequenceNode {
		if !x.add(at(path, value)) {
			return nil
		}
		items = value.Content
	}
	var mappings []*yaml.Node
	for _, item := range items {
		// The mapping an alias gives is a visit of its own, as in visit, and an alias
		// that is already on the way costs no visit.
		source := resolve(item)
		if x.active[source] {
			if !x.loop(at(path, item)) {
				return nil
			}
		} else if !x.add(at(path, item)) || source != item && !x.spend() {
			return nil
		}
		if source.Kind == yaml.MappingNode {
			mappings = append(mappings, source)
		}
	}
	return mappings
}

// hasMergeKey reports a mapping with a merge key.
func hasMergeKey(m *yaml.Node) bool {
	for i := 0; i < len(m.Content); i += 2 {
		if isMergeKey(m.Content[i]) {
			return true
		}
	}
	return false
}

// isMergeKey reports a merge key by its tag: a quoted '<<' is an ordinary key.
func isMergeKey(key *yaml.Node) bool {
	return key.Kind == yaml.ScalarNode && key.ShortTag() == "!!merge"
}

// keyName returns the name of a key as the library constructs it: an alias key is
// resolved, a binary key is decoded. A key that is not a scalar or is null has no name.
func keyName(key *yaml.Node) (string, bool) {
	key = resolve(key)
	if key.Kind != yaml.ScalarNode {
		return "", false
	}
	switch key.ShortTag() {
	case "!!null":
		return "", false
	case "!!binary":
		data, err := base64.StdEncoding.DecodeString(key.Value)
		return string(data), err == nil
	}
	return key.Value, true
}

// joinKey appends a mapping key to path: a name of letters, digits, _ and - after a
// dot, any other name quoted in brackets.
func joinKey(path, name string) string {
	if !plainKey.MatchString(name) {
		return path + "[" + strconv.Quote(name) + "]"
	}
	if path == "" {
		return name
	}
	return path + "." + name
}

// joinPath appends a path relative to an element to the path of the element.
func joinPath(path, rel string) string {
	switch {
	case rel == "":
		return path
	case path == "" || strings.HasPrefix(rel, "["):
		return path + rel
	}
	return path + "." + rel
}

// segment is a mapping key or a list index of a path.
type segment struct {
	name    string
	index   int
	isIndex bool
}

// parsePath splits a path into segments strictly: names of letters, digits, _ and -
// joined by dots, list indexes and quoted keys in brackets right after a segment.
func parsePath(path string) ([]segment, bool) {
	var segments []segment
	for rest := path; rest != ""; {
		switch {
		case strings.HasPrefix(rest, `["`):
			quoted, err := strconv.QuotedPrefix(rest[1:])
			if err != nil {
				return nil, false
			}
			name, err := strconv.Unquote(quoted)
			tail, closed := strings.CutPrefix(rest[1+len(quoted):], "]")
			if err != nil || !closed {
				return nil, false
			}
			segments = append(segments, segment{name: name})
			rest = tail
		case strings.HasPrefix(rest, "["):
			digits, tail, closed := strings.Cut(rest[1:], "]")
			if !closed || !listIndex.MatchString(digits) {
				return nil, false
			}
			i, err := strconv.ParseInt(digits, 10, 0)
			if err != nil {
				return nil, false
			}
			segments = append(segments, segment{index: int(i), isIndex: true})
			rest = tail
		default:
			if len(segments) > 0 {
				var dot bool
				if rest, dot = strings.CutPrefix(rest, "."); !dot {
					return nil, false
				}
			}
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			if !plainKey.MatchString(rest[:end]) {
				return nil, false
			}
			segments = append(segments, segment{name: rest[:end]})
			rest = rest[end:]
		}
	}
	return segments, true
}

// buildPath writes segments the way the index does.
func buildPath(segments []segment) string {
	path := ""
	for _, s := range segments {
		if s.isIndex {
			path += "[" + strconv.Itoa(s.index) + "]"
		} else {
			path = joinKey(path, s.name)
		}
	}
	return path
}

// position is a line and a column of the document.
type position struct {
	line, column int
}

// skips holds what a pass of the lookup finds the library does not construct: the
// marks of the values of the fields set again, where a mapping does not hide its path
// by a repeated key, the paths of the unknown keys, which hide the keys under them, and
// those of the unknown keys and the mappings with a repeated key, which hide the values
// under them.
type skips struct {
	ignored          []bool
	unknown, skipped map[string]bool
}

// newSkips returns the empty skips of the marks of the index.
func (x *index) newSkips() skips {
	return skips{make([]bool, len(x.marks)), map[string]bool{}, map[string]bool{}}
}

// equal reports that the skips are the same as other.
func (s skips) equal(other skips) bool {
	return slices.Equal(s.ignored, other.ignored) && maps.Equal(s.unknown, other.unknown) &&
		maps.Equal(s.skipped, other.skipped)
}

// group holds the problems of one kind at one position of one scope.
type group struct {
	scope string
	at    position
	key   bool
	// name is the field the message names.
	name string
}

// assign finds the paths of the problems the library reports and orders the problems
// at one position: the root first, then the targets by index, then their sources, each
// one in the order of the walk. A problem gets the path of a node at its position among
// the nodes of its scope only when that is unambiguous: there are as many problems of
// its group as candidates, then the i-th problem in the order the library constructs
// gets the path of the i-th candidate of the walk. A false path is worse than none, so
// an incomplete index gives no paths at all. The keys go first, so the values the
// library skips are not candidates for the value problems.
func (x *index) assign(issues []issue) []Problem {
	order := make([]int, len(issues))
	for i := range order {
		order[i] = math.MaxInt
	}
	var unmade map[string]int
	if x.complete {
		unmade = x.lookup(issues, order)
	} else {
		for i := range issues {
			issues[i].Path = ""
		}
	}
	for i, is := range issues {
		if p, ok := x.places[is.Path]; ok && !is.lookup && x.complete {
			order[i] = p.order
		}
	}
	sorted := make([]int, len(issues))
	for i := range sorted {
		sorted[i] = i
	}
	slices.SortStableFunc(sorted, func(a, b int) int {
		return cmp.Or(slices.Compare(rank(issues[a].scope), rank(issues[b].scope)),
			cmp.Compare(order[a], order[b]))
	})
	problems := make([]Problem, 0, len(issues))
	for _, i := range sorted {
		// A null key under a value the library does not construct is not seen by it, an
		// incomplete index does not tell those values.
		dropped := issues[i].null && (!x.complete ||
			underFrom(issues[i].Path, issues[i].seen, unmade) ||
			covered(issues[i].Path, x.isRepeated))
		if !dropped {
			problems = append(problems, issues[i].Problem)
		}
	}
	return problems
}

// lookup sets the paths of the problems to look up and their order in the walk, it
// returns the paths of the values the library does not construct: the skipped ones and
// those with a problem, with the position in the walk they are not constructed from:
// the library constructs the first value of a field set again, not the next one.
// Those values and the unknown keys are known only from the problems, so the passes
// repeat until one finds the same skips as the pass before; when they do not settle, no
// problem gets a path.
func (x *index) lookup(issues []issue, order []int) map[string]int {
	paths := make([]string, len(issues))
	for i := range issues {
		paths[i] = issues[i].Path
	}
	reset := func() {
		for i := range issues {
			issues[i].Path, order[i] = paths[i], math.MaxInt
		}
	}
	x.last = x.newSkips()
	var unmade map[string]int
	for range lookupPasses {
		reset()
		x.next = x.newSkips()
		unmade = x.pass(issues, order)
		if x.next.equal(x.last) {
			return unmade
		}
		x.last = x.next
	}
	reset()
	return unmade
}

// pass is one pass of lookup with the skips from the pass before.
func (x *index) pass(issues []issue, order []int) map[string]int {
	// The marks are grouped by the position and the scope, so a template used in many
	// elements costs a look at each use once.
	type spot struct {
		at    position
		scope string
	}
	at, badKeys := map[spot][]int{}, map[spot]bool{}
	for i, m := range x.marks {
		s := spot{position{m.line, m.column}, scopeOf(m.path)}
		at[s] = append(at[s], i)
		badKeys[s] = badKeys[s] || m.badKey
	}
	unmade := map[string]int{}
	skip := func(paths map[string]bool, kind keyKind, m mark) {
		for _, path := range x.skippedPaths(kind, m) {
			paths[path] = true
		}
	}
	unmake := func(kind keyKind, k int) {
		from := 0
		if kind == setKey {
			from = k
		}
		for _, path := range x.skippedPaths(kind, x.marks[k]) {
			if old, ok := unmade[path]; !ok || from < old {
				unmade[path] = from
			}
		}
	}
	candidates := func(g group, key, anchor bool) []int {
		var found []int
		for _, i := range at[spot{g.at, g.scope}] {
			m := x.marks[i]
			// A key in a mapping with a repeated key is where the library reports it, so
			// only an unknown key hides the keys under it, and not a key of the same path.
			hidden := underAny(m.path, x.last.skipped)
			if g.key {
				hidden = below(m.path, x.last.unknown)
			}
			// The library does not construct the value of a field set again, so the marks
			// of that value are not candidates.
			if m.key == key && !m.bare && (anchor || !m.anchor) &&
				(g.name == "" || m.name == g.name) && !hidden && !x.last.ignored[i] &&
				!x.inRepeated(m) {
				found = append(found, i)
			}
		}
		return found
	}
	find := func(g group) []int {
		found := candidates(g, g.key, false)
		if !g.key && len(found) == 0 {
			found = candidates(g, true, true)
		}
		return found
	}
	for _, key := range []bool{true, false} {
		groups := map[group][]int{}
		var keys []group
		for i, is := range issues {
			if !is.lookup || is.Line == 0 || (is.kind != valueProblem) != key {
				continue
			}
			g := group{is.scope, position{is.Line, is.Column}, key, is.field}
			if _, ok := groups[g]; !ok {
				keys = append(keys, g)
			}
			groups[g] = append(groups[g], i)
		}
		// The keys under an unknown key and the value of a field set again are not
		// constructed, which hides candidates of any group, the pass after knows them.
		assigned := map[group][]int{}
		for _, g := range keys {
			found := find(g)
			// A value problem at a key that is not a string may be about the key or about
			// the mapping that starts at it.
			if len(found) != len(groups[g]) || !key && badKeys[spot{g.at, g.scope}] {
				continue
			}
			assigned[g] = found
			for k, i := range groups[g] {
				kind := issues[i].kind
				if kind == unknownKey {
					skip(x.next.unknown, kind, x.marks[found[k]])
				}
				if kind == setKey {
					for j := found[k] + 1; j < x.marks[found[k]].end; j++ {
						x.next.ignored[j] = true
					}
				}
				if kind == unknownKey || kind == repeatedKey {
					skip(x.next.skipped, kind, x.marks[found[k]])
				}
			}
		}
		for _, g := range keys {
			found, ok := assigned[g]
			if !ok {
				// Any candidate may hold the problem, so none of them is constructed.
				for _, k := range find(g) {
					unmake(issues[groups[g][0]].kind, k)
				}
				continue
			}
			for k, i := range groups[g] {
				issues[i].Path, order[i] = x.marks[found[k]].path, found[k]
				unmake(issues[i].kind, found[k])
			}
		}
	}
	return unmade
}

// inRepeated reports a mark in a mapping with a repeated key the library does not
// construct: a value at or under such a mapping, or a key of a mapping under one. The
// keys of such a mapping itself are where the library reports the repeated one.
func (x *index) inRepeated(m mark) bool {
	path := m.path
	if m.key {
		path = m.owner
	}
	segments, ok := parsePath(path)
	if len(x.repeated) == 0 || !ok {
		return len(x.repeated) > 0 && covered(path, x.isRepeated)
	}
	for i := range len(segments) + 1 {
		if x.isRepeated(buildPath(segments[:i])) && (!m.key || i < len(segments)) {
			return true
		}
	}
	return false
}

// isRepeated reports that the library meets a repeated key in the mapping at path: in
// one of its uses that is not in a value of a field set again.
func (x *index) isRepeated(path string) bool {
	for _, k := range x.repeated[path] {
		if k >= len(x.last.ignored) || !x.last.ignored[k] {
			return true
		}
	}
	return false
}

// skippedPaths returns the paths the library does not construct for a problem of the
// kind at the mark: the value at the mark; for a repeated key the mapping that holds it,
// none for a merged mapping, which the walk has dropped already.
func (x *index) skippedPaths(kind keyKind, m mark) []string {
	switch {
	case kind != repeatedKey:
		return []string{m.path}
	case x.places[m.owner].value == m.source:
		return []string{m.owner}
	}
	return nil
}

// stopAt returns the position in the walk where a fatal error at line and column of the
// element at scope stops the library: the start of the place of the error, the mapping
// for a merge key or a key the library flattens, a key without a name itself, the
// earliest one when there are several. When no node is at the
// position, it is the first node of the walk not before the error in the document, as
// the anchors come before their aliases, or the end of the walk.
func (x *index) stopAt(scope string, line, column int) int {
	stop, next := math.MaxInt, math.MaxInt
	for i, m := range x.marks {
		if !inScope(scope, m.path) {
			continue
		}
		if m.line == line && m.column == column {
			switch p, ok := x.places[m.path]; {
			case m.flat:
				stop = min(stop, x.places[m.owner].order)
			case m.bare:
				stop = min(stop, i)
			case ok:
				stop = min(stop, p.order)
			}
		} else if after := m.line > line || m.line == line && m.column > column; after {
			next = min(next, i)
		}
	}
	if stop != math.MaxInt {
		return stop
	}
	return next
}

// scopeOf returns the element of targets or of their sources that holds path, empty
// for the root.
func scopeOf(path string) string {
	return elementPath.FindString(path)
}

// rank orders the scopes: the root, the targets by index, then the sources by the
// index of the target and of the source.
func rank(scope string) []int {
	m := elementPath.FindStringSubmatch(scope)
	switch {
	case m == nil:
		return []int{0, 0, 0}
	case m[2] == "":
		i, _ := strconv.Atoi(m[1])
		return []int{1, i, 0}
	}
	i, _ := strconv.Atoi(m[1])
	j, _ := strconv.Atoi(m[2])
	return []int{2, i, j}
}

// under reports that path is prefix or a path inside it, every path is inside the root.
func under(path, prefix string) bool {
	return prefix == "" || path == prefix || strings.HasPrefix(path, prefix+".") ||
		strings.HasPrefix(path, prefix+"[")
}

// underAny reports that path is one of paths or inside one of them. It looks up the
// path and its ancestors, so many paths cost no more than a few.
func underAny(path string, paths map[string]bool) bool {
	return len(paths) > 0 && covered(path, func(p string) bool { return paths[p] })
}

// below reports that path is inside one of paths and is not one of them.
func below(path string, paths map[string]bool) bool {
	return len(paths) > 0 && covered(path, func(p string) bool {
		return p != path && paths[p]
	})
}

// underFrom reports that path is at or under one of paths, which are not constructed
// from their position in the walk, and at is not before that position.
func underFrom(path string, at int, paths map[string]int) bool {
	return len(paths) > 0 && covered(path, func(p string) bool {
		from, ok := paths[p]
		return ok && from <= at
	})
}

// covered reports that has holds path or a path that path is under. A path the index
// does not write is under the root only.
func covered(path string, has func(string) bool) bool {
	segments, ok := parsePath(path)
	if !ok {
		return has("") || has(path)
	}
	for i := range len(segments) + 1 {
		if has(buildPath(segments[:i])) {
			return true
		}
	}
	return false
}

// inScope reports that the node at path belongs to the element the library decoded at
// scope: the root holds the targets as nodes, a target holds its sources as nodes.
func inScope(scope, path string) bool {
	switch {
	case scope == "":
		return !strings.HasPrefix(path, "targets[")
	case strings.Contains(scope, ".sources["):
		return under(path, scope)
	}
	return under(path, scope) && !strings.HasPrefix(path, scope+".sources[")
}

// find returns the place at path.
func (x *index) find(path string) (place, bool) {
	if x == nil {
		return place{}, false
	}
	segments, ok := parsePath(path)
	if !ok {
		return place{}, false
	}
	p, ok := x.places[buildPath(segments)]
	return p, ok
}

// Has reports that the key or the element at path is in the document: given directly,
// by an alias or by a merge key. An explicit null is present.
func (l *Loaded) Has(path string) bool {
	_, ok := l.index.find(path)
	return ok
}

// HasValue reports that the key or the element at path is present and its value is
// not null after the alias is resolved.
func (l *Loaded) HasValue(path string) bool {
	p, ok := l.index.find(path)
	return ok && p.value.ShortTag() != "!!null"
}

// Pos returns the position of the key at path: the alias itself for an alias key, the
// element node for an element and 1:1 for the root. A key from a template has the
// position of its declaration.
func (l *Loaded) Pos(path string) (line, column int, ok bool) {
	return l.position(path, true)
}

// ValuePos returns the position of the value node at path after the alias is resolved,
// where the library reports a problem of the value: a value from a template has the
// position in the template. The root has 1:1.
func (l *Loaded) ValuePos(path string) (line, column int, ok bool) {
	return l.position(path, false)
}

// position returns the position of the key or of the value at path.
func (l *Loaded) position(path string, key bool) (line, column int, ok bool) {
	p, ok := l.index.find(path)
	switch {
	case !ok:
		return 0, 0, false
	case path == "":
		return 1, 1, true
	case key && p.key != nil:
		return p.key.Line, p.key.Column, true
	}
	return p.value.Line, p.value.Column, true
}

// sourceProblems places the problems the source j of the target i reports about itself:
// their paths are relative to the source and get its path as the prefix. The position
// is the value at the joined path, or the key of the nearest present ancestor when the
// path is not found.
func (l *Loaded) sourceProblems(i, j int, problems []Problem) []Problem {
	prefix := "targets[" + strconv.Itoa(i) + "].sources[" + strconv.Itoa(j) + "]"
	placed := make([]Problem, 0, len(problems))
	for _, p := range problems {
		p.Path = joinPath(prefix, p.Path)
		var ok bool
		if p.Line, p.Column, ok = l.ValuePos(p.Path); !ok {
			p.Line, p.Column, _ = l.Pos(l.ancestor(p.Path, prefix))
		}
		placed = append(placed, p)
	}
	return placed
}

// ancestor returns the nearest present ancestor of path, or prefix when there is none.
func (l *Loaded) ancestor(path, prefix string) string {
	segments, ok := parsePath(path)
	if !ok {
		return prefix
	}
	for n := len(segments) - 1; n > 0; n-- {
		if parent := buildPath(segments[:n]); l.Has(parent) {
			return parent
		}
	}
	return prefix
}
