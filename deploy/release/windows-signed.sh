#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Windows version resources and the Authenticode stages of the release build. goreleaser calls the subcommands as hooks
# (deploy/release/goreleaser.yaml); the contract is described in deploy/release/README.md.
#
#   prepare <version> <is-snapshot>                          before hook: version resources, staging area, marker
#   build <id> <path> <os> <arch> <is-snapshot> <version>    post hook of myriarch-updater and myrctl, per target
#   seal                                                     after goreleaser in stage collect: the unsigned set
#   check-dist [dist-dir]                                    after goreleaser in stage collect or apply
#   clean                                                    removes the generated version resources (needs no stage)
#
# MYRIARCH_WINDOWS_STAGE (required, validated first in every subcommand but clean):
#
#   off      snapshots only. Nothing is collected or checked; the Windows files are the build as it is.
#   collect  the post-build hook copies the four .exe files to the unsigned directory; seal adds the two scripts and SHA256SUMS.
#            The unsigned directory is then the input of ci/release/windows-sign-hook.sh.
#   apply    the post-build hook accepts the files that the hook returned (signed directory): the rebuilt .exe has to equal the
#            unsigned build, then the signature of the provider's file is attached to it and verified (ci/release/authenticode.py).
#            The before hook does the same for the two scripts. Anything else fails closed.
#
# Environment:
#   MYRIARCH_WINDOWS_UNSIGNED_DIR   default <root>/.cache/release/windows-unsigned
#   MYRIARCH_WINDOWS_SIGNED_DIR     default <root>/.cache/release/windows-signed
#   MYRIARCH_AUTHENTICODE_DIR       trust anchors, default <root>/deploy/release/authenticode; only snapshot builds may set it
#   MYRIARCH_WINDOWS_SIGNING        stage apply: the configured signing mode (off, signpath or kms; the release workflow parses it from
#                                   deploy/release/windows-signing.env). Required for release builds; signer.json has to name the same
#                                   provider ([mode_missing], [provider_mismatch]). Snapshots may leave it unset.
#   TOOLS_BIN                       default <root>/.cache/bin (go-winres, osslsigncode)
# The unsigned and signed directories are staging areas: inside the work tree they have to be git-ignored (use .cache/ or a path
# outside the repository).
#
# State: <root>/.cache/release/windows (recreated by prepare). stage = the marker of the before hook; accepted/<name>.sha256 =
# the files that the hooks accepted (what check-dist expects in dist/); archive/windows/ = the Windows scripts for the archives.
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/../../ci/lib.sh"

STATE="${ROOT}/.cache/release/windows"
AUTH="${ROOT}/ci/release/authenticode.py"
UNSIGNED_DIR="${MYRIARCH_WINDOWS_UNSIGNED_DIR:-${ROOT}/.cache/release/windows-unsigned}"
SIGNED_DIR="${MYRIARCH_WINDOWS_SIGNED_DIR:-${ROOT}/.cache/release/windows-signed}"
TRUST_DIR="${MYRIARCH_AUTHENTICODE_DIR:-${ROOT}/deploy/release/authenticode}"
VERSION_RE='^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$'
SYSO_GLOBS=("${ROOT}"/cmd/myriarch-updater/rsrc_windows_*.syso "${ROOT}"/cmd/myrctl/rsrc_windows_*.syso)

check_stage_value() {
  case "${MYRIARCH_WINDOWS_STAGE-}" in
    '') die "[stage_missing] MYRIARCH_WINDOWS_STAGE is not set: off (snapshots only), collect or apply" ;;
    off|collect|apply) ;;
    *) die "[stage_invalid] MYRIARCH_WINDOWS_STAGE=${MYRIARCH_WINDOWS_STAGE} is not off, collect or apply" ;;
  esac
}

check_stage() { # is-snapshot
  check_stage_value
  case "${1}" in
    true|false) ;;
    *) die "[snapshot_invalid] is-snapshot is '${1}', expected true or false" ;;
  esac
  if [ "${MYRIARCH_WINDOWS_STAGE}" = off ] && [ "${1}" != true ]; then
    die "[stage_off_release] stage off is for snapshot builds only; a release build uses collect or apply"
  fi
}

abs_path() { # path - absolute path with symlinks resolved, also for a path that does not exist yet
  python3 -I -c 'import os, sys; print(os.path.realpath(sys.argv[1]))' "$1"
}
ROOT_REAL="$(abs_path "${ROOT}")"

# A staging directory inside the work tree has to be ignored by git (nothing may end up in a commit or make the tree dirty).
check_staging_dir() { # dir
  local d
  d="$(abs_path "$1")"
  case "${d}/" in
    "${ROOT_REAL}/"*)
      git -C "${ROOT}" check-ignore -q -- "${d#"${ROOT_REAL}"/}" \
        || die "[staging_dir] $1 is inside the work tree and not ignored by git: use a path below .cache/ or outside the repository" ;;
  esac
}

