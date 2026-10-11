#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests of the release downloads of syft and zizmor and of the source build of osslsigncode in ci/install-tools.sh (offline:
# <TOOL>_BASE_URL points at a file:// mirror in the temp directory) and of the pin checks of ci/check.sh for the new tools.
# For each of syft and zizmor:
#  - an archive that matches the pin is installed, and the binary has to report the pinned version (exactly, not as a substring)
#  - an archive whose SHA-256 differs from the pin is refused and nothing is installed (also when the environment carries a different pin)
#  - a missing or malformed pin is refused
# osslsigncode (a source archive that is compiled with cc; the stand-in sources only print the version line) has the same cases, but needs
# a C compiler and the OpenSSL 3 headers: without them that case is skipped locally and fails when CI=true.
# And ci/check.sh rejects a go.mod that disagrees with GH_VERSION or GOWINRES_VERSION, an invalid pin and a Go module for syft.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
set -u
load_versions

# the platform: os and arch as the tools name them, and the upper-case parts of the pin key
case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux)  os=linux ;;
  *)      echo "tool-downloads-test: unsupported platform $(uname -s), skipped"; exit 0 ;;
esac
case "$(uname -m)" in
  arm64|aarch64) arch=arm64; key_arch=ARM64 ;;
  x86_64|amd64)  arch=amd64; key_arch=AMD64 ;;
  *)             echo "tool-downloads-test: unsupported platform $(uname -m), skipped"; exit 0 ;;
esac

# a copy of the installer with its own versions.env (the installer reads the pins from the file next to it)
repo="${TEST_TMP}/repo"; mkdir -p "${repo}/ci"
cp "${CI_DIR}/lib.sh" "${CI_DIR}/install-tools.sh" "${CI_DIR}/versions.env" "${repo}/ci/"

pin() { # key sha - sets a pin in the copied versions.env
  python3 -I - "${repo}/ci/versions.env" "$1" "$2" <<'PY'
import re, sys
path, key, sha = sys.argv[1:4]
text = open(path).read()
text, n = re.subn(r'^' + re.escape(key) + r'=.*$', key + '=' + sha, text, flags=re.M)
assert n == 1, key
open(path, 'w').write(text)
PY
}
unpin() { # key - removes a pin from the copied versions.env
  python3 -I - "${repo}/ci/versions.env" "$1" <<'PY'
import re, sys
path, key = sys.argv[1:3]
text = open(path).read()
open(path, 'w').write(re.sub(r'^' + re.escape(key) + r'=.*\n', '', text, flags=re.M))
PY
}

