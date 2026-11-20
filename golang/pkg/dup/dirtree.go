// cspell:ignore duplicacy

// Package dup is documented in log.go.
package dup

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// dirBatch is the number of the entries that walkDirs reads from a directory at once, so
// that a directory with a million files is not held in memory.
const dirBatch = 256

// dirID identifies a directory as a file object, whatever path leads to it: two paths of
// one directory, through links or mounts, give the same dirID.
type dirID struct {
	volume uint64
	index  uint64
}

// walkItem is a directory that walkDirs reaches: its path and whether the walk came to it
// through a symbolic link, directly or as an irregular entry.
type walkItem struct {
	path string
	link bool
}

// walkStep is what walkDirs gives to visit for a directory: its identity, its path and
// whether the walk came to it through a symbolic link.
type walkStep struct {
	id   dirID
	path string
	link bool
}

// statDirFunc and openDirFunc are statDir and os.Open; tests replace them.
var (
	statDirFunc = statDir
	openDirFunc = os.Open
)

// walkDirs calls visit for every directory of the tree of root, following the symbolic
// links to directories, until visit returns true. A directory that the walk has seen,
// whatever path leads to it, is visited and read once, so a link to an ancestor does not
// loop. The entries of a directory are read by their types without a stat; a stat is made
// for the directories and the links only. A root, an entry or a directory that disappears
// during the walk is skipped. Below the root an entry that the process can't reach, see
// unreachable, is skipped without a failure: lost+found of a mounted partition, a child
// of a directory that can be read but not passed, a link through a file or to a directory
// behind another that can't be passed, or a link in a loop; a directory that can be stat
// but not read is visited, but its subdirectories are not walked. That is safe, as a
// directory that the process can't read is not found by the walk of its descendants, but
// is found by the comparison of the ancestors of the cache directories reached through a
// link and of the path of the storage root, see cacheDirs and checkCache. A bind mount of
// a descendant of such a directory into the cache, which needs root, is not found. Any
// other error, such as an I/O error or a timeout of a network mount, fails the walk, as
// the entry may be a directory of the other tree, and so does any error of the root.
func walkDirs(root string, visit func(walkStep) bool) error {
	seen := map[dirID]struct{}{}
	stack := []walkItem{{path: root}}
	for first := true; len(stack) > 0; first = false {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		id, isDir, err := statDirFunc(item.path)
		if err != nil {
			if first && errors.Is(err, fs.ErrNotExist) || !first && unreachable(err) {
				continue
			}
			return err
		}
		if !isDir {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if visit(walkStep{id: id, path: item.path, link: item.link}) {
			return nil
		}
		subdirs, err := appendSubdirs(stack, item.path)
		stack = subdirs
		if err != nil && (first || !unreachable(err)) {
			return err
		}
	}
	return nil
}

// appendSubdirs appends to items the entries of dir that may be directories: the
// directories and the links, which the walk resolves. A dir that disappears gives no
// entries. On an error the entries that were read are appended all the same.
func appendSubdirs(items []walkItem, dir string) ([]walkItem, error) {
	f, err := openDirFunc(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return items, nil
	}
	if err != nil {
		return items, err
	}
	defer func() { _ = f.Close() }()
	for {
		entries, err := f.ReadDir(dirBatch)
		for _, entry := range entries {
			link := entry.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0
			if entry.IsDir() || link {
				path := filepath.Join(dir, entry.Name())
				items = append(items, walkItem{path: path, link: link})
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, fs.ErrNotExist) {
			return items, nil
		}
		if err != nil {
			return items, err
		}
	}
}
