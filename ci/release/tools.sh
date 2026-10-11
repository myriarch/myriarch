#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Builds the tools of one release run and checks such a tree again (the jobs `signing-tools` and `build-tools` of the release workflow,
# ADR-0035). The tools are built from ci/tools (the pinned modules of ci/versions.env, go.sum enforced, no tool cache) and reach the
# other jobs as two artifacts, so no privileged job installs or downloads anything. They are two sets, built in two jobs, because the
# jobs with id-token, attestations, packages or contents write execute the signing set, and nothing that runs on the same runner may be
# able to change it between the build and the SHA-256 of its tar file:
#
#   signing   cosign, crane, gh      linux-amd64, plus cosign and crane for linux-arm64 and cosign.exe for windows-amd64. Built first
#                                    on a runner of its own: no apt package, no C compiler, no other tool is installed or run there.
#   build     goreleaser, syft, yq, go-winres, osslsigncode (linux-amd64): executed only by the jobs with contents: read.
#
#   ci/release/tools.sh build --set signing|build --out DIR
#   ci/release/tools.sh check --set signing|build --dir DIR --platform linux-amd64|linux-arm64|windows-amd64
#
# build   signing: 1. DIR/linux-amd64: cosign crane gh (ci/install-tools.sh with TOOLS_NO_CACHE=1, stamps included)
#                  2. cross builds with the module files and flags of go_install in ci/install-tools.sh (CGO_ENABLED=0, -mod=readonly,
#                     -trimpath): DIR/linux-arm64/cosign, DIR/linux-arm64/crane, DIR/windows-amd64/cosign.exe
#         build:   1. installs the build prerequisites of osslsigncode (ci/install-tools.sh apt:libssl-dev apt:zlib1g-dev)
#                  2. DIR/linux-amd64: goreleaser syft go-winres yq osslsigncode
#         then    checks the version of every host binary against ci/versions.env [tool_version] (go-winres has no version output,
#                 so its stamp, which names the pinned version, is checked), writes DIR/VERSIONS ("<tool> <version>", sorted) and
#                 DIR/SHA256SUMS (sha256sum format over every other regular file, relative paths, sorted), and appends both to the
#                 job summary, so that the run records what was built.
# check   verifies the platform's files against DIR/SHA256SUMS, refuses a file that SHA256SUMS does not list, and requires that VERSIONS
#         names exactly the tools of the set [tools_sums]. Every consumer runs it right after artifact.sh unpack.
#
# Needs go and the tools above to be buildable (the jobs run setup-go). Exit status: 0 ok, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="tools.sh build --set signing|build --out DIR | check --set signing|build --dir DIR --platform linux-amd64|linux-arm64|windows-amd64"

# cross_build <tool> <module> <goos> <goarch> <output file>
cross_build() {
  local tool="$1" module="$2" goos="$3" goarch="$4" out="$5"
  mkdir -p "$(dirname "${out}")"
  info "building ${tool} for ${goos}/${goarch}"
  ( cd "${ROOT}/ci/tools/${tool}" \
    && GOOS="${goos}" GOARCH="${goarch}" CGO_ENABLED=0 GOFLAGS=-mod=readonly GOTOOLCHAIN=local GOBIN="" \
       go build -trimpath -o "${out}" "${module}" ) \
    || err build_failed "go build ${module} for ${goos}/${goarch} failed"
}

# version_line <binary> <args...>: the lines of a version command
version_line() { # binary args...
  local bin="$1"
  shift
  "${bin}" "$@" 2>&1 || true
}

check_version() { # tool expected-text actual-text
  [ "$2" = "$3" ] || err tool_version "$1 reports '${3:-nothing}', ci/versions.env pins '$2'"
}

# set_tools <set>: the tools of a set, sorted (the names VERSIONS must list)
set_tools() {
  case "$1" in
    signing) printf '%s\n' cosign crane gh ;;
    build) printf '%s\n' go-winres goreleaser osslsigncode syft yq ;;
    *) usage_err "${USAGE}" ;;
  esac
}

