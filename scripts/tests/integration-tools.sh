#!/usr/bin/env bash
# cspell:ignore doub duplicacy flock gilbertchen mktemp txz
# Prints the paths of the duplicacy CLIs that the integration tests restore with as
# NAME=value lines, fit for "${GITHUB_ENV}" and for export: MEGA_DOUB_IT_UPSTREAM, the
# release of gilbertchen/duplicacy, and MEGA_DOUB_IT_PATCHED, the patched build of
# update-graves. Each CLI is downloaded into RUNNER_TEMP, or /tmp without it, only when
# it is not there yet or does not match its SHA256 below, which is checked at every run.
# The cache directory and its path must be closed for changes by users but root and the
# current one; the printed paths are canonical.
set -euo pipefail
upstream_url='https://github.com/gilbertchen/duplicacy/releases/download/v3.3.1'
upstream_name='duplicacy_linux_x64_3.3.1'
upstream_sha256='3ab76827dded13cf935a1886216145e6'  # DevSkim: ignore DS173237
upstream_sha256+='316b74fbe981e4cb4b298db8fa01c117' # DevSkim: ignore DS173237
patched_url='https://github.com/megalomania428/update-graves/releases/download'
patched_url+='/duplicacy-v3.3.0p003'
patched_name='duplicacy-3_3_0-p003-linux-amd64'
patched_sha256='b90a607c25a912347f13cf15d0f97525'      # DevSkim: ignore DS173237
patched_sha256+='77eac4739636f9b1399bf0e16e641ea5'     # DevSkim: ignore DS173237
patched_bin_sha256='763b4d60f360678daf83e42c2eb97306'  # DevSkim: ignore DS173237
patched_bin_sha256+='3e33dcb792e22f34f012ef5f8726b734' # DevSkim: ignore DS173237
root="${RUNNER_TEMP:-/tmp}/mega-doub-it-tools"
if [[ ! "${root}" =~ ^/[A-Za-z0-9/._-]+$ ]]; then
  echo "integration-tools.sh: [${root}] is not an absolute path of ASCII letters," \
    "digits and '/._-'" >&2
  exit 1
fi
# Explicit mode: a umask such as 002 would set the group write bit and fail the check.
# The mode applies to the cache directory only, the parents keep the umask.
# shellcheck disable=SC2174
mkdir -p -m 0755 "${root}"
uid="$(id -u)"
# unsafe <directory> succeeds for a directory that others could change or replace: not
# of root or the user, writable by the group or others, except the sticky parents.
unsafe() {
  local owner mode
  read -r owner mode < <(stat -c '%u %a' -- "$1")
  if [[ "${owner}" != 0 && "${owner}" != "${uid}" ]]; then
    return 0
  fi
  if ((8#${mode} & 8#022)); then
    [[ "$1" == "${cache}" ]] || ! ((8#${mode} & 8#1000))
    return
  fi
  return 1
}
# refuse <directory> exits for a directory that others could change or replace.
refuse() {
  echo "integration-tools.sh: [$1] must belong to root or the current user and not" \
    'be writable by the group or others, except a parent with the sticky bit; set' \
    'RUNNER_TEMP to another directory' >&2
  exit 1
}
if [[ -L "${root}" ]]; then
  echo "integration-tools.sh: [${root}] must be a directory of the current user," \
    'not a link and not writable by the group or others; set RUNNER_TEMP to another' \
    'directory' >&2
  exit 1
fi
# Only the canonical path is safe to print: a link in it may be changed later.
cache="$(realpath -e -- "${root}")"
if [[ ! "${cache}" =~ ^/[A-Za-z0-9/._-]+$ ]]; then
  echo "integration-tools.sh: [${cache}] is not an absolute path of ASCII letters," \
    "digits and '/._-'" >&2
  exit 1
fi
if unsafe "${cache}"; then
  echo "integration-tools.sh: [${cache}] must be a directory of the current user," \
    'not a link and not writable by the group or others; set RUNNER_TEMP to another' \
    'directory' >&2
  exit 1
fi
# Every directory on the way to the cache, and on the way to each link of the given
# path, must stay in the hands of root and the user, or the cache may be swapped.
path=''
IFS=/ read -r -a parts <<<"${root}"
for part in "${parts[@]}"; do
  [[ -n "${part}" ]] || continue
  path+="/${part}"
  if [[ -L "${path}" ]]; then
    link_owner="$(stat -c %u -- "${path}")"
    if [[ "${link_owner}" != 0 && "${link_owner}" != "${uid}" ]]; then
      refuse "${path}"
    fi
  fi
  dir="$(realpath -e -- "${path}")"
  while :; do
    if unsafe "${dir}"; then
      refuse "${dir}"
    fi
    [[ "${dir}" != / ]] || break
    dir="$(dirname -- "${dir}")"
  done
done
root="${cache}"
# Concurrent runs take turns, so none of them replaces a CLI that another one uses.
exec {lock}>"${root}/lock"
flock "${lock}"
work=''
trap 'if [[ -n "${work}" ]]; then rm -rf "${work}"; fi' EXIT
# verified <file> <sha256> succeeds for a plain executable file of the user that only
# the user can write, with the SHA256.
verified() {
  local owner mode
  [[ -f "$1" && ! -L "$1" && -x "$1" ]] || return 1
  read -r owner mode < <(stat -c '%u %a' -- "$1")
  [[ "${owner}" == "${uid}" ]] && ! ((8#${mode} & 8#022)) &&
    printf '%s  %s\n' "$2" "$1" | sha256sum --check --status
}
# fetch <url> <name> <sha256> <bin sha256> puts the executable <name> with the SHA256
# <bin sha256> into the cache unless it is there. The download of <url> with the SHA256
# <sha256> is either the executable or a .txz archive that holds it.
fetch() {
  local url="$1" name="$2" sha256="$3" bin_sha256="$4"
  if verified "${root}/${name}" "${bin_sha256}"; then
    return
  fi
  work="$(mktemp -d -p "${root}")"
  curl -fsSL --retry 3 --connect-timeout 30 --speed-limit 1024 --speed-time 60 \
    -o "${work}/download" "${url}"
  if ! printf '%s  %s\n' "${sha256}" "${work}/download" | sha256sum --check --status; then
    echo "integration-tools.sh: ${url} does not match the SHA256 ${sha256}" >&2
    exit 1
  fi
  if [[ "${url}" == *.txz ]]; then
    # Under root, tar keeps the archive owner; verified needs the current user.
    tar --no-same-owner -C "${work}" -xJf "${work}/download" "${name}"
  else
    mv -T "${work}/download" "${work}/${name}"
  fi
  chmod 0755 "${work}/${name}"
  if ! verified "${work}/${name}" "${bin_sha256}"; then
    echo "integration-tools.sh: ${name} of ${url} does not match the SHA256" \
      "${bin_sha256}" >&2
    exit 1
  fi
  # The CLI moves in complete at once, so a run that stops halfway leaves none.
  mv -T "${work}/${name}" "${root}/${name}"
  rm -rf "${work}"
  work=''
}
fetch "${upstream_url}/${upstream_name}" "${upstream_name}" "${upstream_sha256}" \
  "${upstream_sha256}"
fetch "${patched_url}/${patched_name}.txz" "${patched_name}" "${patched_sha256}" \
  "${patched_bin_sha256}"
printf 'MEGA_DOUB_IT_UPSTREAM=%s\n' "${root}/${upstream_name}"
printf 'MEGA_DOUB_IT_PATCHED=%s\n' "${root}/${patched_name}"