# The cases that both downloads share. The stand-in binary prints what the real one prints for its version query (see mk below).
suite() { # tool PREFIX version asset
  local tool="$1" prefix="$2" version="$3" asset="$4"
  local base="${prefix}_BASE_URL" key good good_sha old_sha near
  key="${prefix}_SHA256_$(printf '%s' "${os}" | tr '[:lower:]' '[:upper:]')_${key_arch}"

  mk() { # reported-version dir - writes <dir>/v<version>/<asset> with a stand-in binary that reports the given version
    local w="${TEST_TMP}/pkg-${tool}-$1"
    mkdir -p "${w}" "$2/v${version}"
    if [ "${tool}" = syft ]; then
      printf '#!/bin/sh\nprintf "Application:         syft\\nVersion:             %%s\\nBuildDate:           2026-01-01T00:00:00Z\\n" "%s"\n' "$1" > "${w}/${tool}"
    else
      printf '#!/bin/sh\necho "zizmor %s"\n' "$1" > "${w}/${tool}"
    fi
    chmod +x "${w}/${tool}"
    tar -czf "$2/v${version}/${asset}" -C "${w}" "${tool}"
  }
  install() { # mirror [env...] - runs the copied installer with a fresh TOOLS_BIN
    local mirror="$1"; shift
    rm -rf "${TEST_TMP:?}/bin"
    env -u "${key}" TOOLS_BIN="${TEST_TMP}/bin" "${base}=file://${mirror}" "$@" "${repo}/ci/install-tools.sh" "${tool}"
  }

  good="${TEST_TMP}/mirror-${tool}-good"; mk "${version}" "${good}"
  mk "0.0.1" "${TEST_TMP}/mirror-${tool}-old"
  # a version that merely contains the pinned one (1.30.1 in 1.30.10) must not be accepted
  near="${version}0"; mk "${near}" "${TEST_TMP}/mirror-${tool}-near"
  good_sha="$(sha256_of "${good}/v${version}/${asset}")"
  old_sha="$(sha256_of "${TEST_TMP}/mirror-${tool}-old/v${version}/${asset}")"

  pin "${key}" "${good_sha}"
  expect_ok "${tool}: an archive that matches the pin is installed" install "${good}"
  [ -x "${TEST_TMP}/bin/${tool}" ] && ok "${tool}: the binary is in TOOLS_BIN" || fail "${tool}: TOOLS_BIN/${tool} is missing"
  [ -f "${TEST_TMP}/bin/.stamp-${tool}-${version}" ] && ok "${tool}: the stamp for the pinned version is written" || fail "${tool}: the stamp is missing"
  expect_ok "${tool}: a second run uses the cache" env TOOLS_BIN="${TEST_TMP}/bin" "${base}=file://${TEST_TMP}/does-not-exist" "${repo}/ci/install-tools.sh" "${tool}"

  # a binary that reports another version than the pinned one is refused and removed
  pin "${key}" "${old_sha}"
  expect_fail "${tool}: a binary that reports another version is refused" "reports '.*0\\.0\\.1'" install "${TEST_TMP}/mirror-${tool}-old"
  [ ! -e "${TEST_TMP}/bin/${tool}" ] && ok "${tool}: the binary of the wrong version is removed" || fail "${tool}: TOOLS_BIN/${tool} of the wrong version is still there"
  pin "${key}" "$(sha256_of "${TEST_TMP}/mirror-${tool}-near/v${version}/${asset}")"
  expect_fail "${tool}: a version that only contains the pinned one is refused" "reports '.*${near}'" install "${TEST_TMP}/mirror-${tool}-near"
  [ ! -e "${TEST_TMP}/bin/${tool}" ] && ok "${tool}: the near-miss binary is removed" || fail "${tool}: TOOLS_BIN/${tool} of the near-miss version is still there"

  # a checksum mismatch: the pin is that of the good archive, the mirror serves another one
  pin "${key}" "${good_sha}"
  expect_fail "${tool}: a checksum mismatch is refused" "SHA-256 mismatch for ${asset}" install "${TEST_TMP}/mirror-${tool}-old"
  [ ! -e "${TEST_TMP}/bin/${tool}" ] && ok "${tool}: a refused download installs nothing" || fail "${tool}: TOOLS_BIN/${tool} exists after a refused download"
  ls "${TEST_TMP}/bin/.stamp-${tool}-"* > /dev/null 2>&1 && fail "${tool}: a stamp was written for a refused download" || ok "${tool}: a refused download writes no stamp"

  # the environment cannot replace the pin: a matching value in <TOOL>_SHA256_* does not make the other archive acceptable
  rm -rf "${TEST_TMP:?}/bin"
  expect_fail "${tool}: the environment cannot override the pin" "SHA-256 mismatch" \
    env TOOLS_BIN="${TEST_TMP}/bin" "${base}=file://${TEST_TMP}/mirror-${tool}-old" "${key}=${old_sha}" "${repo}/ci/install-tools.sh" "${tool}"

  # a missing or malformed pin is refused before anything is downloaded
  pin "${key}" "not-a-sha"
  expect_fail "${tool}: a malformed pin is refused" "${key} in ci/versions.env is missing or not a SHA-256" install "${good}"
  unpin "${key}"
  expect_fail "${tool}: a missing pin is refused" "${key} in ci/versions.env is missing or not a SHA-256" install "${good}"
}

suite zizmor ZIZMOR "${ZIZMOR_VERSION}" "zizmor-$([ "${arch}" = arm64 ] && echo aarch64 || echo x86_64)-$([ "${os}" = darwin ] && echo apple-darwin || echo unknown-linux-gnu).tar.gz"
suite syft SYFT "${SYFT_VERSION}" "syft_${SYFT_VERSION}_${os}_${arch}.tar.gz"

