// cspell:ignore duplicacy

package dup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"

	duplicacy "duplicacy.invalid/v3/src"
)

// operation runs an operation of s and tells whether its result is not nil.
type operation func(ctx context.Context, s *Session) (bool, error)

// backupWith returns the operation of Backup with opts.
func backupWith(opts ...BackupOption) operation {
	return func(ctx context.Context, s *Session) (bool, error) {
		got, err := s.Backup(ctx, opts...)
		return got != nil, err
	}
}

// pruneWith returns the operation of Prune with opts.
func pruneWith(opts ...PruneOption) operation {
	return func(ctx context.Context, s *Session) (bool, error) {
		got, err := s.Prune(ctx, opts...)
		return got != nil, err
	}
}

// countingStorage counts the calls of all methods of the storage and keeps the greatest
// thread index of the calls that have one.
type countingStorage struct {
	duplicacy.Storage
	calls     atomic.Int64
	maxThread atomic.Int64
}

// call counts a call with the thread index.
func (c *countingStorage) call(threadIndex int) {
	c.calls.Add(1)
	for {
		last := c.maxThread.Load()
		index := int64(threadIndex)
		if index <= last || c.maxThread.CompareAndSwap(last, index) {
			return
		}
	}
}

// countStorage replaces createStorage for the test with the one that gives a
// countingStorage over the storage of duplicacy.
func countStorage(t *testing.T) *countingStorage {
	t.Helper()
	storage := &countingStorage{}
	replaceCreateStorage(t, func(
		p duplicacy.Preference, readOnly bool, threads int,
	) duplicacy.Storage {
		storage.Storage = duplicacy.CreateStorage(p, readOnly, threads)
		return storage
	})
	return storage
}

func (c *countingStorage) ListFiles(
	threadIndex int, dir string,
) ([]string, []int64, error) {
	c.call(threadIndex)
	return c.Storage.ListFiles(threadIndex, dir)
}

func (c *countingStorage) DeleteFile(threadIndex int, filePath string) error {
	c.call(threadIndex)
	return c.Storage.DeleteFile(threadIndex, filePath)
}

func (c *countingStorage) MoveFile(threadIndex int, from, to string) error {
	c.call(threadIndex)
	return c.Storage.MoveFile(threadIndex, from, to)
}

func (c *countingStorage) CreateDirectory(threadIndex int, dir string) error {
	c.call(threadIndex)
	return c.Storage.CreateDirectory(threadIndex, dir)
}

func (c *countingStorage) GetFileInfo(
	threadIndex int, filePath string,
) (bool, bool, int64, error) {
	c.call(threadIndex)
	return c.Storage.GetFileInfo(threadIndex, filePath)
}

func (c *countingStorage) FindChunk(
	threadIndex int, chunkID string, isFossil bool, isMetadata bool,
) (string, bool, int64, error) {
	c.call(threadIndex)
	return c.Storage.FindChunk(threadIndex, chunkID, isFossil, isMetadata)
}

func (c *countingStorage) GetChunkDirectory(isMetadata bool) string {
	c.calls.Add(1)
	return c.Storage.GetChunkDirectory(isMetadata)
}

func (c *countingStorage) GetChunkDirectories() []string {
	c.calls.Add(1)
	return c.Storage.GetChunkDirectories()
}

func (c *countingStorage) DownloadFile(
	threadIndex int, filePath string, chunk *duplicacy.Chunk,
) error {
	c.call(threadIndex)
	return c.Storage.DownloadFile(threadIndex, filePath, chunk)
}

func (c *countingStorage) UploadFile(
	threadIndex int, filePath string, content []byte,
) error {
	c.call(threadIndex)
	return c.Storage.UploadFile(threadIndex, filePath, content)
}

func (c *countingStorage) SetNestingLevels(config *duplicacy.Config) {
	c.calls.Add(1)
	c.Storage.SetNestingLevels(config)
}

func (c *countingStorage) IsCacheNeeded() bool {
	c.calls.Add(1)
	return c.Storage.IsCacheNeeded()
}

func (c *countingStorage) IsMoveFileImplemented() bool {
	c.calls.Add(1)
	return c.Storage.IsMoveFileImplemented()
}

func (c *countingStorage) IsStrongConsistent() bool {
	c.calls.Add(1)
	return c.Storage.IsStrongConsistent()
}

func (c *countingStorage) IsFastListing() bool {
	c.calls.Add(1)
	return c.Storage.IsFastListing()
}

func (c *countingStorage) EnableTestMode() {
	c.calls.Add(1)
	c.Storage.EnableTestMode()
}

func (c *countingStorage) SetRateLimits(downloadRateLimit, uploadRateLimit int) {
	c.calls.Add(1)
	c.Storage.SetRateLimits(downloadRateLimit, uploadRateLimit)
}

