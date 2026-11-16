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
// the position is unknown. Path is the key path, it is empty for now.
type Problem struct {
	File    string
	Line    int
	Column  int
	Path    string
	Message string
}

// String formats the problem as <file>:<line>:<column>: <path>: <message>, without
// the position and the path when they are unknown.
func (p Problem) String() string {
	var b strings.Builder
	b.WriteString(p.File)
	if p.Line > 0 {
		b.WriteString(":" + strconv.Itoa(p.Line) + ":" + strconv.Itoa(p.Column))
	}
	if p.Path != "" {
		b.WriteString(": " + p.Path)
	}
	b.WriteString(": " + p.Message)
	return b.String()
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
	slices.SortStableFunc(unique, func(a, b Problem) int {
		if (a.Line == 0) != (b.Line == 0) {
			return cmp.Compare(b.Line, a.Line)
		}
		return cmp.Or(cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column))
	})
	return &Error{Problems: unique}
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
