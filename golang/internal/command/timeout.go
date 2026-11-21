// Package command is documented in command.go.
package command

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/megalomania428/mega-doub/pkg/model"
)

// msgTimedOut is the msg of the record of a run that global.timeout has stopped.
const msgTimedOut = "timed out"

// timeLimit is global.timeout of a run: its effective value and the logger of the record
// of its expiry.
type timeLimit struct {
	timeout time.Duration
	logger  *slog.Logger
}

// errTimeout is the cause of the context of a run that the timeout has expired, unlike
// the expiry of the deadline of the parent context.
var errTimeout = errors.New("global.timeout expired")

// run runs work with a context of ctx that expires after the timeout. When that context
// has expired by the timeout before ctx is done, run logs the record timed out with the
// field timeout, the effective value such as 12h0m0s, and returns the exit code 4
// whatever work returns. Otherwise it returns the code of work. cli.Execute reports a
// canceled ctx with the record interrupted, so a cancellation after the expiry adds that
// record.
func (l timeLimit) run(ctx context.Context, work func(context.Context) int) int {
	workCtx, cancel := context.WithTimeoutCause(ctx, l.timeout, errTimeout)
	defer cancel()
	code := work(workCtx)
	// The context keeps its first cause, so a later cancellation of ctx does not hide it,
	// and the expiry of the deadline of ctx has another cause.
	if errors.Is(context.Cause(workCtx), errTimeout) {
		l.logger.Error(msgTimedOut, "timeout", l.timeout.String())
		return model.ExitInterrupted
	}
	return code
}
