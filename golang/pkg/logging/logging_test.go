package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	type args struct {
		s string
	}
	tests := []struct {
		name    string
		args    args
		want    slog.Level
		wantErr bool
	}{
		{name: "debug", args: args{s: "debug"}, want: slog.LevelDebug},
		{name: "info", args: args{s: "info"}, want: slog.LevelInfo},
		{name: "warn", args: args{s: "warn"}, want: slog.LevelWarn},
		{name: "error", args: args{s: "error"}, want: slog.LevelError},
		{name: "upper case", args: args{s: "DEBUG"}, wantErr: true},
		{name: "unknown", args: args{s: "trace"}, wantErr: true},
		{name: "empty", args: args{s: ""}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLevel(tt.args.s)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseLevel() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				if !strings.Contains(err.Error(), levelNames) {
					t.Errorf("ParseLevel() error = %q, want the list %q", err, levelNames)
				}
				return
			}
			if got != tt.want {
				t.Errorf("ParseLevel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseFormat(t *testing.T) {
	type args struct {
		s string
	}
	tests := []struct {
		name    string
		args    args
		want    Format
		wantErr bool
	}{
		{name: "text", args: args{s: "text"}, want: FormatText},
		{name: "json", args: args{s: "json"}, want: FormatJSON},
		{name: "unknown", args: args{s: "yaml"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseFormat(tt.args.s)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseFormat() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ParseFormat() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNew(t *testing.T) {
	type args struct {
		level  slog.Level
		format Format
	}
	tests := []struct {
		name string
		args args
		want []string
	}{
		{
			name: "text",
			args: args{level: slog.LevelWarn, format: FormatText},
			want: []string{"level=WARN", "msg=shown", "target=db01"},
		},
		{
			name: "json",
			args: args{level: slog.LevelWarn, format: FormatJSON},
			want: []string{`"level":"WARN"`, `"msg":"shown"`, `"target":"db01"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &bytes.Buffer{}
			got := New(w, tt.args.level, tt.args.format)
			got.Info("hidden")
			got.Warn("shown", KeyTarget, "db01")
			gotW := w.String()
			for _, sub := range tt.want {
				if !strings.Contains(gotW, sub) {
					t.Errorf("New() output %q does not contain %q", gotW, sub)
				}
			}
			if strings.Contains(gotW, "hidden") {
				t.Errorf("New() output %q contains a record below the level", gotW)
			}
		})
	}
}
