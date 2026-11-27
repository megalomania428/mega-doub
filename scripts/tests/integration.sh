#!/usr/bin/env bash
# cspell:ignore doub duplicacy gdrive gonosumdb goproxy gotest gotestsum mktemp rustfs
# cspell:ignore testdox
# Runs the integration tests of golang/integration, the Go tests with the build tag
# integration, for the storage of the only argument: local, rustfs, gdrive or all, the
# default; all is local for now. GOPROXY and GONOSUMDB come from scripts/module-proxy.sh
# and the duplicacy CLIs from scripts/tests/integration-tools.sh; the tests take the
# binary of mega-doub from MEGA_DOUB_BIN or build one. gotestsum prints the format
# testdox, or github-actions in GitHub Actions, and the summary of the tests follows.
# MEGA_DOUB_IT_DEMO=1 also shows the output of mega-doub on the terminal.
# MEGA_DOUB_IT_PAUSE=1 also stops the tests at five points until a key is pressed, so
# they run without a timeout.
set -euo pipefail
gotestsum='gotest.tools/gotestsum@v1.13.0'
if [[ $# -gt 1 ]]; then
  echo 'integration.sh: at most one argument: local, rustfs, gdrive or all' >&2
  exit 2
fi
storage="all"
if [[ $# -eq 1 ]]; then
  storage="$1"
fi
case "${storage}" in
local | all)
  # The tests of the helpers need no storage and run with every storage.
  tests='^(Test_.*|TestLocal)$'
  ;;
rustfs | gdrive)
  echo "integration.sh: the storage ${storage} is not implemented yet" >&2
  exit 2
  ;;
*)
  echo "integration.sh: unknown storage [${storage}]," \
    'want local, rustfs, gdrive or all' >&2
  exit 2
  ;;
esac
tmp="${RUNNER_TEMP:-/tmp}"
if [[ "${tmp}" != /* ]]; then
  echo "integration.sh: [${tmp}] is not an absolute path" >&2
  exit 2
fi
mkdir -p "${tmp}"
source_path="${BASH_SOURCE[0]}"
if [[ "${source_path}" != */* ]]; then
  source_path="./${source_path}"
fi
scripts="$(cd "${source_path%/*}/.." && pwd)"
proxy="$("${scripts}/module-proxy.sh")"
tools="$("${scripts}/tests/integration-tools.sh")"
mapfile -t variables <<<"${proxy}"$'\n'"${tools}"
export "${variables[@]}"
summary="$(mktemp -p "${tmp}" mega-doub-it-summary.XXXXXX)"
trap 'rm -f "${summary}"' EXIT
export MEGA_DOUB_IT_SUMMARY="${summary}"
format='testdox'
if [[ "${GITHUB_ACTIONS:-}" == 'true' ]]; then
  format='github-actions'
fi
test_timeout='30m'
if [[ "${MEGA_DOUB_IT_PAUSE:-}" == '1' ]]; then
  test_timeout='0'
fi
cd "${scripts}/../golang"
status=0
go run "${gotestsum}" --format "${format}" -- -tags=integration -count=1 \
  -timeout="${test_timeout}" -run "${tests}" ./integration/ || status=$?
if [[ -s "${summary}" ]]; then
  printf '\nSummary:\n'
  cat "${summary}"
fi
exit "${status}"
