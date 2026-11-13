package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func Test_validator_required(t *testing.T) {
	tests := []struct {
		name    string
		content string
		// value is the value of storage.url that the caller passes.
		value string
		want  []Problem
	}{
		{
			name:    "empty string",
			content: "---\nstorage:\n  url: ''\n",
			want:    []Problem{{Line: 3, Column: 8, Path: "storage.url", Message: "required"}},
		},
		{
			name:    "explicit null",
			content: "---\nstorage:\n  url: null\n",
			want:    []Problem{{Line: 3, Column: 8, Path: "storage.url", Message: "required"}},
		},
		{
			name:    "missing key",
			content: "---\nstorage:\n  name: other\n",
			want:    []Problem{{Line: 2, Column: 1, Path: "storage.url", Message: "required"}},
		},
		{
			name:    "missing section",
			content: "---\nglobal: {}\n",
			want:    []Problem{{Line: 1, Column: 1, Path: "storage.url", Message: "required"}},
		},
		{
			name:    "set value",
			content: "---\nstorage:\n  url: b2://bucket/prefix\n",
			value:   "b2://bucket/prefix",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "main.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(path)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			v := &validator{l: loaded}
			if got := v.required("storage.url", tt.value); got != (tt.value != "") {
				t.Errorf("validator.required() = %v, want %v", got, tt.value != "")
			}
			for i := range tt.want {
				tt.want[i].File = loaded.Path
			}
			if !reflect.DeepEqual(v.problems, tt.want) {
				t.Errorf("validator.required() problems = %v, want %v", v.problems, tt.want)
			}
		})
	}
}

func Test_validator_check(t *testing.T) {
	tests := []struct {
		name string
		// bytes replaces check.test_restore_bytes after loading: a negative size does not
		// load.
		bytes Size
		want  []Problem
	}{
		{
			name:  "negative test restore bytes",
			bytes: -1,
			want: []Problem{{Line: 3, Column: 23, Path: "check.test_restore_bytes",
				Message: "must not be negative"}},
		},
		{name: "zero test restore bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "main.yaml")
			content := "---\ncheck:\n  test_restore_bytes: 0\n"
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(path)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			loaded.Config.Check.TestRestoreBytes = tt.bytes
			v := &validator{l: loaded}
			v.check()
			for i := range tt.want {
				tt.want[i].File = loaded.Path
			}
			if !reflect.DeepEqual(v.problems, tt.want) {
				t.Errorf("validator.check() problems = %v, want %v", v.problems, tt.want)
			}
		})
	}
}
