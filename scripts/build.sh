#!/usr/bin/env bash
# cspell:ignore doub freebsd goarch goarm gonosumdb goos goproxy ldflags loong mipsle
# cspell:ignore flock riscv trimpath
# Builds mega-doub with CGO_ENABLED=0 for every platform of the list below and packs each
# binary into its own tar.xz archive in RUNNER_TEMP, or /tmp without it, then prints the
# path of every archive. A tag build takes GITHUB_REF_NAME as the version, other builds
# take git describe with .dev.<GITHUB_RUN_NUMBER>.<GITHUB_RUN_ATTEMPT> and 0 for a number
# that is not set. GOPROXY and GONOSUMDB come from scripts/module-proxy.sh.
set -euo pipefail
tmp="${RUNNER_TEMP:-/tmp}"
if [[ "${tmp}" != /* ]]; then
  echo "build.sh: [${tmp}] is not an absolute path" >&2
  exit 1
fi
mkdir -p "${tmp}"
# Concurrent runs take turns, so none of them replaces the files that another one uses.
exec {lock}>"${tmp}/mega-doub-build.lock"
flock "${lock}"
source_path="${BASH_SOURCE[0]}"
if [[ "${source_path}" != */* ]]; then
  source_path="./${source_path}"
fi
root="$(cd "${source_path%/*}/.." && pwd)"
# Every platform that builds with CGO_ENABLED=0; the others are left out.
platforms=(
  android/arm64
  darwin/amd64
  darwin/arm64
  freebsd/386
  freebsd/amd64
  freebsd/arm
  freebsd/arm64
  linux/386
  linux/amd64
  linux/arm
  linux/arm64
  linux/loong64
  linux/mips
  linux/mips64
  linux/mips64le
  linux/mipsle
  linux/ppc64
  linux/ppc64le
  linux/riscv64
  linux/s390x
)
if [[ "${GITHUB_REF_TYPE:-}" == 'tag' ]]; then
  version="${GITHUB_REF_NAME:?GITHUB_REF_NAME is not set for a tag build}"
else
  version="$(git -C "${root}" describe --tags --always --dirty)"
  version+=".dev.${GITHUB_RUN_NUMBER:-0}.${GITHUB_RUN_ATTEMPT:-0}"
fi
file_version="${version//[.\/]/_}"
commit="$(git -C "${root}" rev-parse --short HEAD)"
build="${tmp}/mega-doub-build"
out="${tmp}/mega-doub-out"
rm -rf "${build}" "${out}"
mkdir -p "${build}" "${out}"
cd "${root}/golang"
for platform in "${platforms[@]}"; do
  os="${platform%/*}"
  arch="${platform#*/}"
  bin="${build}/${os}-${arch}/mega-doub"
  CGO_ENABLED=0 GOOS="${os}" GOARCH="${arch}" GOARM=7 go build -trimpath -o "${bin}" \
    -ldflags "-s -w -X main.version=${version} -X main.commit=${commit}" ./cmd/mega-doub
  if [[ "${os}" == 'linux' || "${os}" == 'freebsd' ]]; then
    info="$(file -b "${bin}")"
    if [[ "${info}" != *'statically linked'* ]]; then
      echo "build.sh: ${platform} is not statically linked: ${info}" >&2
      exit 1
    fi
  fi
  archive="${out}/mega-doub-${file_version}-${os}-${arch}.tar.xz"
  tar -C "${bin%/*}" --owner=0 --group=0 --numeric-owner --mode=0755 -cJf "${archive}" \
    mega-doub
  printf '%s\n' "${archive}"
done
