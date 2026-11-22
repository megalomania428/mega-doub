// cspell:ignore duplicac duplicacy duplicacyx sftp

package dup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	duplicacy "duplicacy.invalid/v3/src"

	"github.com/megalomania428/mega-doub/pkg/retry"
)

// The passwords and the snapshot ID of the tests.
const (
	testPassword      = "fake-password-a"
	testOtherPassword = "fake-password-b"
	testSnapshotID    = "test"
)

// fastRetry keeps the retries of a test within the budget of 200 ms.
func fastRetry() retry.Options {
	return retry.Options{
		MaxRetriesTime:      200 * time.Millisecond,
		BackoffInitial:      5 * time.Millisecond,
		BackoffStep:         5 * time.Millisecond,
		BackoffStepAttempts: 3,
		BackoffCap:          20 * time.Millisecond,
	}
}

// noRetry disables the retries.
func noRetry() retry.Options {
	options := fastRetry()
	options.MaxRetriesTime = -1
	return options
}

// keepGlobals gives the global state of duplicacy that a session changes back after
// the test.
func keepGlobals(t *testing.T) {
	t.Helper()
	background := duplicacy.RunInBackground
	logFunction := duplicacy.LogFunction
	progressFunction := duplicacy.ProgressFunction
	t.Cleanup(func() {
		duplicacy.RunInBackground = background
		duplicacy.LogFunction = logFunction
		duplicacy.ProgressFunction = progressFunction
	})
}

// testOptions returns the options of a session of the test on storageURL that
// initializes the storage.
func testOptions(
	t *testing.T, storageURL, password string, logger *slog.Logger, r retry.Options,
) []SessionOption {
	t.Helper()
	return append(readOptions(t, storageURL, password, logger, r), WithInit())
}

// readOptions returns the options of a session of the test on storageURL without
// WithInit.
func readOptions(
	t *testing.T, storageURL, password string, logger *slog.Logger, r retry.Options,
) []SessionOption {
	t.Helper()
	return []SessionOption{
		WithCopyDir(filepath.Join(t.TempDir(), "copy")),
		WithStateDir(filepath.Join(t.TempDir(), "state")),
		WithURL(storageURL),
		WithSnapshotID(testSnapshotID),
		WithPassword(password),
		WithThreads(1),
		WithLogger(logger),
		WithRetry(r),
	}
}

// openTest opens a session that is closed after the test.
func openTest(t *testing.T, opts []SessionOption) *Session {
	t.Helper()
	s, err := Open(t.Context(), opts...)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// attempts returns the attempt numbers of the records of failed attempts and checks
// that each one is a warning of operation with an error.
func attempts(t *testing.T, buf *bytes.Buffer, operation string) []float64 {
	t.Helper()
	var numbers []float64
	for _, r := range records(t, buf) {
		if r["msg"] != msgRequestFailed {
			continue
		}
		text, _ := r["error"].(string)
		if r["level"] != "WARN" || r["operation"] != operation || text == "" {
			t.Errorf("record = %v", r)
		}
		number, _ := r["attempt"].(float64)
		numbers = append(numbers, number)
	}
	return numbers
}

// closedURL returns an SFTP URL of a port of 127.0.0.1 that nothing listens on, with
// userinfo when it is not empty.
func closedURL(t *testing.T, userinfo string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if userinfo != "" {
		userinfo += "@"
	}
	return "sftp://" + userinfo + addr + "/x"
}

// initPlain initializes an unencrypted storage in dir, as the duplicacy CLI does
// without a password.
func initPlain(t *testing.T, dir string) {
	t.Helper()
	keepGlobals(t)
	duplicacy.RunInBackground = true
	duplicacy.LogFunction = LogFunction(nil)
	preference := duplicacy.Preference{Name: defaultStorageName, StorageURL: dir}
	storage := duplicacy.CreateStorage(preference, false, 1)
	if !duplicacy.ConfigStorage(storage, duplicacy.CONFIG_DEFAULT_ITERATIONS,
		duplicacy.DEFAULT_COMPRESSION_LEVEL, averageChunkSize, maximumChunkSize,
		minimumChunkSize, "", nil, false, "", 0, 0) {
		t.Fatal("ConfigStorage() = false")
	}
}

// flakyStorage fails CreateDirectory of chunks with a reset connection while failed
// is false and sets failed; the storages of the attempts share failed.
type flakyStorage struct {
	duplicacy.Storage
	failed *bool
}

func (f *flakyStorage) CreateDirectory(threadIndex int, dir string) error {
	if dir == "chunks" && !*f.failed {
		*f.failed = true
		return &net.OpError{Op: "write", Net: "tcp", Err: syscall.ECONNRESET}
	}
	return f.Storage.CreateDirectory(threadIndex, dir)
}

// cancelDelay is the time from the record of the first failed attempt to the
// cancellation, so that the wait after the attempt has begun when it is canceled.
const cancelDelay = 20 * time.Millisecond

// cancelWriter cancels a context cancelDelay after a record of a failed attempt is
// written to buf and keeps the time of the cancellation.
type cancelWriter struct {
	buf      *bytes.Buffer
	cancel   context.CancelFunc
	once     sync.Once
	mu       sync.Mutex
	canceled time.Time
}

func (w *cancelWriter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if bytes.Contains(p, []byte(`"msg":"`+msgRequestFailed+`"`)) {
		w.once.Do(func() {
			time.AfterFunc(cancelDelay, func() {
				w.mu.Lock()
				w.canceled = time.Now()
				w.mu.Unlock()
				w.cancel()
			})
		})
	}
	return n, err
}

// canceledAt returns the time of the cancellation, zero before it.
func (w *cancelWriter) canceledAt() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.canceled
}

