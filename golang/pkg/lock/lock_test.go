// cspell:ignore Getpid Mkfifo

package lock

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// ownPID is the content of the lock file written for this process.
func ownPID() string {
	return strconv.Itoa(os.Getpid()) + "\n"
}

func TestAcquire(t *testing.T) {
	type args struct {
		dir string
	}
	tests := []struct {
		name       string
		args       func(t *testing.T) args
		wantMode   os.FileMode
		wantParent os.FileMode
	}{
		{
			name: "missing directories",
			args: func(t *testing.T) args {
				return args{dir: filepath.Join(t.TempDir(), "state", "main")}
			},
			wantMode:   0o700,
			wantParent: 0o700,
		},
		{
			name: "existing directory keeps its mode",
			args: func(t *testing.T) args {
				dir := filepath.Join(t.TempDir(), "state")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o750); err != nil {
					t.Fatal(err)
				}
				return args{dir: dir}
			},
			wantMode: 0o750,
		},
		{
			name: "stale lock file is truncated",
			args: func(t *testing.T) args {
				dir := filepath.Join(t.TempDir(), "state")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, "lock")
				stale := []byte("4194304 left by a run that is gone\n")
				if err := os.WriteFile(path, stale, 0o600); err != nil {
					t.Fatal(err)
				}
				return args{dir: dir}
			},
			wantMode: 0o700,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := tt.args(t)
			got, err := Acquire(a.dir)
			if err != nil {
				t.Fatalf("Acquire() error = %v", err)
			}
			info, err := os.Stat(a.dir)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != tt.wantMode {
				t.Errorf("directory mode = %v, want %v", info.Mode().Perm(), tt.wantMode)
			}
			if tt.wantParent != 0 {
				info, err = os.Stat(filepath.Dir(a.dir))
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != tt.wantParent {
					mode := info.Mode().Perm()
					t.Errorf("parent directory mode = %v, want %v", mode, tt.wantParent)
				}
			}
			path := filepath.Join(a.dir, "lock")
			info, err = os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			// The open descriptor keeps the inode number from being reused after Release.
			acquired, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("file mode = %v, want 0600", info.Mode().Perm())
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != ownPID() {
				t.Errorf("file = %q, want %q", data, ownPID())
			}
			if err := got.Release(); err != nil {
				t.Errorf("Lock.Release() error = %v", err)
			}
			info, err = os.Stat(path)
			if err != nil {
				t.Fatalf("lock file after Release(): %v", err)
			}
			acquiredInfo, err := acquired.Stat()
			if err != nil {
				t.Fatal(err)
			}
			// Release must keep the file, otherwise two runs can lock different inodes.
			if !os.SameFile(acquiredInfo, info) {
				t.Errorf("lock file after Release() is not the file locked by Acquire()")
			}
			if err := acquired.Close(); err != nil {
				t.Errorf("close of the descriptor opened before Release(): %v", err)
			}
			if !info.Mode().IsRegular() {
				t.Errorf("lock file after Release() mode = %v, want regular", info.Mode())
			}
			data, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != ownPID() {
				t.Errorf("file after Release() = %q, want %q", data, ownPID())
			}
			again, err := Acquire(a.dir)
			if err != nil {
				t.Fatalf("Acquire() after Release() error = %v", err)
			}
			if err := again.Release(); err != nil {
				t.Errorf("Lock.Release() error = %v", err)
			}
		})
	}
}