// The links of the snapshot cache of a session to the local file storage.
const (
	// linkStorage makes the cache directory of the storage a link to its root.
	linkStorage = "storage"
	// linkParent makes the directory cache a link to the parent of the storage, whose name
	// is the one of the storage in the cache.
	linkParent = "parent"
	// linkSnapshots, linkSnapshotID and linkChunks make the directory of the cache of the
	// same relative path a link to the one of the storage.
	linkSnapshots  = "snapshots"
	linkSnapshotID = "snapshots/" + testSnapshotID
	linkChunks     = "chunks"
	// linkChunkDir makes chunks/<xx> of the cache a link to the chunks/<xx> of the storage,
	// where <xx> is a directory of the chunks of the first revision.
	linkChunkDir = "chunks/<xx>"
	// linkOtherID makes the directory of the snapshot ID of the cache a link to the
	// directory of another snapshot ID of the storage, which has a revision.
	linkOtherID = "snapshots/<other>"
	// linkFossils makes fossils of the cache a link to the fossils of the storage.
	linkFossils = "fossils"
	// linkRootInSnapshots makes snapshots of the cache a link to the parent of the storage,
	// so that the root of the storage is inside the cache.
	linkRootInSnapshots = "root in snapshots"
	// linkCacheInStorage makes the cache directory of the storage a link to chunks/<xx> of
	// the storage, so that the cache is inside the storage.
	linkCacheInStorage = "cache in storage"
	// linkLoop makes a link to the cache directory of the storage inside it: a cycle, which
	// does not share any directory with the storage.
	linkLoop = "loop"
	// linkArchChunks makes chunks of the cache a link to arch/chunks of the storage, where
	// arch is a directory that junkArch makes unreadable.
	linkArchChunks = "arch chunks"
	// linkOutside makes chunks of the cache a link to a directory next to the storage.
	linkOutside = "outside"
	// linkOutsidePass is linkOutside where the directory that holds the target can only be
	// passed.
	linkOutsidePass = "outside in a directory to pass"
	// linkRootInPass makes snapshots of the cache a link to the parent of the storage,
	// whose permissions are 0100: the root of the storage is inside a directory of the
	// cache that can only be passed.
	linkRootInPass = "root in a directory to pass"
	// linkRootInWritePass is linkRootInPass with the permissions 0300.
	linkRootInWritePass = "root in a directory to write and pass"
)

// otherSnapshotID is the snapshot ID of another archive of the storage of the linked
// cases.
const otherSnapshotID = "other"

// storageName is the name of the storage of the linked cases: the one of its directory.
const storageName = "storage"

// The layouts of the directories of the state of a session and of the storage, made
// before the session is opened, in which the snapshot cache does not exist yet.
const (
	// stateIsStorage makes the state directory the root of the storage.
	stateIsStorage = "state is the storage"
	// stateInStorage makes the state directory a subdirectory of the storage.
	stateInStorage = "state in the storage"
	// preferenceLink makes the directory of the preferences a link to a subdirectory of the
	// storage.
	preferenceLink = "preferences link"
	// storageInState makes the storage a subdirectory of the state directory: the cache is
	// outside of it.
	storageInState = "storage in the state"
)

// The directories and links that the root of the storage and the cache of the linked
// session get, which a walk of the trees must not fail on.
const (
	// junkLinks adds a link in a loop and a link to nothing to the root of the storage and
	// to the cache directory of the storage.
	junkLinks = "links in a loop and to nothing"
	// junkUnreadable adds to the root of the storage a directory with no permissions.
	junkUnreadable = "unreadable directory"
	// junkNoPass adds to the root of the storage and to the cache directory a directory
	// that can be read but not passed, with a subdirectory.
	junkNoPass = "directory without the right to pass"
	// junkBlocked adds to the root of the storage and to the cache directory a link to a
	// directory behind a directory outside of the storage that can't be passed.
	junkBlocked = "link behind a directory that can't be passed"
	// junkViaFile adds to the root of the storage and to the cache directory a link to a
	// path through a file.
	junkViaFile = "link through a file"
	// junkArch adds to the root of the storage the directory arch with the subdirectory
	// chunks, where arch can be passed but not read.
	junkArch = "arch that can only be passed"
	// junkStatIOStorage and junkStatIOCache add the directory sick to the root of the
	// storage or to the cache directory, whose stat fails with an I/O error.
	junkStatIOStorage = "I/O error of a stat in the storage"
	junkStatIOCache   = "I/O error of a stat in the cache"
	// junkReadIOStorage and junkReadIOCache add the directory sick to the root of the
	// storage or to the cache directory, whose open fails with an I/O error.
	junkReadIOStorage = "I/O error of a read in the storage"
	junkReadIOCache   = "I/O error of a read in the cache"
)

// sickDir is the name of the directory that the I/O errors of the junk are made for.
const sickDir = "sick"

