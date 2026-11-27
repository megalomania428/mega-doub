//go:build integration

package integration

// cspell:ignore duplicacy

import (
	"reflect"
	"testing"
)

func Test_tabular_chunks(t *testing.T) {
	table := tabular(`
snap | rev |  | files | bytes | chunks | bytes | uniq | bytes | new | bytes |
it-1 |   1 | @ 2026-11-26 10:00 | 1000 | 70M | 20 | 71M | 3 | 10M | 20 | 71M |
it-1 |   2 | @ 2026-11-26 10:01 | 1001 | 71M | 21 | 72M | 2 | 5M | 4 | 6M |
it-1 | all |  |  |  | 24 | 77M | 24 | 77M |  |  |
`)
	type args struct {
		rev int
	}
	tests := []struct {
		name    string
		tb      tabular
		args    args
		want    int64
		wantErr bool
	}{
		{name: "first revision", tb: table, args: args{rev: 1}, want: 20},
		{name: "second revision", tb: table, args: args{rev: 2}, want: 21},
		{name: "missing revision", tb: table, args: args{rev: 3}, wantErr: true},
		{name: "no table", tb: tabular("Storage set to /srv\n"), args: args{rev: 1},
			wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.tb.chunks(tt.args.rev)
			if (err != nil) != tt.wantErr {
				t.Errorf("tabular.chunks() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("tabular.chunks() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_tabular_total(t *testing.T) {
	// The tables are the output of the patched CLI before and after prune -r 2 -exclusive.
	const (
		head   = "    snap | rev |                          | files |   bytes |"
		head2  = " chunks |   bytes | uniq |   bytes | new |   bytes |\n"
		rev1   = " it-live |   1 | @ 2026-11-27 06:52 -hash |     3 | 19,726K |"
		rev1b  = "      7 | 19,805K |    5 |  8,856K |   7 | 19,805K |\n"
		rev2   = " it-live |   2 | @ 2026-11-27 06:52       |     3 | 19,726K |"
		rev2b  = "      8 | 21,394K |    3 |      1K |   6 | 10,445K |\n"
		rev3   = " it-live |   3 | @ 2026-11-27 06:52       |     3 | 19,921K |"
		rev3b  = "      9 | 21,689K |    4 |    295K |   4 |    295K |\n"
		rev3c  = "      9 | 21,689K |    7 | 10,740K |   7 | 10,740K |\n"
		all    = " it-live | all |                          |       |         |"
		allOld = "     17 | 30,547K |   17 | 30,547K |     |         |\n"
		allNew = "     14 | 30,546K |   14 | 30,546K |     |         |\n"
		total  = "Storage set to /srv/storage\nTotal chunk size is 30,547K in 17 chunks\n\n"
	)
	tests := []struct {
		name    string
		tb      tabular
		want    storageTotal
		wantErr bool
	}{
		{
			name: "three revisions",
			tb: tabular(total + head + head2 + rev1 + rev1b + rev2 + rev2b + rev3 + rev3b +
				all + allOld),
			want: storageTotal{chunks: 17, bytes: 30547 << 10, revisions: []int{1, 2, 3}},
		},
		{
			name: "revision 2 removed",
			tb:   tabular(head + head2 + rev1 + rev1b + rev3 + rev3c + all + allNew),
			want: storageTotal{chunks: 14, bytes: 30546 << 10, revisions: []int{1, 3}},
		},
		{
			name: "bytes below a KiB",
			tb: tabular("it-1 | 1 |  | 1 | 9 | 1 | 512 | 1 | 512 | 1 | 512 |\n" +
				"it-1 | all |  |  |  | 1 | 512 | 1 | 512 |  |  |\n"),
			want: storageTotal{chunks: 1, bytes: 512, revisions: []int{1}},
		},
		{
			name:    "no row all",
			tb:      tabular("Storage set to /srv\n"),
			wantErr: true,
		},
		{
			name:    "chunks of the row all are not a number",
			tb:      tabular("it-1 | all |  |  |  | x | 512 | 1 | 512 |  |  |\n"),
			wantErr: true,
		},
		{
			name:    "bytes of the row all are not a number",
			tb:      tabular("it-1 | all |  |  |  | 1 | many | 1 | 512 |  |  |\n"),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.tb.total()
			if (err != nil) != tt.wantErr {
				t.Fatalf("tabular.total() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("tabular.total() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_storageTotal_String(t *testing.T) {
	type fields struct {
		chunks    int64
		bytes     int64
		revisions []int
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name:   "three revisions",
			fields: fields{chunks: 17, bytes: 31280128, revisions: []int{1, 2, 3}},
			want:   "17 chunks, 31280128 bytes, revisions 1 2 3",
		},
		{
			name:   "no revisions",
			fields: fields{},
			want:   "0 chunks, 0 bytes, revisions ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := storageTotal{
				chunks:    tt.fields.chunks,
				bytes:     tt.fields.bytes,
				revisions: tt.fields.revisions,
			}
			if got := st.String(); got != tt.want {
				t.Errorf("storageTotal.String() = %v, want %v", got, tt.want)
			}
		})
	}
}
