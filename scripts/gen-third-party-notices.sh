#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Generates THIRD_PARTY_NOTICES: the license texts and NOTICE files of the Go modules and the npm packages that end up in
# the release artifacts (binaries myriarchd, myrctl, myriarch-updater, myriarch-sim and the web UI embedded in myriarchd).
#
#   scripts/gen-third-party-notices.sh [--check] [--out FILE]
#
#   (no flag)    writes THIRD_PARTY_NOTICES in the repository root (or FILE)
#   --check      regenerates into a temporary file and compares it with the committed file (or FILE); on drift it
#                prints a diff and the hint to regenerate, and exits 1
#   --out FILE   output file (default: THIRD_PARTY_NOTICES in the repository root)
#
# Inputs: go-licenses reports of the release builds (4 binaries x linux/windows/darwin, no devpg tag) and of one devpg
# build (the dev/sim image), `go list -deps` of the same builds, the license.json that Vite writes for the web UI
# bundle (build.license in web/vite.config.ts), scripts/notices/third-party-texts.json (texts that no source tree has)
# and ci/license-overrides.txt. The rendering is scripts/lib/third_party_notices.py.
#
# The script writes only the output file. It needs the module cache (go mod download), go-licenses
# (ci/install-tools.sh go-licenses) and web/node_modules (cd web && pnpm install --frozen-lockfile). It builds the web UI
# into a temporary directory and never touches internal/webui/dist.
#
# A relative --out is relative to the current directory. Exit status: 0 = ok, 1 = drift (--check) or missing or inconsistent
# license data (reported by the renderer), 2 = usage or environment error (including a failed run of go-licenses, go list or vite).
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/../ci/lib.sh"

usage() {
  cat <<'USAGE'
Usage: scripts/gen-third-party-notices.sh [--check] [--out FILE]

  (no flag)    writes THIRD_PARTY_NOTICES in the repository root (or FILE)
  --check      regenerates into a temporary file and compares it with the committed file (or FILE);
               on drift it prints a diff and the hint to regenerate, and exits 1
  --out FILE   output file (default: THIRD_PARTY_NOTICES in the repository root)
  -h, --help   show this help

A relative FILE is relative to the current directory.
Exit status: 0 = ok, 1 = drift (--check) or missing license data, 2 = usage or environment error
(including a failed run of go-licenses, go list or vite).
USAGE
}
usage_error() { printf 'gen-third-party-notices: %s\n' "$1" >&2; printf 'usage: scripts/gen-third-party-notices.sh [--check] [--out FILE]\n' >&2; exit 2; }
env_error() { printf 'gen-third-party-notices: %s\n' "$1" >&2; exit 2; }

check=0
out="${ROOT}/THIRD_PARTY_NOTICES"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --check) check=1 ;;
    --out) [ "$#" -ge 2 ] || usage_error "--out needs a file"; out="$2"; shift ;;
    --out=*) out="${1#--out=}" ;;
    -h|--help) usage; exit 0 ;;
    *) usage_error "unknown argument: $1" ;;
  esac
  shift
