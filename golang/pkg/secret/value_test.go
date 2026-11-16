package secret

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// canary is the value that never appears in the output.
const canary = "fake-canary-3e4f5a"

func TestNewValue(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want string
	}{
		{name: "value", s: canary, want: canary},
		{name: "empty", s: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewValue(tt.s).Reveal(); got != tt.want {
				t.Errorf("NewValue().Reveal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValue_Reveal(t *testing.T) {
	tests := []struct {
		name string
		v    Value
		want string
	}{
		{name: "value", v: NewValue(canary), want: canary},
		{name: "zero", v: Value{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.v.Reveal(); got != tt.want {
				t.Errorf("Value.Reveal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValue_Format(t *testing.T) {
	v := NewValue(canary)
	exported := struct{ V Value }{V: v}
	unexported := struct{ v Value }{v: v}
	tests := []struct {
		name   string
		format string
		arg    any
		// want is the whole output, empty when only the absence of the value is checked.
		want string
	}{
		{name: "v", format: "%v", arg: v, want: "***"},
		{name: "plus v", format: "%+v", arg: v, want: "***"},
		{name: "s", format: "%s", arg: v, want: "***"},
		{name: "q", format: "%q", arg: v, want: "***"},
		{name: "sharp v", format: "%#v", arg: v, want: "***"},
		{name: "x", format: "%x", arg: v, want: "***"},
		{name: "pointer", format: "%v", arg: &v, want: "***"},
		{name: "exported field v", format: "%v", arg: exported, want: "{***}"},
		{name: "exported field plus v", format: "%+v", arg: exported, want: "{V:***}"},
		{
			name:   "exported field sharp v",
			format: "%#v",
			arg:    exported,
			want:   "struct { V secret.Value }{V:***}",
		},
		// fmt prints an unexported field by reflection without its methods: the pointer
		// shows an address, not the value.
		{name: "unexported field plus v", format: "%+v", arg: unexported},
		{name: "unexported field sharp v", format: "%#v", arg: unexported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fmt.Sprintf(tt.format, tt.arg)
			if strings.Contains(got, canary) {
				t.Errorf("fmt.Sprintf(%q) = %q, contains the value", tt.format, got)
			}
			if tt.want != "" && got != tt.want {
				t.Errorf("fmt.Sprintf(%q) = %q, want %q", tt.format, got, tt.want)
			}
		})
	}
}

func TestValue_String(t *testing.T) {
	if got := NewValue(canary).String(); got != "***" {
		t.Errorf("Value.String() = %v, want ***", got)
	}
}

func TestValue_GoString(t *testing.T) {
	if got := NewValue(canary).GoString(); got != "***" {
		t.Errorf("Value.GoString() = %v, want ***", got)
	}
}

func TestValue_LogValue(t *testing.T) {
	v := NewValue(canary)
	tests := []struct {
		name     string
		arg      any
		wantText string
		wantJSON string
	}{
		{
			name:     "value",
			arg:      v,
			wantText: "level=INFO msg=m secret=***\n",
			wantJSON: `{"level":"INFO","msg":"m","secret":"***"}` + "\n",
		},
		{
			name:     "exported field",
			arg:      struct{ V Value }{V: v},
			wantText: "level=INFO msg=m secret={V:***}\n",
			wantJSON: `{"level":"INFO","msg":"m","secret":{"V":"***"}}` + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &slog.HandlerOptions{ReplaceAttr: dropTime}
			var text, js bytes.Buffer
			slog.New(slog.NewTextHandler(&text, opts)).Info("m", "secret", tt.arg)
			slog.New(slog.NewJSONHandler(&js, opts)).Info("m", "secret", tt.arg)
			if text.String() != tt.wantText {
				t.Errorf("text record = %q, want %q", text.String(), tt.wantText)
			}
			if js.String() != tt.wantJSON {
				t.Errorf("JSON record = %q, want %q", js.String(), tt.wantJSON)
			}
		})
	}
	if got := v.LogValue(); !got.Equal(slog.StringValue("***")) {
		t.Errorf("Value.LogValue() = %v, want ***", got)
	}
}

func TestValue_MarshalText(t *testing.T) {
	got, err := NewValue(canary).MarshalText()
	if err != nil || string(got) != "***" {
		t.Errorf("Value.MarshalText() = %q, %v, want ***, nil", got, err)
	}
}

func TestValue_MarshalJSON(t *testing.T) {
	v := NewValue(canary)
	tests := []struct {
		name string
		arg  any
		want string
	}{
		{name: "value", arg: v, want: `"***"`},
		{name: "exported field", arg: struct{ V Value }{V: v}, want: `{"V":"***"}`},
		{name: "map", arg: map[string]Value{"k": v}, want: `{"k":"***"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.arg)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("json.Marshal() = %s, want %s", got, tt.want)
			}
		})
	}
}
