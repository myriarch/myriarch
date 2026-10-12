#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Shared helpers of the CI scripts (source with `. ci/lib.sh`). No output and no side effects except defining
# functions and the variables ROOT, CI_DIR, TOOLS_BIN (also when sourced from contrib/enterprise-mirror/*.sh).
#
# Language: everything is English (ADR-0020).

CI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${CI_DIR}/.." && pwd)"
TOOLS_BIN="${TOOLS_BIN:-${ROOT}/.cache/bin}"
export CI_DIR ROOT TOOLS_BIN

log()  { printf '[ci] %s\n' "$*" >&2; }
warn() { printf '[ci] WARNING: %s\n' "$*" >&2; }
die()  { printf '[ci] ERROR: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# Section heading in the log (no folding: the scripts run the same way locally and in any CI system).
section_start() { log "== $2"; } # name title
section_end() { :; }             # name (kept so the callers stay symmetric)

# Loads ci/versions.env into the current shell (all variables are exported). Variables that are already set take
# precedence.
load_versions() {
  local line key
  while IFS= read -r line || [ -n "${line}" ]; do
    case "${line}" in ''|'#'*) continue ;; esac
    key="${line%%=*}"
    if [ -z "${!key:-}" ]; then
      export "${key}=${line#*=}"
    fi
  done < "${CI_DIR}/versions.env"
}

need_env() { # NAME...
  local n
  for n in "$@"; do
    [ -n "${!n:-}" ] || die "variable ${n} is not set"
  done
}

need_cmd() { # cmd...
  local c
  for c in "$@"; do
    have "${c}" || die "command '${c}' not found (ci/install-tools.sh ${c}?)"
  done
}

sha256_of() { # file
  if have sha256sum; then sha256sum "$1" | awk '{print $1}'
  elif have shasum; then shasum -a 256 "$1" | awk '{print $1}'
  else openssl dgst -sha256 -r "$1" | awk '{print $1}'
  fi
}

# Name (without the directory) of the stamp that ci/install-tools.sh keeps in ${TOOLS_BIN} for a tool that is built from the Go
# module ci/tools/<tool>/: .stamp-<tool>-<version>-<digest><suffix>, with the first 12 hex digits of the SHA-256 of go.mod followed
# by go.sum. A cached binary is trusted only when a stamp of exactly this name exists, so a change of either file (a raised
# requirement, for example) rebuilds the tool even when an older tool cache is restored under a new cache key.
go_tool_stamp() { # tool version [suffix]
  local dir="${CI_DIR}/tools/$1" digest
  [ -f "${dir}/go.mod" ] && [ -f "${dir}/go.sum" ] || die "ci/tools/$1/go.mod or go.sum is missing (ci/tools/update.sh $1)"
  digest="$(cat "${dir}/go.mod" "${dir}/go.sum" | sha256_of /dev/stdin)"
  [ "${#digest}" -ge 12 ] || die "cannot compute the digest of ci/tools/$1/go.mod and go.sum"
  printf '.stamp-%s-%s-%s%s' "$1" "$2" "${digest:0:12}" "${3:-}"
}

