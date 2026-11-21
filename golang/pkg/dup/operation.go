// cspell:ignore duplicacy

// Package dup is documented in log.go.
package dup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	duplicacy "duplicacy.invalid/v3/src"
)

// The operations of the session.
const (
	opBackup = "backup"
	opPrune  = "prune"
)

var errSessionClosed = errors.New("session is closed")

// collector gathers the events of ProgressFunction and the end of the backup of an
// operation; duplicacy calls them from its workers at the same time. Further consumers of
// the events, such as a progress bar, are added to next.
type collector struct {
	mu    sync.Mutex
	end   duplicacy.ProgressEvent
	steps map[duplicacy.ProgressAction]int64
	// revision is the revision that the log entry BACKUP_END of the backup reports; 0
	// before it.
	revision int
	// next gets every event after the collector; nil when there is none.
	next func(duplicacy.ProgressEvent)
}

// newCollector returns an empty collector.
func newCollector() *collector {
	return &collector{steps: map[duplicacy.ProgressAction]int64{}}
}

// eventHook gets every event of an operation before next; tests replace it.
var eventHook func(duplicacy.ProgressEvent)

// record is the ProgressFunction of the operation: it keeps the event of the end and
// counts the steps of the actions of prune that changed a chunk.
func (c *collector) record(event duplicacy.ProgressEvent) {
	c.mu.Lock()
	switch {
	case event.Kind == duplicacy.ProgressKindEnd:
		c.end = event
	case event.Kind == duplicacy.ProgressKindStep && event.Action != "" && !event.Skipped:
		c.steps[event.Action]++
	}
	c.mu.Unlock()
	if eventHook != nil {
		eventHook(event)
	}
	if c.next != nil {
		c.next(event)
	}
}

// endEvent returns the event of the end, the zero event before it.
func (c *collector) endEvent() duplicacy.ProgressEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.end
}

// count returns the number of the steps of action without Skipped.
func (c *collector) count(action duplicacy.ProgressAction) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.steps[action]
}

// logBackupEnd is the log ID of the entry that duplicacy writes after the upload of the
// last file of a backup; ProgressEvent of the end holds no revision.
const logBackupEnd = "BACKUP_END"

// The text of the entry BACKUP_END of a backup: "Backup for <top> at revision <N>
// completed", where the path <top> may hold any characters.
const (
	backupEndInfix  = " at revision "
	backupEndSuffix = " completed"
)

// observe is called for every log entry of the operation and keeps the revision that the
// entry BACKUP_END reports; the revision comes from the backup itself, so it is known on
// every storage, whatever its listing shows.
func (c *collector) observe(logID, message string) {
	if logID != logBackupEnd {
		return
	}
	text, ok := strings.CutSuffix(message, backupEndSuffix)
	if !ok {
		return
	}
	i := strings.LastIndex(text, backupEndInfix)
	if i < 0 {
		return
	}
	revision, err := strconv.Atoi(text[i+len(backupEndInfix):])
	if err != nil || revision < 1 {
		return
	}
	c.mu.Lock()
	c.revision = revision
	c.mu.Unlock()
}

// endRevision returns the revision of the entry BACKUP_END, 0 before it.
func (c *collector) endRevision() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revision
}

// work is an operation of duplicacy that operate runs: the name op, the collector c of
// its events and f, the call of duplicacy with the backup manager.
type work struct {
	op string
	c  *collector
	f  func(*duplicacy.BackupManager) error
}

// errCacheIsStorage is the error of an operation of a session whose cache of snapshots is
// the local file storage itself.
var errCacheIsStorage = errors.New("the snapshot cache directory is the storage")

// localRoot returns the root of the local file storage that duplicacy opens for
// storageURL, and false when the URL is a storage of another kind. The forms are those of
// CreateStorage: an absolute path, on Windows also a path with a drive letter and a UNC
// path, and the schemes flat:// and samba://.
func localRoot(storageURL string) (string, bool) {
	switch {
	case strings.HasPrefix(storageURL, "/"):
		return storageURL, true
	case runtime.GOOS == "windows" && hasDrive(storageURL):
		return storageURL, true
	case runtime.GOOS == "windows" && strings.HasPrefix(storageURL, `\\`):
		return storageURL, true
	case strings.HasPrefix(storageURL, "flat://"):
		return storageURL[len("flat://"):], true
	case strings.HasPrefix(storageURL, "samba://"):
		return storageURL[len("samba://"):], true
	}
	return "", false
}

// hasDrive tells whether name starts with a drive letter, a colon and a separator.
func hasDrive(name string) bool {
	if len(name) < 3 || name[1] != ':' || (name[2] != '/' && name[2] != '\\') {
		return false
	}
	letter := name[0] | 0x20
	return letter >= 'a' && letter <= 'z'
}

