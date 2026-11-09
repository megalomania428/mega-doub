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
