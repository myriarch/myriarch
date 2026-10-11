#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Builds the tools of one release run (job `tools` of the release workflow, ADR-0035) and checks such a tree again. The tools are built
# once from ci/tools (the pinned modules of ci/versions.env, go.sum enforced, no tool cache) and reach the other jobs as one artifact,
# so no privileged job installs or downloads anything.
#
#   ci/release/tools.sh build --out DIR
#   ci/release/tools.sh check --dir DIR --platform linux-amd64|linux-arm64|windows-amd64
#
# build   1. installs the build prerequisites of osslsigncode (ci/install-tools.sh apt:libssl-dev apt:zlib1g-dev)
#         2. DIR/linux-amd64: goreleaser cosign crane syft gh go-winres yq osslsigncode (ci/install-tools.sh with TOOLS_NO_CACHE=1,
#            stamps included)
#         3. cross builds with the module files and flags of go_install in ci/install-tools.sh (CGO_ENABLED=0, -mod=readonly,
#            -trimpath): DIR/linux-arm64/cosign, DIR/linux-arm64/crane, DIR/windows-amd64/cosign.exe
#         4. checks the version of every host binary against ci/versions.env [tool_version]; go-winres has no version output, so
#            its stamp (which names the pinned version) is checked
#         5. writes DIR/VERSIONS ("<tool> <version>", sorted) and DIR/SHA256SUMS (sha256sum format over every other regular file,
#            relative paths, sorted)
# check   verifies the platform's files against DIR/SHA256SUMS and refuses a file that SHA256SUMS does not list [tools_sums].
#         Consumers may skip it: the SHA-256 of the artifact already binds the content.
#
# Needs go and the tools of step 2 to be buildable (the job runs setup-go). Exit status: 0 ok, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="tools.sh build --out DIR | check --dir DIR --platform linux-amd64|linux-arm64|windows-amd64"

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

build() {
  local out=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --out) need_value "$1" "$#"; out="$2"; shift 2 ;;
      *) usage_err "${USAGE}" ;;
    esac
  done
  [ -n "${out}" ] || usage_err "${USAGE}"
  check_text out_not_empty "--out" "${out}"
  if [ -d "${out}" ] && [ -n "$(ls -A "${out}")" ]; then err out_not_empty "${out} exists and is not empty"; fi
  mkdir -p "${out}"
  out="$(cd "${out}" && pwd)"
  have go || err tool_missing "go was not found"

  "${ROOT}/ci/install-tools.sh" apt:libssl-dev apt:zlib1g-dev >&2 || err build_failed "installing the osslsigncode prerequisites failed"
  TOOLS_NO_CACHE=1 TOOLS_BIN="${out}/linux-amd64" "${ROOT}/ci/install-tools.sh" goreleaser cosign crane syft gh go-winres yq osslsigncode >&2 \
    || err build_failed "building the release tools failed"

  cross_build cosign github.com/sigstore/cosign/v3/cmd/cosign linux arm64 "${out}/linux-arm64/cosign"
  cross_build crane github.com/google/go-containerregistry/cmd/crane linux arm64 "${out}/linux-arm64/crane"
  cross_build cosign github.com/sigstore/cosign/v3/cmd/cosign windows amd64 "${out}/windows-amd64/cosign.exe"

  local bin="${out}/linux-amd64" v
  v="$(pin GORELEASER_VERSION)"
  check_version goreleaser "${v}" "$(version_line "${bin}/goreleaser" --version | sed -n 's/^GitVersion: *//p' | head -n 1)"
  v="$(pin COSIGN_VERSION)"
  check_version cosign "${v}" "$(version_line "${bin}/cosign" version | sed -n 's/^GitVersion: *//p' | head -n 1)"
  v="$(pin CRANE_VERSION)"
  check_version crane "${v}" "$(version_line "${bin}/crane" version | head -n 1)"
  v="$(pin SYFT_VERSION)"
  check_version syft "${v}" "$(version_line "${bin}/syft" version | sed -n 's/^Version: *//p' | head -n 1)"
  v="$(pin GH_VERSION)"
  check_version gh "gh version ${v#v}" "$(version_line "${bin}/gh" --version | head -n 1)"
  v="$(pin YQ_VERSION)"
  check_version yq "yq (https://github.com/mikefarah/yq/) version ${v}" "$(version_line "${bin}/yq" --version | head -n 1)"
  v="$(pin OSSLSIGNCODE_VERSION)"
  check_version osslsigncode "osslsigncode ${v}, using:" "$(version_line "${bin}/osslsigncode" --version | head -n 1)"
  v="$(pin GOWINRES_VERSION)"
  [ -x "${bin}/go-winres" ] || err tool_version "go-winres was not built"
  # shellcheck disable=SC2144  # exactly one stamp is expected, and the glob must match it
  [ -f "$(ls "${bin}"/.stamp-go-winres-"${v}"-* 2>/dev/null | head -n 1)" ] || err tool_version "go-winres has no stamp for ${v}"

  local gowinres_v
  gowinres_v="${v}"
  {
    printf 'cosign %s\n' "$(pin COSIGN_VERSION)"
    printf 'crane %s\n' "$(pin CRANE_VERSION)"
    printf 'gh %s\n' "$(pin GH_VERSION)"
    printf 'go-winres %s\n' "${gowinres_v}"
    printf 'goreleaser %s\n' "$(pin GORELEASER_VERSION)"
    printf 'osslsigncode %s\n' "$(pin OSSLSIGNCODE_VERSION)"
    printf 'syft %s\n' "$(pin SYFT_VERSION)"
    printf 'yq %s\n' "$(pin YQ_VERSION)"
  } | LC_ALL=C sort > "${out}/VERSIONS"

  local sums="${out}/SHA256SUMS" f
  : > "${sums}"
  ( cd "${out}" && find . -type f ! -name SHA256SUMS | sed 's|^\./||' | LC_ALL=C sort ) | while IFS= read -r f; do
    printf '%s  %s\n' "$(sha256_of "${out}/${f}")" "${f}" >> "${sums}"
  done
  info "tools built in ${out} ($(wc -l < "${sums}" | tr -d ' ') files)"
}

check() {
  local dir="" platform="" line sum name n=0 f listed=" "
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --dir) need_value "$1" "$#"; dir="$2"; shift 2 ;;
      --platform) need_value "$1" "$#"; platform="$2"; shift 2 ;;
      *) usage_err "${USAGE}" ;;
    esac
  done
  [ -n "${dir}" ] && [ -n "${platform}" ] || usage_err "${USAGE}"
  case "${platform}" in linux-amd64|linux-arm64|windows-amd64) ;; *) usage_err "${USAGE}" ;; esac
  [ -f "${dir}/SHA256SUMS" ] || err tools_sums "${dir}/SHA256SUMS is missing"
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
  info "${platform}: ${n} files match SHA256SUMS"
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