build() {
  local out="" set=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --out) need_value "$1" "$#"; out="$2"; shift 2 ;;
      --set) need_value "$1" "$#"; set="$2"; shift 2 ;;
      *) usage_err "${USAGE}" ;;
    esac
  done
  [ -n "${out}" ] && [ -n "${set}" ] || usage_err "${USAGE}"
  set_tools "${set}" > /dev/null
  check_text out_not_empty "--out" "${out}"
  if [ -d "${out}" ] && [ -n "$(ls -A "${out}")" ]; then err out_not_empty "${out} exists and is not empty"; fi
  mkdir -p "${out}"
  out="$(cd "${out}" && pwd)"
  have go || err tool_missing "go was not found"

  local bin="${out}/linux-amd64" v
  if [ "${set}" = signing ]; then
    TOOLS_NO_CACHE=1 TOOLS_BIN="${bin}" "${ROOT}/ci/install-tools.sh" cosign crane gh >&2 || err build_failed "building the signing tools failed"
    cross_build cosign github.com/sigstore/cosign/v3/cmd/cosign linux arm64 "${out}/linux-arm64/cosign"
    cross_build crane github.com/google/go-containerregistry/cmd/crane linux arm64 "${out}/linux-arm64/crane"
    cross_build cosign github.com/sigstore/cosign/v3/cmd/cosign windows amd64 "${out}/windows-amd64/cosign.exe"
    v="$(pin COSIGN_VERSION)"
    check_version cosign "${v}" "$(version_line "${bin}/cosign" version | sed -n 's/^GitVersion: *//p' | head -n 1)"
    v="$(pin CRANE_VERSION)"
    check_version crane "${v}" "$(version_line "${bin}/crane" version | head -n 1)"
    v="$(pin GH_VERSION)"
    check_version gh "gh version ${v#v}" "$(version_line "${bin}/gh" --version | head -n 1)"
  else
    "${ROOT}/ci/install-tools.sh" apt:libssl-dev apt:zlib1g-dev >&2 || err build_failed "installing the osslsigncode prerequisites failed"
    TOOLS_NO_CACHE=1 TOOLS_BIN="${bin}" "${ROOT}/ci/install-tools.sh" goreleaser syft go-winres yq osslsigncode >&2 || err build_failed "building the build tools failed"
    v="$(pin GORELEASER_VERSION)"
    check_version goreleaser "${v}" "$(version_line "${bin}/goreleaser" --version | sed -n 's/^GitVersion: *//p' | head -n 1)"
    v="$(pin SYFT_VERSION)"
    check_version syft "${v}" "$(version_line "${bin}/syft" version | sed -n 's/^Version: *//p' | head -n 1)"
    v="$(pin YQ_VERSION)"
    check_version yq "yq (https://github.com/mikefarah/yq/) version ${v}" "$(version_line "${bin}/yq" --version | head -n 1)"
    v="$(pin OSSLSIGNCODE_VERSION)"
    check_version osslsigncode "osslsigncode ${v}, using:" "$(version_line "${bin}/osslsigncode" --version | head -n 1)"
    v="$(pin GOWINRES_VERSION)"
    [ -x "${bin}/go-winres" ] || err tool_version "go-winres was not built"
    # shellcheck disable=SC2144  # exactly one stamp is expected, and the glob must match it
    [ -f "$(ls "${bin}"/.stamp-go-winres-"${v}"-* 2>/dev/null | head -n 1)" ] || err tool_version "go-winres has no stamp for ${v}"
  fi

  local t
  while IFS= read -r t; do
    case "${t}" in
      cosign) printf 'cosign %s\n' "$(pin COSIGN_VERSION)" ;;
      crane) printf 'crane %s\n' "$(pin CRANE_VERSION)" ;;
      gh) printf 'gh %s\n' "$(pin GH_VERSION)" ;;
      go-winres) printf 'go-winres %s\n' "$(pin GOWINRES_VERSION)" ;;
      goreleaser) printf 'goreleaser %s\n' "$(pin GORELEASER_VERSION)" ;;
      osslsigncode) printf 'osslsigncode %s\n' "$(pin OSSLSIGNCODE_VERSION)" ;;
      syft) printf 'syft %s\n' "$(pin SYFT_VERSION)" ;;
      yq) printf 'yq %s\n' "$(pin YQ_VERSION)" ;;
    esac
  done < <(set_tools "${set}") > "${out}/VERSIONS"

  local sums="${out}/SHA256SUMS" f
  : > "${sums}"
  ( cd "${out}" && find . -type f ! -name SHA256SUMS | sed 's|^\./||' | LC_ALL=C sort ) | while IFS= read -r f; do
    printf '%s  %s\n' "$(sha256_of "${out}/${f}")" "${f}" >> "${sums}"
  done
  info "${set} tools built in ${out} ($(wc -l < "${sums}" | tr -d ' ') files)"
  summary "## Tools of this run: ${set}" "" "Versions (ci/versions.env):" "" '```text' "$(cat "${out}/VERSIONS")" '```' "" \
    "SHA-256 of every file (the SHA-256 of the whole tar file is the output of the pack step):" "" '```text' "$(cat "${sums}")" '```'
}

