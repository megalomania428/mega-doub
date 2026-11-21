// cspell:ignore duplicacy

// Package dup is documented in log.go.
package dup

import (
	"context"
	"errors"
	"sync"

	duplicacy "duplicacy.invalid/v3/src"
	ci "github.com/megalomania428/go-lib-ci"
)

// progressElements are the elements of the progress bar of go-lib-ci by the names of
// global.progress.
var progressElements = map[string]ci.ProgressElement{
	"bytes":   ci.ProgressElementBytes,
	"elapsed": ci.ProgressElementElapsed,
	"rate":    ci.ProgressElementSpeed,
	"bar":     ci.ProgressElementBar,
	"percent": ci.ProgressElementPercent,
	"eta":     ci.ProgressElementETA,
}

// progressOptions are added to the options of every progress bar; tests replace it.
var progressOptions []ci.ProgressOption

// errBackupInterrupted finishes the progress bar of a backup that has not reached its
// event of the end, as after an error or a panic of duplicacy.
var errBackupInterrupted = errors.New("backup is interrupted")

// backupProgress is the progress bar of one backup, drawn from the events of duplicacy.
type backupProgress struct {
	// start creates the bar with the total of the bytes.
	start func(total int64) ci.ProgressBar
	mu    sync.Mutex
	// bar is the bar from the event of the start until it is finished.
	bar ci.ProgressBar
}

// newBackupProgress returns the progress bar of a backup with ctx, or nil when the
// session has no elements of the bar.
func (s *Session) newBackupProgress(ctx context.Context) *backupProgress {
	if len(s.progress) == 0 {
		return nil
	}
	w, elements := s.progressWriter, s.progress
	return &backupProgress{start: func(total int64) ci.ProgressBar {
		opts := []ci.ProgressOption{
			ci.WithProgressTotal(total),
			ci.WithProgressReverse(true),
			ci.WithProgressElements(elements...),
			ci.WithProgressStderr(w),
		}
		return ci.NewProgress(ctx, append(opts, progressOptions...)...)
	}}
}

// event is the next of the collector of the backup: the start creates the bar with
// TotalBytes, a step adds its Bytes and the end finishes the bar. duplicacy calls it
// from its workers at the same time. A panic of the start, as go-lib-ci raises when
// mpb adds a bar to an already cancelled context, goes on to the caller with the
// mutex released, so the deferred finish of Backup does not wait for it forever.
func (p *backupProgress) event(event duplicacy.ProgressEvent) {
	switch event.Kind {
	case duplicacy.ProgressKindStart:
		p.create(event.TotalBytes)
	case duplicacy.ProgressKindStep:
		p.mu.Lock()
		bar := p.bar
		p.mu.Unlock()
		if bar != nil {
			bar.AddBytes(event.Bytes)
		}
	case duplicacy.ProgressKindEnd:
		p.finish(nil)
	}
}

// create makes the bar with total unless it exists; the mutex is released by a defer
// to survive a panic of start.
func (p *backupProgress) create(total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bar == nil {
		p.bar = p.start(total)
	}
}

// finish finishes the bar with err; a bar that is not created or is finished already
// is left as is.
func (p *backupProgress) finish(err error) {
	p.mu.Lock()
	bar := p.bar
	p.bar = nil
	p.mu.Unlock()
	if bar != nil {
		bar.Finish(err)
	}
}
