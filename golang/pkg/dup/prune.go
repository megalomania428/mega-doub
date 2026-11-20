// cspell:ignore duplicacy

// Package dup is documented in log.go.
package dup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	duplicacy "duplicacy.invalid/v3/src"
)

// PruneOption is an option of Session.Prune.
type PruneOption func(*pruneOptions)

// pruneOptions are the settings of Session.Prune.
type pruneOptions struct {
	keep      []string
	exclusive bool
	threads   int
}

// WithKeep adds the retention policies n:m of the duplicacy CLI option -keep: keep a
// revision every n days for the revisions older than m days. They are required and none
// of them may be empty or blank. They are passed to duplicacy as they are, in the given
// order, and duplicacy does not sort them: as with the option -keep of the CLI, they must
// go by the age m from the oldest to the youngest, or a policy of a smaller age applies
// to the older revisions first and the policies after it are never used.
func WithKeep(policies ...string) PruneOption {
	policies = slices.Clone(policies)
	return func(o *pruneOptions) { o.keep = append(o.keep, policies...) }
}

// WithExclusive sets the exclusive mode of the prune: the chunks are removed at once,
// which is safe only when nothing else uses the storage. It is off without the option.
func WithExclusive(exclusive bool) PruneOption {
	return func(o *pruneOptions) { o.exclusive = exclusive }
}

// WithPruneThreads sets the number of the threads of the prune; it is the number of the
// threads of the storage of the session without the option, and a greater one is an
// error, because a storage with a client for every thread has no more of them.
func WithPruneThreads(threads int) PruneOption {
	return func(o *pruneOptions) { o.threads = threads }
}

// PruneResult is the result of a prune. The chunks are counted by the steps of the prune
// that changed them; a step that found its action already done is not counted.
type PruneResult struct {
	NothingToDelete bool          // no revision matched the retention policies
	Deleted         int64         // chunks and fossils deleted
	Fossilized      int64         // chunks turned into fossils
	Resurrected     int64         // fossils turned back into chunks
	Failed          int64         // chunks that failed to be deleted
	Duration        time.Duration // duration of the prune
}

// Prune deletes the revisions of the snapshot ID of the session by the retention
// policies; the revisions of other snapshot IDs are kept. A prune whose snapshot cache
// tree shares a directory with the tree of the local file storage, through any link or
// mount, is an error before duplicacy is called, because the clean-up of the cache and
// the fossils of the prune would change the archive. Backup, Prune and Close of a session
// must not run at the same time.
func (s *Session) Prune(ctx context.Context, opts ...PruneOption) (*PruneResult, error) {
	o := pruneOptions{threads: s.threads}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	switch {
	case len(o.keep) == 0:
		return nil, errors.New("prune keep is required")
	case slices.ContainsFunc(o.keep, func(p string) bool {
		return strings.TrimSpace(p) == ""
	}):
		return nil, errors.New("prune keep has an empty policy")
	case o.threads < 1:
		return nil, errors.New("prune threads must be at least 1")
	case o.threads > s.threads:
		return nil, fmt.Errorf("prune threads must be at most the storage threads %d",
			s.threads)
	}
	start := time.Now()
	c := newCollector()
	result := &PruneResult{}
	id := s.preference.SnapshotID
	err := s.operate(ctx, &work{op: opPrune, c: c,
		f: func(manager *duplicacy.BackupManager) error {
			result.NothingToDelete = !manager.SnapshotManager.PruneSnapshots(id, id, nil,
				nil, o.keep, false, o.exclusive, nil, false, false, false, o.threads)
			return nil
		}})
	if err != nil {
		return nil, err
	}
	result.Deleted = c.count(duplicacy.ProgressActionDelete)
	result.Fossilized = c.count(duplicacy.ProgressActionFossilize)
	result.Resurrected = c.count(duplicacy.ProgressActionResurrect)
	result.Failed = c.endEvent().Failed
	result.Duration = time.Since(start)
	return result, nil
}
