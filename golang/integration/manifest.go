//go:build integration

// Package integration holds the integration tests of mega-doub: the binary backs up the
// revisions of a generated tree into a storage, two builds of the duplicacy CLI restore
// them, and every restored tree is compared with the backed up one. The files build only
// with the tag integration, scripts/tests/integration.sh runs the tests.
package integration

// cspell:ignore duplicacy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The kinds of the entries of a manifest.
const (
	kindFile    = "file"
	kindDir     = "dir"
	kindSymlink = "symlink"
)

// entry is what a restore must reproduce of one path of a tree. A symbolic link has only
// its target: duplicacy restores neither the mode nor the time of a link.
type entry struct {
	kind    string
	mode    fs.FileMode // permission bits
	size    int64
	modTime int64 // Unix seconds
	sha256  string
	target  string
}

// manifest maps the slash-separated path of an entry relative to the root of its tree
// to the entry.
type manifest map[string]entry

// totals are the number of the regular files of a manifest and the sum of their sizes.
type totals struct {
	files int64
	bytes int64
}

// scanTree describes the tree under root without root itself. An entry for which skip
// returns true is left out, a directory together with its content.
func scanTree(root string, skip func(rel string, d fs.DirEntry) bool) (manifest, error) {
	m := manifest{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if skip != nil && skip(rel, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e, err := describe(p, info)
		m[rel] = e
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	return m, nil
}

// describe makes the entry of the file p from its information.
func describe(p string, info fs.FileInfo) (entry, error) {
	mode := info.Mode()
	switch {
	case mode&fs.ModeSymlink != 0:
		target, err := os.Readlink(p)
		return entry{kind: kindSymlink, target: target}, err
	case mode.IsDir():
		return entry{kind: kindDir, mode: mode.Perm(), modTime: info.ModTime().Unix()}, nil
	case mode.IsRegular():
		sum, err := fileSHA256(p)
		return entry{kind: kindFile, mode: mode.Perm(), size: info.Size(),
			modTime: info.ModTime().Unix(), sha256: sum}, err
	default:
		return entry{}, fmt.Errorf("%s has the unsupported type %s", p, mode.Type())
	}
}

// fileSHA256 returns the hexadecimal SHA256 of the content of the file p.
func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// skipPartial leaves out the files and the links named *.part, which backup of mega-doub
// removes from the copy directory; a directory with such a name stays.
func skipPartial(_ string, d fs.DirEntry) bool {
	return !d.IsDir() && strings.HasSuffix(d.Name(), ".part")
}

// skipSettings leaves out the directory .duplicacy that duplicacy init creates at the top
// of a restored tree.
func skipSettings(rel string, _ fs.DirEntry) bool {
	return rel == ".duplicacy"
}

// totals counts the regular files of m and sums their sizes.
func (m manifest) totals() totals {
	var sum totals
	for _, e := range m {
		if e.kind == kindFile {
			sum.files++
			sum.bytes += e.size
		}
	}
	return sum
}

// diff lists the differences of got from m, the expected manifest, one line per path in
// the order of the paths. The modification times of directories are not compared.
func (m manifest) diff(got manifest) []string {
	paths := slices.Collect(maps.Keys(m))
	for p := range got {
		if _, ok := m[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	var lines []string
	for _, p := range paths {
		want, inWant := m[p]
		have, inGot := got[p]
		switch {
		case !inGot:
			lines = append(lines, fmt.Sprintf("%s: missing %s", p, want.kind))
		case !inWant:
			lines = append(lines, fmt.Sprintf("%s: unexpected %s", p, have.kind))
		default:
			if d := want.diff(have); d != "" {
				lines = append(lines, p+": "+d)
			}
		}
	}
	return lines
}

// dirTimes counts the directories of m that got has as directories with another
// modification time.
func (m manifest) dirTimes(got manifest) int {
	count := 0
	for p, want := range m {
		have, ok := got[p]
		if ok && want.kind == kindDir && have.kind == kindDir && have.modTime != want.modTime {
			count++
		}
	}
	return count
}

// diff describes how got differs from e, the expected entry, or returns "".
func (e entry) diff(got entry) string {
	if e.kind != got.kind {
		return fmt.Sprintf("%s, want %s", got.kind, e.kind)
	}
	var parts []string
	if e.mode != got.mode {
		parts = append(parts, fmt.Sprintf("mode %04o, want %04o", got.mode, e.mode))
	}
	if e.size != got.size {
		parts = append(parts, fmt.Sprintf("size %d, want %d", got.size, e.size))
	}
	if e.sha256 != got.sha256 {
		parts = append(parts, fmt.Sprintf("sha256 %.12s, want %.12s", got.sha256, e.sha256))
	}
	if e.kind == kindFile && e.modTime != got.modTime {
		gotTime, wantTime := unixTime(got.modTime), unixTime(e.modTime)
		parts = append(parts, fmt.Sprintf("mtime %s, want %s", gotTime, wantTime))
	}
	if e.target != got.target {
		parts = append(parts, fmt.Sprintf("target %q, want %q", got.target, e.target))
	}
	return strings.Join(parts, "; ")
}

// unixTime formats Unix seconds as UTC time.
func unixTime(seconds int64) string {
	return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
}
