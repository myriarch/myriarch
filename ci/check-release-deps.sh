#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Release dependency guard: release builds (no devpg build tag; linux, windows, darwin) must not link modules whose
# licenses are not on the allowlist, and the UI bundle must not contain font assets.
#
#   ci/check-release-deps.sh
#
# Checks:
#   1. `go list -deps` of ./cmd/myriarchd (tag embedui only if internal/webui/dist/index.html exists), ./cmd/myriarch-updater,
#      ./cmd/myrctl and ./cmd/myriarch-sim for GOOS linux, windows and darwin, WITHOUT the devpg tag, lists none of:
#        github.com/fergusstrange/embedded-postgres, github.com/xi2/xz, github.com/hashicorp/golang-lru
#   2. web/package.json does not mention @fontsource;
#   3. internal/webui/dist (when present) contains no *.woff, *.woff2, *.ttf, *.otf or *.eot file.
#
# Exit status: 0 = clean, non-zero = at least one finding (all findings are printed) or an environment error.
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "${ROOT}"
need_cmd go

FORBIDDEN_MODULES=(
  github.com/fergusstrange/embedded-postgres
  github.com/xi2/xz
  github.com/hashicorp/golang-lru
)
GOOSES=(linux windows darwin)
COMMANDS=(myriarchd myriarch-updater myrctl myriarch-sim)

findings=0

fail() { # message
  printf '[ci] FINDING: %s\n' "$*" >&2
  findings=$((findings + 1))
}

ui_tags=""
if [ -f internal/webui/dist/index.html ]; then ui_tags="embedui"; fi

section_start deps "go list -deps (no devpg tag)"
for goos in "${GOOSES[@]}"; do
  for cmd in "${COMMANDS[@]}"; do
    tags=""
    if [ "${cmd}" = myriarchd ]; then tags="${ui_tags}"; fi
    # the import graph depends on GOOS only; CGO_ENABLED=0 matches the release builds
    if ! deps="$(CGO_ENABLED=0 GOOS="${goos}" go list -tags "${tags}" -deps -f '{{with .Module}}{{.Path}}{{end}}' "./cmd/${cmd}")"; then
      die "go list failed for ./cmd/${cmd} (GOOS=${goos})"
    fi
    for mod in "${FORBIDDEN_MODULES[@]}"; do
      # match the module path itself and its sub-modules (e.g. github.com/hashicorp/golang-lru/v2)
      if grep -Eqx "${mod}(/.*)?" <<<"${deps}"; then
        # name the first importing package so the finding is actionable
        importer="$(CGO_ENABLED=0 GOOS="${goos}" go list -tags "${tags}" -deps -f '{{.ImportPath}} {{join .Imports " "}}' "./cmd/${cmd}" \
          | awk -v m="${mod}" 'index($1, m) != 1 && index($0, " " m) { print $1; exit }')"
        fail "./cmd/${cmd} (GOOS=${goos}${tags:+, tags=${tags}}) links ${mod}${importer:+ via ${importer}}"
      fi
    done
  done
done
section_end deps

section_start fonts "font assets"
if grep -q '@fontsource' web/package.json; then
  fail "web/package.json mentions @fontsource (OFL fonts must not be bundled)"
fi
if [ -d internal/webui/dist ]; then
  fonts="$(find internal/webui/dist -type f \( -name '*.woff' -o -name '*.woff2' -o -name '*.ttf' -o -name '*.otf' -o -name '*.eot' \))"
  if [ -n "${fonts}" ]; then
    fail "internal/webui/dist contains font files: $(tr '\n' ' ' <<<"${fonts}")"
  fi
else
  log "internal/webui/dist is missing: bundle check skipped (build the UI with make web-build)"
fi
section_end fonts

if [ "${findings}" -gt 0 ]; then
  die "${findings} finding(s): release builds link or bundle forbidden dependencies"
fi
log "Release dependency check ok"
