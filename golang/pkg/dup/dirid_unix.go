// cspell:ignore Ino

//go:build unix

// Package dup is documented in log.go.
package dup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// statDir returns the identity of the directory name, following the links, and false when
// name is not a directory.
func statDir(name string) (dirID, bool, error) {
	info, err := os.Stat(name)
	if err != nil {
		return dirID{}, false, err
	}
	if !info.IsDir() {
		return dirID{}, false, nil
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return dirID{}, false, fmt.Errorf("stat %s: no file identity", name)
	}
	return dirID{volume: uint64(st.Dev), index: uint64(st.Ino)}, true, nil
}

// unreachable tells whether err means an entry that the process can't reach: no access,
// no entry, a loop of links or a path through a file.
func unreachable(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR)
}
