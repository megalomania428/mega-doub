#!/usr/bin/env bash
# cspell:ignore duplicacy flock goproxy gonosumdb mktemp txz
# Prints GOPROXY and GONOSUMDB for the module duplicacy.invalid/v3 as NAME=value lines,
# fit for "${GITHUB_ENV}" and for export. The release archive with the file-based Go
# module proxy is downloaded and unpacked into RUNNER_TEMP, or /tmp without it, only
# when the module version is not there yet; go.sum checks the integrity of the module.
set -euo pipefail
# The tag, the archive and the version follow duplicacy.invalid/v3 in golang/go.mod.
tag='duplicacy-v3.3.0p003'
name='duplicacy-3_3_0-p003-goproxy'
version='v3.3.0-p003'
base='https://github.com/megalomania428/update-graves/releases/download'
root="${RUNNER_TEMP:-/tmp}/mega-doub-modules"
proxy="${root}/${name}"
module="duplicacy.invalid/v3/@v/${version}.zip"
# GOPROXY separates its entries with commas, the megalinter action splits PODMAN_ARGS
# on whitespace and podman -v splits on colons.
if [[ ! "${root}" =~ ^/[A-Za-z0-9/._-]+$ ]]; then
  echo "module-proxy.sh: [${root}] is not an absolute path of ASCII letters," \
    "digits and '/._-'" >&2
  exit 1
fi
mkdir -p "${root}"
# Concurrent runs take turns, so none of them replaces a proxy that another one uses.
exec {lock}>"${root}/lock"
flock "${lock}"
if [[ ! -f "${proxy}/${module}" ]]; then
  work="$(mktemp -d -p "${root}")"
  trap 'rm -rf "${work}"' EXIT
  curl -fsSL --retry 3 -o "${work}/${name}.txz" "${base}/${tag}/${name}.txz"
  tar -C "${work}" -xf "${work}/${name}.txz"
  if [[ ! -f "${work}/${name}/${module}" ]]; then
    echo "module-proxy.sh: ${name}.txz has no ${module}" >&2
    exit 1
  fi
  # A proxy that lost its module, such as to a cleanup of /tmp, moves away to be removed.
  if [[ -e "${proxy}" ]]; then
    mv -T "${proxy}" "${work}/stale"
  fi
  # The proxy moves in complete at once, so a run that stops halfway leaves none.
  mv -T "${work}/${name}" "${proxy}"
fi
printf 'GOPROXY=file://%s,https://proxy.golang.org,direct\n' "${proxy}"
printf 'GONOSUMDB=duplicacy.invalid\n'
