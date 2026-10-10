#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Release license check: runs go-licenses over the release builds (no devpg build tag; linux, windows, darwin) and
# fails if a linked package carries a license outside the allowlist, unless the module has a documented exception.
#
#   ci/check-release-licenses.sh
#
# Needs go-licenses (ci/install-tools.sh go-licenses) and the module cache (go mod download).
# Allowlist: Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC, MIT, Zlib. Exceptions: ci/license-overrides.txt.
# Exit status: 0 = clean, non-zero = at least one finding (all findings are printed) or an environment error.
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "${ROOT}"
need_cmd go
export PATH="${TOOLS_BIN}:${PATH}"
have go-licenses || die "go-licenses is missing (ci/install-tools.sh go-licenses)"

ALLOWED='^(Apache-2\.0|BSD-2-Clause|BSD-3-Clause|ISC|MIT|Zlib)$'

# Documented exceptions: ci/license-overrides.txt ("<package path prefix>|<license go-licenses reports>|<license to declare>|
# <license file>|<scope>|<reason>", shared with scripts/gen-third-party-notices.sh). An exception is a prefix match plus the
# license that go-licenses reports. An exception with an explicit package list as scope fails closed: a linked package of the
# prefix outside the list is a finding, because go-licenses reports the module root for every package of the module. Every other
# finding fails. A malformed line, or a declared license outside the allowlist, fails.
OVERRIDES="${LICENSE_OVERRIDES:-${CI_DIR}/license-overrides.txt}"   # LICENSE_OVERRIDES: tests only
[ -f "${OVERRIDES}" ] || die "license overrides file is missing: ${OVERRIDES}"
EXC_PREFIX=(); EXC_LICENSE=(); EXC_SCOPE=()
overrides_name="$(basename "${OVERRIDES}")"
lineno=0
while IFS= read -r line || [ -n "${line}" ]; do
  lineno=$((lineno + 1))
  case "${line}" in ''|'#'*) continue ;; esac
  IFS='|' read -r o_prefix o_reported o_declared o_file o_scope o_reason <<<"${line}"
  if [ -z "${o_prefix}" ] || [ -z "${o_reported}" ] || [ -z "${o_declared}" ] || [ -z "${o_file}" ] || [ -z "${o_scope}" ] || [ -z "${o_reason}" ]; then
    die "${overrides_name}:${lineno}: malformed entry (expected six non-empty fields: prefix|reported|declared|file|scope|reason)"
  fi
  grep -Eq "${ALLOWED}" <<<"${o_declared}" \
    || die "${overrides_name}:${lineno}: declared license ${o_declared} is not on the allowlist"
  EXC_PREFIX+=("${o_prefix}"); EXC_LICENSE+=("${o_reported}"); EXC_SCOPE+=("${o_scope}")
done < "${OVERRIDES}"

findings=0
fail() { printf '[ci] FINDING: %s\n' "$*" >&2; findings=$((findings + 1)); }

excepted() { # package license
  local i prefix
  for i in "${!EXC_PREFIX[@]}"; do
    prefix="${EXC_PREFIX[${i}]}"
    if [ "${EXC_LICENSE[${i}]}" = "$2" ] && { [ "$1" = "${prefix}" ] || [ "${1#"${prefix}/"}" != "$1" ]; }; then return 0; fi
  done
  return 1
}

ui_tags=""
if [ -f internal/webui/dist/index.html ]; then ui_tags="embedui"; fi

# Scope check of the exceptions with an explicit package list (fail closed, see above): every package that a release build
# links below the prefix of such an exception must be in its list.
scoped_exceptions=0
for i in "${!EXC_SCOPE[@]}"; do [ "${EXC_SCOPE[${i}]}" = "*" ] || scoped_exceptions=1; done
check_scopes() { # goos cmd tags
  local deps i prefix pkg scope item listed items
  deps="$(CGO_ENABLED=0 GOOS="$1" GOFLAGS="${3:+-tags=$3}" go list -deps -f '{{ .ImportPath }}' "./cmd/$2" 2>/dev/null)" \
    || die "go list failed for ./cmd/$2 (GOOS=$1)"
  for i in "${!EXC_SCOPE[@]}"; do
    scope="${EXC_SCOPE[${i}]}"
    [ "${scope}" != "*" ] || continue
    prefix="${EXC_PREFIX[${i}]}"
    while IFS= read -r pkg; do
      [ "${pkg}" = "${prefix}" ] || [ "${pkg#"${prefix}/"}" != "${pkg}" ] || continue
      listed=0
      IFS=',' read -r -a items <<<"${scope}"
      for item in "${items[@]}"; do [ "${item}" = "${pkg}" ] && listed=1; done
      [ "${listed}" = 1 ] || fail "./cmd/$2 (GOOS=$1) links ${pkg}, which is outside the scope of the license exception for ${prefix}"
    done <<<"${deps}"
  done
}

tpl="$(mktemp)"; out="$(mktemp)"
trap 'rm -f "${tpl}" "${out}"' EXIT
printf '{{ range . }}{{ .Name }},{{ .LicenseName }}\n{{ end }}' > "${tpl}"

if [ "${scoped_exceptions}" = 1 ]; then
  section_start licenses "scope of the license exceptions"
  for goos in linux windows darwin; do
    for cmd in myriarchd myriarch-updater myrctl myriarch-sim; do
      tags=""
      if [ "${cmd}" = myriarchd ]; then tags="${ui_tags}"; fi
      check_scopes "${goos}" "${cmd}" "${tags}"
    done
  done
  section_end licenses
  [ "${findings}" -eq 0 ] || die "${findings} finding(s): release builds link packages outside the scope of a license exception"
fi

section_start licenses "go-licenses (no devpg tag)"
for goos in linux windows darwin; do
  for cmd in myriarchd myriarch-updater myrctl myriarch-sim; do
    tags=""
    if [ "${cmd}" = myriarchd ]; then tags="${ui_tags}"; fi
    # own packages are ignored; "no license found" warnings of the tool are expected for them
    if ! CGO_ENABLED=0 GOOS="${goos}" GOFLAGS="${tags:+-tags=${tags}}" go-licenses report "./cmd/${cmd}" \
        --ignore github.com/myriarch/myriarch --template "${tpl}" >"${out}" 2>/dev/null; then
      die "go-licenses failed for ./cmd/${cmd} (GOOS=${goos})"
    fi
    # go-licenses exits 0 with an empty report when it cannot resolve GOROOT (binary built with -trimpath); never pass on that
    [ -s "${out}" ] || die "go-licenses reported no packages for ./cmd/${cmd} (GOOS=${goos}); is the binary built without -trimpath?"
    while IFS=, read -r pkg lic; do
      [ -n "${pkg}" ] || continue
      if grep -Eq "${ALLOWED}" <<<"${lic}"; then continue; fi
      if excepted "${pkg}" "${lic}"; then continue; fi
      fail "./cmd/${cmd} (GOOS=${goos}) links ${pkg} with license ${lic}"
    done < <(sort -u "${out}")
  done
done
section_end licenses

if [ "${findings}" -gt 0 ]; then
  die "${findings} finding(s): release builds link packages with licenses outside the allowlist"
fi
log "Release license check ok"