marker_get() { # key
  sed -n "s/^$1=//p" "${STATE}/stage" 2>/dev/null | head -n 1
}

check_marker() { # is-snapshot version
  if [ -f "${STATE}/stage" ] && [ "$(marker_get stage)" = "${MYRIARCH_WINDOWS_STAGE}" ] && [ "$(marker_get snapshot)" = "$1" ] \
    && { [ -z "${2:-}" ] || [ "$(marker_get version)" = "$2" ]; }; then
    return 0
  fi
  die "[stage_marker] the before hook did not prepare this build (stage, version): run goreleaser with deploy/release/goreleaser.yaml"
}

record_sha() { # name file
  mkdir -p "${STATE}/accepted"
  printf '%s\n' "$(sha256_of "$2")" > "${STATE}/accepted/$1.sha256"
}

cmd_prepare() {
  [ "$#" = 2 ] || die "usage: windows-signed.sh prepare <version> <is-snapshot>"
  local version="$1" snapshot="$2" id desc rel arch
  check_stage "${snapshot}"
  [[ "${version}" =~ ${VERSION_RE} ]] || die "[version_invalid] version '${version}' is not X.Y.Z with an optional pre-release or build suffix"
  need_cmd go-winres git python3
  # the staging areas are checked before anything is written
  if [ "${MYRIARCH_WINDOWS_STAGE}" != off ]; then
    check_staging_dir "${UNSIGNED_DIR}"
    case "$(abs_path "${UNSIGNED_DIR}")/" in "$(abs_path "${STATE}")/"*) die "[staging_dir] ${UNSIGNED_DIR} is inside ${STATE}, which the before hook recreates" ;; esac
  fi
  if [ "${MYRIARCH_WINDOWS_STAGE}" = apply ]; then
    check_staging_dir "${SIGNED_DIR}"
    if [ -n "${MYRIARCH_AUTHENTICODE_DIR:-}" ] && [ "${snapshot}" != true ]; then
      die "[trust_override] MYRIARCH_AUTHENTICODE_DIR is set, but only snapshot builds may replace the committed trust anchors in deploy/release/authenticode"
    fi
  fi
  if [ "${MYRIARCH_WINDOWS_STAGE}" = collect ] && [ -d "${UNSIGNED_DIR}" ] && [ -n "$(ls -A "${UNSIGNED_DIR}")" ]; then
    die "[staging_not_empty] ${UNSIGNED_DIR} is not empty: remove it first"
  fi
  rm -rf "${STATE}"
  mkdir -p "${STATE}/archive/windows/scripts" "${STATE}/archive/windows/windows" "${STATE}/accepted" "${STATE}/work" "${STATE}/winres"
  # Version resources: an empty working directory, so that no stray winres/icon.png is picked up. --manifest none adds no execution
  # level or OS manifest, so the runtime behavior of the programs does not change.
  for id in myriarch-updater myrctl; do
    case "${id}" in
      myriarch-updater) desc="Myriarch updater" ;;
      *) desc="Myriarch command line tool" ;;
    esac
    ( cd "${STATE}/winres" && go-winres simply --arch amd64,arm64 --out "${ROOT}/cmd/${id}/rsrc" --manifest none \
        --product-name Myriarch --product-version "${version}" --file-version "${version}" --file-description "${desc}" \
        --original-filename "${id}.exe" --copyright "Copyright The Myriarch Authors" ) >&2 \
      || die "[winres_failed] go-winres failed for ${id}"
    for arch in amd64 arm64; do
      rel="cmd/${id}/rsrc_windows_${arch}.syso"
      [ -f "${ROOT}/${rel}" ] || die "[winres_failed] go-winres did not write ${rel}"
      git -C "${ROOT}" check-ignore -q -- "${rel}" || die "[syso_not_ignored] ${rel} is not ignored by git: Go would stamp vcs.modified=true"
    done
  done
  case "${MYRIARCH_WINDOWS_STAGE}" in
    off|collect)
      install -m 0644 "${ROOT}/deploy/scripts/install-windows.ps1" "${STATE}/archive/windows/scripts/"
      install -m 0644 "${ROOT}/deploy/windows/Set-ServiceEnvironment.ps1" "${STATE}/archive/windows/windows/"
      record_sha install-windows.ps1 "${ROOT}/deploy/scripts/install-windows.ps1"
      record_sha Set-ServiceEnvironment.ps1 "${ROOT}/deploy/windows/Set-ServiceEnvironment.ps1"
      if [ "${MYRIARCH_WINDOWS_STAGE}" = collect ]; then mkdir -p "${UNSIGNED_DIR}"; fi ;;
    apply)
      local rel_flag=()
      [ "${snapshot}" = true ] || rel_flag=(--release)
      python3 -I "${AUTH}" accept-scripts --root "${ROOT}" --state "${STATE}" --unsigned "${UNSIGNED_DIR}" --signed "${SIGNED_DIR}" \
        --trust "${TRUST_DIR}" --expect-provider "${MYRIARCH_WINDOWS_SIGNING-}" ${rel_flag[@]+"${rel_flag[@]}"} ;;
  esac
  printf 'stage=%s\nversion=%s\nsnapshot=%s\n' "${MYRIARCH_WINDOWS_STAGE}" "${version}" "${snapshot}" > "${STATE}/stage"
}