func TestOpen(t *testing.T) {
	keepGlobals(t)
	storageDir := filepath.Join(t.TempDir(), "storage")
	stateDir := filepath.Join(t.TempDir(), "state")
	opts := append(testOptions(t, storageDir, testPassword, nil, fastRetry()),
		WithStateDir(stateDir))
	s := openTest(t, opts)
	if s.storage == nil {
		t.Fatal("Open() storage = nil")
	}
	s.Close()
	for _, name := range []string{"chunks", "snapshots"} {
		info, err := os.Stat(filepath.Join(storageDir, name))
		if err != nil || !info.IsDir() {
			t.Errorf("storage %s: info = %v, error = %v", name, info, err)
		}
	}
	info, err := os.Stat(filepath.Join(stateDir, preferenceDir))
	if err != nil || !info.IsDir() {
		t.Fatalf("preferences: info = %v, error = %v", info, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("preferences mode = %v, want 0700", info.Mode().Perm())
	}
	config := filepath.Join(storageDir, "config")
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(config)
	if err != nil {
		t.Fatal(err)
	}
	readOpts := append(readOptions(t, storageDir, testPassword, nil, fastRetry()),
		WithStateDir(stateDir))
	tests := []struct {
		name string
		opts []SessionOption
	}{
		{name: "without WithInit", opts: readOpts},
		{name: "with WithInit", opts: opts},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			openTest(t, tt.opts).Close()
			after, err := os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}
			afterInfo, err := os.Stat(config)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) ||
				!beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
				t.Error("Open() changed the config")
			}
		})
	}
}

func TestOpen_notInitialized(t *testing.T) {
	keepGlobals(t)
	duplicacy.RunInBackground = false
	storageDir := filepath.Join(t.TempDir(), "storage")
	var buf bytes.Buffer
	logger := newLogger(&buf, slog.LevelDebug)
	s, err := Open(t.Context(),
		readOptions(t, storageDir, testPassword, logger, fastRetry())...)
	if !errors.Is(err, ErrNotInitialized) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open() error = %v, want %v", err, ErrNotInitialized)
	}
	if got := attempts(t, &buf, operationInit+" "+defaultStorageName); len(got) != 0 {
		t.Errorf("attempts = %v, want none", got)
	}
	entries, err := os.ReadDir(storageDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) > 0 {
		t.Errorf("storage entries = %v, want none", entries)
	}
	if duplicacy.RunInBackground || duplicacy.LogFunction != nil {
		t.Error("Open() did not give the global state of duplicacy back")
	}
}

func TestOpen_twice(t *testing.T) {
	keepGlobals(t)
	opts := testOptions(t, filepath.Join(t.TempDir(), "storage"), testPassword, nil,
		fastRetry())
	s := openTest(t, opts)
	if second, err := Open(t.Context(), opts...); !errors.Is(err, errSessionOpen) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("second Open() error = %v, want %v", err, errSessionOpen)
	}
	s.Close()
	openTest(t, opts)
}

