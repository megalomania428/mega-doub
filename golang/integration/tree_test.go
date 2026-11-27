//go:build integration

package integration

// cspell:ignore journalf

import (
	"reflect"
	"testing"
)

func Test_tree_journalf(t *testing.T) {
	type fields struct {
		rev     int
		journal []string
	}
	type args struct {
		format string
		args   []any
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   []string
	}{
		{
			name:   "first line of a revision",
			fields: fields{rev: 1},
			args:   args{format: "synthetic/zeros.bin: written 4 MiB of zeros"},
			want:   []string{"r1: synthetic/zeros.bin: written 4 MiB of zeros"},
		},
		{
			name:   "next revision after the lines of the previous one",
			fields: fields{rev: 2, journal: []string{"r1: git archive HEAD~10 (a1b2c3d)"}},
			args:   args{format: "%s: appended %d KiB", args: []any{"synthetic/append.bin", 256}},
			want: []string{
				"r1: git archive HEAD~10 (a1b2c3d)",
				"r2: synthetic/append.bin: appended 256 KiB",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := &tree{
				rev:     tt.fields.rev,
				journal: tt.fields.journal,
			}
			tr.journalf(tt.args.format, tt.args.args...)
			if !reflect.DeepEqual(tr.journal, tt.want) {
				t.Errorf("tree.journalf() journal = %v, want %v", tr.journal, tt.want)
			}
		})
	}
}

func Test_tree_takeJournal(t *testing.T) {
	type fields struct {
		journal []string
	}
	tests := []struct {
		name   string
		fields fields
		want   []string
	}{
		{
			name:   "lines of two revisions",
			fields: fields{journal: []string{"r2: added", "r3: removed"}},
			want:   []string{"r2: added", "r3: removed"},
		},
		{
			name:   "empty journal",
			fields: fields{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := &tree{
				journal: tt.fields.journal,
			}
			if got := tr.takeJournal(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("tree.takeJournal() = %v, want %v", got, tt.want)
			}
			if tr.journal != nil {
				t.Errorf("tree.takeJournal() left the journal %v", tr.journal)
			}
		})
	}
}