// junkInStorage tells whether junk is added to the root of the storage.
func junkInStorage(junk string) bool {
	return junk != junkStatIOCache && junk != junkReadIOCache
}

// linkNeedsPermissions tells whether link relies on permissions that the process may
// ignore.
func linkNeedsPermissions(link string) bool {
	switch link {
	case linkOutsidePass, linkRootInPass, linkRootInWritePass:
		return true
	}
	return false
}

// failDirIO makes the walk of the trees fail with an I/O error on the directory sick that
// junk adds: the stat or the open of it, for the test only.
func failDirIO(t *testing.T, junk string) {
	t.Helper()
	savedStat, savedOpen := statDirFunc, openDirFunc
	t.Cleanup(func() { statDirFunc, openDirFunc = savedStat, savedOpen })
	sick := func(name string) bool { return filepath.Base(name) == sickDir }
	switch junk {
	case junkStatIOStorage, junkStatIOCache:
		statDirFunc = func(name string) (dirID, bool, error) {
			if sick(name) {
				return dirID{}, false, &fs.PathError{Op: "stat", Path: name, Err: syscall.EIO}
			}
			return savedStat(name)
		}
	case junkReadIOStorage, junkReadIOCache:
		openDirFunc = func(name string) (*os.File, error) {
			if sick(name) {
				return nil, &fs.PathError{Op: "open", Path: name, Err: syscall.EIO}
			}
			return savedOpen(name)
		}
	}
}

// junkInCache tells whether junk is added to the cache directory of the storage too.
func junkInCache(junk string) bool {
	switch junk {
	case junkLinks, junkNoPass, junkBlocked, junkViaFile, junkStatIOCache,
		junkReadIOCache:
		return true
	}
	return false
}

// junkNeedsPermissions tells whether junk relies on permissions that the process may
// ignore.
func junkNeedsPermissions(junk string) bool {
	switch junk {
	case junkUnreadable, junkNoPass, junkBlocked, junkArch:
		return true
	}
	return false
}

// treeOf returns the hashes of the files of dir by their paths and the paths of the
// directories, and the targets of the links; a directory with permissions that deny the
// read or the pass is made readable while it is read and restored after it.
func treeOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	tree := map[string]string{dir: "dir"}
	readTree(t, dir, tree)
	return tree
}

// readTree adds to tree the content of the directory dir.
func readTree(t *testing.T, dir string, tree map[string]string) {
	t.Helper()
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm&0o700 != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chmod(dir, perm); err != nil {
				t.Error(err)
			}
		}()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := filepath.Join(dir, entry.Name())
		switch {
		case entry.IsDir():
			tree[name] = "dir"
			readTree(t, name, tree)
		case entry.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(name)
			if err != nil {
				t.Fatal(err)
			}
			tree[name] = "link to " + target
		default:
			content, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(content)
			tree[name] = hex.EncodeToString(sum[:])
		}
	}
}

// chunkDir returns the name of a directory of the chunks of the storage dir.
func chunkDir(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "chunks"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return entry.Name()
		}
	}
	t.Fatalf("storage %s has no directory of chunks", dir)
	return ""
}

