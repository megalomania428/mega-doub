// Package config is documented in config.go.
package config

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"
)

// Problem is one configuration problem. Line and Column start at 1, zero means that
// the position is unknown. Path is the path of the key or the element at the place of
// its use, such as targets[0].sources[1].port or storage.keys["a.b"], it is empty when
// it is unknown.
type Problem struct {
	File    string
	Line    int
	Column  int
	Path    string
	Message string
}

// String formats the problem as <file>:<line>:<column>: <path>: <message>, without
// the file, the position and the path when they are unknown.
func (p Problem) String() string {
	var b strings.Builder
	b.WriteString(p.File)
	if p.Line > 0 {
		if p.File != "" {
			b.WriteString(":")
		}
		b.WriteString(strconv.Itoa(p.Line) + ":" + strconv.Itoa(p.Column))
	}
	if p.Path != "" {
		b.WriteString(": " + p.Path)
	}
	b.WriteString(": " + p.Message)
	// A problem without a file and a position starts with its path or message.
	return strings.TrimPrefix(b.String(), ": ")
}

// Error holds all problems found in one configuration file.
type Error struct {
	Problems []Problem
}

// Error returns one line per problem.
func (e *Error) Error() string {
	lines := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		lines[i] = p.String()
	}
	return strings.Join(lines, "\n")
}

// newError sets the file of the problems, drops the repeated ones and sorts the
// rest by position, the problems without a position go last.
func newError(file string, problems []Problem) *Error {
	seen := make(map[Problem]bool, len(problems))
	unique := make([]Problem, 0, len(problems))
	for _, p := range problems {
		p.File = file
		if !seen[p] {
			seen[p] = true
			unique = append(unique, p)
		}
	}
	sortProblems(unique)
	return &Error{Problems: unique}
}

// sortProblems sorts the problems by position in place: the problems without a
// position go last, the same position keeps the order of the input.
func sortProblems(problems []Problem) {
	slices.SortStableFunc(problems, func(a, b Problem) int {
		if (a.Line == 0) != (b.Line == 0) {
			return cmp.Compare(b.Line, a.Line)
		}
		return cmp.Or(cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column))
	})
}

// yamlProblems converts an error of the YAML library to problems.
func yamlProblems(err error) []Problem {
	switch e := err.(type) {
	case nil:
		return nil
	case *yaml.LoadErrors:
		problems := make([]Problem, 0, len(e.Errors))
		for _, le := range e.Errors {
			problems = append(problems, loadProblem(le))
		}
		return problems
	case *yaml.LoadError:
		return []Problem{loadProblem(e)}
	}
	return []Problem{{Message: err.Error()}}
}

// quotedValue matches the value the library quotes in a construction error. The tag
// may hold spaces and backticks, so everything from its first quote up to the last
// one goes.
var quotedValue = regexp.MustCompile("(cannot construct (?s:.*?)) `(?s:.*)` (into|as a) ")

// loadProblem converts one library error, the quoted value is dropped: values of the
// configuration stay out of messages.
func loadProblem(e *yaml.LoadError) Problem {
	msg := quotedValue.ReplaceAllString(e.Message, "$1 $2 ")
	if e.ContextMsg != "" {
		msg = e.ContextMsg + ": " + msg
	}
	return Problem{Line: e.Mark.Line, Column: e.Mark.Column, Message: msg}
}

// keyKind tells the problems the library reports at a key from those at a value.
type keyKind int

const (
	valueProblem keyKind = iota
	// unknownKey is a key out of the schema, the library skips its value.
	unknownKey
	// repeatedKey is a key given twice, the library skips the whole mapping.
	repeatedKey
	// setKey is another key that names a field already set.
	setKey
)

// Messages of the library about keys, anything else is about a value.
var (
	unknownField = regexp.MustCompile(`^field (.*) not found in type `)
	setField     = regexp.MustCompile(`^field (.+) already set in type `)
	repeated     = regexp.MustCompile(`^mapping key .+ already defined at line \d+$`)
)

// kindOf tells the kind of a problem by the message of the library and returns the
// field the message names.
func kindOf(msg string) (keyKind, string) {
	if m := unknownField.FindStringSubmatch(msg); m != nil {
		return unknownKey, m[1]
	}
	if m := setField.FindStringSubmatch(msg); m != nil {
		return setKey, m[1]
	}
	if repeated.MatchString(msg) {
		return repeatedKey, ""
	}
	return valueProblem, ""
}
