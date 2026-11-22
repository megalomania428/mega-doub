// cspell:ignore doub duplicacy

// Package command is documented in command.go.
package command

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/megalomania428/mega-doub/pkg/config"
	"github.com/megalomania428/mega-doub/pkg/dup"
	"github.com/megalomania428/mega-doub/pkg/model"
)

// The msg of the records of backup and prune.
const (
	msgNotInitialized  = "storage is not initialized"
	msgPartialRemoved  = "partial files removed"
	msgBackupCompleted = "backup completed"
	msgPruneCompleted  = "prune completed"
)

// The operations of backup and prune in the record storage operation failed.
const (
	operationOpen   = "open"
	operationBackup = "backup"
	operationPrune  = "prune"
)

// hintInit is the field hint of the record storage is not initialized.
const hintInit = "run 'mega-doub init' first"

// partialSuffix ends the names of the incomplete files of the sources in the copy
// directory.
const partialSuffix = ".part"

// runCommand runs the command name, init, backup or prune, with the storage and returns
// its exit code. init opens the session with dup.WithInit and storage.threads threads.
// backup and prune open it with the operation open and the larger of storage.threads
// and, when the run prunes, prune.threads threads: prune always, backup with
// prune.enabled. backup draws the bar to sr.progress when it is set and prepares the
// copy directory under the lock before the opening; prune draws no bar.
func (sr storageRun) runCommand(ctx context.Context, name string) int {
	c := sr.loaded.Config
	threads := c.Storage.Threads
	if name == "init" {
		sr.operation = operationInit
		sr.options = []dup.SessionOption{dup.WithInit(), dup.WithThreads(threads)}
		return sr.run(ctx, sr.initialized)
	}
	sr.operation = operationOpen
	if name == "prune" || c.Prune.Enabled {
		threads = max(threads, c.Prune.Threads)
	}
	sr.options = []dup.SessionOption{dup.WithThreads(threads)}
	if name == "prune" {
		return sr.run(ctx, sr.prune)
	}
	if sr.progress != nil {
		sr.options = append(sr.options, withProgress(sr.progress, c.Global.Progress...))
	}
	sr.prepare = sr.prepareCopy
	return sr.run(ctx, sr.backup)
}

// prepareCopy prepares global.copy_dir for the backup with partialFiles and returns the
// exit code, 0 when the directory is ready. The record partial files removed with the
// field count follows any removal, also one before a failure or a done ctx; the failure
// is reported by failed with the operation backup, a done ctx gives the exit code 4
// without a record.
func (sr storageRun) prepareCopy(ctx context.Context) int {
	p := partialFiles{dir: sr.loaded.Config.Global.CopyDir}
	err := p.remove(ctx)
	if p.count > 0 {
		sr.logger.Info(msgPartialRemoved, "count", p.count)
	}
	if err != nil {
		return sr.failed(ctx, stepFailure{operation: operationBackup,
			err: fmt.Errorf("prepare copy directory: %w", err)})
	}
	return model.ExitOK
}

// partialFiles are the incomplete files of the sources in the copy directory dir and the
// number of the removed ones. afterCheck is the test seam, empty in the product: when it
// is set, it is called with the operation and the entry after the check of the entry and
// before the operation on it, see the constants of the operations.
type partialFiles struct {
	dir        string
	count      int
	afterCheck func(op, rel string)
}

// The operations of afterCheck: the Lstat of an entry, the check after its failure, the
// read of a subdirectory and the removal of a file.
const (
	opLstat   = "lstat"
	opRecheck = "recheck"
	opRead    = "read"
	opRemove  = "remove"
)

// checked is intact followed by the call of afterCheck for the operation op.
func (p *partialFiles) checked(root *os.Root, rel, op string) (bool, error) {
	ok, err := intact(root, rel)
	if ok && err == nil && p.afterCheck != nil {
		p.afterCheck(op, rel)
	}
	return ok, err
}

