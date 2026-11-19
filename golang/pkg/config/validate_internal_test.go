// cspell:ignore gcd sftpc smb webdav

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

func Test_storageURL(t *testing.T) {
	type args struct {
		raw string
	}
	tests := []struct {
		name    string
		args    args
		wantTyp string
		wantMsg string
	}{
		{name: "absolute path", args: args{"/srv/storage"}, wantTyp: "file"},
		{name: "flat path", args: args{"flat:///srv/storage"}, wantTyp: "file"},
		{name: "samba path", args: args{"samba:///srv/storage/"}, wantTyp: "file"},
		{name: "bucket without a path", args: args{"b2://bucket"}, wantTyp: "b2"},
		{name: "b2-custom with a bucket", args: args{"b2-custom://h/bucket"},
			wantTyp: "b2-custom"},
		{name: "root of a drive", args: args{"gcd://drive@/"}, wantTyp: "gcd"},
		{name: "region of s3", args: args{"s3://region@h:9000/bucket"}, wantTyp: "s3"},
		{name: "smb share without a path", args: args{"smb://u@h/share/"}, wantTyp: "smb"},
		{name: "port is not checked", args: args{"sftp://u@h:bad/x"}, wantTyp: "sftp"},
		{name: "colon and @ in the path", args: args{"sftp://u@h/a:b@c"}, wantTyp: "sftp"},
		{name: "password before the type check", args: args{"ftp://u:p@h/x"},
			wantMsg: "must not contain a password"},
		{name: "password of a local path", args: args{"flat://u:p@/x"},
			wantMsg: "must not contain a password"},
		{name: "password of sftpc", args: args{"sftpc://u:@h/x"},
			wantMsg: "must not contain a password, set storage.keys.ssh_password instead"},
		{name: "password of webdav-http", args: args{"webdav-http://u:p@h/x"},
			wantMsg: "must not contain a password, set storage.keys.webdav_password instead"},
		{name: "slashes only", args: args{"//"}, wantMsg: "must not be the root directory"},
		{name: "samba without a path", args: args{"samba://"},
			wantMsg: "must have an absolute path after the scheme"},
		{name: "prefix of a local path in another case", args: args{"Flat://x/y"},
			wantMsg: `"Flat" is not a storage type`},
		{name: "space before the path", args: args{" /srv/storage"},
			wantMsg: "must be an absolute path or a storage URL"},
		{name: "sftp path of slashes", args: args{"sftp://u@h//"},
			wantMsg: "must not be the root directory"},
		{name: "sftpc path of slashes", args: args{"sftpc://h///"},
			wantMsg: "must not be the root directory"},
		{name: "sftp path with a leading slash", args: args{"sftp://u@h//backups"},
			wantTyp: "sftp"},
		{name: "sftp path with a trailing slash", args: args{"sftp://h/x/"},
			wantTyp: "sftp"},
		{name: "scheme with a plus", args: args{"s3+https://h/b"},
			wantMsg: `"s3+https" is not a storage type`},
		{name: "scheme with a dot", args: args{"ftp.x://h/y"},
			wantMsg: `"ftp.x" is not a storage type`},
		{name: "scheme with a digit first", args: args{"1x://h/y"},
			wantMsg: `"1x" is not a storage type`},
		{name: "unknown type before the form", args: args{"ftp:///bucket"},
			wantMsg: `"ftp" is not a storage type`},
		{name: "known type without a host", args: args{"sftp:///x"},
			wantMsg: "must be an absolute path or a storage URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTyp, gotMsg := storageURL(tt.args.raw)
			if gotTyp != tt.wantTyp {
				t.Errorf("storageURL() gotTyp = %v, want %v", gotTyp, tt.wantTyp)
			}
			if gotMsg != tt.wantMsg {
				t.Errorf("storageURL() gotMsg = %v, want %v", gotMsg, tt.wantMsg)
			}
		})
	}
}

func Test_sanitizeStorageURL(t *testing.T) {
	type args struct {
		raw string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{name: "percent in a local path", args: args{raw: "/srv/backup-100%"},
			want: "/srv/backup-100%"},
		{name: "port that is no number", args: args{raw: "sftp://u@h:bad/x"},
			want: "sftp://u@h:bad/x"},
		{name: "storage URL", args: args{raw: "b2://bucket"}, want: "b2://bucket"},
		{name: "password", args: args{raw: "sftp://u:p@h/x"}, want: "sftp://u:***@h/x"},
		{name: "password and a port that is no number",
			args: args{raw: "sftp://u:fake-canary@h:bad/"}, want: "sftp://u:***@h:bad/"},
		{name: "unparsable", args: args{raw: "%zz"}, want: "***"},
		{name: "opaque", args: args{raw: "scheme:user:pass@host"}, want: "scheme:***"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeStorageURL(tt.args.raw); got != tt.want {
				t.Errorf("sanitizeStorageURL() = %v, want %v", got, tt.want)
			}
		})
	}
}
