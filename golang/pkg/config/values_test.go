package config

import (
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v4"
)

// node returns the YAML node of a single value.
func node(t *testing.T, text string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Load([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Content[0]
}

// roundTrip dumps v as the value of a key, checks the text and loads it back.
func roundTrip[T any](t *testing.T, v T, want string) T {
	t.Helper()
	out, err := yaml.Dump(map[string]T{"v": v})
	if err != nil {
		t.Fatalf("yaml.Dump() error = %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "v: "+want {
		t.Errorf("yaml.Dump() = %q, want %q", got, "v: "+want)
	}
	var back map[string]T
	if err := yaml.Load(out, &back, yaml.WithKnownFields()); err != nil {
		t.Fatalf("yaml.Load() error = %v", err)
	}
	return back["v"]
}

func TestDuration_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    Duration
		wantErr bool
	}{
		{name: "seconds", text: "30s", want: Duration(30 * time.Second)},
		{name: "hours and minutes", text: "1h30m", want: Duration(90 * time.Minute)},
		{name: "quoted", text: "'10s'", want: Duration(10 * time.Second)},
		{name: "zero", text: "0", want: 0},
		{name: "number without a unit", text: "30", wantErr: true},
		{name: "word", text: "soon", wantErr: true},
		{name: "empty string", text: "''", wantErr: true},
		{name: "mapping", text: "{a: 1}", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			if err := d.UnmarshalYAML(node(t, tt.text)); (err != nil) != tt.wantErr {
				t.Fatalf("Duration.UnmarshalYAML() error = %v, wantErr %v", err, tt.wantErr)
			}
			if d != tt.want {
				t.Errorf("Duration.UnmarshalYAML() = %v, want %v", d, tt.want)
			}
		})
	}
}

func TestDuration_MarshalYAML(t *testing.T) {
	tests := []struct {
		name string
		d    Duration
		want string
	}{
		{name: "zero", d: 0, want: "0s"},
		{name: "seconds", d: Duration(30 * time.Second), want: "30s"},
		{name: "hours", d: Duration(12 * time.Hour), want: "12h0m0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := roundTrip(t, tt.d, tt.want); got != tt.d {
				t.Errorf("round trip = %v, want %v", got, tt.d)
			}
		})
	}
}

func TestSize_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    Size
		wantErr bool
	}{
		{name: "bytes", text: "100", want: 100},
		{name: "quoted bytes", text: "'100'", want: 100},
		{name: "zero", text: "0", want: 0},
		{name: "K suffix", text: "1536K", want: 1536 << 10},
		{name: "M suffix", text: "256M", want: 256 << 20},
		{name: "negative", text: "-1", wantErr: true},
		{name: "fraction", text: "1.5G", wantErr: true},
		{name: "unknown suffix", text: "10X", wantErr: true},
		{name: "sequence", text: "[1]", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s Size
			if err := s.UnmarshalYAML(node(t, tt.text)); (err != nil) != tt.wantErr {
				t.Fatalf("Size.UnmarshalYAML() error = %v, wantErr %v", err, tt.wantErr)
			}
			if s != tt.want {
				t.Errorf("Size.UnmarshalYAML() = %v, want %v", s, tt.want)
			}
		})
	}
}

func TestSize_MarshalYAML(t *testing.T) {
	tests := []struct {
		name string
		s    Size
		want string
	}{
		{name: "zero", s: 0, want: "'0'"},
		{name: "bytes", s: 100, want: "'100'"},
		{name: "K suffix", s: 1536 << 10, want: "1536K"},
		{name: "M suffix", s: 256 << 20, want: "256M"},
		{name: "G suffix", s: 4 << 30, want: "4G"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := roundTrip(t, tt.s, tt.want); got != tt.s {
				t.Errorf("round trip = %v, want %v", got, tt.s)
			}
		})
	}
}

func TestLogLevel_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    LogLevel
		wantErr bool
	}{
		{name: "debug", text: "debug", want: "debug"},
		{name: "error", text: "error", want: "error"},
		{name: "upper case", text: "INFO", wantErr: true},
		{name: "unknown", text: "trace", wantErr: true},
		{name: "number", text: "1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var l LogLevel
			if err := l.UnmarshalYAML(node(t, tt.text)); (err != nil) != tt.wantErr {
				t.Fatalf("LogLevel.UnmarshalYAML() error = %v, wantErr %v", err, tt.wantErr)
			}
			if l != tt.want {
				t.Errorf("LogLevel.UnmarshalYAML() = %q, want %q", l, tt.want)
			}
			if !tt.wantErr && roundTrip(t, l, tt.text) != l {
				t.Errorf("round trip of %q changed the value", l)
			}
		})
	}
}

func TestLogFormat_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    LogFormat
		wantErr bool
	}{
		{name: "text", text: "text", want: "text"},
		{name: "json", text: "json", want: "json"},
		{name: "unknown", text: "yaml", wantErr: true},
		{name: "sequence", text: "[json]", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var f LogFormat
			if err := f.UnmarshalYAML(node(t, tt.text)); (err != nil) != tt.wantErr {
				t.Fatalf("LogFormat.UnmarshalYAML() error = %v, wantErr %v", err, tt.wantErr)
			}
			if f != tt.want {
				t.Errorf("LogFormat.UnmarshalYAML() = %q, want %q", f, tt.want)
			}
			if !tt.wantErr && roundTrip(t, f, tt.text) != f {
				t.Errorf("round trip of %q changed the value", f)
			}
		})
	}
}
