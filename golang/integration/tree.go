//go:build integration

// Package integration is documented in manifest.go.
package integration

// cspell:ignore chacha chtimes duplicacy goroot toplevel typeflag untar ünïcødé юникод

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// libModule is the module whose versions are a layer of the tree.
const libModule = "github.com/megalomania428/go-lib-ci"

// The large random file and the files that r2 changes: r2 overwrites 1 MiB in the middle
// of the large file and r3 brings back the bytes of r1 there.
const (
	bigFile    = "synthetic/big.bin"
	bigSize    = 45 << 20
	middleAt   = 20 << 20
	middleSize = 1 << 20
	appendFile = "synthetic/append.bin"
)

// tree builds the revisions of a scenario in the copy directory of mega-doub. Every edit
// sets the modification time of the revision being built, distinct for every revision,
// so that the quick mode of duplicacy, which compares sizes and times, sees each change.
type tree struct {
	root    string   // copy directory
	repo    string   // top directory of the git repository of the tests
	commits int      // number of the first-parent commits of HEAD
	rev     int      // revision being built
	middle  []byte   // bytes of r1 in the middle of the large file
	notes   []string // layers of the revision for the summary
}

// file is a regular file of the tree: its slash-separated path relative to the root, its
// content and its permission bits, 0644 when zero.
type file struct {
	rel  string
	data []byte
	mode fs.FileMode
}

// link is a symbolic link of the tree and its target.
type link struct {
	rel    string
	target string
}

// patch writes data over the file rel at offset.
type patch struct {
	rel    string
	offset int64
	data   []byte
}

// move renames the entry from to the path to.
type move struct {
	from string
	to   string
}

// revisionTime is the modification time that the edits of revision n set.
func revisionTime(n int) time.Time {
	return time.Date(2025, time.January, n, 12, 0, 0, 0, time.UTC)
}

// newTree prepares the tree in the directory root, the copy directory of a scenario.
func newTree(t *testing.T, root string) *tree {
	t.Helper()
	repo := text(t, exec.Command("git", "rev-parse", "--show-toplevel"))
	// HEAD~N follows the first parents, so only they count.
	first := exec.Command("git", "-C", repo, "rev-list", "--count", "--first-parent", "HEAD")
	commits, err := strconv.Atoi(text(t, first))
	must(t, err)
	return &tree{root: root, repo: repo, commits: commits}
}

// revision turns the tree of revision n-1 into revision n, from 1 to 4, with new partial
// files.
func (tr *tree) revision(t *testing.T, n int) {
	t.Helper()
	tr.rev = n
	tr.notes = nil
	switch n {
	case 1:
		tr.r1(t)
	case 2:
		tr.r2(t)
	case 3:
		tr.r3(t)
	case 4:
		tr.r4(t)
	default:
		t.Fatalf("no revision %d", n)
	}
	name := fmt.Sprintf("r%d.part", n)
	tr.write(t, file{rel: path.Join("synthetic", name), data: []byte("partial\n")})
	tr.write(t, file{rel: path.Join("synthetic/small", name), data: []byte("partial\n")})
}

