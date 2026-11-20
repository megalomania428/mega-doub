// cspell:ignore duplicacy webdav

package dup

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"maps"
	"math"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	duplicacy "duplicacy.invalid/v3/src"
	"golang.org/x/net/webdav"
)

// testFiles are the files of the copy directory of the tests by their names.
var testFiles = map[string]string{
	"a.txt": strings.Repeat("fake text a\n", 300),
	"b.txt": strings.Repeat("fake text bb\n", 700),
	"c.bin": strings.Repeat("\x00\x01\x02\x03", 2000),
}

// writeCopy creates the copy directory dir with files.
func writeCopy(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
		if err != nil {
			t.Fatal(err)
		}
	}
}

// openBackupTest opens a session of the test on a new storage that WithInit initializes,
// with the options extra after the ones of the tests; duplicacy.RunAtError is given back
// after the test.
func openBackupTest(t *testing.T, extra ...SessionOption) *Session {
	t.Helper()
	keepGlobals(t)
	runAtError := duplicacy.RunAtError
	t.Cleanup(func() { duplicacy.RunAtError = runAtError })
	opts := testOptions(t, filepath.Join(t.TempDir(), "storage"), testPassword, nil,
		fastRetry())
	return openTest(t, append(opts, extra...))
}

// downloadSnapshot returns the revision of the snapshot ID of the tests.
func downloadSnapshot(t *testing.T, s *Session, revision int) *duplicacy.Snapshot {
	t.Helper()
	var snapshot *duplicacy.Snapshot
	err := s.call("download snapshot", func() {
		manager := duplicacy.CreateBackupManager(testSnapshotID, s.storage, s.copyDir,
			testPassword, "", "", false)
		manager.SetupSnapshotCache(s.preference.Name)
		snapshot = manager.SnapshotManager.DownloadSnapshot(testSnapshotID, revision)
	})
	if err != nil {
		t.Fatalf("DownloadSnapshot() error = %v", err)
	}
	return snapshot
}

func TestSession_Backup(t *testing.T) {
	type args struct {
		ctx  context.Context
		opts []BackupOption
	}
	tests := []struct {
		name string
		// files are the files of the copy directory; nil – the directory is missing.
		files     map[string]string
		args      args
		wantLogID string
		wantEmpty bool
	}{
		{
			name:      "empty copy directory",
			files:     map[string]string{},
			args:      args{ctx: t.Context()},
			wantLogID: logIDSnapshotEmpty,
			wantEmpty: true,
		},
		{
			name:      "missing copy directory",
			args:      args{ctx: t.Context()},
			wantLogID: "LIST_FAILURE",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openBackupTest(t)
			if tt.files != nil {
				writeCopy(t, s.copyDir, tt.files)
			}
			// A RunAtError left by an earlier call of duplicacy must not be called.
			stale := false
			duplicacy.RunAtError = func() { stale = true }
			got, err := s.Backup(tt.args.ctx, tt.args.opts...)
			if stale {
				t.Error("Backup() called the RunAtError of an earlier call")
			}
			if got != nil {
				t.Errorf("Backup() = %+v, want nil", got)
			}
			if e, ok := errors.AsType[*Error](err); !ok || e.LogID != tt.wantLogID {
				t.Fatalf("Backup() error = %v, want the log ID %s", err, tt.wantLogID)
			}
			if errors.Is(err, ErrEmptyCopyDir) != tt.wantEmpty {
				t.Errorf("Backup() error = %v, ErrEmptyCopyDir = %v", err, tt.wantEmpty)
			}
			want := "backup: copy directory has no files"
			if tt.wantEmpty && err.Error() != want {
				t.Errorf("Backup() error = %q, want %q", err.Error(), want)
			}
			writeCopy(t, s.copyDir, testFiles)
			got, err = s.Backup(tt.args.ctx, tt.args.opts...)
			if err != nil || got.Revision != 1 {
				t.Errorf("Backup() after the failure = %+v, error = %v", got, err)
			}
		})
	}
}

// lagFS is the file system of a WebDAV server whose listing of the revisions of the
// snapshot ID lacks the newest revision for lag listings after its upload, as the listing
// of a storage without strong consistency may do.
type lagFS struct {
	webdav.FileSystem
	mu       sync.Mutex
	lag      int
	revision string
}

// revisionsDir is the directory of the revisions of the snapshot ID of the tests.
const revisionsDir = "/snapshots/" + testSnapshotID

