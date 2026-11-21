//go:build windows

// Package dup is documented in log.go.
package dup

import (
	"errors"
	"io/fs"
	"syscall"
)

// statDir returns the identity of the directory name, following the links, and false when
// name is not a directory. The identity is the volume serial number and the file index of
// the open directory.
func statDir(name string) (dirID, bool, error) {
	pathPtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return dirID{}, false, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	handle, err := syscall.CreateFile(pathPtr, 0,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return dirID{}, false, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	defer func() { _ = syscall.CloseHandle(handle) }()
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &info); err != nil {
		return dirID{}, false, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return dirID{}, false, nil
	}
	return dirID{
		volume: uint64(info.VolumeSerialNumber),
		index:  uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow),
	}, true, nil
}

// The errors of Windows that a path through a file, a link in a loop or a reparse point
// that can't be resolved give.
const (
	errorCantAccessFile  = syscall.Errno(1920)
	errorCantResolveName = syscall.Errno(1921)
	errorDirectory       = syscall.Errno(267)
)

// unreachable tells whether err means an entry that the process can't reach: no access,
// no entry, a link that can't be resolved or a path through a file.
func unreachable(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, errorCantAccessFile) || errors.Is(err, errorCantResolveName) ||
		errors.Is(err, errorDirectory)
}
