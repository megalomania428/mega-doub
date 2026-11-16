package config

import (
	"reflect"
	"testing"

	"go.yaml.in/yaml/v4"
)

func Test_loader_loadDocument(t *testing.T) {
	// A list has no repeated keys to check, so the library decodes a long one quickly.
	keep := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for range indexBudget {
		keep.Content = append(keep.Content, scalar("1:7"))
	}
	large := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{
		Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{scalar("prune"), {
			Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{scalar("keep"), keep},
		}},
	}}}
	// The unknown key is indexed before the budget runs out, still it gets no path, and
	// the null key in its value is not reported.
	bogus := scalar("bogus")
	bogus.Line, bogus.Column = 2, 1
	null := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null", Line: 3,
		Column: 3}
	exhausted := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{
		Kind: yaml.MappingNode, Tag: "!!map", Content: append([]*yaml.Node{bogus, {
			Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{null, scalar("1")},
		}}, large.Content[0].Content...),
	}}}
	// The null key is the only problem, and an incomplete index drops it.
	nullOnly := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{
		Kind: yaml.MappingNode, Tag: "!!map", Content: append([]*yaml.Node{null,
			scalar("1")}, large.Content[0].Content...),
	}}}
	small := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{
		Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{scalar("storage"), {
			Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{scalar("keys"),
				fanOut(1, 0)},
		}},
	}}}
	type args struct {
		doc *yaml.Node
	}
	tests := []struct {
		name string
		args args
		// wantLoaded reports a loaded configuration.
		wantLoaded bool
		want1      []Problem
	}{
		{
			name:  "too large to index",
			args:  args{doc: large},
			want1: []Problem{{Line: 1, Column: 1, Message: "configuration is too large to index"}},
		},
		{
			name: "no paths over the budget",
			args: args{doc: exhausted},
			want1: []Problem{{Line: 2, Column: 1,
				Message: "field bogus not found in type config.configFile"}},
		},
		{
			name:  "only null keys over the budget",
			args:  args{doc: nullOnly},
			want1: []Problem{{Line: 1, Column: 1, Message: "configuration is too large to index"}},
		},
		{
			name:       "in the budget",
			args:       args{doc: small},
			wantLoaded: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1 := newLoader().loadDocument(tt.args.doc)
			if (got != nil) != tt.wantLoaded {
				t.Errorf("loader.loadDocument() got = %v, want a configuration %v", got,
					tt.wantLoaded)
			}
			if !reflect.DeepEqual(got1, tt.want1) {
				t.Errorf("loader.loadDocument() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}
