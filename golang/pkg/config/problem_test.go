package config

import (
	"errors"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v4"
)

// oddValue fails with a message that names a type in its middle.
type oddValue struct{}

func (*oddValue) UnmarshalYAML(*yaml.Node) error {
	return errors.New("value not found in type list")
}

// kindTarget is the structure the documents of Test_kindOf are decoded into.
type kindTarget struct {
	Templates yaml.Node `yaml:"templates"`
	Name      string    `yaml:"name"`
	Port      int       `yaml:"port"`
	Timeout   Duration  `yaml:"timeout"`
	Odd       oddValue  `yaml:"odd"`
}

// message returns the message of the first problem the library reports on the text.
func message(t *testing.T, text string) string {
	t.Helper()
	var target kindTarget
	err := yaml.Load([]byte(text), &target, newLoader().opts...)
	var loadErrs *yaml.LoadErrors
	if !errors.As(err, &loadErrs) || len(loadErrs.Errors) == 0 {
		t.Fatalf("yaml.Load() error = %v, want problems", err)
	}
	return loadErrs.Errors[0].Message
}

func Test_kindOf(t *testing.T) {
	type args struct {
		msg string
	}
	tests := []struct {
		name string
		args args
		// wantMsg is the exact message of the library, so a new version can not change
		// the kind silently.
		wantMsg string
		want    keyKind
		want1   string
	}{
		{
			name:    "unknown field",
			args:    args{msg: message(t, "---\nx: 1\n")},
			wantMsg: "field x not found in type config.kindTarget",
			want:    unknownKey,
			want1:   "x",
		},
		{
			name:    "field set twice",
			args:    args{msg: message(t, "---\ntemplates:\n  k: &k name\nname: a\n*k : b\n")},
			wantMsg: "field name already set in type config.kindTarget",
			want:    setKey,
			want1:   "name",
		},
		{
			name:    "repeated key",
			args:    args{msg: message(t, "---\nname: a\nname: b\n")},
			wantMsg: `mapping key "name" already defined at line 2`,
			want:    repeatedKey,
		},
		{
			name:    "wrong type",
			args:    args{msg: message(t, "---\nport: abc\n")},
			wantMsg: "cannot construct !!str `abc` into int",
			want:    valueProblem,
		},
		{
			name:    "bad duration",
			args:    args{msg: message(t, "---\ntimeout: 30\n")},
			wantMsg: "invalid duration: want a number with a unit, such as 30s",
			want:    valueProblem,
		},
		{
			name:    "type named in the middle",
			args:    args{msg: message(t, "---\nodd: x\n")},
			wantMsg: "value not found in type list",
			want:    valueProblem,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.args.msg != tt.wantMsg {
				t.Errorf("message = %q, want %q", tt.args.msg, tt.wantMsg)
			}
			got, got1 := kindOf(tt.args.msg)
			if got != tt.want {
				t.Errorf("kindOf() got = %v, want %v", got, tt.want)
			}
			if got1 != tt.want1 {
				t.Errorf("kindOf() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}

func TestProblem_String(t *testing.T) {
	type fields struct {
		File    string
		Line    int
		Column  int
		Path    string
		Message string
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{name: "everything", fields: fields{File: "/etc/m.yaml", Line: 2, Column: 3,
			Path: "global.timeout", Message: "bad"}, want: "/etc/m.yaml:2:3: global.timeout: bad"},
		{name: "no path", fields: fields{File: "/etc/m.yaml", Line: 2, Column: 3,
			Message: "bad"}, want: "/etc/m.yaml:2:3: bad"},
		{name: "no position", fields: fields{File: "/etc/m.yaml", Path: "global",
			Message: "bad"}, want: "/etc/m.yaml: global: bad"},
		{name: "no file", fields: fields{Line: 2, Column: 3, Path: "global",
			Message: "bad"}, want: "2:3: global: bad"},
		{name: "position only", fields: fields{Line: 2, Column: 3, Message: "bad"},
			want: "2:3: bad"},
		{name: "path only", fields: fields{Path: "global", Message: "bad"},
			want: "global: bad"},
		{name: "message only", fields: fields{Message: "bad"}, want: "bad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Problem{
				File:    tt.fields.File,
				Line:    tt.fields.Line,
				Column:  tt.fields.Column,
				Path:    tt.fields.Path,
				Message: tt.fields.Message,
			}
			if got := p.String(); got != tt.want {
				t.Errorf("Problem.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_newError(t *testing.T) {
	const file = "/etc/m.yaml"
	type args struct {
		file     string
		problems []Problem
	}
	tests := []struct {
		name string
		args args
		want *Error
	}{
		{
			// The problems without a position go last in the order they were found, the
			// same position keeps the order of the input.
			name: "order",
			args: args{file: file, problems: []Problem{
				{Message: "first without a position"},
				{Line: 3, Column: 1, Path: "b", Message: "later"},
				{Line: 2, Column: 5, Path: "a", Message: "earlier"},
				{Message: "second without a position"},
				{Line: 3, Column: 1, Path: "c", Message: "later too"},
			}},
			want: &Error{Problems: []Problem{
				{File: file, Line: 2, Column: 5, Path: "a", Message: "earlier"},
				{File: file, Line: 3, Column: 1, Path: "b", Message: "later"},
				{File: file, Line: 3, Column: 1, Path: "c", Message: "later too"},
				{File: file, Message: "first without a position"},
				{File: file, Message: "second without a position"},
			}},
		},
		{
			// A problem repeats only with the same position, path and message.
			name: "repeated",
			args: args{file: file, problems: []Problem{
				{Line: 2, Column: 1, Path: "a", Message: "bad"},
				{Line: 2, Column: 1, Path: "b", Message: "bad"},
				{Line: 2, Column: 1, Path: "a", Message: "bad"},
			}},
			want: &Error{Problems: []Problem{
				{File: file, Line: 2, Column: 1, Path: "a", Message: "bad"},
				{File: file, Line: 2, Column: 1, Path: "b", Message: "bad"},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newError(tt.args.file, tt.args.problems)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("newError() = %v, want %v", got, tt.want)
			}
		})
	}
}