func TestAcquire_busy(t *testing.T) {
	type fields struct {
		write   bool
		content string
	}
	tests := []struct {
		name    string
		fields  fields
		wantPID int
	}{
		{name: "holder", fields: fields{}, wantPID: os.Getpid()},
		{name: "spaces", fields: fields{write: true, content: " 4242 \n"}, wantPID: 4242},
		{name: "empty", fields: fields{write: true, content: ""}, wantPID: 0},
		{name: "not a number", fields: fields{write: true, content: "abc\n"}, wantPID: 0},
		{name: "with text", fields: fields{write: true, content: "42 x\n"}, wantPID: 0},
		{name: "zero", fields: fields{write: true, content: "0\n"}, wantPID: 0},
		{name: "negative", fields: fields{write: true, content: "-7\n"}, wantPID: 0},
		{
			name:    "number before long text",
			fields:  fields{write: true, content: "42" + strings.Repeat(" ", 30) + "x\n"},
			wantPID: 0,
		},
		{
			name:    "number with long padding",
			fields:  fields{write: true, content: "42" + strings.Repeat(" ", 40) + "\n"},
			wantPID: 0,
		},
		{
			name:    "number with padding to the limit",
			fields:  fields{write: true, content: "42" + strings.Repeat(" ", 29) + "\n"},
			wantPID: 42,
		},
		{
			name:    "overflow",
			fields:  fields{write: true, content: "99999999999999999999\n"},
			wantPID: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			held, err := Acquire(dir)
			if err != nil {
				t.Fatalf("Acquire() error = %v", err)
			}
			path := filepath.Join(dir, "lock")
			if tt.fields.write {
				content := []byte(tt.fields.content)
				if err := os.WriteFile(path, content, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Acquire(dir)
			if got != nil {
				t.Errorf("Acquire() = %v, want nil", got)
			}
			var busy *BusyError
			if !errors.As(err, &busy) {
				t.Fatalf("Acquire() error = %v, want *BusyError", err)
			}
			if busy.Path != path || busy.PID != tt.wantPID {
				t.Errorf("BusyError = %+v, want %s and %d", busy, path, tt.wantPID)
			}
			if err := held.Release(); err != nil {
				t.Errorf("Lock.Release() error = %v", err)
			}
			again, err := Acquire(dir)
			if err != nil {
				t.Fatalf("Acquire() after Release() error = %v", err)
			}
			if err := again.Release(); err != nil {
				t.Errorf("Lock.Release() error = %v", err)
			}
		})
	}
}

func TestAcquire_failure(t *testing.T) {
	type args struct {
		dir string
	}
	tests := []struct {
		name    string
		args    func(t *testing.T) args
		wantErr string
		wantIs  error
	}{
		{
			name:    "empty directory",
			args:    func(*testing.T) args { return args{} },
			wantErr: "state directory is required",
		},
		{
			name: "directory under a regular file",
			args: func(t *testing.T) args {
				file := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(file, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return args{dir: filepath.Join(file, "state")}
			},
			wantIs: syscall.ENOTDIR,
		},
		{
			name: "lock file is a symbolic link",
			args: func(t *testing.T) args {
				root := t.TempDir()
				target := filepath.Join(root, "target")
				if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(root, "state")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(dir, "lock")); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					data, err := os.ReadFile(target)
					if err != nil || string(data) != "keep\n" {
						t.Errorf("target = %q, %v, want %q", data, err, "keep\n")
					}
				})
				return args{dir: dir}
			},
			wantIs: syscall.ELOOP,
		},
		{
			name: "lock file is a directory",
			args: func(t *testing.T) args {
				dir := t.TempDir()
				if err := os.Mkdir(filepath.Join(dir, "lock"), 0o700); err != nil {
					t.Fatal(err)
				}
				return args{dir: dir}
			},
			wantIs: syscall.EISDIR,
		},
		{
			name: "lock file is a named pipe",
			args: func(t *testing.T) args {
				dir := t.TempDir()
				if err := syscall.Mkfifo(filepath.Join(dir, "lock"), 0o600); err != nil {
					t.Fatal(err)
				}
				return args{dir: dir}
			},
			wantIs: errNotRegular,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := tt.args(t)
			got, err := Acquire(a.dir)
			if got != nil {
				t.Errorf("Acquire() = %v, want nil", got)
			}
			if err == nil {
				t.Fatal("Acquire() error = nil, want an error")
			}
			var busy *BusyError
			if errors.As(err, &busy) {
				t.Errorf("Acquire() error = %v, want not *BusyError", err)
			}
			if tt.wantErr != "" && err.Error() != tt.wantErr {
				t.Errorf("Acquire() error = %q, want %q", err, tt.wantErr)
			}
			if tt.wantIs != nil {
				if !errors.Is(err, tt.wantIs) {
					t.Errorf("Acquire() error = %v, want cause %v", err, tt.wantIs)
				}
				wantPrefix := "acquire lock " + filepath.Join(a.dir, "lock") + ": "
				if !strings.HasPrefix(err.Error(), wantPrefix) {
					t.Errorf("Acquire() error = %q, want prefix %q", err, wantPrefix)
				}
			}
		})
	}
}

func TestLock_Release(t *testing.T) {
	held, err := Acquire(t.TempDir())
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	tests := []struct {
		name string
		l    *Lock
	}{
		{name: "first", l: held},
		{name: "second", l: held},
		{name: "nil", l: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.l.Release(); err != nil {
				t.Errorf("Lock.Release() error = %v", err)
			}
		})
	}
	t.Run("close error", func(t *testing.T) {
		l, err := Acquire(t.TempDir())
		if err != nil {
			t.Fatalf("Acquire() error = %v", err)
		}
		if err := l.file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := l.Release(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("first Lock.Release() error = %v, want %v", err, os.ErrClosed)
		}
		if err := l.Release(); err != nil {
			t.Errorf("second Lock.Release() error = %v, want nil", err)
		}
	})
	t.Run("concurrent", func(t *testing.T) {
		l, err := Acquire(t.TempDir())
		if err != nil {
			t.Fatalf("Acquire() error = %v", err)
		}
		const callers = 16
		errs := make([]error, callers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = l.Release()
			}()
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("Lock.Release() of caller %d error = %v", i, err)
			}
		}
	})
}

func TestBusyError_Error(t *testing.T) {
	type fields struct {
		Path string
		PID  int
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name:   "with holder",
			fields: fields{Path: "/var/lib/mega-doub/state/main/lock", PID: 4242},
			want:   "another run is in progress",
		},
		{name: "unknown holder", fields: fields{}, want: "another run is in progress"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &BusyError{
				Path: tt.fields.Path,
				PID:  tt.fields.PID,
			}
			if got := e.Error(); got != tt.want {
				t.Errorf("BusyError.Error() = %v, want %v", got, tt.want)
			}
		})
	}
}
