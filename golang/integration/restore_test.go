//go:build integration

package integration

// cspell:ignore duplicacy

import "testing"

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
