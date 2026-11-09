package units

import "testing"

func TestParseSize(t *testing.T) {
	type args struct {
		s string
	}
	tests := []struct {
		name    string
		args    args
		want    int64
		wantErr bool
	}{
		{name: "plain", args: args{s: "1"}, want: 1},
		{name: "K suffix", args: args{s: "512K"}, want: 512 << 10},
		{name: "M suffix", args: args{s: "256M"}, want: 256 << 20},
		{name: "G suffix", args: args{s: "4G"}, want: 4 << 30},
		{name: "lower case g suffix", args: args{s: "4g"}, want: 4 << 30},
		{name: "T suffix", args: args{s: "1T"}, want: 1 << 40},
		{name: "largest T value", args: args{s: "8388607T"}, want: 8388607 << 40},
		{name: "letters", args: args{s: "abc"}, wantErr: true},
		{name: "minus sign", args: args{s: "-1"}, wantErr: true},
		{name: "plus sign", args: args{s: "+1"}, wantErr: true},
		{name: "fraction", args: args{s: "1.5G"}, wantErr: true},
		{name: "inner space", args: args{s: "1 G"}, wantErr: true},
		{name: "unknown suffix", args: args{s: "1P"}, wantErr: true},
		{name: "suffix only", args: args{s: "K"}, wantErr: true},
		{name: "empty", args: args{s: ""}, wantErr: true},
		{name: "number overflow", args: args{s: "9223372036854775808"}, wantErr: true},
		{name: "suffix overflow", args: args{s: "8388608T"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSize(tt.args.s)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseSize() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Errorf("ParseSize() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatSize(t *testing.T) {
	type args struct {
		n int64
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{name: "zero", args: args{n: 0}, want: "0"},
		{name: "bytes", args: args{n: 100}, want: "100"},
		{name: "not a whole K", args: args{n: 1025}, want: "1025"},
		{name: "K suffix", args: args{n: 1536 << 10}, want: "1536K"},
		{name: "M suffix", args: args{n: 256 << 20}, want: "256M"},
		{name: "G suffix", args: args{n: 4 << 30}, want: "4G"},
		{name: "T suffix", args: args{n: 8388607 << 40}, want: "8388607T"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatSize(tt.args.n)
			if got != tt.want {
				t.Errorf("FormatSize() = %q, want %q", got, tt.want)
			}
			back, err := ParseSize(got)
			if err != nil || back != tt.args.n {
				t.Errorf("ParseSize(%q) = %d, %v, want %d", got, back, err, tt.args.n)
			}
		})
	}
}
