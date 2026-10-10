#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Web UI: pnpm install --frozen-lockfile, lint, typecheck, test, build (writes internal/webui/dist) and a
# drift check of the generated API schema (pnpm gen:api == web/src/api/schema.d.ts; without git, via cmp).
#
# Environment: NPM_CONFIG_REGISTRY (internal npm remote), COREPACK_NPM_REGISTRY (where corepack fetches pnpm; default:
# same as NPM_CONFIG_REGISTRY), PNPM_STORE_DIR (default .cache/pnpm).
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "${ROOT}/web"
need_cmd node

want="$(sed -n 's/.*"packageManager": *"pnpm@\([0-9][0-9.]*\)".*/\1/p' package.json | head -n 1)"
[ -n "${want}" ] || die "packageManager (pnpm@x.y.z) is missing in web/package.json"
export COREPACK_NPM_REGISTRY="${COREPACK_NPM_REGISTRY:-${NPM_CONFIG_REGISTRY:-}}"
[ -n "${COREPACK_NPM_REGISTRY}" ] || unset COREPACK_NPM_REGISTRY
export COREPACK_ENABLE_DOWNLOAD_PROMPT=0 CI=true
export npm_config_store_dir="${PNPM_STORE_DIR:-${ROOT}/.cache/pnpm}"

if ! { have pnpm && [ "$(pnpm --version 2>/dev/null)" = "${want}" ]; }; then
  if have corepack; then
    corepack enable 2>/dev/null || true
    corepack prepare "pnpm@${want}" --activate || warn "corepack could not provide pnpm@${want}"
  fi
  if ! { have pnpm && [ "$(pnpm --version 2>/dev/null)" = "${want}" ]; }; then
    log "corepack failed: installing pnpm@${want} through npm (NPM_CONFIG_REGISTRY)"
    npm install --global "pnpm@${want}"
  fi
fi
log "pnpm $(pnpm --version), node $(node --version)"

section_start install "pnpm install --frozen-lockfile"
pnpm install --frozen-lockfile
section_end install
section_start lint "pnpm lint / typecheck"
pnpm lint
pnpm typecheck
section_end lint
section_start test "pnpm test"
pnpm test
section_end test

# Schema drift: gen:api must not change web/src/api/schema.d.ts
section_start drift "API schema drift"
cp src/api/schema.d.ts "${ROOT}/.cache/schema.d.ts.orig" 2>/dev/null || { mkdir -p "${ROOT}/.cache"; cp src/api/schema.d.ts "${ROOT}/.cache/schema.d.ts.orig"; }
pnpm gen:api
if ! cmp -s "${ROOT}/.cache/schema.d.ts.orig" src/api/schema.d.ts; then
  diff -u "${ROOT}/.cache/schema.d.ts.orig" src/api/schema.d.ts | head -n 60 >&2 || true
  die "web/src/api/schema.d.ts is out of date: run 'pnpm gen:api' and commit the result"
fi
section_end drift

section_start build "pnpm build"
pnpm build
section_end build
[ -f "${ROOT}/internal/webui/dist/index.html" ] || die "pnpm build did not create internal/webui/dist/index.html"
log "Web UI ok"
