// cspell:ignore doub

// Package main starts the backup utility.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/megalomania428/mega-doub/internal/cli"
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
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	build := cli.Build{Version: version, Commit: commit}
	return cli.Execute(ctx, args, stdout, stderr, build, notImplemented)
}

// notImplemented logs the parsed invocation of a command that has no implementation yet.
func notImplemented(_ context.Context, inv cli.Invocation, env cli.Env) int {
	attrs := []any{"command", inv.Command, "config", inv.Global.Config,
		"dry_run", inv.Global.DryRun}
	switch inv.Command {
	case "check":
		attrs = append(attrs, "files", inv.Check.Files, "chunks", inv.Check.Chunks)
	case "verify":
		attrs = append(attrs, "revision", inv.Verify.Revision)
		if inv.Verify.BytesSet {
			attrs = append(attrs, "bytes", inv.Verify.Bytes)
		}
	case "list":
		attrs = append(attrs, "revision", inv.List.Revision)
	}
	env.Logger.Info("command", attrs...)
	env.Logger.Error("command is not implemented yet", "command", inv.Command)
	return model.ExitStorage
}
