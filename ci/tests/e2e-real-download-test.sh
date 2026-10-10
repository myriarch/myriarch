#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the download and verification of the real collector binaries (scripts/lib/real-binaries.sh, used by
# scripts/e2e.sh): file:// fixtures with the layout of the upstream release (a tar.gz with a stand-in otelcol-contrib, a
# stand-in supervisor, and a .sha256 per asset). Covers a good download, a repeated run on the kept files, an upper-case
# digest, a checksum mismatch (the bad file is deleted and nothing is installed), a malformed digest, a missing .sha256,
# a changed cache and an unsupported platform. No network, no ports.
set -uo pipefail
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
need_cmd curl tar python3
# shellcheck source=scripts/lib/real-binaries.sh
. "${ROOT}/scripts/lib/real-binaries.sh"
V=9.9.9
export REAL_UNAME_S=Linux REAL_UNAME_M=x86_64

# mkrelease DIR PLATFORM: the release tree below DIR for the platform (for example linux_amd64)
mkrelease() {
  local dir="$1" plat="$2" stage
  stage="$(mktemp -d "${TEST_TMP}/stage.XXXXXX")"
  mkdir -p "${dir}/v${V}" "${dir}/cmd/opampsupervisor/v${V}"
  printf '#!/bin/sh\necho "otelcol-contrib version %s"\n' "${V}" > "${stage}/otelcol-contrib"
  chmod +x "${stage}/otelcol-contrib"
  tar -czf "${dir}/v${V}/otelcol-contrib_${V}_${plat}.tar.gz" -C "${stage}" otelcol-contrib
  printf '#!/bin/sh\necho supervisor\n' > "${dir}/cmd/opampsupervisor/v${V}/opampsupervisor_${V}_${plat}"
  # the digest files as upstream writes them: "<digest>  <name>"
  for f in "${dir}/v${V}/otelcol-contrib_${V}_${plat}.tar.gz" "${dir}/cmd/opampsupervisor/v${V}/opampsupervisor_${V}_${plat}"; do
    printf '%s  %s\n' "$(sha256_of "${f}")" "$(basename "${f}")" > "${f}.sha256"
  done
}
prepare() { # releasedir workdir
  real_prepare "$2" "${V}" "file://$1"
}
cc="otelcol-contrib_${V}_linux_amd64.tar.gz"
ss="opampsupervisor_${V}_linux_amd64"

# platform detection
plat() { REAL_UNAME_S="$1" REAL_UNAME_M="$2" real_platform; }
expect_ok   "linux/x86_64 is linux_amd64"        test "$(plat Linux x86_64)" = linux_amd64
expect_ok   "linux/aarch64 is linux_arm64"       test "$(plat Linux aarch64)" = linux_arm64
expect_ok   "darwin/arm64 is darwin_arm64"       test "$(plat Darwin arm64)" = darwin_arm64
expect_ok   "darwin/x86_64 is darwin_amd64"      test "$(plat Darwin x86_64)" = darwin_amd64
unsupported() { ! plat "$@" >/dev/null; }
expect_ok   "windows is not supported"           unsupported MINGW64_NT x86_64
expect_ok   "linux/riscv64 is not supported"     unsupported Linux riscv64

# a good release
rel="${TEST_TMP}/rel-good"; mkrelease "${rel}" linux_amd64
w="${TEST_TMP}/work-good"
expect_ok   "a good release is downloaded, verified and installed" prepare "${rel}" "${w}"
expect_ok   "otelcol-contrib is installed and executable" test -x "${w}/otelcol-contrib"
expect_ok   "the installed otelcol-contrib is the extracted file" test "$("${w}/otelcol-contrib")" = "otelcol-contrib version ${V}"
expect_ok   "opampsupervisor is installed and executable" test -x "${w}/opampsupervisor"
expect_ok   "archives and checksums stay in dl/" test -f "${w}/dl/${cc}.sha256" -a -f "${w}/dl/${ss}.sha256"
expect_ok   "a second run verifies the kept files again" prepare "${rel}" "${w}"
case "${OUT}" in *"kept from an earlier run"*) ok "the second run reports that it verified the kept files" ;; *) fail "the second run does not report the verification of the kept files" ;; esac

# a changed file in the kept cache is refused and deleted
printf 'tampered' >> "${w}/dl/${ss}"
expect_fail "a changed kept file fails the SHA-256 check" 'SHA-256 mismatch' prepare "${rel}" "${w}"
expect_fail "the changed file is deleted" 'No such file' ls "${w}/dl/${ss}"
expect_ok   "after the deletion the next run downloads it again and passes" prepare "${rel}" "${w}"

# upper-case digest
rel="${TEST_TMP}/rel-upper"; mkrelease "${rel}" linux_amd64
f="${rel}/cmd/opampsupervisor/v${V}/${ss}.sha256"
printf '%s\n' "$(tr 'a-f' 'A-F' < "${f}")" > "${f}"
expect_ok   "an upper-case digest is accepted" prepare "${rel}" "${TEST_TMP}/work-upper"