// remove creates the directory and its missing parents with the mode 0700 when it is
// missing, an existing one keeps its mode, and removes every entry in it and in its
// subdirectories that is not a directory and whose name ends with .part, the letter case
// kept. The directory is made absolute and cleaned lexically first, as duplicacy does
// for the repository, so that a .. after a link does not move the walk to another
// directory. The directory itself may be a link to a directory, which duplicacy follows
// too. The walk and the removals go through an os.Root of the resolved directory, which
// stays the guard for the window between the check of the path of an entry and the
// operation on it.
// The walk does not use root.FS(), which rejects names that are not valid UTF-8, so a
// directory with such a name is walked too. The links inside the directory are never
// followed: a link with such a name is removed itself and its target stays, also one
// that replaced a subdirectory after its parent was read, and a directory with such a
// name stays, even one that replaced a file after its parent was read. An entry that is
// gone, also with its ancestor that was replaced with a file or a link after the parent
// was read, see visit, does not stop the walk and is not counted. ctx is checked before
// every entry; the first failure or the error of a done ctx stops the walk.
func (p *partialFiles) remove(ctx context.Context) error {
	dir, err := filepath.Abs(p.dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return p.visit(ctx, root, ".", true)
}

// visit handles the entry rel of root: a directory is read and its entries are visited in
// the lexical order, any other entry is removed when its name ends with .part. Before
// every operation on an entry that is not the root, see intact, the path of the entry is
// checked: when an ancestor is not a directory any more the entry is gone with it and is
// skipped without an error and without the count, a link that replaced an ancestor is
// never followed. The type of a subdirectory is checked with root.Lstat right before it
// is read: a directory is read, a link or any other entry that is not a directory is
// handled as any other entry and never followed. After a failed read of a subdirectory,
// see settle, the entry is classified again: an entry that is gone is skipped, one that
// is not a directory, even a subdirectory that became a file, is handled as any other
// entry, and a subdirectory that is still a directory returns the read failure, as does
// any failure on the root.
func (p *partialFiles) visit(ctx context.Context, root *os.Root, rel string,
	isDir bool,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !isDir {
		return p.removeFile(root, rel)
	}
	if rel != "." {
		info, err := p.lstatEntry(root, rel)
		switch {
		case err != nil:
			return err
		case info == nil:
			return nil
		case !info.IsDir():
			return p.removeFile(root, rel)
		}
		if p.afterCheck != nil {
			p.afterCheck(opRead, rel)
		}
	}
	entries, err := readEntries(root, rel)
	if err != nil {
		if rel == "." {
			return err
		}
		info, settleErr := settle(root, rel, err)
		switch {
		case settleErr != nil:
			return settleErr
		case info == nil:
			return nil
		case info.IsDir():
			return err
		}
		return p.removeFile(root, rel)
	}
	for _, entry := range entries {
		if err := p.visit(ctx, root, filepath.Join(rel, entry.Name()),
			entry.IsDir()); err != nil {
			return err
		}
	}
	return nil
}

// intact reports whether every ancestor of rel inside root, from the root down, is a
// directory that exists: an ancestor that is gone, a file or a link means that the entry
// is not at this path any more. Every ancestor is checked with root.Lstat only after the
// ones above it passed, so no check goes through a link. os.Root has no operation that
// does not follow a link in the middle of a path, so a change between this check and the
// operation after it is the window that stays, the bound of os.Root.
func intact(root *os.Root, rel string) (bool, error) {
	parent := filepath.Dir(rel)
	if parent == "." {
		return true, nil
	}
	path := ""
	for part := range strings.SplitSeq(parent, string(filepath.Separator)) {
		path = filepath.Join(path, part)
		info, err := root.Lstat(path)
		switch {
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
			return false, nil
		case err != nil:
			return false, err
		case !info.IsDir():
			return false, nil
		}
	}
	return true, nil
}

// lstatEntry returns the root.Lstat of the entry rel that is not the root. It returns
// nil without an error when the entry is gone: an ancestor is not a directory any more,
// see intact, or the entry does not exist. A failed Lstat of an entry that is still there
// is returned as it is, also when the same Lstat would succeed now.
func (p *partialFiles) lstatEntry(root *os.Root, rel string) (fs.FileInfo, error) {
	ok, err := p.checked(root, rel, opLstat)
	switch {
	case err != nil:
		return nil, err
	case !ok:
		return nil, nil
	}
	info, err := root.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if ok, checkErr := p.checked(root, rel, opRecheck); checkErr == nil && !ok {
			return nil, nil
		}
		return nil, err
	}
	return info, nil
}

// settle classifies the entry rel that is not the root after its operation failed with
// opErr. It returns nil without an error when the entry is gone: an ancestor is not a
// directory any more, see intact, or the entry does not exist. It returns the Lstat of
// the entry when it exists and opErr says nothing more, and opErr otherwise.
func settle(root *os.Root, rel string, opErr error) (fs.FileInfo, error) {
	ok, err := intact(root, rel)
	switch {
	case err != nil:
		return nil, opErr
	case !ok:
		return nil, nil
	}
	info, err := root.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, opErr
	}
	return info, nil
}

