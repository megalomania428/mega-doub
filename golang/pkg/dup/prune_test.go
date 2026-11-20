// cspell:ignore duplicacy

package dup

import (
	"context"
	"errors"
	"testing"
)

func TestSession_Prune(t *testing.T) {
	type args struct {
		ctx  context.Context
		opts []PruneOption
	}
	tests := []struct {
		name string
		// backups is the number of the revisions created before the prune.
		backups int
		// threads are the threads of the storage of the session; 0 – the one of the tests.
		threads   int
		args      args
		want      *PruneResult
		wantLogID string
	}{
		{
			name:    "one revision",
			backups: 1,
			args:    args{ctx: t.Context(), opts: []PruneOption{WithKeep("0:1")}},
			want:    &PruneResult{NothingToDelete: true},
		},
		{
			name:      "invalid keep",
			backups:   1,
			args:      args{ctx: t.Context(), opts: []PruneOption{WithKeep("1:0")}},
			wantLogID: "RETENTION_INVALID",
		},
		{
			// The age of a revision is counted in calendar days, so the ages of fresh revisions
			// are 0 or 1 around a midnight; a policy of 3650 days is older than them at any
			// time and zone, so nothing is deleted: the deletion by the age needs a replaced
			// clock.
			name:    "exclusive on several revisions",
			backups: 2,
			threads: 2,
			args: args{ctx: t.Context(), opts: []PruneOption{
				WithKeep("0:3650"), WithExclusive(true), WithPruneThreads(2),
			}},
			want: &PruneResult{NothingToDelete: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := countStorage(t)
			var extra []SessionOption
			storageThreads := 1
			if tt.threads > 0 {
				extra = append(extra, WithThreads(tt.threads))
				storageThreads = tt.threads
			}
			s := openBackupTest(t, extra...)
			defer func() {
				// A backend with a client for every thread has no client above them.
				if got := storage.maxThread.Load(); got >= int64(storageThreads) {
					t.Errorf("thread index = %d, want below the storage threads %d", got,
						storageThreads)
				}
			}()
			writeCopy(t, s.copyDir, testFiles)
			for range tt.backups {
				if _, err := s.Backup(t.Context()); err != nil {
					t.Fatalf("Backup() error = %v", err)
				}
			}
			got, err := s.Prune(tt.args.ctx, tt.args.opts...)
			if tt.wantLogID != "" {
				if e, ok := errors.AsType[*Error](err); !ok || e.LogID != tt.wantLogID {
					t.Fatalf("Prune() error = %v, want the log ID %s", err, tt.wantLogID)
				}
				if got != nil {
					t.Errorf("Prune() = %+v, want nil", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Prune() error = %v", err)
			}
			if got.Duration <= 0 {
				t.Errorf("Prune() duration = %v, want a positive one", got.Duration)
			}
			got.Duration = 0
			if *got != *tt.want {
				t.Errorf("Prune() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
