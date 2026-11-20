// cspell:ignore duplicacy

// Package dup is documented in log.go.
package dup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	duplicacy "duplicacy.invalid/v3/src"
)

// The sizes of the metadata chunks and the number of the entries in memory of the command
// backup of the duplicacy CLI without flags.
const (
	metadataChunkSize      = 1 << 20
	maximumInMemoryEntries = 1 << 20
)

// logIDSnapshotEmpty is the log ID of the Exception of a backup of a copy directory
// without files.
const logIDSnapshotEmpty = "SNAPSHOT_EMPTY"

// ErrEmptyCopyDir is the error of a backup of a copy directory without files.
var ErrEmptyCopyDir = errors.New("copy directory has no files")

var errNoRevision = errors.New("the backup reports no revision")

// errStaleListing is the failure of a read of the revisions that does not list the
// revision of the backup yet; transient takes it for a temporary one.
var errStaleListing = errors.New("the listing of the revisions lacks the new revision")

// BackupOption is an option of Session.Backup.
type BackupOption func(*backupOptions)

// backupOptions are the settings of Session.Backup.
type backupOptions struct {
	tag     string
	threads int
}

// WithTag sets the tag of the revision; it is empty without the option.
func WithTag(tag string) BackupOption {
	return func(o *backupOptions) { o.tag = tag }
}

// WithBackupThreads sets the number of the threads of the backup; it is the number of the
// threads of the storage of the session without the option, and a greater one is an
// error, because a storage with a client for every thread has no more of them.
func WithBackupThreads(threads int) BackupOption {
	return func(o *backupOptions) { o.threads = threads }
}

// BackupResult is the result of a backup.
type BackupResult struct {
	Revision      int           // revision created by the backup
	Files         int64         // number of the files in the revision
	Bytes         int64         // total size of the files in the revision
	NewChunks     int64         // number of the new chunks of the files and the metadata
	NewBytes      int64         // length of the new chunks
	UploadedBytes int64         // bytes written to the storage
	Skipped       int64         // files and directories skipped by the backup
	Duration      time.Duration // duration of the backup
}

// emptyCopyDirError is the error of a backup of a copy directory without files: it is
// ErrEmptyCopyDir and the *Error of SNAPSHOT_EMPTY for errors.Is and errors.As.
type emptyCopyDirError struct {
	err *Error
}

func (e *emptyCopyDirError) Error() string {
	return e.err.Op + ": " + ErrEmptyCopyDir.Error()
}

func (e *emptyCopyDirError) Unwrap() []error {
	return []error{ErrEmptyCopyDir, e.err}
}

// doNothing is the function of duplicacy.RunAtError before an operation and after a
// failed backup.
func doNothing() {}

// backupRead is the data of the read of the revision that a backup has created: the
// backup manager, the collector of the events of the backup and the result to fill.
type backupRead struct {
	manager *duplicacy.BackupManager
	c       *collector
	result  *BackupResult
}

// readRevision reads the snapshot of the revision that the backup has created and sets
// the revision, the files and the bytes of r.result. The number of the revision is the
// one that the backup reports at its end, on every storage, not the largest of the
// listing, which a storage with a lagging listing may still lack. A listing that does not
// hold the revision, whatever its largest number is, is a failed attempt with a transient
// error. A failed read with a transient error is retried as the attempts of Open are; a
// permanent error is returned at once, and so is the lack of a reported revision,
// errNoRevision; a canceled ctx gives its error.
func (s *Session) readRevision(ctx context.Context, r *backupRead) error {
	revision := r.c.endRevision()
	if revision == 0 {
		return errNoRevision
	}
	id := s.preference.SnapshotID
	return s.retryAttempts(ctx, attemptRun{operation: opBackup, run: func() error {
		var readErr error
		err := s.call(opBackup, func() {
			revisions, err := r.manager.SnapshotManager.ListSnapshotRevisions(id)
			if err != nil {
				readErr = err
				return
			}
			if !slices.Contains(revisions, revision) {
				readErr = errStaleListing
				return
			}
			snapshot := r.manager.SnapshotManager.DownloadSnapshot(id, revision)
			r.result.Revision = revision
			r.result.Files = snapshot.NumberOfFiles
			r.result.Bytes = snapshot.FileSize
		})
		if err == nil {
			err = readErr
		}
		return err
	}})
}

// Backup backs the copy directory up to the storage and creates a revision of the
// snapshot ID; the files of the revision are counted after it. An empty copy directory
// gives ErrEmptyCopyDir. The result describes the revision that this backup created, on
// every storage: its number is the one that the backup reports at its end, not the
// largest of the listing of the storage, which may lag behind or hold the newer revision
// of another process. The read of that revision after the upload is retried with the
// retries of the session when the storage fails with a transient error or its listing
// lacks the revision, and ends with an error without a result when the attempts run out;
// the backup itself is never repeated, so a retry does not create another revision. A
// backup whose snapshot cache tree shares a directory with the tree of the local file
// storage, through any link or mount, is an error before duplicacy is called, because the
// clean-up of the cache would change the archive. After an Exception, Backup calls
// duplicacy.RunAtError, as the duplicacy CLI does, and clears it. That function tries to
// save the incomplete snapshot in the cache if the backup of the first revision, which
// hashes all the files, has set it by then; the save is not guaranteed, and an Exception
// before it is set, such as a failed listing, or a failed save keeps nothing. Backup,
// Prune and Close of a session must not run at the same time.
func (s *Session) Backup(
	ctx context.Context, opts ...BackupOption,
) (*BackupResult, error) {
	o := backupOptions{threads: s.threads}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if o.threads < 1 {
		return nil, errors.New("backup threads must be at least 1")
	}
	if o.threads > s.threads {
		return nil, fmt.Errorf("backup threads must be at most the storage threads %d",
			s.threads)
	}
	start := time.Now()
	c := newCollector()
	result := &BackupResult{}
	err := s.operate(ctx, &work{op: opBackup, c: c,
		f: func(manager *duplicacy.BackupManager) error {
			manager.Backup(s.copyDir, true, o.threads, o.tag, false, false, 0, false,
				metadataChunkSize, maximumInMemoryEntries)
			return s.readRevision(ctx, &backupRead{manager: manager, c: c, result: result})
		}})
	if e, ok := errors.AsType[*Error](err); ok {
		duplicacy.RunAtError()
		duplicacy.RunAtError = doNothing
		if e.LogID == logIDSnapshotEmpty {
			return nil, &emptyCopyDirError{err: e}
		}
	}
	if err != nil {
		return nil, err
	}
	end := c.endEvent()
	result.NewChunks = end.TotalChunks
	result.NewBytes = end.TotalBytes
	result.UploadedBytes = end.UploadedBytes
	result.Skipped = end.Failed
	result.Duration = time.Since(start)
	return result, nil
}
