// cspell:ignore duplicacy

package dup

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	duplicacy "duplicacy.invalid/v3/src"
	ci "github.com/megalomania428/go-lib-ci"
)

// cancelWait is the longest wait of a test of the cancellation for the request to reach
// duplicacy and for the goroutines of an operation to end.
const cancelWait = 5 * time.Second

// replaceEventHook replaces eventHook for the test.
func replaceEventHook(t *testing.T, hook func(duplicacy.ProgressEvent)) {
	t.Helper()
	saved := eventHook
	eventHook = hook
	t.Cleanup(func() { eventHook = saved })
}

// stopAtStart returns the event hook that calls stop at the start of operation and then
// waits until the request of the cancellation reaches duplicacy, so that the next check
// of the operation stops it.
func stopAtStart(
	operation duplicacy.ProgressOperation, stop func(),
) func(duplicacy.ProgressEvent) {
	return func(event duplicacy.ProgressEvent) {
		if event.Operation != operation || event.Kind != duplicacy.ProgressKindStart {
			return
		}
		stop()
		deadline := time.Now().Add(cancelWait)
		for !duplicacy.IsCancelRequested() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
}

// expiringCtx is a context that reaches its deadline when the test calls expire, so that
// a test of the timeout expires it at a known point of the operation on a host of any
// speed.
type expiringCtx struct {
	context.Context
	done chan struct{}
	once sync.Once
}

// newExpiringCtx returns the expiringCtx with the values of parent.
func newExpiringCtx(parent context.Context) *expiringCtx {
	return &expiringCtx{Context: parent, done: make(chan struct{})}
}

func (c *expiringCtx) Done() <-chan struct{} {
	return c.done
}

func (c *expiringCtx) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// expire makes the deadline of the context reached.
func (c *expiringCtx) expire() {
	c.once.Do(func() { close(c.done) })
}

// afterFirstCheckCtx is a context that is not done at its first Err call and done after
// it, so that an operation that checks it at its start finds it canceled later.
type afterFirstCheckCtx struct {
	context.Context
	done  chan struct{}
	calls atomic.Int64
}

// newAfterFirstCheckCtx returns the afterFirstCheckCtx with the values of parent.
func newAfterFirstCheckCtx(parent context.Context) *afterFirstCheckCtx {
	return &afterFirstCheckCtx{Context: parent, done: make(chan struct{})}
}

func (c *afterFirstCheckCtx) Done() <-chan struct{} {
	return c.done
}

func (c *afterFirstCheckCtx) Err() error {
	if c.calls.Add(1) == 1 {
		close(c.done)
		return nil
	}
	return context.Canceled
}

// waitGoroutines waits until at most want goroutines run and fails the test when more
// of them stay for cancelWait.
func waitGoroutines(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(cancelWait)
	for runtime.NumGoroutine() > want {
		if time.Now().After(deadline) {
			t.Errorf("goroutines = %d, want at most %d", runtime.NumGoroutine(), want)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// checkCanceled checks the state after a canceled operation: the flag of the
// cancellation of duplicacy is reset and no record of the log in buf holds its log ID.
func checkCanceled(t *testing.T, buf *bytes.Buffer) {
	t.Helper()
	if duplicacy.IsCancelRequested() {
		t.Error("the cancellation of duplicacy is still requested")
	}
	if strings.Contains(buf.String(), duplicacy.CancelLogID) {
		t.Errorf("records hold %s: %s", duplicacy.CancelLogID, buf.String())
	}
}

// revisionFile returns the path of the file of the revision of the snapshot ID of the
// tests in the local storage dir.
func revisionFile(dir string, revision int) string {
	return filepath.Join(dir, "snapshots", testSnapshotID, strconv.Itoa(revision))
}

// finishBar is a progress bar that keeps the errors of its Finish calls and the error of
// the context that it was created with, as it was at the creation.
type finishBar struct {
	ci.ProgressBar
	createErr error
	mu        sync.Mutex
	errs      []error
}

func (b *finishBar) Finish(err error) {
	b.mu.Lock()
	b.errs = append(b.errs, err)
	b.mu.Unlock()
	b.ProgressBar.Finish(err)
}

// finishBars wraps every progress bar that the backups of the test create into a
// finishBar and returns the list of them.
func finishBars(t *testing.T) *[]*finishBar {
	t.Helper()
	var mu sync.Mutex
	var bars []*finishBar
	saved := newProgress
	newProgress = func(ctx context.Context, opts ...ci.ProgressOption) ci.ProgressBar {
		bar := &finishBar{ProgressBar: saved(ctx, opts...), createErr: ctx.Err()}
		mu.Lock()
		bars = append(bars, bar)
		mu.Unlock()
		return bar
	}
	t.Cleanup(func() { newProgress = saved })
	return &bars
}

// readCancelStorage cancels the context of the backup once the file of the first
// revision of the snapshot ID of the tests is uploaded, which comes after the last check
// of the cancellation of the backup: after the upload or, with duringRead, at the first
// listing of the revisions after it. With failRead, that listing fails with a reset
// connection.
type readCancelStorage struct {
	duplicacy.Storage
	cancel     context.CancelFunc
	duringRead bool
	failRead   bool
	uploaded   atomic.Bool
	canceled   atomic.Bool
}

// fire cancels the context of the backup and keeps the mark of it.
func (r *readCancelStorage) fire() {
	r.canceled.Store(true)
	r.cancel()
}

func (r *readCancelStorage) UploadFile(
	threadIndex int, filePath string, content []byte,
) error {
	err := r.Storage.UploadFile(threadIndex, filePath, content)
	if filePath == "snapshots/"+testSnapshotID+"/1" {
		r.uploaded.Store(true)
		if !r.duringRead {
			r.fire()
		}
	}
	return err
}

func (r *readCancelStorage) ListFiles(
	threadIndex int, dir string,
) ([]string, []int64, error) {
	if !r.uploaded.Load() || dir != "snapshots/"+testSnapshotID+"/" {
		return r.Storage.ListFiles(threadIndex, dir)
	}
	if r.duringRead {
		r.fire()
	}
	if r.failRead {
		return nil, nil, &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	}
	return r.Storage.ListFiles(threadIndex, dir)
}

func TestSession_Backup_cancel(t *testing.T) {
	type args struct {
		opts []BackupOption
	}
	tests := []struct {
		name string
		args args
		// atStart cancels the context at the start of the backup or, with expire, makes
		// it reach its deadline there.
		atStart bool
		expire  bool
		// read cancels the context after the upload of the revision; it is a
		// readCancelStorage without cancel.
		read *readCancelStorage
		// elements are the elements of the bar, nil – no bar; progress is the option of
		// go-lib-ci that selects its renderer.
		elements []string
		progress ci.ProgressOption
		wantErr  error
		// wantLogID is the log ID of the *Error of the backup; "" – not checked.
		wantLogID    string
		wantRevision int
		// wantAttempts are the attempt numbers of the records of the failed reads.
		wantAttempts []float64
	}{
		{
			name:      "canceled at the start",
			atStart:   true,
			wantErr:   context.Canceled,
			wantLogID: duplicacy.CancelLogID,
		},
		{
			name:      "timed out at the start",
			atStart:   true,
			expire:    true,
			wantErr:   context.DeadlineExceeded,
			wantLogID: duplicacy.CancelLogID,
		},
		{
			name:      "canceled at the start with an mpb bar",
			atStart:   true,
			elements:  []string{"bytes", "elapsed", "rate", "bar", "percent", "eta"},
			progress:  ci.WithProgressCIMode(false),
			wantErr:   context.Canceled,
			wantLogID: duplicacy.CancelLogID,
		},
		{
			name:      "canceled at the start with a CI bar",
			atStart:   true,
			elements:  []string{"bytes", "elapsed", "rate", "bar", "percent", "eta"},
			progress:  ci.WithProgressCIMode(true),
			wantErr:   context.Canceled,
			wantLogID: duplicacy.CancelLogID,
		},
		{
			name:         "canceled after the upload of the revision",
			read:         &readCancelStorage{},
			wantRevision: 1,
		},
		{
			name:         "canceled after the upload, the read fails",
			read:         &readCancelStorage{failRead: true},
			wantErr:      context.Canceled,
			wantAttempts: []float64{1},
		},
		{
			name:         "canceled during the read that fails",
			read:         &readCancelStorage{duringRead: true, failRead: true},
			wantErr:      context.Canceled,
			wantAttempts: []float64{1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keepGlobals(t)
			runAtError := duplicacy.RunAtError
			t.Cleanup(func() { duplicacy.RunAtError = runAtError })
			if tt.progress != nil {
				replaceProgressOptions(t, tt.progress)
			}
			bars := finishBars(t)
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.read != nil {
				tt.read.cancel = cancel
				replaceCreateStorage(t, func(
					p duplicacy.Preference, readOnly bool, threads int,
				) duplicacy.Storage {
					tt.read.Storage = duplicacy.CreateStorage(p, readOnly, threads)
					return tt.read
				})
			}
			var buf bytes.Buffer
			dir := filepath.Join(t.TempDir(), "storage")
			opts := testOptions(t, dir, testPassword, newLogger(&buf, slog.LevelDebug),
				fastRetry())
			if tt.elements != nil {
				opts = append(opts, WithProgress(&syncBuffer{}, tt.elements...))
			}
			s := openTest(t, opts)
			writeCopy(t, s.copyDir, testFiles)
			ctx := parent
			var stopped atomic.Bool
			stop := func() {
				stopped.Store(true)
				cancel()
			}
			if tt.expire {
				expiring := newExpiringCtx(parent)
				ctx = expiring
				stop = func() {
					stopped.Store(true)
					expiring.expire()
				}
			}
			if tt.atStart {
				replaceEventHook(t, stopAtStart(duplicacy.ProgressOperationBackup, stop))
			}
			before := runtime.NumGoroutine()
			got, err := s.Backup(ctx, tt.args.opts...)
			if tt.atStart && !stopped.Load() {
				t.Error("the backup did not reach its event of the start")
			}
			_, statErr := os.Stat(revisionFile(dir, 1))
			if tt.wantErr == nil {
				if err != nil || got == nil || got.Revision != tt.wantRevision {
					t.Fatalf("Session.Backup() = %+v, error = %v, want the revision %d",
						got, err, tt.wantRevision)
				}
			} else {
				if got != nil {
					t.Errorf("Session.Backup() = %+v, want nil", got)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("Session.Backup() error = %v, want %v", err, tt.wantErr)
				}
			}
			if tt.wantLogID != "" {
				if e, ok := errors.AsType[*Error](err); !ok || e.LogID != tt.wantLogID {
					t.Errorf("Session.Backup() error = %v, want the log ID %s", err,
						tt.wantLogID)
				}
				if !errors.Is(statErr, fs.ErrNotExist) {
					t.Errorf("revision 1: error = %v, want %v", statErr, fs.ErrNotExist)
				}
			} else if statErr != nil {
				t.Errorf("revision 1 after the upload: %v", statErr)
			}
			numbers := attempts(t, &buf, opBackup)
			if !reflect.DeepEqual(numbers, tt.wantAttempts) {
				t.Errorf("failed attempts = %v, want %v", numbers, tt.wantAttempts)
			}
			if tt.elements != nil {
				if len(*bars) != 1 {
					t.Fatalf("progress bars = %d, want 1", len(*bars))
				}
				bar := (*bars)[0]
				bar.mu.Lock()
				errs := bar.errs
				bar.mu.Unlock()
				if !errors.Is(bar.createErr, tt.wantErr) {
					t.Errorf("the context of the bar at its creation: error = %v, want %v",
						bar.createErr, tt.wantErr)
				}
				if len(errs) != 1 || !errors.Is(errs[0], errBackupInterrupted) {
					t.Errorf("Finish() calls = %v, want one with %v", errs,
						errBackupInterrupted)
				}
			}
			if tt.read != nil && !tt.read.canceled.Load() {
				t.Error("the context was not canceled after the upload of the revision")
			}
			checkCanceled(t, &buf)
			waitGoroutines(t, before)
		})
	}
}

func TestSession_Prune_cancel(t *testing.T) {
	type args struct {
		opts []PruneOption
	}
	tests := []struct {
		name string
		args args
		// expire makes the context reach its deadline at the start of the prune instead
		// of the cancellation there.
		expire  bool
		wantErr error
	}{
		{
			name:    "canceled at the start",
			args:    args{opts: []PruneOption{WithKeep("0:1")}},
			wantErr: context.Canceled,
		},
		{
			name:    "timed out at the start",
			args:    args{opts: []PruneOption{WithKeep("0:1")}},
			expire:  true,
			wantErr: context.DeadlineExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keepGlobals(t)
			runAtError := duplicacy.RunAtError
			t.Cleanup(func() { duplicacy.RunAtError = runAtError })
			var buf bytes.Buffer
			dir := filepath.Join(t.TempDir(), "storage")
			s := openTest(t, testOptions(t, dir, testPassword,
				newLogger(&buf, slog.LevelDebug), fastRetry()))
			writeCopy(t, s.copyDir, testFiles)
			if _, err := s.Backup(t.Context()); err != nil {
				t.Fatalf("Session.Backup() error = %v", err)
			}
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx := parent
			var stopped atomic.Bool
			stop := func() {
				stopped.Store(true)
				cancel()
			}
			if tt.expire {
				expiring := newExpiringCtx(parent)
				ctx = expiring
				stop = func() {
					stopped.Store(true)
					expiring.expire()
				}
			}
			replaceEventHook(t, stopAtStart(duplicacy.ProgressOperationPrune, stop))
			before := runtime.NumGoroutine()
			got, err := s.Prune(ctx, tt.args.opts...)
			if !stopped.Load() {
				t.Error("the prune did not reach its event of the start")
			}
			if got != nil {
				t.Errorf("Session.Prune() = %+v, want nil", got)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Session.Prune() error = %v, want %v", err, tt.wantErr)
			}
			e, ok := errors.AsType[*Error](err)
			if !ok || e.LogID != duplicacy.CancelLogID {
				t.Errorf("Session.Prune() error = %v, want the log ID %s", err,
					duplicacy.CancelLogID)
			}
			if _, statErr := os.Stat(revisionFile(dir, 1)); statErr != nil {
				t.Errorf("revision 1 after the canceled prune: %v", statErr)
			}
			checkCanceled(t, &buf)
			waitGoroutines(t, before)
		})
	}
}

func TestSession_operate_canceledAfterCheckCache(t *testing.T) {
	tests := []struct {
		name string
		op   string
		run  func(ctx context.Context, s *Session) (bool, error)
	}{
		{name: "backup", op: opBackup, run: backupWith()},
		{name: "prune", op: opPrune, run: pruneWith(WithKeep("0:1"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keepGlobals(t)
			runAtError := duplicacy.RunAtError
			t.Cleanup(func() { duplicacy.RunAtError = runAtError })
			storage := countStorage(t)
			var buf bytes.Buffer
			dir := filepath.Join(t.TempDir(), "storage")
			s := openTest(t, testOptions(t, dir, testPassword,
				newLogger(&buf, slog.LevelDebug), fastRetry()))
			writeCopy(t, s.copyDir, testFiles)
			if _, err := s.Backup(t.Context()); err != nil {
				t.Fatalf("Session.Backup() error = %v", err)
			}
			var events atomic.Int64
			replaceEventHook(t, func(duplicacy.ProgressEvent) { events.Add(1) })
			calls := storage.calls.Load()
			before := runtime.NumGoroutine()
			got, err := tt.run(newAfterFirstCheckCtx(t.Context()), s)
			if got {
				t.Errorf("the %s gave a result, want none", tt.name)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("the %s error = %v, want %v", tt.name, err, context.Canceled)
			}
			if !strings.HasPrefix(err.Error(), tt.op+": ") {
				t.Errorf("the %s error = %v, want the prefix %q", tt.name, err, tt.op+": ")
			}
			if n := storage.calls.Load() - calls; n != 0 {
				t.Errorf("calls of the storage = %d, want 0", n)
			}
			if n := events.Load(); n != 0 {
				t.Errorf("events of duplicacy = %d, want 0", n)
			}
			checkCanceled(t, &buf)
			waitGoroutines(t, before)
		})
	}
}
