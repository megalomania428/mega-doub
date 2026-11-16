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
- An empty string in a required field is reported as `required` only, without the checks of its format; an empty string in an optional field, such as `vault.address` or `vault.ca_file`, means it is not set.
- `vault.address` and `vault.token` are required when a secret reference starts with `vault:`, except `vault.token` itself and the fields that a local target does not allow; a malformed `vault:` reference counts too. `vault.address`, when set, is an `http` or `https` URL with a host, an optional port from `1` to `65535`, an optional path `/` and without user information, query and fragment; only the first broken rule is reported, without the address. An `http` address gives the warning `vault token is sent in clear text`. `vault.token` is not a `vault:` reference, `vault.timeout` is above zero and `vault.ca_file`, when set, is an absolute path.
- `storage.name` is not empty and not `ssh` in any letter case. `storage.url` is a URL with a scheme and a host, such as `b2://bucket/prefix` or `sftp://user@host/path`; a local path is not supported. A password in its user information, an empty one included, gives the warning `url contains a password`; the URL is never logged. `storage.snapshot_id` matches `^[A-Za-z0-9_-]+$`, `storage.password` is required, `storage.threads` is at least `1`, and every key of `storage.keys` and its value are not empty.
- Every element of `prune.keep` is `n:m` without spaces: keep one snapshot every `n` days for the snapshots older than `m` days, `n` of `0` removes them. Both numbers fit an integer, `m` is at least `1` and strictly decreases along the list, while `n` is not ordered; an element gets at most one problem. The format is always checked, a non-empty `prune.keep` is required when `prune.enabled` is set; `prune.threads` is at least `1`.
- `check.every` is not negative and is above zero when `check.enabled` is set; `check.test_restore_bytes` is not negative, `0` is allowed.
- An empty or missing `targets` gives the warning `no targets configured`. Every target has a `name` that matches `^[A-Za-z0-9_][A-Za-z0-9_.-]*$`, so `.`, `..` and a name starting with `.` or `-` fail, unique among the targets, and a `transport` of `local` or `ssh`. A target without sources gives the warning `target "<name>" has no sources`.
- An `ssh` target requires `host`, `user` and `known_hosts`, an absolute path; `port` is from `1` to `65535`, `connect_timeout` is above zero and `auth` is a non-empty list of `agent`, `key` and `password` without repeats. `key` is required when `auth` has `key` and `password` when it has `password`; a `key` or a `password` without its method is allowed, a non-empty `key_passphrase` requires `key` in `auth`.
- A `local` target has none of `host`, `user`, `port`, `auth`, `key`, `key_passphrase`, `password`, `known_hosts` and `connect_timeout`, also through `<<`; every such field is reported at its key, an explicit `null` is allowed and `''` is not.
- Every source has an `id` that matches the pattern of `name` and is unique inside its target; the problems of the source type are reported too. A repeated `name` or `id` is reported at the key of every repeat after the first, an empty one only as `required`.
- Every secret reference has the syntax of its prefix below, except the fields that a local target does not allow and a `vault:` reference in `vault.token`; a broken reference is reported without its value.

### Secrets

A secret field – `vault.token`, `storage.password`, the values of `storage.keys`, `targets[].key`, `targets[].key_passphrase`, `targets[].password` and the secret fields of the sources – holds a reference:

- `plain:<value>` – the value itself;
- `env:<NAME>` – the environment variable `NAME`;
- `file:<absolute path>` – the content of the file without the surrounding whitespace;
- `vault:<mount>/<path>/<key>` – the key of a secret of the Vault KV version 2 engine at `vault.address`;
- a string without a prefix is taken as `plain:` with the warning `value without prefix is treated as plain`, so a typo in a prefix, such as `evn:X`, gives a plain value with this warning.

The values are not resolved yet.

## Exit codes

- `0` – success, help, version or a valid configuration for `config validate`;
- `2` – invalid invocation or configuration: no command, unknown command or flag, extra argument, invalid flag value, unreadable or invalid configuration file;
- `3` – the command is not implemented yet;
- `4` – interrupted by a signal before or during the command.

<!-- cspell:ignore doub evn ldflags testdata -->
