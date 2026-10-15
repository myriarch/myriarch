#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Vulnerability gate: runs govulncheck over the release build and the development build (-tags devpg) and fails when
# a vulnerability is CALLED by the code (symbol level) and is not in ci/vulncheck-allowlist.txt. Findings at module or
# package level (a required module has a known vulnerability, the code does not call it) are listed but do not fail;
# list them in the allowlist when they are accepted, with a reason and a review date. An entry past its review date is
# reported as a warning.
#
#   ci/vulncheck.sh                         both builds, the gate
#   ci/vulncheck.sh --sarif DIR             both builds, but no gate: writes the reports for GitHub code scanning to DIR
#                                           (govulncheck-source-release.sarif and govulncheck-source-devpg.sarif, without the
#                                           findings that the allowlist accepts) and exits 0 whatever it found; only a failing tool
#                                           fails. The workflow .github/workflows/vulncheck.yml uploads them; the gate above stays the
#                                           check that blocks (job compliance of ci.yml).
#   GOVULNCHECK=/path/to/govulncheck ...    other binary (default: govulncheck in PATH or .cache/bin)
#   VULNCHECK_ALLOWLIST=file ...            other allowlist
#
# The vulnerability database is read from vuln.go.dev: without network access skip the gate (SKIP=vulncheck in
# ci/check-local.sh). The tool is built by ci/install-tools.sh govulncheck.
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "${ROOT}"
export PATH="${TOOLS_BIN}:${PATH}"

sarif_dir=""
case "$#" in
  0) ;;
  2) [ "$1" = --sarif ] || die "usage: ci/vulncheck.sh [--sarif DIR]"; sarif_dir="$2" ;;
  *) die "usage: ci/vulncheck.sh [--sarif DIR]" ;;
esac

tool="${GOVULNCHECK:-govulncheck}"
have "${tool}" || [ -x "${tool}" ] || die "govulncheck not found: run ci/install-tools.sh govulncheck"
allowlist="${VULNCHECK_ALLOWLIST:-${CI_DIR}/vulncheck-allowlist.txt}"
[ -f "${allowlist}" ] || die "allowlist ${allowlist} is missing"

work="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-vulncheck.XXXXXX")"
trap 'rm -rf "${work}"' EXIT

runs=("release:" "devpg:-tags devpg")

if [ -n "${sarif_dir}" ]; then
  # govulncheck exits 0 in the formats json and sarif whatever it found, so a non-zero status is a failure of the tool.
  mkdir -p "${sarif_dir}"
  for run in "${runs[@]}"; do
    name="${run%%:*}"; flags="${run#*:}"
    log "govulncheck -format sarif (${name} build)"
    # shellcheck disable=SC2086
    "${tool}" -format sarif ${flags} ./... > "${work}/${name}.sarif" 2> "${work}/${name}.err" \
      || { cat "${work}/${name}.err" >&2; die "govulncheck failed for the ${name} build (no network access?)"; }
    python3 -I "${CI_DIR}/vulncheck-sarif.py" filter "${allowlist}" "${work}/${name}.sarif" "${sarif_dir}/govulncheck-source-${name}.sarif" \
      || die "the report of the ${name} build is not a SARIF report that code scanning accepts"
  done
  log "SARIF reports written to ${sarif_dir}"
  exit 0
fi

files=()
for run in "${runs[@]}"; do
  name="${run%%:*}"; flags="${run#*:}"
  log "govulncheck (${name} build)"
  # shellcheck disable=SC2086
  "${tool}" -format json ${flags} ./... > "${work}/${name}.json" 2> "${work}/${name}.err" \
    || { cat "${work}/${name}.err" >&2; die "govulncheck failed for the ${name} build (no network access? SKIP=vulncheck skips the gate)"; }
  files+=("${work}/${name}.json")
done

python3 -I "${CI_DIR}/vulncheck-report.py" "${allowlist}" "${files[@]}"
