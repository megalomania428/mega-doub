// Package cli declares the command tree and parses arguments into an invocation.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/megalomania428/mega-doub/internal/project"
	"github.com/megalomania428/mega-doub/pkg/logging"
	"github.com/megalomania428/mega-doub/pkg/model"
	"github.com/megalomania428/mega-doub/pkg/units"
)

const hint = "run '" + project.Name + " --help'"

var errCommandRequired = errors.New("command is required")

// Build describes the binary in the version output.
type Build struct {
	Version string
	Commit  string
}

// Global holds the global options and whether each of them was given explicitly.
type Global struct {
	Config        string
	LogLevel      slog.Level
	LogFormat     logging.Format
	NoProgress    bool
	DryRun        bool
	LogLevelSet   bool
	LogFormatSet  bool
	NoProgressSet bool
	DryRunSet     bool
}

// Check holds the flags of the check command.
type Check struct {
	Files  bool
	Chunks bool
}

// Verify holds the flags of the verify command; zero Revision means the latest one.
type Verify struct {
	Revision int
	Bytes    int64
	BytesSet bool
}

// List holds the flags of the list command; zero Revision means all revisions.
type List struct {
	Revision int
}

// Invocation is a parsed command line.
type Invocation struct {
	// Command is the command path without the binary name, such as "config validate".
	Command string
	Global  Global
	Check   Check
	Verify  Verify
	List    List
}

// Env holds the output streams and the logger configured by the global flags.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
	Logger *slog.Logger
}

// Handler runs a parsed invocation and returns the process exit code.
type Handler func(ctx context.Context, inv Invocation, env Env) int

// Execute parses args, runs handler for a command and returns the process exit code.
// Help and version are printed to stdout, log records are written to stderr.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer, build Build,
	handler Handler) int {
	p := &parser{
		stdout:  stdout,
		stderr:  stderr,
		handler: handler,
		logger:  logging.New(stderr, slog.LevelInfo, logging.FormatText),
		level:   levelValue{name: "info", level: slog.LevelInfo},
		format:  formatValue(logging.FormatText),
	}
	if err := ctx.Err(); err != nil {
		p.logger.Error("interrupted", "error", err)
		return model.ExitInterrupted
	}
	root := p.newRoot(build)
	root.SetArgs(append([]string{}, args...))
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		err = p.err
	}
	// Interruption takes precedence over an invalid invocation.
	if ctxErr := ctx.Err(); ctxErr != nil {
		p.logger.Error("interrupted", "error", ctxErr)
		return model.ExitInterrupted
	}
	if err != nil {
		p.logger.Error("invalid invocation", "error", err.Error(), "hint", hint)
		return model.ExitConfig
	}
	return p.code
}

// parser holds the flag values of one Execute call and its outcome.
type parser struct {
	stdout  io.Writer
	stderr  io.Writer
	handler Handler
	logger  *slog.Logger
	code    int
	err     error
	version bool
	global  Global
	level   levelValue
	format  formatValue
	check   Check
	verify  revisionValue
	bytes   sizeValue
	list    revisionValue
}

func (p *parser) newRoot(build Build) *cobra.Command {
	line := project.Name + " " + build.Version + " (" + build.Commit + ")\n"
	printVersion := func(cmd *cobra.Command, _ []string) error {
		_, err := io.WriteString(cmd.OutOrStdout(), line)
		return err
	}
	// The version flag is handled here instead of by cobra, so extra arguments
	// after --version are rejected by cobra.NoArgs first.
	root := &cobra.Command{
		Use:   project.Name,
		Short: "Backup utility",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !p.version {
				return errCommandRequired
			}
			return printVersion(cmd, args)
		},
		SilenceUsage:      true,
		SilenceErrors:     true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	root.Flags().BoolVar(&p.version, "version", false, "print the version and exit")
	flags := root.PersistentFlags()
	flags.StringVarP(&p.global.Config, "config", "c", project.DefaultConfigPath,
		"configuration file")
	flags.Var(&p.level, "log-level", "log level: debug, info, warn, error")
	flags.Var(&p.format, "log-format", "log format: text, json")
	flags.BoolVar(&p.global.NoProgress, "no-progress", false, "disable progress output")
	flags.BoolVar(&p.global.DryRun, "dry-run", false, "plan the run without changes")
	check := p.leaf("check", "Check the storage integrity")
	check.Flags().BoolVar(&p.check.Files, "files", false, "verify every file")
	check.Flags().BoolVar(&p.check.Chunks, "chunks", false, "verify every chunk")
	verify := p.leaf("verify", "Verify a revision by a test restore")
	verify.Flags().Var(&p.verify, "revision", "revision to verify, 0 for the latest")
	verify.Flags().Var(&p.bytes, "bytes", "amount of data to restore, such as 256M")
	list := p.leaf("list", "List revisions in the storage")
	list.Flags().Var(&p.list, "revision", "revision to list, 0 for all")
	config := &cobra.Command{
		Use:   "config",
		Short: "Work with the configuration file",
		Args:  cobra.NoArgs,
		RunE:  requireCommand,
	}
	config.AddCommand(
		p.leaf("validate", "Validate the configuration file"),
		p.leaf("show", "Show the effective configuration"),
	)
	root.AddCommand(
		p.leaf("backup", "Back up all targets"),
		p.leaf("prune", "Prune old revisions in the storage"),
		check, verify, list, config,
		&cobra.Command{
			Use:   "version",
			Short: "Print the version",
			Args:  cobra.NoArgs,
			RunE:  printVersion,
		},
	)
	topic := func(args []string) (*cobra.Command, error) {
		target, rest, err := root.Find(args)
		if err != nil {
			return nil, err
		}
		if len(rest) > 0 {
			return nil, fmt.Errorf("unknown command %q for %q", rest[0], target.CommandPath())
		}
		return target, nil
	}
	// The topic is checked as the positional arguments of help, so it is also
	// rejected when help itself is called with --help.
	helpCmd := &cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command",
		Args: func(_ *cobra.Command, args []string) error {
			_, err := topic(args)
			return err
		},
		RunE: func(_ *cobra.Command, args []string) error {
			target, err := topic(args)
			if err != nil {
				return err
			}
			return target.Help()
		},
	}
	root.SetHelpCommand(helpCmd)
	// cobra prints help before it validates positional arguments, so extra
	// arguments after --help are rejected here and reported by Execute. The
	// help command with --help shows the help of its topic, as without --help.
	help := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if err := cmd.ValidateArgs(cmd.Flags().Args()); err != nil {
			p.err = err
			return
		}
		if cmd == helpCmd {
			cmd, _ = topic(cmd.Flags().Args())
		}
		help(cmd, args)
	})
	// Without these flags registered up front, cobra treats a leading --help or
	// --version as a flag with a value and swallows the next argument.
	root.InitDefaultHelpCmd()
	initHelpFlags(root)
	return root
}

