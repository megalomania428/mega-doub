// cspell:ignore doub

// Package main starts the backup utility.
package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/megalomania428/mega-doub/internal/project"
	"github.com/megalomania428/mega-doub/pkg/logging"
	"github.com/megalomania428/mega-doub/pkg/model"
)

// Build metadata, overridden at link time with -X main.version and -X main.commit.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run executes one invocation and returns the process exit code.
// Arguments and stdout are not used yet.
func run(ctx context.Context, _ []string, _, stderr io.Writer) int {
	logger := logging.New(stderr, slog.LevelInfo, logging.FormatText)
	logger.Info(project.Name+" started", "version", version, "commit", commit)
	if ctx.Err() != nil {
		return model.ExitInterrupted
	}
	return model.ExitOK
}