func TestSession_Close(t *testing.T) {
	keepGlobals(t)
	duplicacy.RunInBackground = false
	duplicacy.LogFunction = nil
	duplicacy.ProgressFunction = nil
	opts := testOptions(t, filepath.Join(t.TempDir(), "storage"), testPassword, nil,
		fastRetry())
	t.Cleanup(duplicacy.ResetCancel)
	s := openTest(t, opts)
	if !duplicacy.RunInBackground || duplicacy.LogFunction == nil {
		t.Fatal("Open() did not set the global state of duplicacy")
	}
	duplicacy.ProgressFunction = func(duplicacy.ProgressEvent) {}
	duplicacy.RequestCancel()
	s.Close()
	s.Close()
	if duplicacy.RunInBackground || duplicacy.LogFunction != nil ||
		duplicacy.ProgressFunction != nil {
		t.Error("Close() did not give the global state of duplicacy back")
	}
	if duplicacy.IsCancelRequested() {
		t.Error("Close() did not reset the request of the cancellation")
	}
	openTest(t, opts)
}

func TestOpen_required(t *testing.T) {
	keepGlobals(t)
	duplicacy.RunInBackground = false
	tests := []struct {
		name string
		opt  SessionOption
	}{
		{name: "copy directory", opt: WithCopyDir("")},
		{name: "state directory", opt: WithStateDir("")},
		{name: "storage name", opt: WithStorageName("")},
		{name: "storage URL", opt: WithURL("")},
		{name: "snapshot ID", opt: WithSnapshotID("")},
		{name: "storage password", opt: WithPassword("")},
		{name: "threads", opt: WithThreads(0)},
		{name: "progress writer", opt: WithProgress(nil, "bar")},
		{name: "progress element", opt: WithProgress(&bytes.Buffer{}, "speed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storageDir := filepath.Join(t.TempDir(), "storage")
			opts := append(testOptions(t, storageDir, testPassword, nil, fastRetry()),
				tt.opt)
			s, err := Open(t.Context(), opts...)
			if err == nil {
				s.Close()
				t.Fatal("Open() error = nil")
			}
			if !strings.Contains(err.Error(), tt.name) {
				t.Errorf("Open() error = %v, want one about %s", err, tt.name)
			}
			if duplicacy.RunInBackground {
				t.Error("Open() changed the global state of duplicacy")
			}
			if _, err := os.Stat(storageDir); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("storage: error = %v, want %v", err, fs.ErrNotExist)
			}
		})
	}
}

func TestOpen_wrongPassword(t *testing.T) {
	keepGlobals(t)
	duplicacy.RunInBackground = false
	storageDir := filepath.Join(t.TempDir(), "storage")
	openTest(t, testOptions(t, storageDir, testPassword, nil, fastRetry())).Close()
	var buf bytes.Buffer
	logger := newLogger(&buf, slog.LevelDebug)
	s, err := Open(t.Context(),
		testOptions(t, storageDir, testOtherPassword, logger, fastRetry())...)
	if !errors.Is(err, errWrongPassword) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open() error = %v, want %v", err, errWrongPassword)
	}
	if got := attempts(t, &buf, operationInit+" "+defaultStorageName); len(got) != 0 {
		t.Errorf("attempts = %v, want none", got)
	}
	if duplicacy.RunInBackground || duplicacy.LogFunction != nil {
		t.Error("Open() did not give the global state of duplicacy back")
	}
	openTest(t, testOptions(t, storageDir, testPassword, nil, fastRetry()))
}

func TestOpen_unencrypted(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "storage")
	initPlain(t, storageDir)
	var buf bytes.Buffer
	logger := newLogger(&buf, slog.LevelDebug)
	s, err := Open(t.Context(),
		testOptions(t, storageDir, testPassword, logger, fastRetry())...)
	if !errors.Is(err, errUnencrypted) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open() error = %v, want %v", err, errUnencrypted)
	}
	if got := attempts(t, &buf, operationInit+" "+defaultStorageName); len(got) != 0 {
		t.Errorf("attempts = %v, want none", got)
	}
}