// r1 is the base tree: the oldest selected commit, the oldest version of the module, the
// subtree of GOROOT and the synthetic files.
func (tr *tree) r1(t *testing.T) {
	t.Helper()
	tr.gitLayer(t, 10)
	tr.moduleLayer(t, "v1.0.5")
	tr.gorootLayer(t)
	big := randomBytes(1, bigSize)
	tr.middle = bytes.Clone(big[middleAt : middleAt+middleSize])
	tr.write(t, file{rel: bigFile, data: big})
	tr.write(t, file{rel: "synthetic/zeros.bin", data: make([]byte, 4<<20)})
	tr.write(t, file{rel: appendFile, data: randomBytes(2, 1<<20)})
	tr.smallFiles(t)
	tr.write(t, file{rel: "synthetic/empty.txt"})
	tr.mkdir(t, "synthetic/empty-dir")
	tr.write(t, file{rel: "synthetic/modes/private.txt", data: []byte("private\n"),
		mode: 0o600})
	tr.write(t, file{rel: "synthetic/modes/public.txt", data: []byte("public\n")})
	tr.write(t, file{rel: "synthetic/modes/script.sh", data: []byte("#!/bin/sh\necho hi\n"),
		mode: 0o755})
	deep := "synthetic/deep"
	for i := range 30 {
		deep = path.Join(deep, fmt.Sprintf("level-%02d", i+1))
	}
	tr.write(t, file{rel: path.Join(deep, "leaf.txt"), data: []byte("deep\n")})
	names := []string{"юникод-ünïcødé.txt", "with spaces.txt", "-leading-dash.txt",
		strings.Repeat("n", 251) + ".txt"}
	for _, name := range names {
		tr.write(t, file{rel: path.Join("synthetic/names", name), data: []byte(name + "\n")})
	}
	tr.symlink(t, link{rel: "synthetic/links/relative", target: "../modes/public.txt"})
	tr.symlink(t, link{rel: "synthetic/links/dangling", target: "missing-target"})
	tr.write(t, file{rel: "synthetic/swap-a", data: []byte("r3 makes it a directory\n")})
	tr.write(t, file{rel: "synthetic/swap-b/inside.txt", data: []byte("r3 swaps it\n")})
	tr.write(t, file{rel: "synthetic/same.txt", data: []byte("r2 changes only the time\n")})
	tr.write(t, file{rel: "synthetic/dir.part/kept.txt", data: []byte("it stays\n")})
}

// r2 makes the changes that show the deduplication: a part of the large file, an append,
// added, removed and renamed files, a mode, a time alone and the next real layers.
func (tr *tree) r2(t *testing.T) {
	t.Helper()
	tr.patch(t, patch{rel: bigFile, offset: middleAt, data: randomBytes(5, middleSize)})
	tr.appendTo(t, file{rel: appendFile, data: randomBytes(6, 256<<10)})
	tr.write(t, file{rel: "synthetic/added-r2.txt", data: []byte("added in r2\n")})
	tr.remove(t, "synthetic/small/001.dat")
	tr.move(t, move{from: "synthetic/small/002.dat", to: "synthetic/renamed-r2.dat"})
	// The content and the time stay, only the mode changes.
	tr.chmod(t, file{rel: "synthetic/modes/public.txt", mode: 0o640})
	// The content stays, only the time changes.
	tr.touch(t, "synthetic/same.txt")
	tr.gitLayer(t, 5)
	tr.moduleLayer(t, "v1.0.6")
}

// r3 makes the changes of the structure: a removed subtree, a file and a directory that
// swap their kinds, a link replaced with a file, the large file back to its content of r1
// and the newest real layers.
func (tr *tree) r3(t *testing.T) {
	t.Helper()
	tr.remove(t, "synthetic/deep")
	tr.remove(t, "synthetic/swap-a")
	tr.write(t, file{rel: "synthetic/swap-a/inside.txt", data: []byte("now a directory\n")})
	tr.remove(t, "synthetic/swap-b")
	tr.write(t, file{rel: "synthetic/swap-b", data: []byte("now a file\n")})
	tr.remove(t, "synthetic/links/relative")
	tr.write(t, file{rel: "synthetic/links/relative", data: []byte("now a file\n")})
	tr.patch(t, patch{rel: bigFile, offset: middleAt, data: tr.middle})
	tr.gitLayer(t, 0)
	tr.moduleLayer(t, "v1.0.7")
}

// r4 is the revision after the exclusive prune of r2: an added file and an append.
func (tr *tree) r4(t *testing.T) {
	t.Helper()
	tr.write(t, file{rel: "synthetic/added-r4.txt", data: []byte("added in r4\n")})
	tr.appendTo(t, file{rel: appendFile, data: randomBytes(7, 128<<10)})
}