done
[ -n "${out}" ] || usage_error "--out needs a file"
# a relative --out is relative to the caller's directory (the script changes into the repository root later)
case "${out}" in
  /*) ;;
  *)
    if [ -d "$(dirname "${out}")" ]; then
      out="$(cd "$(dirname "${out}")" && pwd)/$(basename "${out}")"
    else
      out="${PWD}/${out}"
    fi ;;
esac

hint='run scripts/gen-third-party-notices.sh (make notices)'
if [ "${check}" = 1 ] && [ ! -f "${out}" ]; then
  printf 'gen-third-party-notices: %s does not exist\n%s\n' "${out#"${ROOT}"/}" "${hint}" >&2
  exit 1
fi

have go || env_error "go not found"
have go-licenses || env_error "go-licenses not found in ${TOOLS_BIN} (ci/install-tools.sh go-licenses)"
have python3 || env_error "python3 not found"
vite="${ROOT}/web/node_modules/.bin/vite"
[ -x "${vite}" ] || env_error "web/node_modules/.bin/vite is missing (cd web && pnpm install --frozen-lockfile)"

work="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-notices.XXXXXX")"
trap 'rm -rf "${work}"' EXIT
cd "${ROOT}"

ui_tags=""
if [ -f internal/webui/dist/index.html ]; then ui_tags="embedui"; fi

tpl="${work}/report.tmpl"
printf '{{ range . }}{{ .Name }}\t{{ .Version }}\t{{ .LicenseName }}\t{{ .LicensePath }}\n{{ end }}' > "${tpl}"
# go list: packages of modules (own packages included, the renderer only reads those of the report) and time/tzdata
list_tpl='{{ if .Module }}{{ .ImportPath }}{{ "\t" }}{{ .Module.Path }}{{ "\t" }}{{ .Module.Version }}{{ "\t" }}{{ .Module.Dir }}{{ "\n" }}{{ else if eq .ImportPath "time/tzdata" }}{{ .ImportPath }}{{ "\t\t\t\n" }}{{ end }}'

reports="${work}/reports.tsv"
packages="${work}/packages.tsv"
: > "${reports}"; : > "${packages}"

collect() { # goos binary build tags
  local goos="$1" bin="$2" build="$3" tags="$4" r l
  r="${work}/r.out"; l="${work}/l.out"
  if ! CGO_ENABLED=0 GOOS="${goos}" GOFLAGS="${tags:+-tags=${tags}}" go-licenses report "./cmd/${bin}" \
      --ignore github.com/myriarch/myriarch --template "${tpl}" >"${r}" 2>"${work}/r.err"; then
    tail -n 20 "${work}/r.err" >&2
    env_error "go-licenses failed for ./cmd/${bin} (GOOS=${goos}, ${build})"
  fi
  [ -s "${r}" ] || env_error "go-licenses reported no packages for ./cmd/${bin} (GOOS=${goos}); is the binary built without -trimpath?"
  if ! CGO_ENABLED=0 GOOS="${goos}" GOFLAGS="${tags:+-tags=${tags}}" go list -deps -f "${list_tpl}" "./cmd/${bin}" >"${l}" 2>"${work}/l.err"; then
    tail -n 20 "${work}/l.err" >&2
    env_error "go list failed for ./cmd/${bin} (GOOS=${goos}, ${build})"
  fi
  awk -F'\t' -v b="${bin}" -v k="${build}" 'NF > 0 { print b "\t" k "\t" $0 }' "${r}" >> "${reports}"
  awk -F'\t' -v b="${bin}" -v k="${build}" 'NF > 0 { print b "\t" k "\t" $0 }' "${l}" >> "${packages}"
}

section_start notices "go-licenses and go list (release builds)"
for goos in linux windows darwin; do
  for bin in myriarchd myrctl myriarch-updater myriarch-sim; do
    tags=""
    if [ "${bin}" = myriarchd ]; then tags="${ui_tags}"; fi
    collect "${goos}" "${bin}" release "${tags}"
  done
done
# The dev/sim image ships a devpg build of myriarchd (embedded PostgreSQL for `myriarchd dev`).
collect linux myriarchd devpg "${ui_tags:+${ui_tags},}devpg"
section_end notices

section_start notices "web UI license output (vite build)"
(cd web && "${vite}" build --outDir "${work}/ui" --emptyOutDir --logLevel warn >"${work}/vite.log" 2>&1) \
  || { tail -n 30 "${work}/vite.log" >&2; env_error "vite build failed"; }
[ -f "${work}/ui/.vite/license.json" ] || env_error "vite did not write .vite/license.json (build.license in web/vite.config.ts?)"
section_end notices

section_start notices "render"
rc=0
python3 -I "${ROOT}/scripts/lib/third_party_notices.py" \
  --reports "${reports}" --packages "${packages}" --vite-license "${work}/ui/.vite/license.json" \
  --texts "${ROOT}/scripts/notices/third-party-texts.json" --overrides "${ROOT}/ci/license-overrides.txt" \
  --goroot "$(go env GOROOT)" --out "${work}/THIRD_PARTY_NOTICES" || rc=$?
case "${rc}" in
  0) ;;
  1) exit 1 ;;
  *) env_error "the renderer failed (exit ${rc})" ;;
esac
section_end notices

if [ "${check}" = 1 ]; then
  if cmp -s "${work}/THIRD_PARTY_NOTICES" "${out}"; then
    log "${out#"${ROOT}"/} is up to date"
    exit 0
  fi
  diff -u --label "${out#"${ROOT}"/} (committed)" --label "(regenerated)" "${out}" "${work}/THIRD_PARTY_NOTICES" | head -n 80 || true
  printf '\n%s: out of date\n%s\n' "${out#"${ROOT}"/}" "${hint}" >&2
  exit 1
fi
mkdir -p "$(dirname "${out}")"
cp "${work}/THIRD_PARTY_NOTICES" "${out}"
log "wrote ${out#"${ROOT}"/}"
