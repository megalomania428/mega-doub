// cspell:ignore duplicacy

// Package command is documented in command.go.
package command

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/megalomania428/mega-doub/pkg/config"
	"github.com/megalomania428/mega-doub/pkg/dup"
	"github.com/megalomania428/mega-doub/pkg/lock"
	"github.com/megalomania428/mega-doub/pkg/model"
	"github.com/megalomania428/mega-doub/pkg/secret"
)

// The msg of the records of a run with the storage.
const (
	msgLockBusy           = "another run is in progress"
	msgLockFailed         = "lock failed"
	msgLockReleaseFailed  = "lock release failed"
	msgOperationFailed    = "storage operation failed"
	msgInitialized        = "storage initialized"
	msgAlreadyInitialized = "storage already initialized"
)

// operationInit is the operation of the command init in the record storage operation
// failed.
const operationInit = "init"

// openSession opens the session of a run, a variable of the package for its tests.
var openSession = dup.Open

// storageRun is a run of a command with the storage of the configuration: it takes the
// run lock of global.state_dir, opens the session of duplicacy and runs the work of the
// command with the session. resolver is the resolver of the run with the secrets of the
// configuration in its cache, operation names the opening of the session in the record
// storage operation failed, and options follow the options of the configuration, such as
// dup.WithInit.
type storageRun struct {
	logger    *slog.Logger
	loaded    *config.Loaded
	resolver  secret.Resolver
	operation string
	options   []dup.SessionOption
}

// stepFailure is a failed step of a run with the storage: the operation of the record,
// the error and the open session of the step, nil for the opening of the session; the
// secrets of the session are replaced with *** in the text of the error.
type stepFailure struct {
	operation string
	err       error
	session   *dup.Session
}

// run runs work with the open session of the storage under the run lock and returns the
// code of work. The secrets of the storage come again from the cache of the resolver,
// then the lock is taken before the session opens: a lock held by another run is the
// record another run is in progress with the fields lock and pid, any other failure to
// take it is the record lock failed with the fields lock and error, both with the exit
// code 3. A failure to open the session is reported by failed with the operation of the
// run. The session is closed and then the lock is released on every return after the
// lock is taken; a failure to release it is the warning lock release failed with the
// fields lock and error and keeps the code.
func (sr storageRun) run(
	ctx context.Context, work func(context.Context, *dup.Session) int,
) int {
	opts, err := sr.sessionOptions(ctx)
	if err != nil {
		return sr.failed(ctx, stepFailure{operation: sr.operation, err: err})
	}
	stateDir := sr.loaded.Config.Global.StateDir
	l, err := lock.Acquire(stateDir)
	if err != nil {
		if busy, ok := errors.AsType[*lock.BusyError](err); ok {
			sr.logger.Error(msgLockBusy, "lock", busy.Path, "pid", busy.PID)
		} else {
			sr.logger.Error(msgLockFailed, "lock", lock.Path(stateDir), "error", err.Error())
		}
		return model.ExitStorage
	}
	defer sr.release(l)
	session, err := openSession(ctx, opts...)
	if err != nil {
		return sr.failed(ctx, stepFailure{operation: sr.operation, err: err})
	}
	defer session.Close()
	return work(ctx, session)
}

// sessionOptions returns the options of the session: the directories of global and the
// settings of storage, the password and the keys with their values from the resolver,
// the logger, the retries of global.retry with the overrides from the environment, and
// then the options of the run. The values come from the cache of the resolver, so only a
// done ctx fails them; a key with an empty reference is left out, as config.Refs leaves
// it out.
func (sr storageRun) sessionOptions(ctx context.Context) ([]dup.SessionOption, error) {
	c := sr.loaded.Config
	password, err := sr.resolver.Resolve(ctx, c.Storage.Password)
	if err != nil {
		return nil, fmt.Errorf("resolve storage.password: %w", err)
	}
	keys := make(map[string]string, len(c.Storage.Keys))
	for name, ref := range c.Storage.Keys {
		if ref == "" {
			continue
		}
		value, err := sr.resolver.Resolve(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("resolve storage.keys.%s: %w", name, err)
		}
		keys[name] = value.Reveal()
	}
	opts := []dup.SessionOption{
		dup.WithCopyDir(c.Global.CopyDir),
		dup.WithStateDir(c.Global.StateDir),
		dup.WithStorageName(c.Storage.Name),
		dup.WithURL(c.Storage.URL),
		dup.WithSnapshotID(c.Storage.SnapshotID),
		dup.WithPassword(password.Reveal()),
		dup.WithKeys(keys),
		dup.WithLogger(sr.logger),
		dup.WithRetry(sr.loaded.Retry),
	}
	return append(opts, sr.options...), nil
}

// failed reports the failure f of a step and returns the exit code. An error for which
// errors.Is tells context.Canceled or context.DeadlineExceeded with ctx done gives the
// exit code 4 without a record: timeLimit.run or cli.Execute report the stop. Any other
// error, also one that tells context.DeadlineExceeded while ctx is not done, such as a
// timeout of one attempt, is the record storage operation failed with the fields
// operation, id, the log ID of duplicacy when the error holds a *dup.Error, and error,
// the full text of the error with the secrets replaced with ***, and the exit code 3.
func (sr storageRun) failed(ctx context.Context, f stepFailure) int {
	if ctx.Err() != nil && (errors.Is(f.err, context.Canceled) ||
		errors.Is(f.err, context.DeadlineExceeded)) {
		return model.ExitInterrupted
	}
	text := dup.ErrorText(f.err)
	if f.session != nil {
		text = f.session.ErrorText(f.err)
	}
	attrs := []any{"operation", f.operation}
	if e, ok := errors.AsType[*dup.Error](f.err); ok {
		attrs = append(attrs, "id", e.LogID)
	}
	sr.logger.Error(msgOperationFailed, append(attrs, "error", text)...)
	return model.ExitStorage
}

// release releases the run lock l; a failure is the warning lock release failed.
func (sr storageRun) release(l *lock.Lock) {
	if err := l.Release(); err != nil {
		sr.logger.Warn(msgLockReleaseFailed, "lock",
			lock.Path(sr.loaded.Config.Global.StateDir), "error", err.Error())
	}
}

// initialized is the work of the command init with the open session: the record storage
// initialized when this opening has created the config of the storage, the record
// storage already initialized otherwise, both with the fields instance and storage, the
// URL of the storage without the password of its user information, and the exit code 0.
func (sr storageRun) initialized(_ context.Context, session *dup.Session) int {
	msg := msgAlreadyInitialized
	if session.Created() {
		msg = msgInitialized
	}
	c := sr.loaded.Config
	sr.logger.Info(msg, "instance", c.Global.Instance, "storage",
		config.Redact(c).Storage.URL)
	return model.ExitOK
}
