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
- `config validate [--resolve]` – validate the configuration file, with `--resolve` also resolve the secrets, see [Secrets](#secrets);
- `config show` – print the effective configuration of the file, without the overrides of `global.retry` from the environment, with the secrets masked, see [Output](#output);
- `version` – print `mega-doub <version> (<commit>)`, same as `--version` given without a command;
- `help [command]` – print help, same as `--help` and `-h`.

Global flags are accepted before and after the command name:

- `--config`, `-c` – configuration file, `/usr/local/etc/mega-doub/main.yaml` by default;
- `--log-level` – `debug`, `info`, `warn` or `error`, `info` by default;
- `--log-format` – `text` or `json`, `text` by default;
- `--no-progress` – disable progress output;
- `--dry-run` – plan the run without changes; for now `backup`, `prune`, `check`, `verify` and `list` only resolve the secrets, log their invocation and print the configuration, see [Output](#output).

## Configuration

The configuration is one YAML document, `/usr/local/etc/mega-doub/main.yaml` by default or the file given with `--config`. The commented example [golang/testdata/example.yaml](golang/testdata/example.yaml) shows the layout, and [golang/testdata/example-vault.yaml](golang/testdata/example-vault.yaml) takes the secrets from Vault with `vault:` references.

- The file is at most 1048576 bytes; an empty file, a file of comments only and a `null` document are errors.
- Loading is strict: an unknown key at any level and a repeated key in one mapping are errors, also for keys that come from an alias or `<<`.
- Anchors, aliases and the merge key `<<` are allowed anywhere; nesting deeper than 64 levels and excessive alias expansion are errors. The `templates` section holds anchors: a template is checked only where it is used, an unused one is not checked.
- `<<` takes one alias or a list such as `<<: [*a, *b]`; two `<<` keys in one mapping are an error. A local key overrides the same key from `<<`, an earlier template of the list overrides a later one. A repeated template name in `templates` is not detected.
- A missing key and an explicit `null` select the default; an explicit `0`, `false` or `''` is kept. `global.copy_dir` and `global.state_dir` default to `/var/lib/mega-doub/copy/<instance>` and `/var/lib/mega-doub/state/<instance>`.
- Durations need a unit, such as `30s` or `12h`; only `0` is allowed without one. Sizes take an optional `K`, `M`, `G` or `T` suffix, a plain number is bytes.
- Booleans are `true`, `false`, `yes`, `no`, `on` and `off` in lower case, capitalized or in upper case, and `y`, `Y`, `n`, `N`; `1`, `0` and a quoted `'true'` are errors.
- `global.log_level` and `global.log_format` take the values of `--log-level` and `--log-format`; a flag given explicitly wins over the file.
- `global.retry` defaults to the values of go-lib-ci: `max_retries_time: 111s`, `backoff_initial: 1s`, `backoff_step: 1s`, `backoff_step_attempts: 3` and `backoff_cap: 7s`; explicit values are kept as written. As in go-lib-ci, `max_retries_time: 0` selects the default, a negative value disables the retries and a positive one limits the total wait; a `backoff_*` value of `0` or below selects its default.
- The environment variables `MAX_RETRIES_TIME`, `BACKOFF_INITIAL`, `BACKOFF_STEP`, `BACKOFF_STEP_ATTEMPTS` and `BACKOFF_CAP` override `global.retry.max_retries_time`, `backoff_initial`, `backoff_step`, `backoff_step_attempts` and `backoff_cap`: the environment wins over the file, which wins over the default, and an empty variable counts as unset. They are read with every file that is read, `config show` included, and take a Go duration, such as `30s` or `-1s`, or an integer for `BACKOFF_STEP_ATTEMPTS`. The same rules apply: `MAX_RETRIES_TIME=0s` selects the default of go-lib-ci (111 seconds), a negative value disables the retries and a `BACKOFF_*` value of `0` or below selects its default.

Every problem of the file is logged at once as a separate record `level=ERROR msg="invalid configuration"` with the fields `file` (absolute path), `line`, `column`, `path` and `error`, ordered by position with the problems without a position last in the order they are found, and the exit code is `2`. An environment variable of `global.retry` that does not parse is such a problem too: its `file` is `<env NAME>`, such as `file="<env MAX_RETRIES_TIME>"`, its `path` is the key it overrides, such as `global.retry.max_retries_time`, it has no position, and the file is not validated then. After the file loads, every variable of `global.retry` taken from the environment is logged before the records of the validation as `level=DEBUG msg="retry option from environment"` with the fields `variable` and `value`, the value as written. The path names the key where it is used, with list indexes, such as `targets[1].sources[0].port`, `prune.keep[2]` or `storage.keys.b2_id`; a key with characters other than ASCII letters, digits, `_` and `-` is quoted in brackets as a Go string, such as `storage.keys["a.b"]` or `storage.keys[""]` for an empty one. The position is where the key or the value is written: a key or a value that comes from a template through `<<` and a value given by an alias are reported at their line in the template or at the anchor, once for every place that uses them, or once without a path when the path is not known, while an alias written as a key is reported where it is written. A field without a value is left out: a problem without a known position has no `line` and `column`, a problem whose path is not certain, such as a value of an alias used by fields of different types, has no `path`. `mega-doub config validate` checks the file and, when it has no problems and with `--resolve` all its secrets resolve, logs `level=INFO msg="configuration is valid"` with the absolute path in the field `config`; the `command` record of the other commands carries the same path.

A file that loads is validated next, a file that fails to load is not. The problems of the validation are logged all at once in the same way and give the exit code `2`; a problem of a field is reported at its value, an explicit `null` included, or at the key of the nearest present ancestor when the field is missing; a problem of `global.retry` that a value from the environment causes has the `file` `<env NAME>` of the variable and no position. Warnings are logged before the problems as `level=WARN msg="configuration warning"` with the same fields and `warning` instead of `error`, they do not change the exit code. The rules:

- `global.instance` matches `^[A-Za-z0-9_-]+$`, `global.parallel_targets` and `global.parallel_sources` are at least `1`, `global.progress` is `auto`, `always` or `never`, `global.timeout` is above zero; an empty string is checked like any other value.
- `global.copy_dir` and `global.state_dir` are absolute paths that are neither equal nor inside each other. They are compared as written after cleaning, without resolving links and with letter case: `/a/copy` and `/a/copy2` pass, while `copy_dir: /` holds any `state_dir`.
- `global.retry`, with the overrides from the environment applied: the effective `backoff_cap`, after the defaults replace the values of `0` and below, is not less than the effective `backoff_initial`, and `(backoff_cap / backoff_step + 1) * backoff_step_attempts` of the effective values with integer division fits a signed 64-bit integer; there are no other limits.
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

The commands `backup`, `prune`, `check`, `verify` and `list`, also with `--dry-run`, resolve every reference after the validation and before anything else; `config validate --resolve` does the same after the validation, while `config validate` without the flag and `config show` do not resolve. The reference in `vault.token` is not resolved with them: the Vault client resolves it at the first `vault:` reference, so without `vault:` references `vault.token` is not read and Vault gets no request. The references are resolved one by one in a fixed order of the fields of the configuration, which does not depend on the order of the sections in the file, a value is read once per run and is never written to the log or the output:

- `plain:` and a string without a prefix give the value as it is;
- `env:` gives the value of the variable as it is, the surrounding whitespace included; a variable that is not set or is empty is an error;
- `file:` gives the content of a regular file or of a link to one, such as a file in `/run/secrets`, without a leading UTF-8 byte order mark and the surrounding whitespace, a trailing CR included; a directory, a device, a named pipe, a socket, a file larger than 65536 bytes and a file that is empty after the trimming are errors;
- `vault:` reads the key from the Vault KV version 2 engine at `vault.address` with the token of `vault.token`: an `env:`, `file:` or `plain:` reference or a string without a prefix, an empty token is an error. One client serves the run and reads every reference with one request; `vault.timeout`, `30s` by default, limits the request as a whole: the connection, TLS, the response headers and the body. A missing secret, a missing key, an empty or non-string value, a response body larger than 1 MiB (1048576 bytes), a failed connection and a response other than `2xx`, such as `vault returned 403`, are errors; redirects are not followed, so a `3xx` response is the error `vault returned <code>` too. The errors name the path of the secret but never the URL, the token or the body of the response; the error of a failed connection may name the host and the port. The proxy and the trusted certificates come from the environment: `HTTP_PROXY` for an `http://` address, `HTTPS_PROXY`, `NO_PROXY` and their lower-case forms, `SSL_CERT_FILE` and `SSL_CERT_DIR`; requests to loopback addresses bypass the proxy.

Every reference that fails is logged at once as a separate record `level=ERROR msg="secret resolution failed"` with the fields `path`, `kind` (`plain`, `env`, `file` or `vault`), `file` (absolute path), `line` and `column` of the value and `error`, which names the variable or the path but never the value, ordered by position, and the exit code is `2` with nothing in stdout. When every reference resolves, `level=INFO msg="secrets resolved"` is logged with the number of the references in the field `count`. A boolean flag with a value is written as `--resolve=false` or `--dry-run=false`: `--resolve false` gives an extra argument and the exit code `2`.

## Output

`mega-doub config show` prints the configuration of the file that the commands use, except the overrides of `global.retry` from the environment, as one YAML document that starts with `---`: the defaults are filled in, the anchors and `<<` are resolved, the `templates` section is left out and a `local` target has no ssh fields. The keys follow the order of the configuration above and the keys of `storage.keys` are sorted, durations are written as `12h0m0s` and sizes as `1536K` or `256M`; a size that looks like a number is quoted, such as `'0'` or `'100'`, and is read back with or without quotes. The commands `backup`, `prune`, `check`, `verify` and `list` are stubs for now: each checks the file the same way, resolves the secrets as described in [Secrets](#secrets), logs `level=INFO msg="secrets resolved"` and then `level=INFO msg=command` with the fields `command`, `config`, `dry_run`, `log_level`, `log_format`, `no_progress`, the effective values of the flags of the command and `enabled` for `prune` and `check`, and prints the same output. With `--dry-run` it then logs `level=INFO msg="dry run, nothing to do"` and exits with `0`, otherwise it logs `level=ERROR msg="command is not implemented yet"` and exits with `3`. `mega-doub config validate` prints nothing to stdout and writes no `msg=command` record: it logs the warnings and the problems of the file, with `--resolve` the records of the secrets, and, for a valid file whose secrets resolve when `--resolve` is given, `level=INFO msg="configuration is valid"`. `mega-doub config show` does not resolve the secrets. Every command writes a record only when its level is not below the log level: `warn` leaves out the `INFO` records, `error` leaves out the warnings too. An invalid file and a secret that fails to resolve give no output, a failed write of the output and an output larger than the limit of 1 MiB (1048576 bytes) for a configuration file, which would not load again and is not printed, are logged as `level=ERROR msg="output write failed"` with the field `error` and give the exit code `2`.

The output masks the secrets: a non-empty `plain:` reference and a value without a prefix become `plain:***`, while the `env:`, `file:` and `vault:` references are printed as they are, and the password in the user information of `storage.url` and `vault.address`, an empty one included, becomes `***`, such as `sftp://user:***@host/path`. The output with `plain:***` is not meant to be run as it is: the value of such a secret becomes `***`. Only the password in the user information is masked: secrets in the query or the fragment of a URL are not supported and are not masked. The user information ends before the first `/`, `?` or `#` after `://`, so these characters in a password must be percent-encoded, such as `%23` for `#`: otherwise the rest of the password is read as the host, the path, the query or the fragment and is not masked.

## Exit codes

- `0` – success, help, version, a valid configuration for `config validate`, with `--resolve` only when all its secrets resolve, the output of `config show` or a dry run;
- `2` – invalid invocation or configuration: no command, unknown command or flag, extra argument, invalid flag value, unreadable or invalid configuration file, an environment variable of `global.retry` that does not parse, a secret that fails to resolve, a failed write of the output or an output larger than 1048576 bytes;
- `3` – the command is not implemented yet;
- `4` – interrupted by a signal before or during the command.

<!-- cspell:ignore doub evn ldflags testdata -->