// readEntries returns the entries of the directory rel of root sorted by name and closes
// the directory before it returns.
func readEntries(root *os.Root, rel string) ([]os.DirEntry, error) {
	dir, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	entries, err := dir.ReadDir(-1)
	if closeErr := dir.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return entries, nil
}

// removeFile removes the entry rel of root when its name ends with .part and it is not a
// directory right now, and counts it; a link is removed itself. An entry that is gone,
// see intact, lstatEntry and settle, is skipped without an error and without the count.
func (p *partialFiles) removeFile(root *os.Root, rel string) error {
	if !strings.HasSuffix(rel, partialSuffix) {
		return nil
	}
	info, err := p.lstatEntry(root, rel)
	switch {
	case err != nil:
		return err
	case info == nil, info.IsDir():
		return nil
	}
	if p.afterCheck != nil {
		p.afterCheck(opRemove, rel)
	}
	if err := root.Remove(rel); err != nil {
		if info, settled := settle(root, rel, err); info == nil && settled == nil {
			return nil
		}
		return err
	}
	p.count++
	return nil
}

// backup is the work of the command backup with the open session: it backs up the copy
// directory with the tag storage.tag and storage.threads threads, logs the record backup
// completed and then prunes when prune.enabled is set. A failure is reported by failed
// with the operation backup. A backup that returns its result after ctx is done logs its
// record and returns the exit code 4 without the prune.
func (sr storageRun) backup(ctx context.Context, session *dup.Session) int {
	c := sr.loaded.Config
	result, err := session.Backup(ctx, dup.WithTag(c.Storage.Tag),
		dup.WithBackupThreads(c.Storage.Threads))
	if err != nil {
		return sr.failed(ctx, stepFailure{operation: operationBackup, err: err,
			session: session})
	}
	sr.logger.Info(msgBackupCompleted, append(sr.storageAttrs(),
		"revision", result.Revision, "files", result.Files, "bytes", result.Bytes,
		"new_chunks", result.NewChunks, "new_bytes", result.NewBytes,
		"uploaded_bytes", result.UploadedBytes, "skipped", result.Skipped,
		"duration", result.Duration.String())...)
	switch {
	case ctx.Err() != nil:
		return model.ExitInterrupted
	case !c.Prune.Enabled:
		return model.ExitOK
	}
	return sr.prune(ctx, session)
}

// prune is the work of the command prune with the open session and the prune of backup:
// it prunes the revisions of storage.snapshot_id by prune.keep with prune.exclusive and
// prune.threads threads and logs the record prune completed. A failure is reported by
// failed with the operation prune. A prune that returns its result after ctx is done
// logs its record and returns the exit code 4.
func (sr storageRun) prune(ctx context.Context, session *dup.Session) int {
	p := sr.loaded.Config.Prune
	result, err := session.Prune(ctx, dup.WithKeep(p.Keep...),
		dup.WithExclusive(p.Exclusive), dup.WithPruneThreads(p.Threads))
	if err != nil {
		return sr.failed(ctx, stepFailure{operation: operationPrune, err: err,
			session: session})
	}
	sr.logger.Info(msgPruneCompleted, append(sr.storageAttrs(),
		"nothing_to_delete", result.NothingToDelete, "deleted", result.Deleted,
		"fossilized", result.Fossilized, "resurrected", result.Resurrected,
		"failed", result.Failed, "duration", result.Duration.String())...)
	if ctx.Err() != nil {
		return model.ExitInterrupted
	}
	return model.ExitOK
}

// storageAttrs returns the first fields of the records backup completed and prune
// completed: instance, storage, the URL of the storage without the password of its user
// information, and snapshot_id.
func (sr storageRun) storageAttrs() []any {
	c := sr.loaded.Config
	return []any{"instance", c.Global.Instance, "storage", config.Redact(c).Storage.URL,
		"snapshot_id", c.Storage.SnapshotID}
}

// notInitialized logs the record storage is not initialized with the fields instance,
// storage, the URL of the storage without the password of its user information, and
// hint, and returns the exit code 3.
func (sr storageRun) notInitialized() int {
	c := sr.loaded.Config
	sr.logger.Error(msgNotInitialized, "instance", c.Global.Instance, "storage",
		config.Redact(c).Storage.URL, "hint", hintInit)
	return model.ExitStorage
}
