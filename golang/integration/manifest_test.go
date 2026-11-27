//go:build integration

package integration

// cspell:ignore aaaaaaaaaaaa bbbbbbbbbbbb duplicacy retargeted

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func Test_manifest_diff(t *testing.T) {
	dir := entry{kind: kindDir, mode: 0o755, modTime: 100}
	data := entry{kind: kindFile, mode: 0o644, size: 3, modTime: 100,
		sha256: "aaaaaaaaaaaaaaaa"}
	target := entry{kind: kindSymlink, target: "a"}
	base := manifest{"d": dir, "d/f": data, "l": target}
	with := func(path string, e entry) manifest {
		m := manifest{"d": dir, "d/f": data, "l": target}
		m[path] = e
		return m
	}
	dirTime := with("d", entry{kind: kindDir, mode: 0o755, modTime: 200})
	byteChanged := with("d/f", entry{kind: kindFile, mode: 0o644, size: 3, modTime: 100,
		sha256: "bbbbbbbbbbbbbbbb"})
	extra := manifest{"d": dir, "d/f": data, "x": data}
	attributes := with("d/f", entry{kind: kindFile, mode: 0o600, size: 4, modTime: 200,
		sha256: "aaaaaaaaaaaaaaaa"})
	attributesDiff := "d/f: mode 0600, want 0644; size 4, want 3; " +
		"mtime 1970-01-01T00:03:20Z, want 1970-01-01T00:01:40Z"
	retargeted := with("l", entry{kind: kindSymlink, target: "b"})
	type args struct {
		got manifest
	}
	tests := []struct {
		name string
		m    manifest
		args args
		want []string
	}{
		{
			name: "equal but the time of a directory",
			m:    base,
			args: args{got: dirTime},
		},
		{
			name: "changed byte",
			m:    base,
			args: args{got: byteChanged},
			want: []string{"d/f: sha256 bbbbbbbbbbbb, want aaaaaaaaaaaa"},
		},
		{
			name: "extra file and lost link",
			m:    base,
			args: args{got: extra},
			want: []string{"l: missing symlink", "x: unexpected file"},
		},
		{
			name: "file turned into a directory",
			m:    base,
			args: args{got: with("d/f", dir)},
			want: []string{"d/f: dir, want file"},
		},
		{
			name: "mode, size and time",
			m:    base,
			args: args{got: attributes},
			want: []string{attributesDiff},
		},
		{
			name: "target of a link",
			m:    base,
			args: args{got: retargeted},
			want: []string{`l: target "b", want "a"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.diff(tt.args.got); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("manifest.diff() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_scanTree(t *testing.T) {
	root := t.TempDir()
	at := time.Unix(1_700_000_000, 0)
	write := func(name, content string, mode fs.FileMode) {
		p := filepath.Join(root, name)
		must(t, os.WriteFile(p, []byte(content), mode))
		must(t, os.Chmod(p, mode))
		must(t, os.Chtimes(p, at, at))
	}
	mkdir := func(name string, mode fs.FileMode) {
		p := filepath.Join(root, name)
		must(t, os.Mkdir(p, mode))
		must(t, os.Chmod(p, mode))
		must(t, os.Chtimes(p, at, at))
	}
	sum := func(content string) string {
		h := sha256.Sum256([]byte(content))
		return hex.EncodeToString(h[:])
	}
	write("a.txt", "abc", 0o640)
	write("p.part", "x", 0o644)
	mkdir("d", 0o750)
	mkdir(".duplicacy", 0o700)
	must(t, os.Symlink("a.txt", filepath.Join(root, "l")))
	a := entry{kind: kindFile, mode: 0o640, size: 3, modTime: at.Unix(), sha256: sum("abc")}
	p := entry{kind: kindFile, mode: 0o644, size: 1, modTime: at.Unix(), sha256: sum("x")}
	d := entry{kind: kindDir, mode: 0o750, modTime: at.Unix()}
	settings := entry{kind: kindDir, mode: 0o700, modTime: at.Unix()}
	l := entry{kind: kindSymlink, target: "a.txt"}
	type args struct {
		root string
		skip func(rel string, d fs.DirEntry) bool
	}
	tests := []struct {
		name    string
		args    args
		want    manifest
		wantErr bool
	}{
		{
			name: "partial files left out",
			args: args{root: root, skip: skipPartial},
			want: manifest{"a.txt": a, "d": d, ".duplicacy": settings, "l": l},
		},
		{
			name: "settings left out",
			args: args{root: root, skip: skipSettings},
			want: manifest{"a.txt": a, "d": d, "l": l, "p.part": p},
		},
		{
			name: "everything",
			args: args{root: root},
			want: manifest{"a.txt": a, "d": d, ".duplicacy": settings, "l": l, "p.part": p},
		},
		{
			name:    "missing root",
			args:    args{root: filepath.Join(root, "missing")},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := scanTree(tt.args.root, tt.args.skip)
			if (err != nil) != tt.wantErr {
				t.Errorf("scanTree() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("scanTree() = %v, want %v", got, tt.want)
			}
		})
	}
}
