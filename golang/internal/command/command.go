// cspell:ignore doub

// Package command runs the commands of the backup utility.
package command

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"

	"github.com/megalomania428/mega-doub/internal/cli"
	"github.com/megalomania428/mega-doub/pkg/config"
	"github.com/megalomania428/mega-doub/pkg/logging"
	"github.com/megalomania428/mega-doub/pkg/model"
	"github.com/megalomania428/mega-doub/pkg/secret"
)

// Deps holds the dependencies of the commands. They are given explicitly, so the tests
// and the implementations of the commands substitute their own.
type Deps struct {
	// NewResolver returns the secret resolver of one run, secret.New when it is nil.
	NewResolver func() secret.Resolver
}

// New returns the handler of all commands with the given dependencies.
func New(deps Deps) cli.Handler {
	if deps.NewResolver == nil {
		deps.NewResolver = secret.New
	}
	return func(ctx context.Context, inv cli.Invocation, env cli.Env) int {
		return handle(ctx, deps, inv, env)
	}
}

// handle loads and validates the configuration, configures the logger by it, resolves
// the secrets and runs the command. config show does not resolve the secrets, config
// validate does it only with --resolve. Interruption is reported by cli.Execute, the
// handler keeps its code.
func handle(ctx context.Context, deps Deps, inv cli.Invocation, env cli.Env) int {
	loaded, err := config.Load(inv.Global.Config)
	if err != nil {
		logProblems(env.Logger, err)
		return model.ExitConfig
	}
	// An explicit flag wins over the configuration, which wins over the default.
	if !inv.Global.LogLevelSet && loaded.HasValue("global.log_level") {
		inv.Global.LogLevel = loaded.Config.Global.LogLevel.Level()
	}
	if !inv.Global.LogFormatSet && loaded.HasValue("global.log_format") {
		inv.Global.LogFormat = loaded.Config.Global.LogFormat.Format()
	}
	// The logger is replaced in place, so the records that cli.Execute writes after the
	// handler, such as interrupted, follow the same settings.
	*env.Logger = *logging.New(env.Stderr, inv.Global.LogLevel, inv.Global.LogFormat)
	// The records carry the absolute path of the configuration.
	inv.Global.Config = loaded.Path
	result := config.Validate(loaded)
	logResult(env.Logger, result)
	if len(result.Problems) > 0 {
		return model.ExitConfig
	}
	if inv.Command == "config show" {
		return show(env, loaded.Config)
	}
	validate := inv.Command == "config validate"
	if !validate || inv.ConfigValidate.Resolve {
		if code := resolve(ctx, deps.NewResolver(), loaded, env.Logger); code != model.ExitOK {
			return code
		}
	}
	if validate {
		env.Logger.Info("configuration is valid", "config", inv.Global.Config)
		return model.ExitOK
	}
	return stub(inv, env, loaded.Config)
}

// failure is a secret reference that failed to resolve, line is zero without a position.
type failure struct {
	path, kind   string
	line, column int
	err          error
}

// resolve resolves the secret references of the configuration one by one in the order of
// config.Refs and logs every failure, or the count of the references on success. The
// values stay in the cache of the resolver. A canceled context stops the loop: the
// failures before it are logged, cli.Execute reports the interruption. A cancellation
// during the last reference that resolves is an interruption too, so the command does
// not run.
func resolve(ctx context.Context, r secret.Resolver, loaded *config.Loaded,
	logger *slog.Logger) int {
	refs := config.Refs(loaded.Config)
	var failures []failure
	for _, ref := range refs {
		if ctx.Err() != nil {
			break
		}
		_, err := r.Resolve(ctx, ref.Ref)
		if err == nil {
			continue
		}
		// A failure caused by the cancellation is not logged, another one is logged even
		// when the cancellation comes at the same time.
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			break
		}
		// The syntax is valid after config.Validate, the resolver reports a failure itself.
		parsed, _ := secret.Parse(ref.Ref)
		f := failure{path: ref.Path, kind: string(parsed.Kind), err: err}
		if line, column, ok := loaded.ValuePos(ref.Path); ok {
			f.line, f.column = line, column
		}
		failures = append(failures, f)
	}
	sortFailures(failures)
	for _, f := range failures {
		attrs := []any{"path", f.path, "kind", f.kind, "file", loaded.Path}
		if f.line > 0 {
			attrs = append(attrs, "line", f.line, "column", f.column)
		}
		logger.Error("secret resolution failed", append(attrs, "error", f.err.Error())...)
	}
	switch {
	case len(failures) > 0:
		return model.ExitConfig
	case ctx.Err() != nil:
		return model.ExitInterrupted
	}
	logger.Info("secrets resolved", "count", len(refs))
	return model.ExitOK
}

