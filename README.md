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

## Configuration

The configuration is one YAML document, `/usr/local/etc/mega-doub/main.yaml` by default or the file given with `--config`. The commented example [golang/testdata/example.yaml](golang/testdata/example.yaml) shows the layout.

- The file is at most 1048576 bytes; an empty file, a file of comments only and a `null` document are errors.
- Loading is strict: an unknown key at any level and a repeated key in one mapping are errors, also for keys that come from an alias or `<<`.
- Anchors, aliases and the merge key `<<` are allowed anywhere; nesting deeper than 64 levels and excessive alias expansion are errors. The `templates` section holds anchors: a template is checked only where it is used, an unused one is not checked.
- `<<` takes one alias or a list such as `<<: [*a, *b]`; two `<<` keys in one mapping are an error. A local key overrides the same key from `<<`, an earlier template of the list overrides a later one. A repeated template name in `templates` is not detected.
- A missing key and an explicit `null` select the default; an explicit `0`, `false` or `''` is kept. `global.copy_dir` and `global.state_dir` default to `/var/lib/mega-doub/copy/<instance>` and `/var/lib/mega-doub/state/<instance>`.
- Durations need a unit, such as `30s` or `12h`; only `0` is allowed without one. Sizes take an optional `K`, `M`, `G` or `T` suffix, a plain number is bytes.
- Booleans are `true`, `false`, `yes`, `no`, `on` and `off` in lower case, capitalized or in upper case, and `y`, `Y`, `n`, `N`; `1`, `0` and a quoted `'true'` are errors.
- `global.log_level` and `global.log_format` take the values of `--log-level` and `--log-format`; a flag given explicitly wins over the file.
- `global.retry` defaults to the values of go-lib-ci: `max_retries_time: 111s`, `backoff_initial: 1s`, `backoff_step: 1s`, `backoff_step_attempts: 3` and `backoff_cap: 7s`; explicit values are kept as written. As in go-lib-ci, `max_retries_time: 0` selects the default, a negative value disables the retries and a positive one limits the total wait; a `backoff_*` value of `0` or below selects its default.

Every problem of the file is logged at once as a separate record `level=ERROR msg="invalid configuration"` with the fields `file` (absolute path), `line`, `column`, `path` and `error`, ordered by position, and the exit code is `2`. The path names the key where it is used, with list indexes, such as `targets[1].sources[0].port`, `prune.keep[2]` or `storage.keys.b2_id`; a key with characters other than ASCII letters, digits, `_` and `-` is quoted in brackets as a Go string, such as `storage.keys["a.b"]` or `storage.keys[""]` for an empty one. The position is where the key or the value is written: a key or a value that comes from a template through `<<` and a value given by an alias are reported at their line in the template or at the anchor, once for every place that uses them, or once without a path when the path is not known, while an alias written as a key is reported where it is written. A field without a value is left out: a problem without a known position has no `line` and `column`, a problem whose path is not certain, such as a value of an alias used by fields of different types, has no `path`. `mega-doub config validate` checks the file and, when it has no problems, logs `level=INFO msg="configuration is valid"` with the absolute path in the field `config`; the `command` record of the other commands carries the same path.

A file that loads is validated next, a file that fails to load is not. The problems of the validation are logged all at once in the same way and give the exit code `2`; a problem of a field is reported at its value, an explicit `null` included, or at the key of the nearest present ancestor when the field is missing. Warnings are logged before the problems as `level=WARN msg="configuration warning"` with the same fields and `warning` instead of `error`, they do not change the exit code. The rules:

- `global.instance` matches `^[A-Za-z0-9_-]+$`, `global.parallel_targets` and `global.parallel_sources` are at least `1`, `global.progress` is `auto`, `always` or `never`, `global.timeout` is above zero; an empty string is checked like any other value.
- `global.copy_dir` and `global.state_dir` are absolute paths that are neither equal nor inside each other. They are compared as written after cleaning, without resolving links and with letter case: `/a/copy` and `/a/copy2` pass, while `copy_dir: /` holds any `state_dir`.
- `global.retry`: the effective `backoff_cap`, after the defaults replace the values of `0` and below, is not less than the effective `backoff_initial`, and `(backoff_cap / backoff_step + 1) * backoff_step_attempts` of the effective values with integer division fits a signed 64-bit integer; there are no other limits.

## Exit codes

- `0` – success, help, version or a valid configuration for `config validate`;
- `2` – invalid invocation or configuration: no command, unknown command or flag, extra argument, invalid flag value, unreadable or invalid configuration file;
- `3` – the command is not implemented yet;
- `4` – interrupted by a signal before or during the command.

<!-- cspell:ignore doub ldflags testdata -->
