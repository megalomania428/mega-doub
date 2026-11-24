#!/usr/bin/env bash
# cspell:ignore doub mapfile mktemp testdata
# Checks the exit codes of the mega-doub binary given as the only argument: the commands,
# the configuration files of golang/testdata and init, backup and prune on a local
# storage in RUNNER_TEMP, or /tmp without it. The binary is not built here.
set -euo pipefail
if [[ $# -ne 1 || ! -f "${1:-}" || ! -x "${1:-}" ]]; then
  echo 'go-smoke.sh: the only argument must be an executable mega-doub binary' >&2
  exit 2
fi
bin="$1"
if [[ "${bin}" != /* ]]; then
  bin="${PWD}/${bin}"
fi
tmp="${RUNNER_TEMP:-/tmp}"
if [[ "${tmp}" != /* ]]; then
  echo "go-smoke.sh: [${tmp}] is not an absolute path" >&2
  exit 2
fi
source_path="${BASH_SOURCE[0]}"
if [[ "${source_path}" != */* ]]; then
  source_path="./${source_path}"
fi
cd "${source_path%/*}/../../golang"
dir="${tmp}/mega-doub-smoke"
mkdir -p "${dir}"
expect() {
  local want="$1" got=0
  shift
  "${bin}" "$@" >/dev/null 2>&1 || got=$?
  if [[ "${got}" -ne "${want}" ]]; then
    printf 'mega-doub %s: exit code %s, want %s\n' "$*" "${got}" "${want}"
    exit 1
  fi
}
expect 0 --help
expect 0 version
expect 0 --version
expect 2 bogus
expect 2 backup --bogus
expect 0 config validate -c testdata/example.yaml
expect 0 config validate -c testdata/example-vault.yaml
(
  export MAX_RETRIES_TIME=bogus
  expect 2 config validate -c testdata/example.yaml
) || exit 1
expect 2 config validate -c testdata/invalid/unknown-key-root.yaml
expect 2 config validate -c testdata/invalid/storage-keys.yaml
expect 0 config show -c testdata/example.yaml
# The env: references of example.yaml resolve to fake values.
mapfile -t names < <(grep -oE 'env:MEGA_DOUB_EXAMPLE_[A-Za-z0-9_]+' \
  testdata/example.yaml | sed 's/^env://' | sort -u)
if [[ "${#names[@]}" -eq 0 ]]; then
  echo 'no MEGA_DOUB_EXAMPLE_* references found in testdata/example.yaml' >&2
  exit 1
fi
for name in "${names[@]}"; do
  value="${name,,}"
  export "${name}=fake-${value//_/-}"
done
expect 0 config validate --resolve -c testdata/example.yaml
(
  unset "${names[@]}"
  expect 2 config validate --resolve -c testdata/example.yaml
) || exit 1
expect 0 backup --dry-run -c testdata/example.yaml
expect 0 init --dry-run -c testdata/example.yaml
(
  init_dir="$(mktemp -d -p "${dir}")"
  export MEGA_DOUB_SMOKE_PASSWORD="fake-${init_dir##*.}-password"
  printf '%s\n' '---' 'global:' "  copy_dir: '${init_dir}/copy'" \
    "  state_dir: '${init_dir}/state'" '  progress: []' 'storage:' \
    "  url: '${init_dir}/storage'" '  snapshot_id: smoke' \
    "  password: 'env:MEGA_DOUB_SMOKE_PASSWORD'" 'prune:' \
    "  keep: ['1:7']" >"${init_dir}/init.yaml"
  mkdir -p "${init_dir}/copy"
  printf 'smoke\n' >"${init_dir}/copy/data.txt"
  expect 3 backup -c "${init_dir}/init.yaml"
  if [[ -e "${init_dir}/storage/config" ]]; then
    printf 'mega-doub backup: config in %s before init\n' "${init_dir}/storage"
    exit 1
  fi
  expect 0 init -c "${init_dir}/init.yaml"
  expect 0 init -c "${init_dir}/init.yaml"
  if [[ ! -f "${init_dir}/storage/config" ]]; then
    printf 'mega-doub init: no config in %s\n' "${init_dir}/storage"
    exit 1
  fi
  expect 0 backup -c "${init_dir}/init.yaml"
  expect 0 backup -c "${init_dir}/init.yaml"
  expect 0 prune -c "${init_dir}/init.yaml"
) || exit 1