// leaf returns a command without positional arguments that runs the handler.
func (p *parser) leaf(use, short string) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs, RunE: p.run}
}

func (p *parser) run(cmd *cobra.Command, _ []string) error {
	flags := cmd.Flags()
	inv := Invocation{
		Command: strings.TrimPrefix(cmd.CommandPath(), project.Name+" "),
		Global:  p.global,
		Check:   p.check,
		Verify: Verify{
			Revision: int(p.verify),
			Bytes:    p.bytes.bytes,
			BytesSet: flags.Changed("bytes"),
		},
		List: List{Revision: int(p.list)},
	}
	inv.Global.LogLevel = p.level.level
	inv.Global.LogFormat = logging.Format(p.format)
	inv.Global.LogLevelSet = flags.Changed("log-level")
	inv.Global.LogFormatSet = flags.Changed("log-format")
	inv.Global.NoProgressSet = flags.Changed("no-progress")
	inv.Global.DryRunSet = flags.Changed("dry-run")
	p.logger = logging.New(p.stderr, inv.Global.LogLevel, inv.Global.LogFormat)
	env := Env{Stdout: p.stdout, Stderr: p.stderr, Logger: p.logger}
	p.code = p.handler(cmd.Context(), inv, env)
	return nil
}

func requireCommand(_ *cobra.Command, _ []string) error {
	return errCommandRequired
}

func initHelpFlags(cmd *cobra.Command) {
	cmd.InitDefaultHelpFlag()
	for _, sub := range cmd.Commands() {
		initHelpFlags(sub)
	}
}

// levelValue is the --log-level flag value.
type levelValue struct {
	name  string
	level slog.Level
}

func (v *levelValue) String() string { return v.name }

func (v *levelValue) Type() string { return "string" }

func (v *levelValue) Set(s string) error {
	level, err := logging.ParseLevel(s)
	if err != nil {
		return err
	}
	v.name, v.level = s, level
	return nil
}

// formatValue is the --log-format flag value.
type formatValue logging.Format

func (v *formatValue) String() string { return string(*v) }

func (v *formatValue) Type() string { return "string" }

func (v *formatValue) Set(s string) error {
	format, err := logging.ParseFormat(s)
	if err != nil {
		return err
	}
	*v = formatValue(format)
	return nil
}

// revisionValue is a non-negative --revision flag value.
type revisionValue int

func (v *revisionValue) String() string { return strconv.Itoa(int(*v)) }

func (v *revisionValue) Type() string { return "int" }

func (v *revisionValue) Set(s string) error {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return fmt.Errorf("invalid revision %q: want a non-negative integer", s)
	}
	*v = revisionValue(n)
	return nil
}

// sizeValue is the --bytes flag value parsed with units.ParseSize.
type sizeValue struct {
	text  string
	bytes int64
}

func (v *sizeValue) String() string { return v.text }

func (v *sizeValue) Type() string { return "size" }

func (v *sizeValue) Set(s string) error {
	n, err := units.ParseSize(s)
	if err != nil {
		return err
	}
	v.text, v.bytes = s, n
	return nil
}
