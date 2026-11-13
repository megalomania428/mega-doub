package secret

import (
	"bytes"
	"fmt"
	"log/slog"
	"strconv"
	"testing"
)

// dropTime removes the time from the records to compare them.
func dropTime(groups []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey && len(groups) == 0 {
		return slog.Attr{}
	}
	return a
}

func TestRef_String(t *testing.T) {
	// output is a verb of fmt, its argument and the result.
	type output struct {
		format string
		arg    any
		want   string
	}
	tests := []struct {
		name string
		r    Ref
		want string
	}{
		{name: "plain", r: "plain:fake-value", want: "plain:***"},
		{name: "empty plain", r: "plain:", want: "plain:***"},
		{name: "no prefix", r: "fake-value", want: "plain:***"},
		{name: "misspelled prefix", r: "evn:FAKE_VAR", want: "plain:***"},
		{name: "prefix in another case", r: "Env:FAKE_VAR", want: "plain:***"},
		{name: "env", r: "env:FAKE_VAR", want: "env:FAKE_VAR"},
		{name: "file", r: "file:/run/secrets/fake", want: "file:/run/secrets/fake"},
		{name: "vault", r: "vault:depot/fake/key", want: "vault:depot/fake/key"},
		{name: "empty", r: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.String(); got != tt.want {
				t.Errorf("Ref.String() = %v, want %v", got, tt.want)
			}
			holder := struct{ Token Ref }{Token: tt.r}
			for _, o := range []output{
				{format: "%v", arg: tt.r, want: tt.want},
				{format: "%s", arg: tt.r, want: tt.want},
				{format: "%q", arg: tt.r, want: strconv.Quote(tt.want)},
				{format: "%#v", arg: tt.r, want: strconv.Quote(tt.want)},
				{format: "%+v", arg: holder, want: "{Token:" + tt.want + "}"},
			} {
				if got := fmt.Sprintf(o.format, o.arg); got != o.want {
					t.Errorf("fmt.Sprintf(%q) = %q, want %q", o.format, got, o.want)
				}
			}
		})
	}
}

func TestRef_LogValue(t *testing.T) {
	tests := []struct {
		name string
		r    Ref
		want slog.Value
	}{
		{name: "plain", r: "plain:fake-value", want: slog.StringValue("plain:***")},
		{name: "no prefix", r: "fake-value", want: slog.StringValue("plain:***")},
		{name: "env", r: "env:FAKE_VAR", want: slog.StringValue("env:FAKE_VAR")},
		{
			name: "file",
			r:    "file:/run/secrets/fake",
			want: slog.StringValue("file:/run/secrets/fake"),
		},
		{
			name: "vault",
			r:    "vault:depot/fake/key",
			want: slog.StringValue("vault:depot/fake/key"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.LogValue(); !got.Equal(tt.want) {
				t.Errorf("Ref.LogValue() = %v, want %v", got, tt.want)
			}
			// The text and JSON handlers resolve the value of an attribute.
			opts := &slog.HandlerOptions{ReplaceAttr: dropTime}
			var text, json bytes.Buffer
			slog.New(slog.NewTextHandler(&text, opts)).Info("m", "ref", tt.r)
			slog.New(slog.NewJSONHandler(&json, opts)).Info("m", "ref", tt.r)
			value := tt.want.String()
			if want := "level=INFO msg=m ref=" + value + "\n"; text.String() != want {
				t.Errorf("text record = %q, want %q", text.String(), want)
			}
			want := `{"level":"INFO","msg":"m","ref":"` + value + `"}` + "\n"
			if json.String() != want {
				t.Errorf("JSON record = %q, want %q", json.String(), want)
			}
		})
	}
}

func TestRef_GoString(t *testing.T) {
	tests := []struct {
		name string
		r    Ref
		want string
	}{
		{name: "plain", r: "plain:fake-value", want: `"plain:***"`},
		{name: "no prefix", r: "fake-value", want: `"plain:***"`},
		{name: "vault", r: "vault:depot/fake/key", want: `"vault:depot/fake/key"`},
		{name: "empty", r: "", want: `""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.GoString(); got != tt.want {
				t.Errorf("Ref.GoString() = %v, want %v", got, tt.want)
			}
		})
	}
}
