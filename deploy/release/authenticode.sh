#!/bin/sh
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Myriarch - Authenticode signature of the Windows binaries (post-build hook of goreleaser.yaml).
#
# Usage: authenticode.sh <binary> <is-snapshot>
#   - Non-Windows binaries (no .exe): nothing to do.
#   - Snapshot builds (is-snapshot=true) stay unsigned.
#   - Release builds abort without a certificate (fail closed): AUTHENTICODE_PFX (path) and
#     AUTHENTICODE_PFX_PASSWORD_FILE (path of the password file); optionally AUTHENTICODE_TIMESTAMP_URL.
set -eu

bin="${1:?binary}"
snapshot="${2:-false}"

case "${bin}" in
  *.exe) ;;
  *) exit 0 ;;
esac
if [ "${snapshot}" = "true" ]; then
  echo "authenticode: snapshot build, ${bin} stays unsigned" >&2
  exit 0
fi
command -v osslsigncode >/dev/null 2>&1 || { echo "authenticode: osslsigncode is missing" >&2; exit 1; }
: "${AUTHENTICODE_PFX:?AUTHENTICODE_PFX (certificate) is not set: release builds must be signed}"
: "${AUTHENTICODE_PFX_PASSWORD_FILE:?AUTHENTICODE_PFX_PASSWORD_FILE is not set}"
ts="${AUTHENTICODE_TIMESTAMP_URL:-http://timestamp.digicert.com}"

osslsigncode sign -pkcs12 "${AUTHENTICODE_PFX}" -readpass "${AUTHENTICODE_PFX_PASSWORD_FILE}" \
  -h sha256 -t "${ts}" -in "${bin}" -out "${bin}.signed"
mv "${bin}.signed" "${bin}"
# Trust anchors of the verification: AUTHENTICODE_CA_FILE (private PKI of the code-signing certificate; default:
# EXTRA_CA_FILE) and AUTHENTICODE_TSA_CA_FILE (CA of the timestamp service; default: the same file). Without a CA file the
# system trust store applies (public CAs).
ca="${AUTHENTICODE_CA_FILE:-${EXTRA_CA_FILE:-}}"
tsa_ca="${AUTHENTICODE_TSA_CA_FILE:-${ca}}"
vargs=""
if [ -n "${ca}" ] && [ -r "${ca}" ]; then vargs="-CAfile ${ca}"; fi
if [ -n "${tsa_ca}" ] && [ -r "${tsa_ca}" ]; then vargs="${vargs} -TSA-CAfile ${tsa_ca}"; fi
# shellcheck disable=SC2086 # vargs: file paths without spaces
osslsigncode verify ${vargs} -in "${bin}" >/dev/null 2>&1 || { echo "authenticode: verification of ${bin} failed" >&2; exit 1; }
echo "authenticode: ${bin} signed"