cmd_build() {
  [ "$#" = 6 ] || die "usage: windows-signed.sh build <id> <path> <os> <arch> <is-snapshot> <version>"
  local id="$1" path="$2" os="$3" arch="$4" snapshot="$5" version="$6" name dir base
  check_stage "${snapshot}"
  check_marker "${snapshot}" "${version}"
  [ "${os}" = windows ] || return 0
  case "${id}" in myriarch-updater|myrctl) ;; *) die "[unexpected_target] build id '${id}' is not myriarch-updater or myrctl" ;; esac
  case "${arch}" in amd64|arm64) ;; *) die "[unexpected_target] architecture '${arch}' is not amd64 or arm64" ;; esac
  [ -f "${path}" ] && [ ! -L "${path}" ] || die "[unexpected_target] ${path} is not a regular file"
  dir="$(cd "$(dirname "${path}")" && pwd -P)"; base="$(basename "${path}")"
  case "${dir}/" in "${ROOT_REAL}/dist/"*) ;; *) die "[unexpected_target] ${path} is not below ${ROOT}/dist/" ;; esac
  [ "${base}" = "${id}.exe" ] || die "[unexpected_target] ${path} is not ${id}.exe"
  path="${dir}/${base}"
  name="${id}_windows_${arch}.exe"
  case "${MYRIARCH_WINDOWS_STAGE}" in
    off) ;;
    collect)
      [ ! -e "${UNSIGNED_DIR}/${name}" ] || die "[duplicate] ${UNSIGNED_DIR}/${name} exists already: the build ran twice into one directory"
      cp "${path}" "${UNSIGNED_DIR}/${name}"
      [ "$(sha256_of "${path}")" = "$(sha256_of "${UNSIGNED_DIR}/${name}")" ] || die "[copy_mismatch] ${name} was not copied byte for byte"
      record_sha "${name}" "${path}" ;;
    apply)
      local rel_flag=()
      [ "${snapshot}" = true ] || rel_flag=(--release)
      python3 -I "${AUTH}" accept-exe --root "${ROOT}" --name "${name}" --fresh "${path}" --state "${STATE}" --unsigned "${UNSIGNED_DIR}" \
        --signed "${SIGNED_DIR}" --trust "${TRUST_DIR}" --expect-provider "${MYRIARCH_WINDOWS_SIGNING-}" ${rel_flag[@]+"${rel_flag[@]}"} ;;
  esac
}

cmd_seal() {
  [ "$#" = 0 ] || die "usage: windows-signed.sh seal"
  check_stage_value
  [ "${MYRIARCH_WINDOWS_STAGE}" = collect ] || die "[stage_invalid] seal belongs to stage collect (MYRIARCH_WINDOWS_STAGE=${MYRIARCH_WINDOWS_STAGE})"
  check_marker "$(marker_get snapshot)" ""
  check_staging_dir "${UNSIGNED_DIR}"
  python3 -I "${AUTH}" seal --root "${ROOT}" --state "${STATE}" --dir "${UNSIGNED_DIR}"
}

cmd_check_dist() {
  local dist="${1:-dist}"
  check_stage_value
  case "${MYRIARCH_WINDOWS_STAGE}" in collect|apply) ;; *) die "[stage_invalid] check-dist needs stage collect or apply" ;; esac
  check_marker "$(marker_get snapshot)" ""
  python3 -I "${ROOT}/ci/release/dist-check.py" check --root "${ROOT}" --dist "${dist}" --state "${STATE}" --version "$(marker_get version)"
}

cmd_clean() {
  rm -f "${SYSO_GLOBS[@]}"
}

sub="${1:-}"
[ "$#" -eq 0 ] || shift
case "${sub}" in
  prepare)    cmd_prepare "$@" ;;
  build)      cmd_build "$@" ;;
  seal)       cmd_seal "$@" ;;
  check-dist) cmd_check_dist "$@" ;;
  clean)      cmd_clean ;;
  *) die "usage: windows-signed.sh prepare|build|seal|check-dist|clean" ;;
esac