check() {
  local dir="" platform="" set="" line sum name n=0 f listed=" " want got
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --dir) need_value "$1" "$#"; dir="$2"; shift 2 ;;
      --platform) need_value "$1" "$#"; platform="$2"; shift 2 ;;
      --set) need_value "$1" "$#"; set="$2"; shift 2 ;;
      *) usage_err "${USAGE}" ;;
    esac
  done
  [ -n "${dir}" ] && [ -n "${platform}" ] && [ -n "${set}" ] || usage_err "${USAGE}"
  case "${platform}" in linux-amd64|linux-arm64|windows-amd64) ;; *) usage_err "${USAGE}" ;; esac
  want="$(set_tools "${set}")"
  [ -f "${dir}/SHA256SUMS" ] || err tools_sums "${dir}/SHA256SUMS is missing"
  [ -f "${dir}/VERSIONS" ] || err tools_sums "${dir}/VERSIONS is missing"
  got="$(awk '{ print $1 }' "${dir}/VERSIONS")"
  [ "${got}" = "${want}" ] || err tools_sums "VERSIONS names '$(printf '%s' "${got}" | tr '\n' ' ')', the ${set} set is '$(printf '%s' "${want}" | tr '\n' ' ')'"
  while IFS= read -r line || [ -n "${line}" ]; do
    check_text tools_sums "a line of SHA256SUMS" "${line}"
    [[ "${line}" =~ ^([0-9a-f]{64})\ \ ([^/][^[:space:]]*)$ ]] || err tools_sums "SHA256SUMS has a malformed line"
    sum="${BASH_REMATCH[1]}"
    name="${BASH_REMATCH[2]}"
    case "${name}" in "${platform}"/*) ;; *) continue ;; esac
    [ -f "${dir}/${name}" ] || err tools_sums "${name} is listed but missing"
    [ "$(sha256_of "${dir}/${name}")" = "${sum}" ] || err tools_sums "${name} differs from SHA256SUMS"
    listed="${listed}${name} "
    n=$((n + 1))
  done < "${dir}/SHA256SUMS"
  [ "${n}" -gt 0 ] || err tools_sums "SHA256SUMS lists nothing for ${platform}"
  while IFS= read -r f; do
    case "${listed}" in *" ${f} "*) ;; *) err tools_sums "${f} is not listed in SHA256SUMS" ;; esac
  done < <(cd "${dir}" && find "${platform}" -type f | LC_ALL=C sort)
  info "${set} tools, ${platform}: ${n} files match SHA256SUMS"
}

[ "$#" -ge 1 ] || usage_err "${USAGE}"
cmd="$1"
shift
case "${cmd}" in
  build) build "$@" ;;
  check) check "$@" ;;
  -h|--help|help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
  *) usage_err "${USAGE}" ;;
esac
