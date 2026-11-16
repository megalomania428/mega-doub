package config

import (
	"reflect"
	"testing"

	"go.yaml.in/yaml/v4"

	"github.com/megalomania428/mega-doub/pkg/secret"
)

// visit is a reference the walk passes with its path.
type visit struct {
	path string
	ref  secret.Ref
}

// walkedInner holds one reference.
type walkedInner struct {
	Inner secret.Ref `yaml:"inner"`
}

// walked has a field of every kind the walk passes or skips.
type walked struct {
	Untagged secret.Ref
	hidden   secret.Ref
	Tagged   secret.Ref            `yaml:"tagged"`
	Flagged  secret.Ref            `yaml:",omitempty"`
	Skipped  secret.Ref            `yaml:"-"`
	Empty    secret.Ref            `yaml:"empty"`
	Embedded walkedInner           `yaml:",inline"`
	Pointer  *walkedInner          `yaml:"pointer"`
	Nil      *walkedInner          `yaml:"nil"`
	Keys     map[string]secret.Ref `yaml:"keys"`
	Values   []any                 `yaml:"values"`
	Node     yaml.Node             `yaml:"node"`
}

func Test_walkRefs(t *testing.T) {
	w := walked{
		Untagged: "plain:fake-no-tag",
		hidden:   "plain:fake-hidden",
		Tagged:   "env:FAKE_TAGGED",
		Flagged:  "file:/fake/flagged",
		Skipped:  "plain:fake-skipped",
		Embedded: walkedInner{Inner: "vault:fake/embedded/key"},
		Pointer:  &walkedInner{Inner: "plain:fake-pointer"},
		Keys: map[string]secret.Ref{"b": "plain:fake-b", "a": "plain:fake-a",
			"B": "plain:fake-upper", "": "plain:fake-empty-key", "a.b": "plain:fake-dot",
			"e": ""},
		Values: []any{walkedInner{Inner: "plain:fake-value"}, secret.Ref("plain:fake-ref"),
			&walkedInner{Inner: "plain:fake-value-pointer"}, nil},
	}
	// The walk would not end in the cycle of the node.
	w.Node.Alias = &w.Node
	type args struct {
		v    reflect.Value
		path string
	}
	tests := []struct {
		name string
		args args
		want []visit
		// wantKept maps the references the walk must not change to their values.
		wantKept map[*secret.Ref]secret.Ref
	}{
		{
			name: "fields, mappings and interfaces",
			args: args{v: reflect.ValueOf(&w).Elem()},
			want: []visit{
				{"untagged", "plain:fake-no-tag"},
				{"tagged", "env:FAKE_TAGGED"},
				{"flagged", "file:/fake/flagged"},
				{"inner", "vault:fake/embedded/key"},
				{"pointer.inner", "plain:fake-pointer"},
				{`keys[""]`, "plain:fake-empty-key"},
				{"keys.B", "plain:fake-upper"},
				{"keys.a", "plain:fake-a"},
				{`keys["a.b"]`, "plain:fake-dot"},
				{"keys.b", "plain:fake-b"},
				{"values[0].inner", "plain:fake-value"},
				{"values[1]", "plain:fake-ref"},
				{"values[2].inner", "plain:fake-value-pointer"},
			},
			wantKept: map[*secret.Ref]secret.Ref{&w.Skipped: "plain:fake-skipped",
				&w.hidden: "plain:fake-hidden"},
		},
		{
			name: "path of a nested value",
			args: args{v: reflect.ValueOf(&walkedInner{Inner: "env:FAKE_NESTED"}).Elem(),
				path: "targets[0]"},
			want: []visit{{"targets[0].inner", "env:FAKE_NESTED"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []visit
			walkRefs(tt.args.v, tt.args.path, func(path string, ref *secret.Ref) {
				got = append(got, visit{path, *ref})
				*ref = secret.Ref("plain:changed-" + path)
			})
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("walkRefs() visits = %v, want %v", got, tt.want)
			}
			// A second walk sees every change the first one stored.
			var changed []visit
			walkRefs(tt.args.v, tt.args.path, func(path string, ref *secret.Ref) {
				changed = append(changed, visit{path, *ref})
			})
			want := make([]visit, 0, len(tt.want))
			for _, v := range tt.want {
				want = append(want, visit{v.path, secret.Ref("plain:changed-" + v.path)})
			}
			if !reflect.DeepEqual(changed, want) {
				t.Errorf("walkRefs() visits after the change = %v, want %v", changed, want)
			}
			for ref, value := range tt.wantKept {
				if *ref != value {
					t.Errorf("walkRefs() changed a skipped reference to %q, want %q",
						string(*ref), string(value))
				}
			}
		})
	}
}