// gitLayer replaces the directory git with the files of the commit back commits before
// HEAD, or of the oldest commit of a shorter history.
func (tr *tree) gitLayer(t *testing.T, back int) {
	t.Helper()
	rev := fmt.Sprintf("HEAD~%d", min(back, tr.commits-1))
	commit := text(t, exec.Command("git", "-C", tr.repo, "rev-parse", "--short", rev))
	archive := output(t, exec.Command("git", "-C", tr.repo, "archive", "--format=tar", rev))
	tr.remove(t, "git")
	tr.mkdir(t, "git")
	tr.untar(t, tar.NewReader(bytes.NewReader(archive)))
	tr.notes = append(tr.notes, fmt.Sprintf("git %s (%s)", rev, commit))
}

// untar writes the entries of the archive of git archive into the directory git.
func (tr *tree) untar(t *testing.T, r *tar.Reader) {
	t.Helper()
	for {
		hdr, err := r.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		must(t, err)
		if !filepath.IsLocal(hdr.Name) {
			t.Fatalf("archive entry %q is outside its directory", hdr.Name)
		}
		rel := path.Join("git", hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			tr.mkdir(t, rel)
		case tar.TypeReg:
			data, err := io.ReadAll(r)
			must(t, err)
			tr.write(t, file{rel: rel, data: data, mode: fs.FileMode(hdr.Mode).Perm()})
		case tar.TypeSymlink:
			tr.symlink(t, link{rel: rel, target: hdr.Linkname})
		case tar.TypeXGlobalHeader:
			// The global header of git archive holds only the ID of the commit.
		default:
			t.Fatalf("archive entry %q has the unsupported type %q", hdr.Name, hdr.Typeflag)
		}
	}
}

// moduleLayer replaces the directory go-lib-ci with the files of the version of the
// module from go mod download.
func (tr *tree) moduleLayer(t *testing.T, version string) {
	t.Helper()
	cmd := exec.Command("go", "mod", "download", "-json", libModule+"@"+version)
	// Outside the module of mega-doub go mod download does not touch its go.mod.
	cmd.Dir = t.TempDir()
	var info struct{ Zip string }
	must(t, json.Unmarshal(output(t, cmd), &info))
	archive, err := zip.OpenReader(info.Zip)
	must(t, err)
	defer func() { _ = archive.Close() }()
	tr.remove(t, "go-lib-ci")
	tr.mkdir(t, "go-lib-ci")
	prefix := libModule + "@" + version + "/"
	for _, f := range archive.File {
		name, ok := strings.CutPrefix(f.Name, prefix)
		if !ok || !filepath.IsLocal(name) {
			t.Fatalf("module entry %q is outside %s", f.Name, prefix)
		}
		tr.write(t, file{rel: path.Join("go-lib-ci", name), data: readZip(t, f)})
	}
	tr.notes = append(tr.notes, "go-lib-ci "+version)
}

// readZip returns the content of the file f of a zip archive.
func readZip(t *testing.T, f *zip.File) []byte {
	t.Helper()
	r, err := f.Open()
	must(t, err)
	data, err := io.ReadAll(r)
	must(t, errors.Join(err, r.Close()))
	return data
}

// gorootLayer copies src/cmd/compile of the installed Go into the directory goroot.
func (tr *tree) gorootLayer(t *testing.T) {
	t.Helper()
	goroot := text(t, exec.Command("go", "env", "GOROOT"))
	version := text(t, exec.Command("go", "env", "GOVERSION"))
	src := filepath.Join(goroot, "src", "cmd", "compile")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := path.Join("goroot", filepath.ToSlash(rel))
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			tr.mkdir(t, target)
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			tr.write(t, file{rel: target, data: data, mode: info.Mode().Perm()})
		default:
			return fmt.Errorf("%s has the unsupported type %s", p, info.Mode().Type())
		}
		return nil
	})
	must(t, err)
	tr.notes = append(tr.notes, "goroot "+version+" src/cmd/compile")
}