// restrictDir sets the permissions perm of dir and restores them after the test.
func restrictDir(t *testing.T, dir string, perm fs.FileMode) {
	t.Helper()
	if err := os.Chmod(dir, perm); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// linkPath returns the link of the cache that link tells and the path it points to; the
// cache of the storage name is cacheDir in the cache directory cache.
func linkPath(t *testing.T, storageDir, cache, link string) (string, string) {
	t.Helper()
	cacheDir := filepath.Join(cache, storageName)
	switch link {
	case linkStorage:
		return cacheDir, storageDir
	case linkParent:
		return cache, filepath.Dir(storageDir)
	case linkChunkDir:
		name := chunkDir(t, storageDir)
		return filepath.Join(cacheDir, "chunks", name),
			filepath.Join(storageDir, "chunks", name)
	case linkOtherID:
		return filepath.Join(cacheDir, linkSnapshotID),
			filepath.Join(storageDir, "snapshots", otherSnapshotID)
	case linkFossils:
		fossils := filepath.Join(storageDir, "fossils")
		if err := os.MkdirAll(fossils, 0o700); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(cacheDir, "fossils"), fossils
	case linkRootInSnapshots:
		return filepath.Join(cacheDir, "snapshots"), filepath.Dir(storageDir)
	case linkCacheInStorage:
		return cacheDir, filepath.Join(storageDir, "chunks", chunkDir(t, storageDir))
	case linkLoop:
		return filepath.Join(cacheDir, "loop"), cacheDir
	case linkArchChunks:
		return filepath.Join(cacheDir, "chunks"), filepath.Join(storageDir, "arch", "chunks")
	case linkOutside:
		neighbor := filepath.Join(filepath.Dir(storageDir), "neighbor", "chunks")
		if err := os.MkdirAll(neighbor, 0o700); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(cacheDir, "chunks"), neighbor
	case linkOutsidePass:
		neighbor := filepath.Join(filepath.Dir(storageDir), "neighbor")
		if err := os.MkdirAll(filepath.Join(neighbor, "chunks"), 0o700); err != nil {
			t.Fatal(err)
		}
		restrictDir(t, neighbor, 0o100)
		return filepath.Join(cacheDir, "chunks"), filepath.Join(neighbor, "chunks")
	case linkRootInPass, linkRootInWritePass:
		parent := filepath.Dir(storageDir)
		perm := fs.FileMode(0o100)
		if link == linkRootInWritePass {
			perm = 0o300
		}
		restrictDir(t, parent, perm)
		return filepath.Join(cacheDir, "snapshots"), parent
	}
	return filepath.Join(cacheDir, filepath.FromSlash(link)),
		filepath.Join(storageDir, filepath.FromSlash(link))
}

// addJunk adds the entries of junk to the directory dir; the directories with
// permissions are readable again after the test.
func addJunk(t *testing.T, dir, junk string) {
	t.Helper()
	switch junk {
	case junkLinks:
		loop := filepath.Join(dir, "loop")
		if err := os.Symlink(loop, loop); err != nil {
			t.Fatal(err)
		}
		gone := filepath.Join(dir, "gone")
		if err := os.Symlink(filepath.Join(dir, "no", "such", "target"), gone); err != nil {
			t.Fatal(err)
		}
	case junkUnreadable:
		locked := filepath.Join(dir, "lost+found")
		if err := os.Mkdir(locked, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	case junkNoPass:
		closed := filepath.Join(dir, "no-pass")
		if err := os.MkdirAll(filepath.Join(closed, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(closed, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(closed, 0o700) })
	case junkBlocked:
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.MkdirAll(filepath.Join(outside, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(outside, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(outside, 0o700) })
		blocked := filepath.Join(dir, "blocked")
		if err := os.Symlink(filepath.Join(outside, "sub"), blocked); err != nil {
			t.Fatal(err)
		}
	case junkViaFile:
		file := filepath.Join(dir, "plain")
		if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		through := filepath.Join(dir, "through")
		if err := os.Symlink(filepath.Join(file, "x"), through); err != nil {
			t.Fatal(err)
		}
	case junkArch:
		arch := filepath.Join(dir, "arch")
		if err := os.MkdirAll(filepath.Join(arch, "chunks"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(arch, 0o300); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(arch, 0o700) })
	case junkStatIOStorage, junkStatIOCache, junkReadIOStorage, junkReadIOCache:
		if err := os.Mkdir(filepath.Join(dir, sickDir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

// layoutOptions makes the directories of layout in a new test directory and returns the
// options of a session with them and the root of its storage.
func layoutOptions(t *testing.T, layout string) ([]SessionOption, string) {
	t.Helper()
	base := t.TempDir()
	storageDir := filepath.Join(base, "storage")
	state := filepath.Join(base, "state")
	switch layout {
	case stateIsStorage:
		state = storageDir
	case stateInStorage:
		state = filepath.Join(storageDir, "state")
	case preferenceLink:
		inside := filepath.Join(storageDir, "inside")
		if err := os.MkdirAll(inside, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(state, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(inside, filepath.Join(state, preferenceDir)); err != nil {
			t.Fatal(err)
		}
	case storageInState:
		storageDir = filepath.Join(state, "archive")
	}
	return []SessionOption{WithURL(storageDir), WithStateDir(state)}, storageDir
}

// linkCache closes s and opens a session of its storage whose snapshot cache is linked to
// the storage as link tells.
func linkCache(t *testing.T, s *Session, link string) *Session {
	t.Helper()
	storageDir := s.preference.StorageURL
	s.Close()
	state := filepath.Join(t.TempDir(), "state")
	cache := filepath.Join(state, preferenceDir, "cache")
	from, to := linkPath(t, storageDir, cache, link)
	if err := os.MkdirAll(filepath.Dir(from), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(to, from); err != nil {
		t.Fatal(err)
	}
	opts := readOptions(t, storageDir, testPassword, nil, fastRetry())
	opts = append(opts, WithStateDir(state), WithStorageName(storageName))
	return openTest(t, opts)
}

func TestSession_operate(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	keep := WithKeep("0:1")
	type args struct {
		ctx context.Context
		run operation
		// closed closes the session before the operation.
		closed bool
		// threads are the threads of the storage of the session; 0 – the one of the tests.
		threads int
		// link is the link of the snapshot cache, or its directory, to the storage root or
		// the directory after the first revision, made by a session without it; empty – the
		// cache is its own directory.
		link string
		// layout is the layout of the directories of the state and of the storage, with the
		// cache that does not exist; empty – the usual one.
		layout string
		// junk is added to the storage and its cache after the first revision.
		junk string
	}
	tests := []struct {
		name string
		args args
		// wantErr is the cause of the error; nil – an error of the options.
		wantErr error
		// ok is a success of the operation instead of an error.
		ok bool
	}{
		{
			name: "backup threads 0",
			args: args{ctx: t.Context(), run: backupWith(WithBackupThreads(0))},
		},
		{name: "prune without keep", args: args{ctx: t.Context(), run: pruneWith()}},
		{
			name: "prune empty policy",
			args: args{ctx: t.Context(), run: pruneWith(WithKeep(""))},
		},
		{
			name: "prune blank policy",
			args: args{ctx: t.Context(), run: pruneWith(WithKeep(" "))},
		},
		{
			name: "prune mixed list with an empty policy",
			args: args{ctx: t.Context(), run: pruneWith(WithKeep("0:1", "", "7:30"))},
		},
		{
			name: "prune blank policy of a tab",
			args: args{ctx: t.Context(), run: pruneWith(WithKeep("\t"))},
		},
		{
			name: "prune blank policy of a no-break space",
			args: args{ctx: t.Context(), run: pruneWith(WithKeep("\u00a0"))},
		},
		{
			name: "prune mixed blank policy",
			args: args{ctx: t.Context(), run: pruneWith(WithKeep("0:1", " \t\u00a0 "))},
		},
		{
			name: "backup threads above the storage threads",
			args: args{ctx: t.Context(), run: backupWith(WithBackupThreads(3)), threads: 2},
		},
		{
			name: "backup threads above the default storage threads",
			args: args{ctx: t.Context(), run: backupWith(WithBackupThreads(2))},
		},
		{
			name: "prune threads above the storage threads",
			args: args{
				ctx: t.Context(), run: pruneWith(keep, WithPruneThreads(3)), threads: 2,
			},
		},
		{
			name: "prune threads 0",
			args: args{ctx: t.Context(), run: pruneWith(keep, WithPruneThreads(0))},
		},
		{
			name:    "backup canceled",
			args:    args{ctx: canceled, run: backupWith()},
			wantErr: context.Canceled,
		},
		{
			name:    "prune canceled",
			args:    args{ctx: canceled, run: pruneWith(keep)},
			wantErr: context.Canceled,
		},
		{
			name:    "backup cache is the storage",
			args:    args{ctx: t.Context(), run: backupWith(), link: linkStorage},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "prune cache is the storage",
			args:    args{ctx: t.Context(), run: pruneWith(keep), link: linkStorage},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "backup cache directory is the parent of the storage",
			args:    args{ctx: t.Context(), run: backupWith(), link: linkParent},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "prune cache directory is the parent of the storage",
			args:    args{ctx: t.Context(), run: pruneWith(keep), link: linkParent},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "backup cache snapshots is the one of the storage",
			args:    args{ctx: t.Context(), run: backupWith(), link: linkSnapshots},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "prune cache snapshots is the one of the storage",
			args:    args{ctx: t.Context(), run: pruneWith(keep), link: linkSnapshots},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "backup cache snapshot ID is the one of the storage",
			args:    args{ctx: t.Context(), run: backupWith(), link: linkSnapshotID},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "prune cache snapshot ID is the one of the storage",
			args:    args{ctx: t.Context(), run: pruneWith(keep), link: linkSnapshotID},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "backup cache chunks is the one of the storage",
			args:    args{ctx: t.Context(), run: backupWith(), link: linkChunks},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "prune cache chunks is the one of the storage",
			args:    args{ctx: t.Context(), run: pruneWith(keep), link: linkChunks},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "backup cache chunks directory is the one of the storage",
			args:    args{ctx: t.Context(), run: backupWith(), link: linkChunkDir},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "prune cache chunks directory is the one of the storage",
			args:    args{ctx: t.Context(), run: pruneWith(keep), link: linkChunkDir},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "backup cache snapshot ID is the one of another ID of the storage",
			args:    args{ctx: t.Context(), run: backupWith(), link: linkOtherID},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "prune cache snapshot ID is the one of another ID of the storage",
			args:    args{ctx: t.Context(), run: pruneWith(keep), link: linkOtherID},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "backup cache fossils is the one of the storage",
			args:    args{ctx: t.Context(), run: backupWith(), link: linkFossils},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "prune cache fossils is the one of the storage",
			args:    args{ctx: t.Context(), run: pruneWith(keep), link: linkFossils},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "backup storage root is inside the cache snapshots",
			args:    args{ctx: t.Context(), run: backupWith(), link: linkRootInSnapshots},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "prune storage root is inside the cache snapshots",
			args:    args{ctx: t.Context(), run: pruneWith(keep), link: linkRootInSnapshots},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "backup cache is inside the storage",
			args:    args{ctx: t.Context(), run: backupWith(), link: linkCacheInStorage},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "prune cache is inside the storage",
			args:    args{ctx: t.Context(), run: pruneWith(keep), link: linkCacheInStorage},
			wantErr: errCacheIsStorage,
		},
		{
			name: "backup cache with a link to its own directory",
			args: args{ctx: t.Context(), run: backupWith(), link: linkLoop},
			ok:   true,
		},
		{
			name: "prune cache with a link to its own directory",
			args: args{ctx: t.Context(), run: pruneWith(keep), link: linkLoop},
			ok:   true,
		},
		{
			name:    "backup state is the storage without the cache",
			args:    args{ctx: t.Context(), run: backupWith(), layout: stateIsStorage},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "backup state is inside the storage without the cache",
			args:    args{ctx: t.Context(), run: backupWith(), layout: stateInStorage},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "backup preferences link to the inside of the storage without the cache",
			args:    args{ctx: t.Context(), run: backupWith(), layout: preferenceLink},
			wantErr: errCacheIsStorage,
		},
		{
			name: "backup storage is inside the state without the cache",
			args: args{ctx: t.Context(), run: backupWith(), layout: storageInState},
			ok:   true,
		},
		{
			name: "backup links in a loop and to nothing",
			args: args{ctx: t.Context(), run: backupWith(), junk: junkLinks},
			ok:   true,
		},
		{
			name: "backup unreadable directory in the storage",
			args: args{ctx: t.Context(), run: backupWith(), junk: junkUnreadable},
			ok:   true,
		},
		{
			name: "backup unreadable directory and cache chunks directory is the storage one",
			args: args{
				ctx: t.Context(), run: backupWith(), link: linkChunkDir, junk: junkUnreadable,
			},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "prune state is the storage without the cache",
			args:    args{ctx: t.Context(), run: pruneWith(keep), layout: stateIsStorage},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "prune state is inside the storage without the cache",
			args:    args{ctx: t.Context(), run: pruneWith(keep), layout: stateInStorage},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "prune preferences link to the inside of the storage without the cache",
			args:    args{ctx: t.Context(), run: pruneWith(keep), layout: preferenceLink},
			wantErr: errCacheIsStorage,
		},
		{
			name: "prune storage is inside the state without the cache",
			args: args{ctx: t.Context(), run: pruneWith(keep), layout: storageInState},
			ok:   true,
		},
		{
			name: "prune links in a loop and to nothing",
			args: args{ctx: t.Context(), run: pruneWith(keep), junk: junkLinks},
			ok:   true,
		},
		{
			name: "prune unreadable directory in the storage",
			args: args{ctx: t.Context(), run: pruneWith(keep), junk: junkUnreadable},
			ok:   true,
		},
		{
			name: "prune unreadable directory and cache chunks directory is the storage one",
			args: args{
				ctx: t.Context(), run: pruneWith(keep), link: linkChunkDir, junk: junkUnreadable,
			},
			wantErr: errCacheIsStorage,
		},
		{
			name: "backup no pass entries in the storage and the cache",
			args: args{ctx: t.Context(), run: backupWith(), junk: junkNoPass},
			ok:   true,
		},
		{
			name: "backup no pass entries and cache chunks directory is the storage one",
			args: args{
				ctx: t.Context(), run: backupWith(), link: linkChunkDir, junk: junkNoPass,
			},
			wantErr: errCacheIsStorage,
		},
		{
			name: "backup blocked entries in the storage and the cache",
			args: args{ctx: t.Context(), run: backupWith(), junk: junkBlocked},
			ok:   true,
		},
		{
			name: "backup blocked entries and cache chunks directory is the storage one",
			args: args{
				ctx: t.Context(), run: backupWith(), link: linkChunkDir, junk: junkBlocked,
			},
			wantErr: errCacheIsStorage,
		},
		{
			name: "backup via file entries in the storage and the cache",
			args: args{ctx: t.Context(), run: backupWith(), junk: junkViaFile},
			ok:   true,
		},
		{
			name: "backup via file entries and cache chunks directory is the storage one",
			args: args{
				ctx: t.Context(), run: backupWith(), link: linkChunkDir, junk: junkViaFile,
			},
			wantErr: errCacheIsStorage,
		},
		{
			name: "backup cache chunks is a link into a directory that can only be passed",
			args: args{
				ctx: t.Context(), run: backupWith(), link: linkArchChunks, junk: junkArch,
			},
			wantErr: errCacheIsStorage,
		},
		{
			name: "backup cache chunks is outside of the storage with a directory to pass",
			args: args{
				ctx: t.Context(), run: backupWith(), link: linkOutside, junk: junkArch,
			},
			ok: true,
		},
		{
			name: "prune no pass entries in the storage and the cache",
			args: args{ctx: t.Context(), run: pruneWith(keep), junk: junkNoPass},
			ok:   true,
		},
		{
			name: "prune no pass entries and cache chunks directory is the storage one",
			args: args{
				ctx: t.Context(), run: pruneWith(keep), link: linkChunkDir, junk: junkNoPass,
			},
			wantErr: errCacheIsStorage,
		},
		{
			name: "prune blocked entries in the storage and the cache",
			args: args{ctx: t.Context(), run: pruneWith(keep), junk: junkBlocked},
			ok:   true,
		},
		{
			name: "prune blocked entries and cache chunks directory is the storage one",
			args: args{
				ctx: t.Context(), run: pruneWith(keep), link: linkChunkDir, junk: junkBlocked,
			},
			wantErr: errCacheIsStorage,
		},
		{
			name: "prune via file entries in the storage and the cache",
			args: args{ctx: t.Context(), run: pruneWith(keep), junk: junkViaFile},
			ok:   true,
		},
		{
			name: "prune via file entries and cache chunks directory is the storage one",
			args: args{
				ctx: t.Context(), run: pruneWith(keep), link: linkChunkDir, junk: junkViaFile,
			},
			wantErr: errCacheIsStorage,
		},
		{
			name: "prune cache chunks is a link into a directory that can only be passed",
			args: args{
				ctx: t.Context(), run: pruneWith(keep), link: linkArchChunks, junk: junkArch,
			},
			wantErr: errCacheIsStorage,
		},
		{
			name: "prune cache chunks is outside of the storage with a directory to pass",
			args: args{
				ctx: t.Context(), run: pruneWith(keep), link: linkOutside, junk: junkArch,
			},
			ok: true,
		},
		{
			name:    "backup I/O error of a stat of a directory in the storage",
			args:    args{ctx: t.Context(), run: backupWith(), junk: junkStatIOStorage},
			wantErr: syscall.EIO,
		},
		{
			name:    "backup I/O error of a stat of a directory in the cache",
			args:    args{ctx: t.Context(), run: backupWith(), junk: junkStatIOCache},
			wantErr: syscall.EIO,
		},
		{
			name:    "backup I/O error of a read of a directory in the storage",
			args:    args{ctx: t.Context(), run: backupWith(), junk: junkReadIOStorage},
			wantErr: syscall.EIO,
		},
		{
			name:    "backup I/O error of a read of a directory in the cache",
			args:    args{ctx: t.Context(), run: backupWith(), junk: junkReadIOCache},
			wantErr: syscall.EIO,
		},
		{
			name:    "backup storage root is in a cache directory with the permissions 0100",
			args:    args{ctx: t.Context(), run: backupWith(), link: linkRootInPass},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "backup storage root is in a cache directory with the permissions 0300",
			args:    args{ctx: t.Context(), run: backupWith(), link: linkRootInWritePass},
			wantErr: errCacheIsStorage,
		},
		{
			name: "backup cache chunks is outside of the storage in a directory to pass",
			args: args{ctx: t.Context(), run: backupWith(), link: linkOutsidePass},
			ok:   true,
		},
		{
			name:    "prune I/O error of a stat of a directory in the storage",
			args:    args{ctx: t.Context(), run: pruneWith(keep), junk: junkStatIOStorage},
			wantErr: syscall.EIO,
		},
		{
			name:    "prune I/O error of a stat of a directory in the cache",
			args:    args{ctx: t.Context(), run: pruneWith(keep), junk: junkStatIOCache},
			wantErr: syscall.EIO,
		},
		{
			name:    "prune I/O error of a read of a directory in the storage",
			args:    args{ctx: t.Context(), run: pruneWith(keep), junk: junkReadIOStorage},
			wantErr: syscall.EIO,
		},
		{
			name:    "prune I/O error of a read of a directory in the cache",
			args:    args{ctx: t.Context(), run: pruneWith(keep), junk: junkReadIOCache},
			wantErr: syscall.EIO,
		},
		{
			name:    "prune storage root is in a cache directory with the permissions 0100",
			args:    args{ctx: t.Context(), run: pruneWith(keep), link: linkRootInPass},
			wantErr: errCacheIsStorage,
		},
		{
			name:    "prune storage root is in a cache directory with the permissions 0300",
			args:    args{ctx: t.Context(), run: pruneWith(keep), link: linkRootInWritePass},
			wantErr: errCacheIsStorage,
		},
		{
			name: "prune cache chunks is outside of the storage in a directory to pass",
			args: args{ctx: t.Context(), run: pruneWith(keep), link: linkOutsidePass},
			ok:   true,
		},
		{
			name:    "backup closed",
			args:    args{ctx: t.Context(), run: backupWith(), closed: true},
			wantErr: errSessionClosed,
		},
		{
			name:    "prune closed",
			args:    args{ctx: t.Context(), run: pruneWith(keep), closed: true},
			wantErr: errSessionClosed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if (junkNeedsPermissions(tt.args.junk) || linkNeedsPermissions(tt.args.link)) &&
				(runtime.GOOS == "windows" || os.Geteuid() == 0) {
				t.Skip("permissions do not deny the access of the process")
			}
			storage := countStorage(t)
			var extra []SessionOption
			storageDir := ""
			if tt.args.layout != "" {
				var layout []SessionOption
				layout, storageDir = layoutOptions(t, tt.args.layout)
				extra = append(extra, layout...)
			}
			if tt.args.threads > 0 {
				extra = append(extra, WithThreads(tt.args.threads))
			}
			if tt.args.link != "" {
				extra = append(extra, WithStorageName(storageName))
			}
			s := openBackupTest(t, extra...)
			writeCopy(t, s.copyDir, testFiles)
			var before map[string]string
			if tt.args.layout != "" {
				before = treeOf(t, storageDir)
			}
			if tt.args.link != "" || tt.args.junk != "" {
				if got, err := s.Backup(t.Context()); err != nil || got.Revision != 1 {
					t.Fatalf("Backup() before the link = %+v, error = %v", got, err)
				}
				storageDir = s.preference.StorageURL
				if tt.args.link == linkOtherID {
					s.Close()
					other := openTest(t, append(readOptions(t, storageDir, testPassword,
						nil, fastRetry()), WithSnapshotID(otherSnapshotID)))
					writeCopy(t, other.copyDir, testFiles)
					if got, err := other.Backup(t.Context()); err != nil || got.Revision != 1 {
						t.Fatalf("Backup() of another ID = %+v, error = %v", got, err)
					}
					other.Close()
				}
				if tt.args.link != "" {
					s = linkCache(t, s, tt.args.link)
					writeCopy(t, s.copyDir, testFiles)
				}
				if tt.args.junk != "" {
					if junkInStorage(tt.args.junk) {
						addJunk(t, storageDir, tt.args.junk)
					}
					if junkInCache(tt.args.junk) {
						addJunk(t, filepath.Join(s.stateDir, preferenceDir, "cache",
							s.preference.Name), tt.args.junk)
					}
					failDirIO(t, tt.args.junk)
				}
				before = treeOf(t, storageDir)
			}
			cacheBase := filepath.Join(s.stateDir, preferenceDir, "cache")
			snapshotCache := filepath.Join(cacheBase, s.preference.Name)
			_, statErr := os.Stat(snapshotCache)
			hadCache := statErr == nil
			_, statErr = os.Stat(cacheBase)
			hadBase := statErr == nil
			if tt.args.closed {
				s.Close()
			}
			storage.calls.Store(0)
			gotResult, err := tt.args.run(tt.args.ctx, s)
			if tt.ok {
				if err != nil || !gotResult {
					t.Errorf("result = %v, error = %v, want a result", gotResult, err)
				}
				return
			}
			if calls := storage.calls.Load(); calls != 0 {
				t.Errorf("operation called the storage %d times, want 0", calls)
			}
			if _, err := os.Stat(snapshotCache); err == nil && !hadCache {
				t.Error("operation created the cache of the snapshot ID, want none")
			}
			if _, err := os.Stat(cacheBase); err == nil && !hadBase {
				t.Error("operation created the cache directory, want none")
			}
			if err == nil || gotResult {
				t.Fatalf("result = %v, error = %v, want no result and an error",
					gotResult, err)
			}
			if _, ok := errors.AsType[*Error](err); ok {
				t.Errorf("error = %v, want one before the storage is called", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want one wrapping %v", err, tt.wantErr)
			}
			if tt.wantErr != errCacheIsStorage && errors.Is(err, errCacheIsStorage) {
				t.Errorf("error = %v, want one that is not %v", err, errCacheIsStorage)
			}
			if before != nil {
				// Neither the revisions nor the chunks of the storage have changed.
				if after := treeOf(t, s.preference.StorageURL); !maps.Equal(after, before) {
					t.Errorf("files of the storage changed: %v, want %v", after, before)
				}
				return
			}
			// The failed operation has created no revision; a session of the same storage tells
			// it, as the closed one can't.
			next := s
			if tt.args.closed {
				next = openTest(t, readOptions(t, s.preference.StorageURL, testPassword,
					nil, fastRetry()))
				writeCopy(t, next.copyDir, testFiles)
			}
			got, err := next.Backup(t.Context())
			if err != nil || got.Revision != 1 {
				t.Errorf("Backup() after the failure = %+v, error = %v", got, err)
			}
			if tt.wantErr == nil {
				// Options are checked before operate, so a closed session does not replace their
				// error.
				s.Close()
				result, err := tt.args.run(tt.args.ctx, s)
				if err == nil || result || errors.Is(err, errSessionClosed) {
					t.Errorf("closed session: result = %v, error = %v, want an error "+
						"of the options", result, err)
				}
			}
		})
	}
}
