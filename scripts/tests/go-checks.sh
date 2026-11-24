#!/usr/bin/env bash
# cspell:ignore duplicacy gofmt gonosumdb goproxy
# Runs the Go checks of the module in golang/: the module verification, the formatting,
# the build, vet and the tests with the race detector. GOPROXY and GONOSUMDB for the
# module duplicacy.invalid/v3 come from scripts/module-proxy.sh in the environment.
set -euo pipefail
source_path="${BASH_SOURCE[0]}"
if [[ "${source_path}" != */* ]]; then
  source_path="./${source_path}"
fi
cd "${source_path%/*}/../../golang"
go mod verify
unformatted="$(gofmt -l .)"
if [[ -n "${unformatted}" ]]; then
  printf 'unformatted files:\n%s\n' "${unformatted}"
  exit 1
fi
go build ./...
go vet ./...
go test -race ./...
