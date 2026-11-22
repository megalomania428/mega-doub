// cspell:ignore EINTR EWOULDBLOCK Getpid NOFOLLOW NONBLOCK flock

// Package lock keeps the runs of one instance from overlapping on one host.
package lock

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// fileName is the name of the lock file in the state directory.
const fileName = "lock"

// openFlags open or create the lock file without following a symbolic link and without
// waiting on a special file.
const openFlags = os.O_RDWR | os.O_CREATE | syscall.O_NOFOLLOW | syscall.O_NONBLOCK

// pidLimit bounds the bytes of the lock file that may hold the process ID of the holder;
// a longer file holds no valid one.
const pidLimit = 32

// errNotRegular is the cause when the lock file is not a regular file.
var errNotRegular = errors.New("not a regular file")

// BusyError is the error of Acquire when another open file of the lock file holds the
// lock, in another process or in this one.
type BusyError struct {
	Path string // path of the lock file
	// PID is the process ID read from the file: usually of the holder, but right after
	// the holder changes it may be of the previous run; 0 when the file holds no number.
	PID int
}

// Error returns the short text; the path and the process ID are in the fields.
func (e *BusyError) Error() string {
	return "another run is in progress"
}

// Lock is the lock of a state directory held until Release.
type Lock struct {
	file *os.File
	once sync.Once
}

// Path returns the path of the lock file of the state directory dir, the one that
// Acquire locks: dir joined with the name lock as written, links are not resolved.
func Path(dir string) string {
	return filepath.Join(dir, fileName)
}

// Acquire locks the state directory dir without waiting. It creates dir and its missing
// parents with the mode 0700 and keeps the mode of an existing one, opens or creates the
// file lock in it with the mode 0600 and takes flock(LOCK_EX|LOCK_NB) on it. On success
// the file holds the process ID of the caller and a newline. A lock held by another open
// file gives *BusyError with the process ID read from the file; a symbolic link or not a
// regular file in place of the lock file and other failures give an error with the path.
func Acquire(dir string) (*Lock, error) {
	if dir == "" {
		return nil, errors.New("state directory is required")
	}
	path := Path(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("acquire lock %s: %w", path, err)
	}
	file, err := os.OpenFile(path, openFlags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("acquire lock %s: %w", path, err)
	}
	if err := hold(file); err != nil {
		_ = file.Close()
		var busy *BusyError
		if errors.As(err, &busy) {
			busy.Path = path
			return nil, busy
		}
		return nil, fmt.Errorf("acquire lock %s: %w", path, err)
	}
	return &Lock{file: file}, nil
}

// hold locks the open lock file and writes the process ID to it; a held lock gives
// *BusyError without the path.
func hold(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errNotRegular
	}
	if err := flock(file); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return &BusyError{PID: readPID(file)}
		}
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	_, err = file.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	return err
}

// flock takes the exclusive lock of the file without waiting and repeats the call
// interrupted by a signal.
func flock(file *os.File) error {
	conn, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var lockErr error
	err = conn.Control(func(fd uintptr) {
		for {
			lockErr = syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
			if !errors.Is(lockErr, syscall.EINTR) {
				return
			}
		}
	})
	if err != nil {
		return err
	}
	return lockErr
}

// readPID reads the process ID of the holder from the file: a positive decimal number
// with optional white space around it and nothing else in at most pidLimit bytes,
// otherwise 0. One byte past pidLimit is read to tell a longer file.
func readPID(file *os.File) int {
	data, err := io.ReadAll(io.LimitReader(file, pidLimit+1))
	if err != nil || len(data) > pidLimit {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// Release unlocks the state directory by closing the lock file. The file stays: removing
// it would let a new run lock a new file while another run still holds the removed one.
// Only the first call closes the file and returns the error of the close; later calls,
// concurrent ones included, and a nil Lock return nil.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	var err error
	l.once.Do(func() {
		err = l.file.Close()
	})
	return err
}
