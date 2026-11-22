// cspell:ignore duplicacy

package dup

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	duplicacy "duplicacy.invalid/v3/src"
)

func TestSession_Created(t *testing.T) {
	type fields struct {
		// preexisting is a config created before Open by another client.
		preexisting bool
		// read opens the session without WithInit.
		read bool
		// state scripts the calls of the storage, see spyStorage.
		state spyState
	}
	tests := []struct {
		name   string
		fields fields
		want   bool
	}{
		{name: "empty storage", want: true},
		{name: "initialized storage", fields: fields{preexisting: true}, want: false},
		{
			name:   "initialized storage without WithInit",
			fields: fields{preexisting: true, read: true},
			want:   false,
		},
		{
			name: "config created by another client",
			fields: fields{
				preexisting: true, state: spyState{configMiss: map[int]bool{1: true}},
			},
			want: false,
		},
		{
			name:   "lost response of the upload of the config",
			fields: fields{state: spyState{uploadFails: map[int]bool{1: true}}},
			want:   false,
		},
		{
			name:   "mkdir failure after the upload of the config",
			fields: fields{state: spyState{dirFails: map[int]bool{1: true}}},
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keepGlobals(t)
			storageDir := filepath.Join(t.TempDir(), "storage")
			if tt.fields.preexisting {
				initWithoutSubdirectories(t, storageDir)
			}
			state := tt.fields.state
			replaceCreateStorage(t, func(
				preference duplicacy.Preference, resetPassword bool, threads int,
			) duplicacy.Storage {
				storage := duplicacy.CreateStorage(preference, resetPassword, threads)
				return &spyStorage{Storage: storage, state: &state}
			})
			opts := testOptions(t, storageDir, testPassword, nil, fastRetry())
			if tt.fields.read {
				opts = readOptions(t, storageDir, testPassword, nil, fastRetry())
			}
			s := openTest(t, opts)
			if got := s.Created(); got != tt.want {
				t.Errorf("Session.Created() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestErrorText(t *testing.T) {
	cause := errors.New("dial fake-secret: connection refused")
	exception := &Error{Op: opCreateStorage, LogID: "STORAGE_CREATE", Err: cause}
	masks := []string{"fake-secret"}
	type args struct {
		err error
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "error of Open with an *Error",
			args: args{err: &maskedError{
				err: fmt.Errorf("storage init default: %w", exception), masks: masks,
			}},
			want: "storage init default: create storage: duplicacy STORAGE_CREATE: " +
				"dial ***: connection refused",
		},
		{
			name: "error of Open without an *Error",
			args: args{err: &maskedError{
				err: fmt.Errorf("storage init default: %w", errWrongPassword), masks: masks,
			}},
			want: "storage init default: " + errWrongPassword.Error(),
		},
		{
			name: "*Error without a cause",
			args: args{err: &Error{Op: opCreateStorage, LogID: "STORAGE_CREATE"}},
			want: "create storage: duplicacy STORAGE_CREATE",
		},
		{
			name: "error without the masks of a session",
			args: args{err: exception},
			want: "create storage: duplicacy STORAGE_CREATE: dial fake-secret: " +
				"connection refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ErrorText(tt.args.err); got != tt.want {
				t.Errorf("ErrorText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSession_ErrorText(t *testing.T) {
	cause := errors.New("dial fake-key-value: connection refused")
	type fields struct {
		masks []string
	}
	type args struct {
		err error
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   string
	}{
		{
			name:   "*Error with a key in its cause",
			fields: fields{masks: []string{testPassword, "fake-key-value"}},
			args: args{err: &Error{Op: opCreateStorage, LogID: "STORAGE_CREATE",
				Err: cause}},
			want: "create storage: duplicacy STORAGE_CREATE: dial ***: connection refused",
		},
		{
			name:   "error without an *Error",
			fields: fields{masks: []string{testPassword}},
			args:   args{err: fmt.Errorf("read %s: denied", testPassword)},
			want:   "read ***: denied",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{masks: tt.fields.masks}
			if got := s.ErrorText(tt.args.err); got != tt.want {
				t.Errorf("Session.ErrorText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpen_secretsMasked(t *testing.T) {
	const key = "fake-key-value"
	type args struct {
		// init adds WithInit.
		init bool
	}
	tests := []struct {
		name string
		args args
	}{
		{name: "with WithInit", args: args{init: true}},
		{name: "without WithInit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keepGlobals(t)
			dir := t.TempDir()
			file := filepath.Join(dir, "file")
			if err := os.WriteFile(file, []byte("fake"), 0o600); err != nil {
				t.Fatal(err)
			}
			// The path of the storage holds the values of the password and the key, so
			// the errors of duplicacy hold them too.
			storageURL := filepath.Join(file, key, testPassword, "storage")
			var buf bytes.Buffer
			logger := newLogger(&buf, slog.LevelDebug)
			opts := readOptions(t, storageURL, testPassword, logger, fastRetry())
			if tt.args.init {
				opts = append(opts, WithInit())
			}
			opts = append(opts, WithKeys(map[string]string{"ssh_password": key}))
			s, err := Open(t.Context(), opts...)
			if err == nil {
				s.Close()
				t.Fatal("Open() error = nil, want an error")
			}
			text := ErrorText(err)
			if !strings.Contains(text, "***") {
				t.Errorf("ErrorText() = %q, want *** in place of the secrets", text)
			}
			for _, secret := range []string{testPassword, key} {
				if strings.Contains(text, secret) || strings.Contains(err.Error(), secret) {
					t.Errorf("Open() error %q contains %q", text, secret)
				}
				if strings.Contains(buf.String(), secret) {
					t.Errorf("records %q contain %q", buf.String(), secret)
				}
			}
		})
	}
}

func TestOpen_secretsMaskedInRetries(t *testing.T) {
	const key = "fake-key-value"
	type args struct {
		// init adds WithInit.
		init bool
	}
	tests := []struct {
		name string
		args args
	}{
		{name: "with WithInit", args: args{init: true}},
		{name: "without WithInit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keepGlobals(t)
			// Every creation of the storage fails with a refused connection whose text
			// holds the values of the key and the password, so every attempt is retried.
			replaceCreateStorage(t, func(duplicacy.Preference, bool, int) duplicacy.Storage {
				cause := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
				panic(duplicacy.Exception{
					LogID:   "STORAGE_CREATE",
					Message: "dial " + key + " with " + testPassword,
					Err:     fmt.Errorf("dial %s with %s: %w", key, testPassword, cause),
				})
			})
			var buf bytes.Buffer
			logger := newLogger(&buf, slog.LevelDebug)
			storageDir := filepath.Join(t.TempDir(), "storage")
			opts := readOptions(t, storageDir, testPassword, logger, fastRetry())
			if tt.args.init {
				opts = append(opts, WithInit())
			}
			opts = append(opts, WithKeys(map[string]string{"ssh_password": key}))
			s, err := Open(t.Context(), opts...)
			if err == nil {
				s.Close()
				t.Fatal("Open() error = nil, want an error")
			}
			retries := 0
			for _, r := range records(t, &buf) {
				if r["msg"] == msgRequestFailed {
					retries++
				}
			}
			if retries == 0 {
				t.Errorf("Open() wrote no records %s, want the retries: %s", msgRequestFailed,
					&buf)
			}
			text := ErrorText(err)
			if !strings.Contains(text, "***") {
				t.Errorf("ErrorText() = %q, want *** in place of the secrets", text)
			}
			for _, secret := range []string{testPassword, key} {
				if strings.Contains(text, secret) || strings.Contains(err.Error(), secret) {
					t.Errorf("Open() error %q contains %q", text, secret)
				}
				if strings.Contains(buf.String(), secret) {
					t.Errorf("records %q contain %q", buf.String(), secret)
				}
			}
		})
	}
}