# ---- osslsigncode: source archive, compiled with cc ----------------------------------------------------------------------------
# A stand-in source tree: osslsigncode.c prints the version line, the other eight files only need to compile.
ossl_mk() { # reported-version dir - writes <dir>/<version>.tar.gz with osslsigncode-<version>/*.c
  local v="${OSSLSIGNCODE_VERSION}" w="${TEST_TMP}/ossl-src-$1/osslsigncode-${OSSLSIGNCODE_VERSION}" n f
  mkdir -p "${w}" "$2"
  printf '#include <stdio.h>\nint main(void){puts("osslsigncode %s, using:");return 0;}\n' "$1" > "${w}/osslsigncode.c"
  n=0
  for f in helpers utf msi pe cab cat appx script; do
    n=$((n + 1)); printf 'int dummy_%s(void){return 0;}\n' "${n}" > "${w}/${f}.c"
  done
  tar -czf "$2/${v}.tar.gz" -C "${TEST_TMP}/ossl-src-$1" "osslsigncode-${v}"
}
ossl_install() { # mirror [env...] - runs the copied installer with a fresh TOOLS_BIN
  local mirror="$1"; shift
  rm -rf "${TEST_TMP:?}/bin"
  env -u OSSLSIGNCODE_SOURCE_SHA256 TOOLS_BIN="${TEST_TMP}/bin" OSSLSIGNCODE_BASE_URL="file://${mirror}" "$@" "${repo}/ci/install-tools.sh" osslsigncode
}
ossl_good="${TEST_TMP}/mirror-ossl-good"; ossl_mk "${OSSLSIGNCODE_VERSION}" "${ossl_good}"
ossl_old="${TEST_TMP}/mirror-ossl-old"; ossl_mk "0.1" "${ossl_old}"
ossl_near="${TEST_TMP}/mirror-ossl-near"; ossl_mk "${OSSLSIGNCODE_VERSION}0" "${ossl_near}"
ossl_key=OSSLSIGNCODE_SOURCE_SHA256
ossl_good_sha="$(sha256_of "${ossl_good}/${OSSLSIGNCODE_VERSION}.tar.gz")"
# the pin checks come first: they run before the compiler is needed
pin "${ossl_key}" "not-a-sha"
expect_fail "osslsigncode: a malformed pin is refused" "${ossl_key} in ci/versions.env is missing or not a SHA-256" ossl_install "${ossl_good}"
pin "${ossl_key}" "${ossl_good_sha}"
if ! have cc; then
  ossl_skip=1
elif ossl_install "${ossl_good}" > "${TEST_TMP}/ossl-probe.log" 2>&1; then
  ossl_skip=0
elif grep -q 'OpenSSL 3 and zlib headers were not found' "${TEST_TMP}/ossl-probe.log"; then
  ossl_skip=1
else
  ossl_skip=0   # the cases below report the build failure
fi
if [ "${ossl_skip}" = 1 ]; then
  if [ "${CI:-}" = true ]; then
    fail "osslsigncode: no C compiler or OpenSSL 3 headers (CI=true: ci/install-tools.sh apt:libssl-dev apt:zlib1g-dev)"
  else
    echo "  skipped: no C compiler or OpenSSL 3 headers (osslsigncode build cases)"
  fi
else
  pin "${ossl_key}" "${ossl_good_sha}"
  expect_ok "osslsigncode: a source archive that matches the pin is built and installed" ossl_install "${ossl_good}"
  [ -x "${TEST_TMP}/bin/osslsigncode" ] && ok "osslsigncode: the binary is in TOOLS_BIN" || fail "osslsigncode: TOOLS_BIN/osslsigncode is missing"
  [ "$("${TEST_TMP}/bin/osslsigncode" --version | head -n 1)" = "osslsigncode ${OSSLSIGNCODE_VERSION}, using:" ] && ok "osslsigncode: --version is exact" || fail "osslsigncode: --version differs"
  [ -f "${TEST_TMP}/bin/.stamp-osslsigncode-${OSSLSIGNCODE_VERSION}" ] && ok "osslsigncode: the stamp for the pinned version is written" || fail "osslsigncode: the stamp is missing"
  expect_ok "osslsigncode: a second run uses the cache" env TOOLS_BIN="${TEST_TMP}/bin" OSSLSIGNCODE_BASE_URL="file://${TEST_TMP}/does-not-exist" "${repo}/ci/install-tools.sh" osslsigncode
  # a checksum mismatch: the pin is that of the good archive, the mirror serves another one
  expect_fail "osslsigncode: a checksum mismatch is refused" "SHA-256 mismatch for src.tar.gz" ossl_install "${ossl_old}"
  [ ! -e "${TEST_TMP}/bin/osslsigncode" ] && ok "osslsigncode: a refused download installs nothing" || fail "osslsigncode: a binary exists after a refused download"
  # a binary that reports another version than the pinned one (0.1) or a near miss (2.140) is refused and removed
  pin "${ossl_key}" "$(sha256_of "${ossl_old}/${OSSLSIGNCODE_VERSION}.tar.gz")"
  expect_fail "osslsigncode: a binary that reports another version is refused" "reports 'osslsigncode 0\\.1, using:'" ossl_install "${ossl_old}"
  [ ! -e "${TEST_TMP}/bin/osslsigncode" ] && ok "osslsigncode: the binary of the wrong version is removed" || fail "osslsigncode: the binary of the wrong version is still there"
  pin "${ossl_key}" "$(sha256_of "${ossl_near}/${OSSLSIGNCODE_VERSION}.tar.gz")"
  expect_fail "osslsigncode: a version that only contains the pinned one is refused" "reports 'osslsigncode ${OSSLSIGNCODE_VERSION}0, using:'" ossl_install "${ossl_near}"
  [ ! -e "${TEST_TMP}/bin/osslsigncode" ] && ok "osslsigncode: the near-miss binary is removed" || fail "osslsigncode: the near-miss binary is still there"
  # the environment cannot replace the pin
  pin "${ossl_key}" "${ossl_good_sha}"
  expect_fail "osslsigncode: the environment cannot override the pin" "SHA-256 mismatch" \
    env TOOLS_BIN="${TEST_TMP}/bin" OSSLSIGNCODE_BASE_URL="file://${ossl_old}" "${ossl_key}=$(sha256_of "${ossl_old}/${OSSLSIGNCODE_VERSION}.tar.gz")" "${repo}/ci/install-tools.sh" osslsigncode
