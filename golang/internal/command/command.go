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
	"time"

	"github.com/megalomania428/mega-doub/internal/cli"
	"github.com/megalomania428/mega-doub/pkg/config"
	"github.com/megalomania428/mega-doub/pkg/logging"
	"github.com/megalomania428/mega-doub/pkg/model"
	"github.com/megalomania428/mega-doub/pkg/secret"
)

// Deps holds the dependencies of the commands. They are given explicitly, so the tests
// and the implementations of the commands substitute their own.
type Deps struct {
	// NewResolver returns the secret resolver of one run with the settings of its Vault
	// client, secret.New when it is nil.
	NewResolver func(secret.VaultOptions) secret.Resolver
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
// validate does it only with --resolve. prune without prune.keep is a problem of the
// configuration among the problems of Validate, also with --dry-run. init, backup and
// prune without --dry-run write the record command and run with the storage by
// storageRun.runCommand, the other commands and a dry run are stubs. global.timeout
// limits init, backup and prune after the validation, the secrets and the command
// included; its expiry is the record timed out and the exit code 4.
// Interruption is reported by cli.Execute, the handler keeps its code.
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
	// The overrides were applied while loading, before the logger followed the file.
	for _, o := range loaded.RetryEnv {
		env.Logger.Debug("retry option from environment", "variable", o.Variable,
			"value", o.Value)
	}
	// The records carry the absolute path of the configuration.
	inv.Global.Config = loaded.Path
	result := config.Validate(loaded)
	// The policies are optional while prune.enabled is off, the command prune needs them;
	// with prune.enabled Validate reports an empty prune.keep itself.
	c := loaded.Config
	if inv.Command == "prune" && !c.Prune.Enabled && len(c.Prune.Keep) == 0 {
		result.Add(loaded.Problem("prune.keep", "required by the command prune"))
	}
	logResult(env.Logger, result)
	if len(result.Problems) > 0 {
		return model.ExitConfig
	}
	if inv.Command == "config show" {
		return show(env, loaded.Config)
	}
	validate := inv.Command == "config validate"
	work := func(workCtx context.Context) int {
		var r secret.Resolver
		if !validate || inv.ConfigValidate.Resolve {
			// The resolver follows the logger of the file; the Vault client starts only at
			// the first vault: reference.
			r = deps.NewResolver(vaultOptions(loaded, env.Logger))
			if code := resolve(workCtx, r, loaded, env.Logger); code != model.ExitOK {
				return code
			}
		}
		if validate {
			env.Logger.Info("configuration is valid", "config", inv.Global.Config)
			return model.ExitOK
		}
		if storageCommand(inv.Command) && !inv.Global.DryRun {
			env.Logger.Info("command", commandAttrs(inv, loaded.Config)...)
			run := storageRun{logger: env.Logger, loaded: loaded, resolver: r}
			if !noProgress(inv, loaded.Config) {
				run.progress = env.Stderr
			}
			return run.runCommand(workCtx, inv.Command)
		}
		return stub(inv, env, loaded.Config)
	}
	if storageCommand(inv.Command) {
		limit := timeLimit{timeout: time.Duration(loaded.Config.Global.Timeout),
			logger: env.Logger}
		return limit.run(ctx, work)
	}
	return work(ctx)
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
// not run. The first failure of the token of Vault in the order of the walk adds one
// failure of vault.token with the full text of its cause and the position of the value
// of the token; the failures of the vault: references keep the short text. Then all
// the failures are sorted by position, so vault.token takes the place of its line.
func resolve(ctx context.Context, r secret.Resolver, loaded *config.Loaded,
	logger *slog.Logger) int {
	refs := config.Refs(loaded.Config)
	var failures []failure
	// tokenFailed is set once the failure of vault.token is added.
	tokenFailed := false
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
		var tokenErr *secret.TokenError
		if !tokenFailed && errors.As(err, &tokenErr) {
			tokenFailed = true
			failures = append(failures, tokenFailure(loaded, tokenErr))
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

// tokenFailure returns the failure of vault.token behind err: the kind of the reference
// of the token, the position of its value and the full text of the cause of err.
func tokenFailure(loaded *config.Loaded, err *secret.TokenError) failure {
	parsed, _ := secret.Parse(loaded.Config.Vault.Token)
	f := failure{path: "vault.token", kind: string(parsed.Kind), err: err.Unwrap()}
	if f.err == nil {
		f.err = err
	}
	if line, column, ok := loaded.ValuePos("vault.token"); ok {
		f.line, f.column = line, column
	}
	return f
}

// stub logs the invocation of a command that has no implementation yet or of a dry run
// with the effective values of its flags and prints the safe configuration; a dry run
// succeeds and a real run reports that the command is not implemented.
func stub(inv cli.Invocation, env cli.Env, c *config.Config) int {
	env.Logger.Info("command", commandAttrs(inv, c)...)
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

// commandAttrs returns the fields of the record command: the command, the configuration
// and the effective values of the flags of the command.
func commandAttrs(inv cli.Invocation, c *config.Config) []any {
	attrs := []any{"command", inv.Command, "config", inv.Global.Config, "dry_run",
		inv.Global.DryRun, "log_level", strings.ToLower(inv.Global.LogLevel.String()),
		"log_format", string(inv.Global.LogFormat), "no_progress", noProgress(inv, c)}
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
	return attrs
}

// storageCommand tells whether the command name runs with the storage: init, backup
// and prune.
func storageCommand(name string) bool {
	return name == "init" || name == "backup" || name == "prune"
}

// noProgress tells whether the run draws no bar: the flag turns the bar off over the
// file, and an empty list draws no bar even with --no-progress=false.
func noProgress(inv cli.Invocation, c *config.Config) bool {
	return inv.Global.NoProgress || len(c.Global.Progress) == 0
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