# Release version from the git tag (v1.2.3 -> 1.2.3); VERSION overrides RELEASE_TAG.
release_version() {
  local v="${VERSION:-${RELEASE_TAG:-}}"
  [ -n "${v}" ] || die "no version: set RELEASE_TAG (vX.Y.Z) or VERSION"
  v="${v#v}"
  [[ "${v}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]] || die "invalid version '${v}' (expected X.Y.Z or X.Y.Z-rc.N)"
  printf '%s' "${v}"
}

# Path of the Go binary from the goreleaser build (linux/amd64; the directory name contains the GOAMD64 level).
dist_binary() { # name
  local p="${ROOT}/dist/${1}_linux_amd64_v1/${1}"
  [ -f "${p}" ] || die "binary missing: ${p} (goreleaser build: ci/build-release.sh snapshot?)"
  printf '%s' "${p}"
}

# Stages the release files from the goreleaser dist directory under their upload names in one flat directory
# (symlinks): <out>/<name>. This is the layout of the mirror (<repo>/v<version>/<name>).
# The bare myriarch-updater binaries are in dist/myriarch-updater_<os>_<arch>_<level>/myriarch-updater[.exe]
# (goreleaser assigns the upload name myriarch-updater_<ver>_<os>_<arch>[.exe] only when uploading); their signature
# is next to them as <binary>.sig and is staged as <name>.sig.
# Included: myriarch-updater (bare and archives), the myrctl archives, checksums.txt and every matching .sig; with
# scope=all also the myriarchd and myriarch-sim archives (for the release page; a mirror does not need them).
stage_release_assets() { # dist-dir version out-dir [host|all]
  local dist ver="$2" out="$3" scope="${4:-host}" f d base rest os arch ext name n=0
  local globs=("myriarch-updater_${ver}_" "myrctl_${ver}_")
  [ "${scope}" = all ] && globs+=("myriarchd_${ver}_" "myriarch-sim_${ver}_")
  dist="$(cd "$1" && pwd)" || die "dist directory not found: $1"   # absolute: the symlinks point relative to ${out}
  mkdir -p "${out}"
  stage_link() { ln -sf "$1" "${out}/$2"; n=$((n + 1)); }
  local g
  for g in "${globs[@]}"; do
    for f in "${dist}/${g}"*.tar.gz "${dist}/${g}"*.zip; do
      [ -f "${f}" ] || continue
      stage_link "${f}" "$(basename "${f}")"
      [ -f "${f}.sig" ] && stage_link "${f}.sig" "$(basename "${f}").sig"
    done
  done
  for d in "${dist}"/myriarch-updater_*_*_*/; do
    [ -d "${d}" ] || continue
    base="$(basename "${d}")"; rest="${base#myriarch-updater_}"; os="${rest%%_*}"; rest="${rest#*_}"; arch="${rest%%_*}"
    ext=""; [ "${os}" = windows ] && ext=".exe"
    f="${d%/}/myriarch-updater${ext}"
    [ -f "${f}" ] || continue
    name="myriarch-updater_${ver}_${os}_${arch}${ext}"
    stage_link "${f}" "${name}"
    [ -f "${f}.sig" ] && stage_link "${f}.sig" "${name}.sig"
  done
  for f in checksums.txt checksums.txt.sig; do
    [ -f "${dist}/${f}" ] && stage_link "${dist}/${f}" "${f}"
  done
  [ "${n}" -gt 0 ] || die "no release files for version ${ver} found in ${dist}"
}

# Release only from reviewed history. A protected tag restricts who may create the tag, not which commit it points to:
# a maintainer could otherwise tag an unreviewed commit from any branch and have it built and signed with the release
# keys (the signature would be accepted on every host). Therefore the tagged commit must be an ancestor of (or equal to)
# the release branch tip.
# Inputs (all optional except where noted):
#   RELEASE_COMMIT  commit to check (default: HEAD of RELEASE_GIT_DIR / the repository)
#   RELEASE_TAG     tag name; if the tag exists, it must point at the checked commit
#   RELEASE_BRANCH  release branch (default: main)
#   RELEASE_REMOTE  remote the branch is fetched from (default: origin)
#   RELEASE_ALLOW_ANY_COMMIT=1  skips the check (local tests only; never set it in a release workflow)
#   RELEASE_GIT_DIR  repository to check (tests only; default: the repository root)
require_tag_on_release_branch() {
  if [ "${RELEASE_ALLOW_ANY_COMMIT:-0}" = 1 ]; then warn "RELEASE_ALLOW_ANY_COMMIT=1: the tag is not checked against the release branch"; return 0; fi
  local gd="${RELEASE_GIT_DIR:-${ROOT}}" br="${RELEASE_BRANCH:-main}" remote="${RELEASE_REMOTE:-origin}" tag="${RELEASE_TAG:-?}" sha
  have git || die "git not found (needed to check that the tag is on ${br})"
  sha="${RELEASE_COMMIT:-$(git -C "${gd}" rev-parse HEAD 2>/dev/null)}"
  [ -n "${sha}" ] || die "could not determine the commit to check: set RELEASE_COMMIT"
  # An existing tag must point at exactly the commit that is checked and built, not just at any commit on the branch.
  if git -C "${gd}" rev-parse -q --verify "refs/tags/${tag}" >/dev/null 2>&1; then
    local tag_sha want_sha
    tag_sha="$(git -C "${gd}" rev-parse "refs/tags/${tag}^{commit}")"
    want_sha="$(git -C "${gd}" rev-parse -q --verify "${sha}^{commit}" 2>/dev/null || printf '%s' "${sha}")"
    [ "${tag_sha}" = "${want_sha}" ] \
      || die "tag ${tag} points at ${tag_sha:0:12}, not at the commit being released (${want_sha:0:12})"
  fi
  if [ "$(git -C "${gd}" rev-parse --is-shallow-repository 2>/dev/null)" = true ]; then
    git -C "${gd}" fetch --quiet --no-tags --unshallow "${remote}" 2>/dev/null || die "shallow clone and --unshallow failed: fetch the full history (for example fetch-depth 0)"
  fi
  git -C "${gd}" fetch --quiet --no-tags "${remote}" "+refs/heads/${br}:refs/remotes/${remote}/${br}" \
    || die "could not fetch branch ${br} from ${remote}"
  git -C "${gd}" merge-base --is-ancestor "${sha}" "refs/remotes/${remote}/${br}" \
    || die "commit ${sha:0:12} of tag ${tag} is not on branch ${br}: release only from reviewed (merged) history; merge into ${br} first and tag there"
  log "tag ${tag}: commit ${sha:0:12} is on ${br}"
}

# curl with an extra CA bundle (CURL_CA_FILE); proxy and NO_PROXY come from the environment.
curl_ca_args() {
  local ca="${CURL_CA_FILE:-}"
  if [ -n "${ca}" ] && [ -r "${ca}" ]; then printf '%s\n' --cacert "${ca}"; fi
}

# isolate_tools <dir> <tool>...: makes <dir> the TOOLS_BIN of the rest of the script. It holds symlinks to the given tools (and their
# .stamp-* files) of the current TOOLS_BIN, and the former TOOLS_BIN leaves PATH. The release workflow runs the same way: pinned tools
# in a directory of their own that is on no PATH, found only through TOOLS_BIN. A script that is tested like this cannot rely on the
# default directory or on PATH for a pinned tool without failing here. Call it before anything else puts tools on PATH.
isolate_tools() {
  local dir="$1" old="${TOOLS_BIN}" t f p rest="" IFS_SAVE="${IFS}"
  shift
  mkdir -p "${dir}"
  for t in "$@"; do
    [ -x "${old}/${t}" ] || die "${t} is not in ${old} (ci/install-tools.sh ${t})"
    ln -s "${old}/${t}" "${dir}/${t}"
    for f in "${old}"/.stamp-"${t}"-*; do
      [ -e "${f}" ] && ln -s "${f}" "${dir}/$(basename "${f}")"
    done
  done
  IFS=:
  for p in ${PATH}; do
    [ "${p}" = "${old}" ] || rest="${rest:+${rest}:}${p}"
  done
  IFS="${IFS_SAVE}"
  TOOLS_BIN="$(cd "${dir}" && pwd)"
  PATH="${rest}"
  export TOOLS_BIN PATH
}

export PATH="${TOOLS_BIN}:${PATH}"