func TestOpen_environment(t *testing.T) {
	const value = "fake-environment-password"
	tests := []struct {
		name     string
		caseless bool
		set      []string
		removed  []string
		kept     []string
	}{
		{
			name:     "case sensitive",
			caseless: false,
			set: []string{
				"duplicacy_b2_id", "Duplicacy_S3_Id", "DUPLICACY_GCD_ID",
				"DUPLICACY_PASSWORD",
			},
			removed: []string{"DUPLICACY_GCD_ID", "DUPLICACY_PASSWORD"},
			kept:    []string{"duplicacy_b2_id", "Duplicacy_S3_Id"},
		},
		{
			name:     "case insensitive",
			caseless: true,
			set: []string{
				"DUPLICAC", "DUPLICACYX_A", "DUPLICACY", "DUPLICACY_B2_ID",
				"Duplicacy_S3_Id", "duplicacy_gcd_id", "DUPLICACY_PASSWORD",
			},
			removed: []string{
				"DUPLICACY_B2_ID", "Duplicacy_S3_Id", "duplicacy_gcd_id",
				"DUPLICACY_PASSWORD",
			},
			kept: []string{"DUPLICAC", "DUPLICACYX_A", "DUPLICACY"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keepGlobals(t)
			replaceEnvCaseless(t, tt.caseless)
			for _, name := range tt.set {
				t.Setenv(name, value)
			}
			var buf bytes.Buffer
			logger := newLogger(&buf, slog.LevelDebug)
			openTest(t, testOptions(t, filepath.Join(t.TempDir(), "storage"),
				testPassword, logger, fastRetry()))
			var got []any
			for _, r := range records(t, &buf) {
				if variables, ok := r["variables"].([]any); ok &&
					r["msg"] == msgEnvRemoved && r["level"] == "DEBUG" {
					got = append(got, variables...)
				}
			}
			for _, name := range tt.removed {
				if _, ok := os.LookupEnv(name); ok {
					t.Errorf("Open() kept %s", name)
				}
				if !slices.Contains(got, any(name)) {
					t.Errorf("records = %s, want one about the removed %s",
						buf.String(), name)
				}
			}
			for _, name := range tt.kept {
				if _, ok := os.LookupEnv(name); !ok {
					t.Errorf("Open() removed %s", name)
				}
				if slices.Contains(got, any(name)) {
					t.Errorf("records = %s, want none about the kept %s",
						buf.String(), name)
				}
			}
			if strings.Contains(buf.String(), value) {
				t.Errorf("records = %s, want no value %q", buf.String(), value)
			}
		})
	}
}

func TestOpen_transient(t *testing.T) {
	keepGlobals(t)
	var buf bytes.Buffer
	logger := newLogger(&buf, slog.LevelDebug)
	start := time.Now()
	s, err := Open(t.Context(), testOptions(t, closedURL(t, ""), testPassword, logger,
		fastRetry())...)
	elapsed := time.Since(start)
	if err == nil {
		s.Close()
		t.Fatal("Open() error = nil")
	}
	if _, ok := errors.AsType[*net.OpError](err); !ok {
		t.Errorf("Open() error = %v, want a *net.OpError", err)
	}
	if strings.Contains(err.Error(), "sftp://") {
		t.Errorf("Open() error = %v, want no URL", err)
	}
	got := attempts(t, &buf, operationInit+" "+defaultStorageName)
	if len(got) < 2 {
		t.Fatalf("attempts = %v, want at least 2", got)
	}
	for i, number := range got {
		if number != float64(i+1) {
			t.Fatalf("attempts = %v, want 1, 2, …", got)
		}
	}
	if minimum := time.Duration(len(got)-1) * 5 * time.Millisecond; elapsed < minimum {
		t.Errorf("Open() took %v, want at least %v", elapsed, minimum)
	}
	if elapsed > time.Second {
		t.Errorf("Open() took %v, want at most 1s", elapsed)
	}
}

