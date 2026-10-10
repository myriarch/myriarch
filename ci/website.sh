#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Builds, serves and checks the project website (website/, ADR-0031): Hugo extended with the Docsy theme, built from the sources
# under website/ and the Markdown files of the repository that it mounts (docs/, the root files).
#
#   ci/website.sh build      production build into website/public (Hugo with --panicOnWarning)
#   ci/website.sh serve      Hugo development server on 127.0.0.1:${WEBSITE_PORT:-1313}
#   ci/website.sh preview    production build, served as static files on 127.0.0.1:${WEBSITE_PORT:-1313} (Lighthouse, request check)
#   ci/website.sh check      build plus every check of the website gate (see below)
#
# Needs go (Hugo modules fetch the Docsy theme), node, pnpm and python3; the check subcommand needs Node.js 22 or newer.
#
# Every subcommand puts ${TOOLS_BIN} on PATH, installs the pinned tools (hugo, sass, lychee; ci/install-tools.sh) and the Node
# dependencies of website/ (pnpm install --frozen-lockfile), and keeps Hugo's module cache in .cache/hugo. The first run needs network
# access (Go proxy, npm registry, release downloads); later runs use the caches.
#
# check: hugo mod verify, lychee over the built pages (offline, with fragments), markdownlint over the Markdown sources, the front
# matter of the site content, every website/scripts/check-*.py over the built site and the unit tests of those checks.
#
# Environment:
#   WEBSITE_PORT      port of serve and preview (default 1313; never 8080 or the ports of the end-to-end tests)
#   WEBSITE_BASEURL   overrides the base URL of the build (previews on a host with another name)
#   WEBSITE_ENV       production (default) or preview. Both build with the Hugo environment "production" (minified, fingerprinted assets,
#                     no source maps), so that a preview ships the same bytes as production; preview only sets the site parameter
#                     params.preview (noindex on every page, "Disallow: /" in robots.txt, no sitemap line)
#   TOOLS_BIN         directory of the pinned tools (default .cache/bin; a git worktree points it at the main working copy)
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_versions
cd "${ROOT}"

usage() {
  cat <<'USAGE' >&2
usage: ci/website.sh <build|serve|preview|check>
  build     production build into website/public
  serve     Hugo development server (127.0.0.1:${WEBSITE_PORT:-1313})
  preview   production build served as static files (127.0.0.1:${WEBSITE_PORT:-1313})
  check     build and run every check of the website gate
USAGE
}

cmd="${1:-}"
case "${cmd}" in
  build|serve|preview|check) ;;
  -h|--help) usage; exit 0 ;;
  *) usage; exit 2 ;;
esac
[ "$#" -le 1 ] || { usage; exit 2; }

PORT="${WEBSITE_PORT:-1313}"
case "${PORT}" in
  ''|*[!0-9]*) die "WEBSITE_PORT must be a number" ;;
  8080|18080|18081|54339|1946[4-9]|19998|19999) die "WEBSITE_PORT ${PORT} is reserved (development server, end-to-end tests)" ;;
esac
WEBSITE_ENV="${WEBSITE_ENV:-production}"
case "${WEBSITE_ENV}" in
  production) unset HUGO_PARAMS_PREVIEW ;;
  preview) export HUGO_PARAMS_PREVIEW=true ;;
  *) die "WEBSITE_ENV must be production or preview" ;;
esac
export WEBSITE_ENV

export PATH="${TOOLS_BIN}:${PATH}"
export HUGO_CACHEDIR="${ROOT}/.cache/hugo"
export GOWORK=off
mkdir -p "${HUGO_CACHEDIR}"

"${CI_DIR}/install-tools.sh" hugo sass lychee
need_cmd go node pnpm python3
log "installing the Node dependencies of website/ (pnpm install --frozen-lockfile)"
(cd website && CI=true pnpm install --frozen-lockfile) >&2 || die "pnpm install failed in website/ (network access? pnpm-lock.yaml up to date?)"

hugo_build() {
  local args=(--source website --environment production --gc --minify --panicOnWarning --cleanDestinationDir --destination public)
  if [ -n "${WEBSITE_BASEURL:-}" ]; then args+=(--baseURL "${WEBSITE_BASEURL}"); fi
  log "hugo ${args[*]}"
  hugo "${args[@]}"
}

# The Markdown sources that the website renders (website/content, docs/ and the root files that website/data/repo_pages.yaml mounts).
markdown_globs() {
  printf '%s\n' 'docs/**/*.md' 'website/content/**/*.md' website/README.md \
    ROADMAP.md CHANGELOG.md CONTRIBUTING.md GOVERNANCE.md SECURITY.md CODE_OF_CONDUCT.md MAINTAINERS.md ADOPTERS.md CONTRIBUTOR_LADDER.md
  local extra
  for extra in deploy/helm/myriarch/README.md gitops-template/README.md assets/brand/README.md; do
    if grep -Fq "${extra}" website/data/repo_pages.yaml 2>/dev/null; then printf '%s\n' "${extra}"; fi
  done
}

website_check() {
  # markdownlint-cli2 declares Node.js 22 or newer; pnpm does not enforce "engines", so the gate says it before it fails
  local node_major
  node_major="$(node -p 'process.versions.node.split(".")[0]')"
  [ "${node_major}" -ge 22 ] 2>/dev/null || die "the website gate needs Node.js 22 or newer (markdownlint-cli2); found $(node --version)"
  hugo_build
  log "hugo mod verify"
  (cd website && hugo mod verify)
  log "lychee (offline, with fragments)"
  lychee --config website/lychee.toml --offline --include-fragments --index-files index.html \
    --root-dir "${ROOT}/website/public" --no-progress 'website/public/**/*.html'
  log "markdownlint"
  local globs=()
  while IFS= read -r g; do globs+=("${g}"); done < <(markdown_globs)
  website/node_modules/.bin/markdownlint-cli2 --config website/.markdownlint-cli2.jsonc "${globs[@]}"
  log "front matter of website/content"
  python3 -I scripts/check-docs.py front-matter --site website/content
  local chk
  for chk in $(find website/scripts -maxdepth 1 -name 'check-*.py' -type f | sort); do
    log "$(basename "${chk}")"
    python3 -I "${chk}" website/public
  done
  log "unit tests of the checks"
  python3 -I -m unittest discover -s website/scripts -p 'test_*.py'
  log "website: all checks passed"
}

case "${cmd}" in
  build) hugo_build ;;
  serve)
    log "hugo server on http://127.0.0.1:${PORT}/ (Ctrl-C stops it)"
    exec hugo server --source website --bind 127.0.0.1 --port "${PORT}" --disableFastRender
    ;;
  preview)
    hugo_build
    log "serving website/public on http://127.0.0.1:${PORT}/ (Ctrl-C stops it)"
    exec python3 -I website/scripts/preview-server.py --port "${PORT}" --bind 127.0.0.1 --directory website/public
    ;;
  check) website_check ;;
esac