// sortFailures sorts the failures by the position of the value in place: the failures
// without a position go last, an equal position keeps the order of the references.
func sortFailures(failures []failure) {
	slices.SortStableFunc(failures, func(a, b failure) int {
		if (a.line == 0) != (b.line == 0) {
			return cmp.Compare(b.line, a.line)
		}
		return cmp.Or(cmp.Compare(a.line, b.line), cmp.Compare(a.column, b.column))
	})
}

// stub logs the invocation of a command that has no implementation yet with the
// effective values of its flags and prints the safe configuration; a dry run succeeds
// and a real run reports that the command is not implemented.
func stub(inv cli.Invocation, env cli.Env, c *config.Config) int {
	noProgress := c.Global.Progress == "never"
	if inv.Global.NoProgressSet {
		noProgress = inv.Global.NoProgress
	}
	attrs := []any{"command", inv.Command, "config", inv.Global.Config, "dry_run",
		inv.Global.DryRun, "log_level", strings.ToLower(inv.Global.LogLevel.String()),
		"log_format", string(inv.Global.LogFormat), "no_progress", noProgress}
	switch inv.Command {
	case "prune":
		attrs = append(attrs, "enabled", c.Prune.Enabled)
	case "check":
		files, chunks := c.Check.Files, c.Check.Chunks
		if inv.Check.FilesSet {
			files = inv.Check.Files
		}
		if inv.Check.ChunksSet {
			chunks = inv.Check.Chunks
		}
		attrs = append(attrs, "files", files, "chunks", chunks, "enabled", c.Check.Enabled)
	case "verify":
		bytes := int64(c.Check.TestRestoreBytes)
		if inv.Verify.BytesSet {
			bytes = inv.Verify.Bytes
		}
		attrs = append(attrs, "revision", inv.Verify.Revision, "bytes", bytes)
	case "list":
		attrs = append(attrs, "revision", inv.List.Revision)
	}
	env.Logger.Info("command", attrs...)
	if code := show(env, c); code != model.ExitOK {
		return code
	}
	if inv.Global.DryRun {
		env.Logger.Info("dry run, nothing to do", "command", inv.Command)
		return model.ExitOK
	}
	env.Logger.Error("command is not implemented yet", "command", inv.Command)
	return model.ExitStorage
}

// show writes the safe form of the configuration to stdout, a failure is logged and
// gives the exit code 2.
func show(env cli.Env, c *config.Config) int {
	out, err := config.Show(c)
	if err == nil {
		_, err = env.Stdout.Write(out)
	}
	if err != nil {
		env.Logger.Error("output write failed", "error", err.Error())
		return model.ExitConfig
	}
	return model.ExitOK
}

// logProblems writes one record per configuration problem.
func logProblems(logger *slog.Logger, err error) {
	var cfgErr *config.Error
	if !errors.As(err, &cfgErr) {
		logger.Error("invalid configuration", "error", err.Error())
		return
	}
	for _, p := range cfgErr.Problems {
		logger.Error("invalid configuration", problemAttrs(p, "error")...)
	}
}

// logResult writes one record per warning of the validation and then one record per
// problem.
func logResult(logger *slog.Logger, result config.Result) {
	for _, w := range result.Warnings {
		logger.Warn("configuration warning", problemAttrs(w, "warning")...)
	}
	for _, p := range result.Problems {
		logger.Error("invalid configuration", problemAttrs(p, "error")...)
	}
}

// problemAttrs returns the fields of the record of a problem with its message in the
// field key, a field without a value is left out.
func problemAttrs(p config.Problem, key string) []any {
	var attrs []any
	if p.File != "" {
		attrs = append(attrs, "file", p.File)
	}
	if p.Line > 0 {
		attrs = append(attrs, "line", p.Line, "column", p.Column)
	}
	if p.Path != "" {
		attrs = append(attrs, "path", p.Path)
	}
	return append(attrs, key, p.Message)
}