// checkCache fails when the snapshot cache of the session and the local file storage
// share a directory, compared as file objects, so that any link or mount that makes two
// paths one directory is found, whatever their relative paths are. The clean-up of the
// cache after a backup or a prune, and the files that a prune keeps in the cache, would
// then change the archive itself. Three comparisons are made, all through the links to
// directories and without a call of the storage. The cache tree is the cache directory of
// the storage with all its subdirectories, or the nearest existing ancestor of it when it
// is missing, and all the ancestors of that directory, with the links resolved, up to the
// root of the file system; the storage tree is the root of the storage with all its
// subdirectories, and they must not share a directory. A cache that is not created yet is
// found inside the storage by the ancestors, as is a cache inside a directory of the
// storage that can be passed but not read, and a link of the cache to a descendant of
// such a directory, as the ancestors of the target of every link of the cache tree are in
// it. The third comparison finds the storage root inside a directory of the cache that
// can't be read: the directories that the walk of the cache visited, the cache directory,
// its subdirectories and the targets of its links, but not their ancestors, must not hold
// the storage root or any of its ancestors, with the links resolved; the common ancestor
// of the cache and of the storage, such as the root of the file system, is not a refusal.
// The entries below the roots that the process can't reach are skipped, see walkDirs. A
// missing root of the storage is not checked; any other failed stat or read of a root, or
// of an entry below it that is not unreachable, fails the operation op, as the check
// cannot tell. The storages of other kinds are not checked.
func (s *Session) checkCache(op string) error {
	root, ok := localRoot(s.preference.StorageURL)
	if !ok {
		return nil
	}
	_, isDir, err := statDir(root)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !isDir) {
		return nil
	}
	if err != nil {
		return s.operationError(op, fmt.Errorf("stat the storage root: %w", err))
	}
	tree, err := s.cacheDirs()
	if err != nil {
		return s.operationError(op, fmt.Errorf("walk the snapshot cache: %w", err))
	}
	if len(tree.all) == 0 {
		return nil
	}
	inside, err := tree.holds(root)
	if err != nil {
		return s.operationError(op, fmt.Errorf("resolve the storage root: %w", err))
	}
	if inside {
		return s.operationError(op, errCacheIsStorage)
	}
	shared := false
	err = walkDirs(root, func(step walkStep) bool {
		_, shared = tree.all[step.id]
		return shared
	})
	if err != nil {
		return s.operationError(op, fmt.Errorf("walk the storage: %w", err))
	}
	if shared {
		return s.operationError(op, errCacheIsStorage)
	}
	return nil
}

// cacheTree is the directories of the snapshot cache tree of a session by their
// identities: all of them, and the ones that the walk of the cache visited.
type cacheTree struct {
	all     map[dirID]struct{}
	visited map[dirID]struct{}
}

// holds tells whether the directory root or one of its ancestors, with the links
// resolved, is one that the walk of the cache visited.
func (c cacheTree) holds(root string) (bool, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return false, err
	}
	found := false
	err = eachAncestor(resolved, func(id dirID) bool {
		_, found = c.visited[id]
		return found
	})
	return found, err
}

// eachAncestor calls f for the directory dir and each of its ancestors up to the root
// of the file system, until f returns true. An ancestor that the process can't reach,
// see unreachable, is skipped.
func eachAncestor(dir string, f func(dirID) bool) error {
	for ; ; dir = filepath.Dir(dir) {
		id, isDir, err := statDir(dir)
		if err != nil && !unreachable(err) {
			return err
		}
		if err == nil && isDir && f(id) {
			return nil
		}
		if dir == filepath.Dir(dir) {
			return nil
		}
	}
}

