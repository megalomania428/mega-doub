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
	"github.com/megalomania428/mega-doub/internal/command"
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
	return cli.Execute(ctx, args, stdout, stderr, build, command.New(command.Deps{}))
}
