// cspell:ignore doub

// Package main starts the backup utility.
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/megalomania428/mega-doub/internal/cli"
	"github.com/megalomania428/mega-doub/pkg/config"
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
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	build := cli.Build{Version: version, Commit: commit}
	return cli.Execute(ctx, args, stdout, stderr, build, handle)
}

// handle loads the configuration, configures the logger by it and runs the command.
func handle(ctx context.Context, inv cli.Invocation, env cli.Env) int {
	loaded, err := config.Load(inv.Global.Config)
	if err != nil {
		logProblems(env.Logger, err)
		return model.ExitConfig
	}
	// An explicit flag wins over the configuration, which wins over the default.
	level, format := inv.Global.LogLevel, inv.Global.LogFormat
	if !inv.Global.LogLevelSet && loaded.LogLevelSet {
		level = loaded.Config.Global.LogLevel.Level()
	}
	if !inv.Global.LogFormatSet && loaded.LogFormatSet {
		format = loaded.Config.Global.LogFormat.Format()
	}
	// The logger is replaced in place, so the records that cli.Execute writes after the
	// handler, such as interrupted, follow the same settings.
	*env.Logger = *logging.New(env.Stderr, level, format)
	// The records carry the absolute path of the configuration.
	inv.Global.Config = loaded.Path
	if inv.Command == "config validate" {
		env.Logger.Info("configuration is valid", "config", inv.Global.Config)
		return model.ExitOK
	}
	return notImplemented(ctx, inv, env)
}

// logProblems writes one record per configuration problem.
func logProblems(logger *slog.Logger, err error) {
	var cfgErr *config.Error
	if !errors.As(err, &cfgErr) {
		logger.Error("invalid configuration", "error", err.Error())
		return
	}
	for _, p := range cfgErr.Problems {
		attrs := []any{"file", p.File}
		if p.Line > 0 {
			attrs = append(attrs, "line", p.Line, "column", p.Column)
		}
		if p.Path != "" {
			attrs = append(attrs, "path", p.Path)
		}
		attrs = append(attrs, "error", p.Message)
		logger.Error("invalid configuration", attrs...)
	}
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
