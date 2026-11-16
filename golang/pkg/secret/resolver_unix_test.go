//go:build unix

// cspell:ignore Geteuid Mkfifo

package secret

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func Test_resolver_Resolve_special(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		ref  Ref
	}{
		{name: "device", ref: "file:/dev/null"},
		{name: "named pipe without a writer", ref: Ref("file:" + fifo)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				_, err := New().Resolve(t.Context(), tt.ref)
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
					t.Errorf("resolver.Resolve() error = %v, want not a regular file", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("resolver.Resolve() hangs")
			}
		})
	}
}

func Test_resolver_Resolve_unreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file without the read permission")
	}
	path := writeSecret(t, canary)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	_, err := New().Resolve(t.Context(), Ref("file:"+path))
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("resolver.Resolve() error = %v, want %v", err, os.ErrPermission)
	}
	if !strings.Contains(err.Error(), path) || strings.Contains(err.Error(), canary) {
		t.Errorf("resolver.Resolve() error = %v, want the path without the content", err)
	}
}
