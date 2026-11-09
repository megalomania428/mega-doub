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
		ctx context.Context
		in1 []string
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	record := regexp.MustCompile(`^time=\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}` +
		`(Z|[+-]\d{2}:\d{2}) level=INFO msg="mega-doub started" ` +
		`version=dev commit=unknown\n$`)
	tests := []struct {
		name       string
		args       args
		want       int
		want1      string
		wantStderr []string
	}{
		{
			name: "live context",
			args: args{ctx: t.Context()},
			want: 0,
			wantStderr: []string{
				"level=INFO", `msg="mega-doub started"`, "version=dev", "commit=unknown",
			},
		},
		{
			name: "canceled context",
			args: args{ctx: canceled},
			want: 4,
			wantStderr: []string{
				"level=INFO", `msg="mega-doub started"`, "version=dev", "commit=unknown",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in2 := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			if got := run(tt.args.ctx, tt.args.in1, in2, stderr); got != tt.want {
				t.Errorf("run() = %v, want %v", got, tt.want)
			}
			if got1 := in2.String(); got1 != tt.want1 {
				t.Errorf("run() stdout = %q, want %q", got1, tt.want1)
			}
			gotStderr := stderr.String()
			for _, sub := range tt.wantStderr {
				if !strings.Contains(gotStderr, sub) {
					t.Errorf("run() stderr %q does not contain %q", gotStderr, sub)
				}
			}
			if n := strings.Count(gotStderr, "\n"); n != 1 {
				t.Errorf("run() stderr has %d records, want 1: %q", n, gotStderr)
			}
			if !record.MatchString(gotStderr) {
				t.Errorf("run() stderr = %q, want a match for %q", gotStderr, record)
			}
		})
	}
}