// cacheDirs returns the directories of the snapshot cache tree of the session: those of
// the cache directory of the storage and all its subdirectories, or of its nearest
// existing ancestor when it is missing, and of all the ancestors of that directory, with
// the links resolved, up to the root of the file system. The same goes for every
// directory of the cache that the walk reached through a link: the directory is the one
// of its target, so the target may lie in a directory of the storage that can be passed
// but not read, whose descendants the walk of the storage does not find, while its
// ancestors, which the walk of the storage does, are in the result. The directories that
// the walk visited, without the ancestors, are kept apart, see cacheTree.holds: the
// storage root may lie in a directory of them that the walk can't read. A link or an
// ancestor that can't be resolved because the process can't reach it, see unreachable,
// is skipped; any other error fails. A bind mount of a descendant of a directory of the
// storage that can't be read into the cache, which needs root, is not found.
func (s *Session) cacheDirs() (cacheTree, error) {
	tree := cacheTree{all: map[dirID]struct{}{}, visited: map[dirID]struct{}{}}
	root, err := filepath.Abs(filepath.Join(duplicacy.GetDuplicacyPreferencePath(),
		"cache", s.preference.Name))
	if err != nil {
		return tree, err
	}
	addAncestors := func(dir string) error {
		return eachAncestor(dir, func(id dirID) bool {
			tree.all[id] = struct{}{}
			return false
		})
	}
	near := root
	for {
		id, isDir, err := statDir(near)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return tree, err
		}
		if err == nil && isDir {
			tree.all[id] = struct{}{}
			break
		}
		parent := filepath.Dir(near)
		if parent == near {
			return tree, nil
		}
		near = parent
	}
	resolved, err := filepath.EvalSymlinks(near)
	if err != nil {
		return tree, err
	}
	if err := addAncestors(resolved); err != nil {
		return tree, err
	}
	var linkErr error
	err = walkDirs(root, func(step walkStep) bool {
		tree.all[step.id] = struct{}{}
		tree.visited[step.id] = struct{}{}
		if !step.link {
			return false
		}
		target, err := filepath.EvalSymlinks(step.path)
		if err == nil {
			err = addAncestors(target)
		}
		if err != nil && !unreachable(err) {
			linkErr = err
		}
		return linkErr != nil
	})
	if err == nil {
		err = linkErr
	}
	return tree, err
}

// operationError returns err of the operation op with the secrets of the session replaced
// with *** in its text.
func (s *Session) operationError(op string, err error) error {
	return &maskedError{err: fmt.Errorf("%s: %w", op, err), masks: s.masks}
}

// operate runs w.f, the operation w.op of duplicacy, with a new backup manager of the
// session and its snapshot cache, and with w.c as ProgressFunction and as the observer of
// the log. A canceled ctx, at the start or when checkCache is done, and a closed session
// fail before duplicacy is called, and so does a snapshot cache tree that shares a
// directory with the tree of the local file storage (see checkCache), without any call of
// the storage and without a change of the files of the storage and of the cache.
// RunAtError is cleared before the backup manager is created, so that it holds no
// function of an earlier call. While w.f runs, a done ctx requests the cancellation of
// duplicacy, which stops the operation at its next check; a request after the last check
// does not stop it. The Exception that duplicacy panics with becomes an *Error of the
// operation. The one of the cancellation wraps ctx.Err(), so errors.Is tells
// context.Canceled or context.DeadlineExceeded, and its record of the log is left out, as
// the caller reports the cancellation; an error of a worker of duplicacy that comes
// before the cancellation stays as it is. An *Error returned by w.f is returned as is,
// and any other error of w.f is wrapped with the operation.
func (s *Session) operate(ctx context.Context, w *work) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", w.op, err)
	}
	if s.storage == nil {
		return fmt.Errorf("%s: %w", w.op, errSessionClosed)
	}
	if err := s.checkCache(w.op); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", w.op, err)
	}
	duplicacy.RunAtError = doNothing
	duplicacy.ProgressFunction = w.c.record
	logFunction := duplicacy.LogFunction
	duplicacy.LogFunction = func(level int, logID, message string) {
		w.c.observe(logID, message)
		// The caller reports the cancellation from ctx, not from the record of duplicacy.
		if logFunction != nil && logID != duplicacy.CancelLogID {
			logFunction(level, logID, message)
		}
	}
	defer func() {
		duplicacy.ProgressFunction = nil
		duplicacy.LogFunction = logFunction
	}()
	stop := watchCancel(ctx)
	defer stop()
	var err error
	callErr := s.call(w.op, func() {
		manager := duplicacy.CreateBackupManager(s.preference.SnapshotID, s.storage,
			s.copyDir, s.password, "", "", false)
		manager.SetupSnapshotCache(s.preference.Name)
		err = w.f(manager)
	})
	if callErr != nil {
		e, ok := errors.AsType[*Error](callErr)
		if ctxErr := ctx.Err(); ok && e.LogID == duplicacy.CancelLogID && ctxErr != nil {
			// The cancellation that ctx requested tells its cause.
			e.Err = ctxErr
		}
		return callErr
	}
	if _, ok := errors.AsType[*Error](err); ok {
		return err
	}
	if err != nil {
		return s.operationError(w.op, err)
	}
	return nil
}

// watchCancel resets the cancellation of duplicacy and requests it from a new goroutine
// once ctx is done. The returned function stops that goroutine, waits for it and resets
// the cancellation again, so that neither an earlier nor a late request reaches another
// operation.
func watchCancel(ctx context.Context) func() {
	duplicacy.ResetCancel()
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		select {
		case <-ctx.Done():
			duplicacy.RequestCancel()
		case <-done:
		}
	})
	return func() {
		close(done)
		wg.Wait()
		duplicacy.ResetCancel()
	}
}
