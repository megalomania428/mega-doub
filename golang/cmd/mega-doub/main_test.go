// cspell:ignore doub

package main

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"
)

func Test_run(t *testing.T) {
	type args struct {
		ctx  context.Context
		args []string
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	const stamp = `^time=\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}(Z|[+-]\d{2}:\d{2}) `
	tests := []struct {
		name       string
		args       args
		want       int
		want1      string
		wantStderr []string
	}{
		{
			name: "backup",
			args: args{ctx: t.Context(), args: []string{"backup"}},
			want: 3,
			wantStderr: []string{
				`level=INFO msg=command command=backup ` +
					`config=/usr/local/etc/mega-doub/main\.yaml dry_run=false$`,
				`level=ERROR msg="command is not implemented yet" command=backup$`,
			},
		},
		{
			name: "canceled context",
			args: args{ctx: canceled, args: []string{"backup"}},
			want: 4,
			wantStderr: []string{
				`level=ERROR msg=interrupted error="context canceled"$`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in2 := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			if got := run(tt.args.ctx, tt.args.args, in2, stderr); got != tt.want {
				t.Errorf("run() = %v, want %v", got, tt.want)
			}
			if got1 := in2.String(); got1 != tt.want1 {
				t.Errorf("run() stdout = %q, want %q", got1, tt.want1)
			}
			gotStderr := stderr.String()
			lines := strings.Split(strings.TrimSuffix(gotStderr, "\n"), "\n")
			if len(lines) != len(tt.wantStderr) {
				t.Fatalf("run() stderr has %d records, want %d: %q", len(lines),
					len(tt.wantStderr), gotStderr)
			}
			for i, pattern := range tt.wantStderr {
				if record := regexp.MustCompile(stamp + pattern); !record.MatchString(lines[i]) {
					t.Errorf("run() stderr record %q, want a match for %q", lines[i], record)
				}
			}
		})
	}
}
