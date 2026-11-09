# mega-doub

Backup utility written in Go.

## Build

```bash
cd golang
version="$(git describe --tags --always --dirty)"
commit="$(git rev-parse --short HEAD)"
go build -o mega-doub -ldflags "-X main.version=${version} -X main.commit=${commit}" \
  ./cmd/mega-doub
```

## Usage

```text
mega-doub [global flags] <command> [flags]
```

Commands:

- `backup` – back up all targets;
- `prune` – prune old revisions in the storage;
- `check [--files] [--chunks]` – check the storage integrity, optionally every file or every chunk;
- `verify [--revision N] [--bytes SIZE]` – verify a revision by a test restore: revision `0` is the latest one, `SIZE` is a number with an optional `K`, `M`, `G` or `T` suffix;
- `list [--revision N]` – list revisions in the storage, revision `0` lists all of them;
- `config validate` – validate the configuration file;
- `config show` – show the effective configuration;
- `version` – print `mega-doub <version> (<commit>)`, same as `--version` given without a command;
- `help [command]` – print help, same as `--help` and `-h`.

Global flags are accepted before and after the command name:

- `--config`, `-c` – configuration file, `/usr/local/etc/mega-doub/main.yaml` by default;
- `--log-level` – `debug`, `info`, `warn` or `error`, `info` by default;
- `--log-format` – `text` or `json`, `text` by default;
- `--no-progress` – disable progress output;
- `--dry-run` – plan the run without changes.

## Exit codes

- `0` – success, help or version;
- `2` – invalid invocation: no command, unknown command or flag, extra argument, invalid flag value;
- `3` – the command is not implemented yet;
- `4` – interrupted by a signal before or during the command.

<!-- cspell:ignore doub ldflags -->