func (l *lagFS) OpenFile(
	ctx context.Context, name string, flag int, perm os.FileMode,
) (webdav.File, error) {
	file, err := l.FileSystem.OpenFile(ctx, name, flag, perm)
	if err != nil {
		return nil, err
	}
	if revision, ok := strings.CutPrefix(name, revisionsDir+"/"); ok &&
		flag&os.O_CREATE != 0 {
		l.mu.Lock()
		l.revision = revision
		l.mu.Unlock()
	}
	if strings.TrimSuffix(name, "/") == revisionsDir {
		return &lagDir{File: file, fs: l}, nil
	}
	return file, nil
}

// arm makes the next lag listings of the revisions after the next upload lack the
// uploaded revision.
func (l *lagFS) arm(lag int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lag = lag
	l.revision = ""
}

// lagDir is the directory of the revisions that lagFS lists.
type lagDir struct {
	webdav.File
	fs *lagFS
}

func (d *lagDir) Readdir(count int) ([]fs.FileInfo, error) {
	infos, err := d.File.Readdir(count)
	d.fs.mu.Lock()
	defer d.fs.mu.Unlock()
	if err != nil || d.fs.lag <= 0 || d.fs.revision == "" {
		return infos, err
	}
	d.fs.lag--
	var kept []fs.FileInfo
	for _, info := range infos {
		if info.Name() != d.fs.revision {
			kept = append(kept, info)
		}
	}
	return kept, nil
}

// lagLimit is a lag that is never used up within the retries of a test.
const lagLimit = math.MaxInt

// openWebDAVTest opens a session of the test on a new WebDAV storage whose listing of the
// revisions lags as lagFS does; the arming of the lag is up to the caller.
func openWebDAVTest(t *testing.T) (*Session, *lagFS) {
	t.Helper()
	keepGlobals(t)
	runAtError := duplicacy.RunAtError
	t.Cleanup(func() { duplicacy.RunAtError = runAtError })
	files := &lagFS{FileSystem: webdav.Dir(t.TempDir())}
	server := httptest.NewServer(&webdav.Handler{
		Prefix: "/dav", FileSystem: files, LockSystem: webdav.NewMemLS(),
	})
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")
	opts := testOptions(t, "webdav-http://fake-user@"+address+"/dav", testPassword, nil,
		fastRetry())
	opts = append(opts, WithKeys(map[string]string{"webdav_password": "fake-pass"}))
	return openTest(t, opts), files
}

func TestSession_Backup_revisions(t *testing.T) {
	type args struct {
		ctx  context.Context
		opts []BackupOption
	}
	tests := []struct {
		name string
		// revisions is the number of the backups made one after another.
		revisions int
		// threads are the threads of the storage of the session; 0 – the one of the tests.
		threads int
		// lag is the number of the listings of the revisions after the upload of a
		// revision that lack it; positive – the storage is a WebDAV one, which duplicacy
		// tells apart by its type, so the storage of the session is not wrapped.
		lag     int
		args    args
		wantTag string
		wantErr bool
	}{
		{
			name:      "two tagged revisions",
			revisions: 2,
			args:      args{ctx: t.Context(), opts: []BackupOption{WithTag("fake-tag")}},
			wantTag:   "fake-tag",
		},
		{
			name:      "two threads of the storage and the backup",
			revisions: 1,
			threads:   2,
			args: args{
				ctx: t.Context(), opts: []BackupOption{WithBackupThreads(2)},
			},
		},
		{
			name: "webdav listing lags once", revisions: 2, lag: 1,
			args: args{ctx: t.Context()},
		},
		{
			name: "webdav listing lags twice", revisions: 3, lag: 2,
			args: args{ctx: t.Context()},
		},
		{
			name: "webdav listing lags longer than the retries", revisions: 2,
			lag: lagLimit, args: args{ctx: t.Context()}, wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s *Session
			var files *lagFS
			if tt.lag > 0 {
				s, files = openWebDAVTest(t)
			} else {
				storage := countStorage(t)
				var extra []SessionOption
				storageThreads := 1
				if tt.threads > 0 {
					extra = append(extra, WithThreads(tt.threads))
					storageThreads = tt.threads
				}
				s = openBackupTest(t, extra...)
				defer func() {
					// A backend with a client for every thread has no client above them.
					if got := storage.maxThread.Load(); got >= int64(storageThreads) {
						t.Errorf("thread index = %d, want below the storage threads %d",
							got, storageThreads)
					}
				}()
			}
			writeCopy(t, s.copyDir, testFiles)
			var size int64
			for _, content := range testFiles {
				size += int64(len(content))
			}
			var first *BackupResult
			for revision := 1; revision <= tt.revisions; revision++ {
				if files != nil {
					files.arm(tt.lag)
				}
				got, err := s.Backup(tt.args.ctx, tt.args.opts...)
				if (err != nil) != tt.wantErr {
					t.Fatalf("Backup() %d error = %v, wantErr %v", revision, err, tt.wantErr)
				}
				if tt.wantErr {
					return
				}
				if got.Revision != revision {
					t.Errorf("Backup() %d revision = %d", revision, got.Revision)
				}
				snapshot := downloadSnapshot(t, s, got.Revision)
				if got.Files != snapshot.NumberOfFiles || got.Bytes != snapshot.FileSize {
					t.Errorf("Backup() %d = %+v, snapshot files = %d, bytes = %d",
						revision, got, snapshot.NumberOfFiles, snapshot.FileSize)
				}
				if got.Files != int64(len(testFiles)) || got.Bytes != size {
					t.Errorf("Backup() %d files = %d, bytes = %d, want %d, %d", revision,
						got.Files, got.Bytes, len(testFiles), size)
				}
				if snapshot.Tag != tt.wantTag {
					t.Errorf("Backup() %d tag = %q, want %s", revision, snapshot.Tag,
						tt.wantTag)
				}
				if got.Skipped != 0 || got.Duration <= 0 {
					t.Errorf("Backup() %d = %+v, want no skipped files and a duration",
						revision, got)
				}
				if first == nil {
					first = got
					if got.NewChunks <= 0 || got.NewBytes <= 0 || got.UploadedBytes <= 0 {
						t.Errorf("Backup() 1 = %+v, want new chunks", got)
					}
					continue
				}
				if got.NewChunks >= first.NewChunks ||
					got.UploadedBytes >= first.UploadedBytes {
					t.Errorf("Backup() 2 = %+v, want fewer new chunks and bytes than %+v",
						got, first)
				}
			}
		})
	}
}