# a digest without the file name
rel="${TEST_TMP}/rel-bare"; mkrelease "${rel}" linux_amd64
f="${rel}/v${V}/${cc}.sha256"; awk '{print $1}' "${f}" > "${f}.tmp" && mv "${f}.tmp" "${f}"
expect_ok   "a digest without a file name is accepted" prepare "${rel}" "${TEST_TMP}/work-bare"

# a wrong digest
rel="${TEST_TMP}/rel-bad"; mkrelease "${rel}" linux_amd64
printf '%064d  %s\n' 0 "${ss}" > "${rel}/cmd/opampsupervisor/v${V}/${ss}.sha256"
w="${TEST_TMP}/work-bad"
expect_fail "a checksum mismatch fails" 'SHA-256 mismatch' prepare "${rel}" "${w}"
expect_fail "the file with the wrong checksum is not kept" 'No such file' ls "${w}/dl/${ss}"
expect_fail "the checksum file of it is not kept" 'No such file' ls "${w}/dl/${ss}.sha256"
expect_fail "nothing is installed from an unverified download" 'No such file' ls "${w}/opampsupervisor"
expect_fail "no partial file is left" 'No such file' ls "${w}/dl/${ss}.part"

# malformed digests
for bad in "not-a-digest  x" "$(printf 'a%.0s' $(seq 1 63))  x" "$(printf 'g%.0s' $(seq 1 64))  x" ""; do
  rel="${TEST_TMP}/rel-malformed"; rm -rf "${rel}"; mkrelease "${rel}" linux_amd64
  printf '%s\n' "${bad}" > "${rel}/v${V}/${cc}.sha256"
  rm -rf "${TEST_TMP}/work-malformed"
  expect_fail "a malformed digest file is refused ('${bad:0:20}')" 'SHA-256 \(64 hex digits\)|missing or empty' prepare "${rel}" "${TEST_TMP}/work-malformed"
done

# a missing .sha256 on the server
rel="${TEST_TMP}/rel-nosum"; mkrelease "${rel}" linux_amd64
rm -f "${rel}/v${V}/${cc}.sha256"
expect_fail "a release without a .sha256 fails" 'download of .* failed' prepare "${rel}" "${TEST_TMP}/work-nosum"

# a missing asset
rel="${TEST_TMP}/rel-noasset"; mkrelease "${rel}" linux_amd64
rm -f "${rel}/v${V}/${cc}"
expect_fail "a missing asset fails" 'download of .* failed' prepare "${rel}" "${TEST_TMP}/work-noasset"

# an archive without the binary
rel="${TEST_TMP}/rel-empty"; mkrelease "${rel}" linux_amd64
stage="$(mktemp -d "${TEST_TMP}/stage.XXXXXX")"; : > "${stage}/other"
tar -czf "${rel}/v${V}/${cc}" -C "${stage}" other
printf '%s  %s\n' "$(sha256_of "${rel}/v${V}/${cc}")" "${cc}" > "${rel}/v${V}/${cc}.sha256"
expect_fail "an archive without otelcol-contrib fails" 'otelcol-contrib|not found in archive' prepare "${rel}" "${TEST_TMP}/work-empty"

# other platforms use their own asset names
rel="${TEST_TMP}/rel-mac"; mkrelease "${rel}" darwin_arm64
expect_ok   "darwin/arm64 downloads the darwin_arm64 assets" env REAL_UNAME_S=Darwin REAL_UNAME_M=arm64 bash -c '. "$1"; real_prepare "$2" "$3" "file://$4"' _ "${ROOT}/scripts/lib/real-binaries.sh" "${TEST_TMP}/work-mac" "${V}" "${rel}"
rel="${TEST_TMP}/rel-arm"; mkrelease "${rel}" linux_arm64
expect_ok   "linux/aarch64 downloads the linux_arm64 assets" env REAL_UNAME_S=Linux REAL_UNAME_M=aarch64 bash -c '. "$1"; real_prepare "$2" "$3" "file://$4"' _ "${ROOT}/scripts/lib/real-binaries.sh" "${TEST_TMP}/work-arm" "${V}" "${rel}"

# an unsupported platform returns status 2 and installs nothing
w="${TEST_TMP}/work-unsupported"
OUT="$(env REAL_UNAME_S=FreeBSD REAL_UNAME_M=amd64 bash -c '. "$1"; real_prepare "$2" "$3" "file://$4"' _ "${ROOT}/scripts/lib/real-binaries.sh" "${w}" "${V}" "${rel}" 2>&1)"; rc=$?
if [ "${rc}" = 2 ] && [ ! -e "${w}/opampsupervisor" ]; then ok "an unsupported platform returns status 2 and installs nothing"; else fail "an unsupported platform returns status 2 (got ${rc}: ${OUT})"; fi

# only https and file are accepted
expect_fail "a plain http URL is refused" 'download of .* failed' real_fetch_verified "http://127.0.0.1:9" x "${TEST_TMP}/http/x"

finish_tests "e2e-real-download-test"