// smallFiles writes 300 files of 0 to 4 KiB with random content.
func (tr *tree) smallFiles(t *testing.T) {
	t.Helper()
	sizes := rand.New(chacha(3))
	content := chacha(4)
	for i := range 300 {
		data := make([]byte, sizes.IntN(4<<10+1))
		_, _ = content.Read(data)
		tr.write(t, file{rel: fmt.Sprintf("synthetic/small/%03d.dat", i), data: data})
	}
}

// randomBytes returns the first n bytes of the ChaCha8 stream of seed, the same in every
// run.
func randomBytes(seed uint64, n int) []byte {
	data := make([]byte, n)
	_, _ = chacha(seed).Read(data)
	return data
}

// chacha returns the ChaCha8 generator of seed.
func chacha(seed uint64) *rand.ChaCha8 {
	var key [32]byte
	binary.LittleEndian.PutUint64(key[:], seed)
	return rand.NewChaCha8(key)
}

// abs returns the path of rel in the file system.
func (tr *tree) abs(rel string) string {
	return filepath.Join(tr.root, filepath.FromSlash(rel))
}

// write creates or replaces the file f with its parents and sets its mode and time.
func (tr *tree) write(t *testing.T, f file) {
	t.Helper()
	mode := f.mode
	if mode == 0 {
		mode = 0o644
	}
	p := tr.abs(f.rel)
	must(t, os.MkdirAll(filepath.Dir(p), 0o755))
	must(t, os.WriteFile(p, f.data, mode))
	must(t, os.Chmod(p, mode))
	tr.touch(t, f.rel)
}

// patch writes over a part of a file and sets its time.
func (tr *tree) patch(t *testing.T, p patch) {
	t.Helper()
	w, err := os.OpenFile(tr.abs(p.rel), os.O_WRONLY, 0)
	must(t, err)
	_, err = w.WriteAt(p.data, p.offset)
	must(t, errors.Join(err, w.Close()))
	tr.touch(t, p.rel)
}

// appendTo adds the data of f to the end of the file f.rel and sets its time.
func (tr *tree) appendTo(t *testing.T, f file) {
	t.Helper()
	info, err := os.Stat(tr.abs(f.rel))
	must(t, err)
	tr.patch(t, patch{rel: f.rel, offset: info.Size(), data: f.data})
}

// chmod sets the mode of f.rel to f.mode and keeps its content and time.
func (tr *tree) chmod(t *testing.T, f file) {
	t.Helper()
	must(t, os.Chmod(tr.abs(f.rel), f.mode))
}

// touch sets the access and modification times of rel to the time of the revision.
func (tr *tree) touch(t *testing.T, rel string) {
	t.Helper()
	at := revisionTime(tr.rev)
	must(t, os.Chtimes(tr.abs(rel), at, at))
}

// mkdir creates the directory rel with its parents.
func (tr *tree) mkdir(t *testing.T, rel string) {
	t.Helper()
	must(t, os.MkdirAll(tr.abs(rel), 0o755))
}

// symlink creates the link l with the parents of its path.
func (tr *tree) symlink(t *testing.T, l link) {
	t.Helper()
	p := tr.abs(l.rel)
	must(t, os.MkdirAll(filepath.Dir(p), 0o755))
	must(t, os.Symlink(l.target, p))
}

// remove removes rel with its content.
func (tr *tree) remove(t *testing.T, rel string) {
	t.Helper()
	must(t, os.RemoveAll(tr.abs(rel)))
}

// move renames an entry and sets the time of its new path.
func (tr *tree) move(t *testing.T, m move) {
	t.Helper()
	must(t, os.Rename(tr.abs(m.from), tr.abs(m.to)))
	tr.touch(t, m.to)
}