fi
unpin "${ossl_key}"
expect_fail "osslsigncode: a missing pin is refused" "${ossl_key} in ci/versions.env is missing or not a SHA-256" ossl_install "${ossl_good}"

# the real pins: the SHA-256 values are well-formed, and ci/check.sh rejects bad pins (variables of the environment take
# precedence in load_versions, so the checks run against the real tree)
for v in SYFT_SHA256_DARWIN_ARM64 SYFT_SHA256_DARWIN_AMD64 SYFT_SHA256_LINUX_ARM64 SYFT_SHA256_LINUX_AMD64 \
         ZIZMOR_SHA256_DARWIN_ARM64 ZIZMOR_SHA256_DARWIN_AMD64 ZIZMOR_SHA256_LINUX_ARM64 ZIZMOR_SHA256_LINUX_AMD64; do
  printf '%s' "${!v:-}" | grep -Eq '^[0-9a-f]{64}$' && ok "${v} is a SHA-256" || fail "${v} is missing or not a SHA-256 in ci/versions.env"
done
expect_ok "ci/check.sh accepts the pins of the tree" "${CI_DIR}/check.sh"
expect_fail "ci/check.sh rejects a malformed zizmor SHA-256" "ZIZMOR_SHA256_LINUX_ARM64 is missing or not a SHA-256" \
  env ZIZMOR_SHA256_LINUX_ARM64=xyz "${CI_DIR}/check.sh"
expect_fail "ci/check.sh rejects a zizmor version with a v prefix" "ZIZMOR_VERSION .* not a plain version" \
  env ZIZMOR_VERSION=v1.30.1 "${CI_DIR}/check.sh"
expect_fail "ci/check.sh rejects a malformed syft SHA-256" "SYFT_SHA256_LINUX_AMD64 is missing or not a SHA-256" \
  env SYFT_SHA256_LINUX_AMD64=xyz "${CI_DIR}/check.sh"
expect_fail "ci/check.sh rejects a syft version with a v prefix" "SYFT_VERSION .* not a plain version" \
  env SYFT_VERSION=v1.54.1 "${CI_DIR}/check.sh"
expect_fail "ci/check.sh rejects a malformed osslsigncode SHA-256" "OSSLSIGNCODE_SOURCE_SHA256 is missing or not a SHA-256" \
  env OSSLSIGNCODE_SOURCE_SHA256=xyz "${CI_DIR}/check.sh"
expect_fail "ci/check.sh rejects an osslsigncode version with a v prefix" "OSSLSIGNCODE_VERSION .* not a plain version" \
  env OSSLSIGNCODE_VERSION=v2.14 "${CI_DIR}/check.sh"
expect_fail "ci/check.sh rejects a go.mod that disagrees with GH_VERSION" "ci/tools/gh/go.mod: .* is not v0.0.1" \
  env GH_VERSION=v0.0.1 "${CI_DIR}/check.sh"
expect_fail "ci/check.sh rejects a go.mod that disagrees with GOWINRES_VERSION" "ci/tools/go-winres/go.mod: .* is not v0.0.1" \
  env GOWINRES_VERSION=v0.0.1 "${CI_DIR}/check.sh"
finish_tests "tool-downloads-test"
