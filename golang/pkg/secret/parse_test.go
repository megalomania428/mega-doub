package secret

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	type args struct {
		ref Ref
	}
	tests := []struct {
		name    string
		args    args
		want    Parsed
		wantErr bool
	}{
		{
			name: "plain",
			args: args{ref: "plain:fake-value"},
			want: Parsed{Kind: KindPlain, Body: "fake-value", Prefixed: true},
		},
		{
			name: "plain with a colon",
			args: args{ref: "plain:fake:value"},
			want: Parsed{Kind: KindPlain, Body: "fake:value", Prefixed: true},
		},
		{
			name: "env",
			args: args{ref: "env:FAKE_VAR_1"},
			want: Parsed{Kind: KindEnv, Body: "FAKE_VAR_1", Prefixed: true},
		},
		{
			name: "env from an underscore",
			args: args{ref: "env:_fake"},
			want: Parsed{Kind: KindEnv, Body: "_fake", Prefixed: true},
		},
		{
			name: "file",
			args: args{ref: "file:/run/secrets/fake"},
			want: Parsed{Kind: KindFile, Body: "/run/secrets/fake", Prefixed: true},
		},
		{
			name: "file at the root",
			args: args{ref: "file:/"},
			want: Parsed{Kind: KindFile, Body: "/", Prefixed: true},
		},
		{
			name: "vault",
			args: args{ref: "vault:depot/fake/key"},
			want: Parsed{Kind: KindVault, Body: "depot/fake/key", Prefixed: true},
		},
		{
			name: "vault with a deep path",
			args: args{ref: "vault:depot/fake/deep/key"},
			want: Parsed{Kind: KindVault, Body: "depot/fake/deep/key", Prefixed: true},
		},
		{
			name: "no prefix",
			args: args{ref: "fake-value"},
			want: Parsed{Kind: KindPlain, Body: "fake-value"},
		},
		{
			name: "prefix in another case",
			args: args{ref: "Env:FAKE_VAR"},
			want: Parsed{Kind: KindPlain, Body: "Env:FAKE_VAR"},
		},
		{
			name: "misspelled prefix",
			args: args{ref: "evn:FAKE_VAR"},
			want: Parsed{Kind: KindPlain, Body: "evn:FAKE_VAR"},
		},
		{name: "empty", args: args{ref: ""}, wantErr: true},
		{name: "empty plain", args: args{ref: "plain:"}, wantErr: true},
		{name: "empty env", args: args{ref: "env:"}, wantErr: true},
		{name: "env from a digit", args: args{ref: "env:1FAKE"}, wantErr: true},
		{name: "env with a dash", args: args{ref: "env:FAKE-VAR"}, wantErr: true},
		{name: "empty file", args: args{ref: "file:"}, wantErr: true},
		{name: "relative file", args: args{ref: "file:fake/secret"}, wantErr: true},
		{name: "file with //", args: args{ref: "file://fake/secret"}, wantErr: true},
		{name: "file with /./", args: args{ref: "file:/fake/./secret"}, wantErr: true},
		{name: "file with ..", args: args{ref: "file:/fake/../secret"}, wantErr: true},
		{name: "file with a trailing /", args: args{ref: "file:/fake/dir/"}, wantErr: true},
		{name: "empty vault", args: args{ref: "vault:"}, wantErr: true},
		{name: "vault with two segments", args: args{ref: "vault:fake/key"}, wantErr: true},
		{name: "vault from /", args: args{ref: "vault:/fake/path/key"}, wantErr: true},
		{name: "vault ending in /", args: args{ref: "vault:fake/path/key/"}, wantErr: true},
		{name: "vault with //", args: args{ref: "vault:fake//path/key"}, wantErr: true},
		{name: "vault with .", args: args{ref: "vault:fake/./key"}, wantErr: true},
		{name: "vault with ..", args: args{ref: "vault:fake/../key"}, wantErr: true},
		{name: "vault with a space", args: args{ref: "vault:fake/pa th/key"}, wantErr: true},
		{name: "vault with NBSP", args: args{ref: "vault:fake/pa\u00a0th/key"}, wantErr: true},
		{name: "vault with \\x00", args: args{ref: "vault:fake/pa\x00th/key"}, wantErr: true},
		{name: "vault with a newline", args: args{ref: "vault:fake/pa\nth/key"}, wantErr: true},
		{name: "vault with \\", args: args{ref: `vault:fake/pa\th/key`}, wantErr: true},
		{name: "vault with ?", args: args{ref: "vault:fake/pa?th/key"}, wantErr: true},
		{name: "vault with #", args: args{ref: "vault:fake/pa#th/key"}, wantErr: true},
		{name: "vault with %", args: args{ref: "vault:fake/pa%2fth/key"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.args.ref)
			if (err != nil) != tt.wantErr {
				t.Errorf("Parse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			// The error holds no body, each body of the failing cases has fake or FAKE.
			_, body, _ := strings.Cut(string(tt.args.ref), ":")
			if err != nil && body != "" && strings.Contains(err.Error(), body) {
				t.Errorf("Parse() error = %q, want no %q", err, body)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse() = %v, want %v", got, tt.want)
			}
		})
	}
}