func TestSession_Backup_runAtError(t *testing.T) {
	type args struct {
		ctx  context.Context
		opts []BackupOption
	}
	tests := []struct {
		name string
		// failChunk is DUPLICACY_FAIL_CHUNK; Open has removed DUPLICACY_* before.
		failChunk string
		args      args
		want      *BackupResult
		wantLogID string
		wantErr   bool
	}{
		{
			// The first revision is a backup in the hash mode, which sets RunAtError, and the
			// variable makes it fail at its first chunk.
			name:      "first revision fails at the first chunk",
			failChunk: "0",
			args:      args{ctx: t.Context()},
			wantLogID: "SNAPSHOT_FAIL",
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openBackupTest(t)
			writeCopy(t, s.copyDir, testFiles)
			t.Setenv("DUPLICACY_FAIL_CHUNK", tt.failChunk)
			got, err := s.Backup(tt.args.ctx, tt.args.opts...)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Backup() error = %v, wantErr %v", err, tt.wantErr)
			}
			e, ok := errors.AsType[*Error](err)
			if !ok || e.LogID != tt.wantLogID || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Backup() = %+v, error = %v, want the log ID %s", got, err,
					tt.wantLogID)
			}
			cleared := reflect.ValueOf(doNothing).Pointer()
			if reflect.ValueOf(duplicacy.RunAtError).Pointer() != cleared {
				t.Error("Backup() did not clear RunAtError")
			}
			incomplete := filepath.Join(s.stateDir, preferenceDir, "cache",
				s.preference.Name, "incomplete_snapshot")
			if _, err := os.Stat(incomplete); err != nil {
				t.Errorf("Backup() did not call RunAtError: %v", err)
			}
		})
	}
}

// readFailStorage fails a read of the revisions of the snapshot ID of the tests after the
// revision is uploaded: ListFiles of the revision directory, GetFileInfo or DownloadFile
// of the file of the revision, as op tells. The failure is a reset connection while fails
// is positive, which it decrements, and a permanent error when permanent is set. With
// failStale, ListFiles of the revision directory does not give the revision, as a storage
// with a lagging listing, while fails is positive. With failPhantom, that listing also
// holds the number after the revision, as the one of a revision that another process with
// the same snapshot ID has created; there is no file of it, as the listing alone is under
// test.
type readFailStorage struct {
	duplicacy.Storage
	op        string
	revision  int
	uploaded  bool
	fails     int
	permanent bool
	reads     int
}

const (
	failList     = "ListFiles"
	failInfo     = "GetFileInfo"
	failDownload = "DownloadFile"
	failStale    = "stale listing"
	failPhantom  = "phantom listing"
)