func TestOpen_permanent(t *testing.T) {
	keepGlobals(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	logger := newLogger(&buf, slog.LevelDebug)
	s, err := Open(t.Context(), testOptions(t, filepath.Join(file, "sub"), testPassword,
		logger, fastRetry())...)
	if err == nil {
		s.Close()
		t.Fatal("Open() error = nil")
	}
	if e, ok := errors.AsType[*Error](err); !ok || e.LogID != "STORAGE_CREATE" {
		t.Errorf("Open() error = %v, want an *Error of STORAGE_CREATE", err)
	}
	if _, ok := errors.AsType[*fs.PathError](err); !ok {
		t.Errorf("Open() error = %v, want a *fs.PathError", err)
	}
	if got := attempts(t, &buf, operationInit+" "+defaultStorageName); len(got) != 0 {
		t.Errorf("attempts = %v, want none", got)
	}
}

func TestOpen_canceled(t *testing.T) {
	keepGlobals(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var buf bytes.Buffer
	w := &cancelWriter{buf: &buf, cancel: cancel}
	options := &slog.HandlerOptions{Level: slog.LevelDebug}
	logger := slog.New(slog.NewJSONHandler(w, options))
	// The wait after the first attempt is close to the whole budget, so a wait that the
	// cancellation does not stop takes about 190 ms.
	slow := retry.Options{
		MaxRetriesTime:      200 * time.Millisecond,
		BackoffInitial:      190 * time.Millisecond,
		BackoffStep:         190 * time.Millisecond,
		BackoffStepAttempts: 3,
		BackoffCap:          190 * time.Millisecond,
	}
	s, err := Open(ctx, testOptions(t, closedURL(t, ""), testPassword, logger, slow)...)
	returned := time.Now()
	if !errors.Is(err, context.Canceled) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open() error = %v, want %v", err, context.Canceled)
	}
	canceled := w.canceledAt()
	if canceled.IsZero() {
		t.Fatal("no attempt failed before the cancellation")
	}
	if wait := returned.Sub(canceled); wait >= 100*time.Millisecond {
		t.Errorf("Open() returned %v after the cancellation, want less than 100ms", wait)
	}
	if got := attempts(t, &buf, operationInit+" "+defaultStorageName); len(got) != 1 {
		t.Errorf("attempts = %v, want 1", got)
	}
}

// replaceCreateStorage replaces createStorage for the test.
func replaceCreateStorage(
	t *testing.T, create func(duplicacy.Preference, bool, int) duplicacy.Storage,
) {
	t.Helper()
	saved := createStorage
	createStorage = create
	t.Cleanup(func() { createStorage = saved })
}

// replaceEnvCaseless replaces envCaseless for the test.
func replaceEnvCaseless(t *testing.T, caseless bool) {
	t.Helper()
	saved := envCaseless
	envCaseless = caseless
	t.Cleanup(func() { envCaseless = saved })
}

func TestOpen_panic(t *testing.T) {
	keepGlobals(t)
	duplicacy.RunInBackground = false
	duplicacy.LogFunction = nil
	replaceCreateStorage(t, func(duplicacy.Preference, bool, int) duplicacy.Storage {
		panic("fake-panic")
	})
	func() {
		defer func() {
			if r := recover(); r != "fake-panic" {
				t.Errorf("panic = %v, want fake-panic", r)
			}
		}()
		s, err := Open(t.Context(), testOptions(t, filepath.Join(t.TempDir(), "storage"),
			testPassword, nil, fastRetry())...)
		if s != nil {
			s.Close()
		}
		t.Errorf("Open() error = %v, want a panic", err)
	}()
	if duplicacy.RunInBackground || duplicacy.LogFunction != nil {
		t.Error("Open() did not give the global state of duplicacy back")
	}
	createStorage = duplicacy.CreateStorage
	openTest(t, testOptions(t, filepath.Join(t.TempDir(), "storage"), testPassword, nil,
		fastRetry()))
}

func TestOpen_subdirectories(t *testing.T) {
	keepGlobals(t)
	failed := false
	replaceCreateStorage(t, func(
		preference duplicacy.Preference, resetPassword bool, threads int,
	) duplicacy.Storage {
		storage := duplicacy.CreateStorage(preference, resetPassword, threads)
		return &flakyStorage{Storage: storage, failed: &failed}
	})
	storageDir := filepath.Join(t.TempDir(), "storage")
	var buf bytes.Buffer
	logger := newLogger(&buf, slog.LevelDebug)
	openTest(t, testOptions(t, storageDir, testPassword, logger, fastRetry()))
	if !failed {
		t.Fatal("CreateDirectory of chunks did not fail")
	}
	for _, name := range subdirectories {
		info, err := os.Stat(filepath.Join(storageDir, name))
		if err != nil || !info.IsDir() {
			t.Errorf("storage %s: info = %v, error = %v", name, info, err)
		}
	}
	if got := attempts(t, &buf, operationInit+" "+defaultStorageName); len(got) != 1 {
		t.Errorf("attempts = %v, want 1", got)
	}
}

// spyState scripts and records the calls of the storages that spyStorage wraps; the
// storages of all attempts share it. The numbers of the calls start at 1.
type spyState struct {
	dirCalls    []string
	configCalls int
	uploads     int
	dirFails    map[int]bool
	configMiss  map[int]bool
	uploadFails map[int]bool
}

// spyStorage records CreateDirectory, answers the check of the config with a missing
// file and fails UploadFile of the config after it has stored the file, as the
// numbers of spyState tell.
type spyStorage struct {
	duplicacy.Storage
	state *spyState
}

func (s *spyStorage) CreateDirectory(threadIndex int, dir string) error {
	s.state.dirCalls = append(s.state.dirCalls, dir)
	if s.state.dirFails[len(s.state.dirCalls)] {
		return &net.OpError{Op: "write", Net: "tcp", Err: syscall.ECONNRESET}
	}
	return s.Storage.CreateDirectory(threadIndex, dir)
}

func (s *spyStorage) GetFileInfo(
	threadIndex int, filePath string,
) (exist, isDir bool, size int64, err error) {
	if filePath == "config" {
		s.state.configCalls++
		if s.state.configMiss[s.state.configCalls] {
			return false, false, 0, nil
		}
	}
	return s.Storage.GetFileInfo(threadIndex, filePath)
}

func (s *spyStorage) UploadFile(threadIndex int, filePath string, content []byte) error {
	err := s.Storage.UploadFile(threadIndex, filePath, content)
	if err == nil && filePath == "config" {
		s.state.uploads++
		if s.state.uploadFails[s.state.uploads] {
			return &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
		}
	}
	return err
}

// initWithoutSubdirectories creates the config of the storage in dir with the password,
// as another client does before it creates the subdirectories.
func initWithoutSubdirectories(t *testing.T, dir string) {
	t.Helper()
	keepGlobals(t)
	duplicacy.RunInBackground = true
	duplicacy.LogFunction = LogFunction(nil)
	preference := duplicacy.Preference{Name: defaultStorageName, StorageURL: dir}
	storage := duplicacy.CreateStorage(preference, false, 1)
	if !duplicacy.ConfigStorage(storage, duplicacy.CONFIG_DEFAULT_ITERATIONS,
		duplicacy.DEFAULT_COMPRESSION_LEVEL, averageChunkSize, maximumChunkSize,
		minimumChunkSize, testPassword, nil, false, "", 0, 0) {
		t.Fatal("ConfigStorage() = false")
	}
	for _, name := range subdirectories {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpen_subdirectoriesAfterInit(t *testing.T) {
	tests := []struct {
		name        string
		preexisting bool
		state       spyState
		wantAttempt int
		wantDirs    []string
	}{
		{
			name:        "no failure",
			wantAttempt: 0,
			wantDirs:    []string{"chunks", "snapshots"},
		},
		{
			name: "mkdir failure and a false missing config",
			state: spyState{
				dirFails: map[int]bool{1: true}, configMiss: map[int]bool{3: true},
			},
			wantAttempt: 1,
			wantDirs:    []string{"chunks", "chunks", "snapshots"},
		},
		{
			name:        "mkdir failure and the config found at once",
			state:       spyState{dirFails: map[int]bool{1: true}},
			wantAttempt: 1,
			wantDirs:    []string{"chunks", "chunks", "snapshots"},
		},
		{
			name:        "lost response of the upload of the config",
			state:       spyState{uploadFails: map[int]bool{1: true}},
			wantAttempt: 1,
			wantDirs:    []string{"chunks", "snapshots"},
		},
		{
			name:        "mkdir failures of the attempts",
			state:       spyState{dirFails: map[int]bool{1: true, 2: true}},
			wantAttempt: 2,
			wantDirs:    []string{"chunks", "chunks", "chunks", "snapshots"},
		},
		{
			name:        "config created by another client",
			preexisting: true,
			state:       spyState{configMiss: map[int]bool{1: true}},
			wantAttempt: 0,
			wantDirs:    []string{"chunks", "snapshots"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keepGlobals(t)
			storageDir := filepath.Join(t.TempDir(), "storage")
			if tt.preexisting {
				initWithoutSubdirectories(t, storageDir)
			}
			state := tt.state
			replaceCreateStorage(t, func(
				preference duplicacy.Preference, resetPassword bool, threads int,
			) duplicacy.Storage {
				storage := duplicacy.CreateStorage(preference, resetPassword, threads)
				return &spyStorage{Storage: storage, state: &state}
			})
			var buf bytes.Buffer
			logger := newLogger(&buf, slog.LevelDebug)
			openTest(t, testOptions(t, storageDir, testPassword, logger, fastRetry()))
			for _, name := range subdirectories {
				info, err := os.Stat(filepath.Join(storageDir, name))
				if err != nil || !info.IsDir() {
					t.Errorf("storage %s: info = %v, error = %v", name, info, err)
				}
			}
			if !slices.Equal(state.dirCalls, tt.wantDirs) {
				t.Errorf("CreateDirectory calls = %v, want %v", state.dirCalls,
					tt.wantDirs)
			}
			got := attempts(t, &buf, operationInit+" "+defaultStorageName)
			if len(got) != tt.wantAttempt {
				t.Errorf("failed attempts = %v, want %d", got, tt.wantAttempt)
			}
		})
	}
}

func TestOpen_masks(t *testing.T) {
	keepGlobals(t)
	const (
		urlPasswordWritten = "fake%21url%21password"
		urlPasswordDecoded = "fake!url!password"
		keyValue           = "fake-key-value"
	)
	secrets := []string{urlPasswordWritten, urlPasswordDecoded, keyValue, testPassword}
	text := strings.Join(secrets, " ")
	replaceCreateStorage(t, func(duplicacy.Preference, bool, int) duplicacy.Storage {
		duplicacy.LOG_WARN("TEST_LOG", "connect with %s", text)
		panic(duplicacy.Exception{
			Level: duplicacy.ERROR, LogID: "TEST_ID", Message: "failed with " + text,
			Err: fmt.Errorf("dial %s: %w", text, syscall.ECONNRESET),
		})
	})
	var buf bytes.Buffer
	logger := newLogger(&buf, slog.LevelDebug)
	storageURL := "sftp://user:" + urlPasswordWritten + "@127.0.0.1:22/x"
	opts := append(testOptions(t, storageURL, testPassword, logger, noRetry()),
		WithKeys(map[string]string{"ssh_password": keyValue}))
	s, err := Open(t.Context(), opts...)
	if err == nil {
		s.Close()
		t.Fatal("Open() error = nil")
	}
	e, ok := errors.AsType[*Error](err)
	if !ok {
		t.Fatalf("Open() error = %v, want *Error", err)
	}
	texts := map[string]string{
		"error": err.Error(), "message": e.Message, "records": buf.String(),
	}
	for name, got := range texts {
		for _, secret := range secrets {
			if strings.Contains(got, secret) {
				t.Errorf("%s = %s, want no %q", name, got, secret)
			}
		}
		if name != "error" && !strings.Contains(got, masked) {
			t.Errorf("%s = %s, want a masked one", name, got)
		}
	}
	if len(attempts(t, &buf, operationInit+" "+defaultStorageName)) != 1 {
		t.Errorf("records = %s, want one failed attempt", buf.String())
	}
}

func TestOpen_checkRedirect(t *testing.T) {
	saved := http.DefaultClient.CheckRedirect
	t.Cleanup(func() { http.DefaultClient.CheckRedirect = saved })
	tests := []struct {
		name string
		init bool
	}{
		{name: "failed Open", init: false},
		{name: "closed session", init: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keepGlobals(t)
			marked := 0
			http.DefaultClient.CheckRedirect = func(
				*http.Request, []*http.Request,
			) error {
				marked++
				return nil
			}
			replaceCreateStorage(t, func(
				preference duplicacy.Preference, resetPassword bool, threads int,
			) duplicacy.Storage {
				http.DefaultClient.CheckRedirect = func(
					*http.Request, []*http.Request,
				) error {
					return http.ErrUseLastResponse
				}
				return duplicacy.CreateStorage(preference, resetPassword, threads)
			})
			storageDir := filepath.Join(t.TempDir(), "storage")
			var s *Session
			if tt.init {
				s = openTest(t, testOptions(t, storageDir, testPassword, nil, noRetry()))
				err := http.DefaultClient.CheckRedirect(nil, nil)
				if !errors.Is(err, http.ErrUseLastResponse) || marked != 0 {
					t.Errorf("during the session: error = %v, marked = %d, want the "+
						"policy of the backend", err, marked)
				}
				s.Close()
			} else {
				s, err := Open(t.Context(),
					readOptions(t, storageDir, testPassword, nil, noRetry())...)
				if !errors.Is(err, ErrNotInitialized) {
					if s != nil {
						s.Close()
					}
					t.Fatalf("Open() error = %v, want %v", err, ErrNotInitialized)
				}
			}
			if err := http.DefaultClient.CheckRedirect(nil, nil); err != nil ||
				marked != 1 {
				t.Errorf("after the session: error = %v, marked = %d, want nil and 1",
					err, marked)
			}
		})
	}
}

func TestSession_call(t *testing.T) {
	s := &Session{masks: []string{"fake-a"}}
	cause := errors.New("fake cause")
	exception := duplicacy.Exception{
		Level: duplicacy.ERROR, LogID: "TEST_ID", Message: "at fake-a", Err: cause,
	}
	type args struct {
		op string
		f  func()
	}
	tests := []struct {
		name string
		args args
		want *Error
	}{
		{name: "no panic", args: args{op: opCreateStorage, f: func() {}}, want: nil},
		{
			name: "exception",
			args: args{op: opCreateStorage, f: func() { panic(exception) }},
			want: &Error{
				Op: opCreateStorage, LogID: "TEST_ID", Message: "at ***", Err: cause,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.call(tt.args.op, tt.args.f)
			if tt.want == nil {
				if err != nil {
					t.Errorf("call() error = %v, want nil", err)
				}
				return
			}
			if e, ok := errors.AsType[*Error](err); !ok || *e != *tt.want {
				t.Errorf("call() error = %#v, want %#v", err, tt.want)
			}
			if !errors.Is(err, cause) {
				t.Errorf("call() error = %v, want one wrapping %v", err, cause)
			}
		})
	}
}

func TestSession_call_panic(t *testing.T) {
	s := &Session{}
	defer func() {
		if r := recover(); r != "fake-panic" {
			t.Errorf("panic = %v, want fake-panic", r)
		}
	}()
	_ = s.call(opCreateStorage, func() { panic("fake-panic") })
	t.Error("call() returned, want a panic")
}

func Test_urlPasswords(t *testing.T) {
	type args struct {
		storageURL string
	}
	tests := []struct {
		name string
		args args
		want []string
	}{
		{name: "no userinfo", args: args{storageURL: "sftp://host/path"}, want: nil},
		{name: "no password", args: args{storageURL: "sftp://user@host/path"}, want: nil},
		{name: "local path", args: args{storageURL: "/srv/storage"}, want: nil},
		{
			name: "password",
			args: args{storageURL: "sftp://user:fake-a@host/path"},
			want: []string{"fake-a"},
		},
		{
			name: "encoded password",
			args: args{storageURL: "sftp://user:fake%40a@host/path"},
			want: []string{"fake%40a", "fake@a"},
		},
		{
			name: "@ in password",
			args: args{storageURL: "sftp://user:fake@a@host/path"},
			want: []string{"fake", "fake@a"},
		},
		{
			name: "@ in query without path",
			args: args{storageURL: "sftp://user:fake-a@host:1?x=@y"},
			want: []string{"fake-a", "fake-a@host:1?x="},
		},
		{
			name: "@ in path",
			args: args{storageURL: "sftp://user:fake-a@host/a@b"},
			want: []string{"fake-a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := urlPasswords(tt.args.storageURL)
			if !slices.Equal(got, tt.want) {
				t.Errorf("urlPasswords() = %q, want %q", got, tt.want)
			}
		})
	}
}