// path returns the path of the snapshot file of the revision under test.
func (r *readFailStorage) path() string {
	return "snapshots/" + testSnapshotID + "/" + strconv.Itoa(r.revision)
}

func (r *readFailStorage) fail(op string, match bool) error {
	if !r.uploaded || r.op != op || !match {
		return nil
	}
	r.reads++
	if r.permanent {
		return errors.New("fake permanent failure")
	}
	if r.fails > 0 {
		r.fails--
		return &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	}
	return nil
}

func (r *readFailStorage) UploadFile(
	threadIndex int, filePath string, content []byte,
) error {
	err := r.Storage.UploadFile(threadIndex, filePath, content)
	if err == nil && filePath == r.path() {
		r.uploaded = true
	}
	return err
}

func (r *readFailStorage) ListFiles(
	threadIndex int, dir string,
) ([]string, []int64, error) {
	match := dir == "snapshots/"+testSnapshotID+"/"
	if err := r.fail(failList, match); err != nil {
		return nil, nil, err
	}
	files, sizes, err := r.Storage.ListFiles(threadIndex, dir)
	stale := r.op == failStale || r.op == failPhantom
	if err != nil || !r.uploaded || !stale || !match || r.fails <= 0 {
		return files, sizes, err
	}
	r.fails--
	r.reads++
	var kept []string
	var keptSizes []int64
	for i, file := range files {
		if file != strconv.Itoa(r.revision) {
			kept = append(kept, file)
			keptSizes = append(keptSizes, sizes[i])
		}
	}
	if r.op == failPhantom {
		kept = append(kept, strconv.Itoa(r.revision+1))
		keptSizes = append(keptSizes, 1)
	}
	return kept, keptSizes, nil
}

func (r *readFailStorage) GetFileInfo(
	threadIndex int, filePath string,
) (bool, bool, int64, error) {
	match := filePath == r.path()
	if err := r.fail(failInfo, match); err != nil {
		return false, false, 0, err
	}
	return r.Storage.GetFileInfo(threadIndex, filePath)
}

func (r *readFailStorage) DownloadFile(
	threadIndex int, filePath string, chunk *duplicacy.Chunk,
) error {
	match := filePath == r.path()
	if err := r.fail(failDownload, match); err != nil {
		return err
	}
	return r.Storage.DownloadFile(threadIndex, filePath, chunk)
}

func TestSession_Backup_readFailure(t *testing.T) {
	type args struct {
		ctx  context.Context
		opts []BackupOption
	}
	tests := []struct {
		name string
		op   string
		// revision is the revision that the backup under test creates; 0 – the first.
		revision  int
		fails     int
		permanent bool
		args      args
		wantErr   bool
		// exhausted means every attempt fails with a transient error, so the retries end
		// by the time limit with an error.
		exhausted bool
		// wantAttempts are the attempt numbers of the records of failed reads.
		wantAttempts []float64
	}{
		{
			name: "transient once", op: failList, fails: 1,
			args: args{ctx: t.Context()}, wantAttempts: []float64{1},
		},
		{
			name: "transient twice", op: failList, fails: 2,
			args: args{ctx: t.Context()}, wantAttempts: []float64{1, 2},
		},
		{
			name: "permanent", op: failList, permanent: true,
			args: args{ctx: t.Context()}, wantErr: true,
		},
		{
			name: "info transient once", op: failInfo, fails: 1,
			args: args{ctx: t.Context()}, wantAttempts: []float64{1},
		},
		{
			name: "info permanent", op: failInfo, permanent: true,
			args: args{ctx: t.Context()}, wantErr: true,
		},
		{
			name: "download transient once", op: failDownload, fails: 1,
			args: args{ctx: t.Context()}, wantAttempts: []float64{1},
		},
		{
			name: "download permanent", op: failDownload, permanent: true,
			args: args{ctx: t.Context()}, wantErr: true,
		},
		{
			name: "stale listing once", op: failStale, revision: 2, fails: 1,
			args: args{ctx: t.Context()}, wantAttempts: []float64{1},
		},
		{
			name: "stale listing twice", op: failStale, revision: 2, fails: 2,
			args: args{ctx: t.Context()}, wantAttempts: []float64{1, 2},
		},
		{
			name: "stale listing of the first revision", op: failStale, fails: 1,
			args: args{ctx: t.Context()}, wantAttempts: []float64{1},
		},
		{
			name: "newer revision without the own once", op: failPhantom, revision: 2,
			fails: 1, args: args{ctx: t.Context()}, wantAttempts: []float64{1},
		},
		{
			name: "newer revision without the own twice", op: failPhantom, revision: 2,
			fails: 2, args: args{ctx: t.Context()}, wantAttempts: []float64{1, 2},
		},
		{
			name: "newer revision without the own of the first", op: failPhantom,
			fails: 1, args: args{ctx: t.Context()}, wantAttempts: []float64{1},
		},
		{
			name: "newer revision without the own exhausted", op: failPhantom,
			revision: 2, fails: math.MaxInt, args: args{ctx: t.Context()},
			wantErr: true, exhausted: true,
		},
		{
			name: "stale listing exhausted", op: failStale, revision: 2,
			fails: math.MaxInt, args: args{ctx: t.Context()}, wantErr: true,
			exhausted: true,
		},
		{
			name: "stale listing of the first revision exhausted", op: failStale,
			fails: math.MaxInt, args: args{ctx: t.Context()}, wantErr: true,
			exhausted: true,
		},
		{
			name: "list exhausted", op: failList, fails: math.MaxInt,
			args: args{ctx: t.Context()}, wantErr: true, exhausted: true,
		},
		{
			name: "info exhausted", op: failInfo, fails: math.MaxInt,
			args: args{ctx: t.Context()}, wantErr: true, exhausted: true,
		},
		{
			name: "download exhausted", op: failDownload, fails: math.MaxInt,
			args: args{ctx: t.Context()}, wantErr: true, exhausted: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keepGlobals(t)
			runAtError := duplicacy.RunAtError
			t.Cleanup(func() { duplicacy.RunAtError = runAtError })
			revision := max(tt.revision, 1)
			storage := &readFailStorage{
				op: tt.op, revision: revision, fails: tt.fails, permanent: tt.permanent,
			}
			replaceCreateStorage(t, func(
				p duplicacy.Preference, readOnly bool, threads int,
			) duplicacy.Storage {
				storage.Storage = duplicacy.CreateStorage(p, readOnly, threads)
				return storage
			})
			var buf bytes.Buffer
			logger := newLogger(&buf, slog.LevelDebug)
			s := openTest(t, testOptions(t, filepath.Join(t.TempDir(), "storage"),
				testPassword, logger, fastRetry()))
			writeCopy(t, s.copyDir, testFiles)
			for range revision - 1 {
				if _, err := s.Backup(t.Context()); err != nil {
					t.Fatalf("Backup() before error = %v", err)
				}
			}
			// The files of the revision under test differ from those of the earlier ones.
			copyFiles := maps.Clone(testFiles)
			if revision > 1 {
				copyFiles["d.txt"] = strings.Repeat("fake text ddd\n", 11)
				writeCopy(t, s.copyDir, copyFiles)
			}
			var size int64
			for _, content := range copyFiles {
				size += int64(len(content))
			}
			storage.reads = 0
			got, err := s.Backup(tt.args.ctx, tt.args.opts...)
			if tt.wantErr {
				if got != nil || err == nil {
					t.Fatalf("Backup() = %+v, error = %v, want an error", got, err)
				}
				if tt.exhausted && storage.reads < 2 {
					t.Errorf("reads = %d, want retries", storage.reads)
				}
				if !tt.exhausted && storage.reads != 1 {
					t.Errorf("reads = %d, want 1 without retries", storage.reads)
				}
			} else if err != nil || got.Revision != revision ||
				got.Files != int64(len(copyFiles)) || got.Bytes != size {
				t.Fatalf("Backup() = %+v, error = %v, want the revision %d with %d files "+
					"and %d bytes", got, err, revision, len(copyFiles), size)
			}
			numbers := attempts(t, &buf, opBackup)
			if tt.exhausted {
				want := make([]float64, storage.reads)
				for i := range want {
					want[i] = float64(i + 1)
				}
				if !reflect.DeepEqual(numbers, want) {
					t.Errorf("failed attempts = %v, want 1..%d, one for every failed "+
						"read", numbers, storage.reads)
				}
			} else if !reflect.DeepEqual(numbers, tt.wantAttempts) {
				t.Errorf("failed attempts = %v, want %v", numbers, tt.wantAttempts)
			}
			files, err := os.ReadDir(filepath.Join(s.preference.StorageURL, "snapshots",
				testSnapshotID))
			if err != nil || len(files) != revision {
				t.Errorf("revisions = %v, error = %v, want %d of them, none more", files,
					err, revision)
			}
		})
	}
}
