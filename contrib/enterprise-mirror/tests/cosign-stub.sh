#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Stand-in for cosign in the keyless tests of verify-release.sh and publish-mirror.sh (copied as "cosign" into a directory that
# the test puts first in TOOLS_BIN and PATH). It is no signature check:
#   cosign version      prints a GitVersion line (STUB_COSIGN_VERSION, default v3.1.3)
#   cosign verify-blob  appends its arguments to STUB_LOG (one per line, "----" after each call) and accepts a call only when a
#                       non-empty --bundle exists and --certificate-identity equals STUB_EXPECT_IDENTITY, like a bundle that
#                       was signed for exactly one tag
if [ "$1" = version ]; then printf 'GitVersion:    %s\n' "${STUB_COSIGN_VERSION:-v3.1.3}"; exit 0; fi
{ for a in "$@"; do printf '%s\n' "$a"; done; echo ----; } >> "${STUB_LOG}"
[ "$1" = verify-blob ] || exit 2
id=""; bundle=""
while [ "$#" -gt 0 ]; do
  case "$1" in --certificate-identity) id="$2"; shift 2 ;; --bundle) bundle="$2"; shift 2 ;; *) shift ;; esac
done
[ -s "${bundle}" ] || { echo "Error: bundle missing" >&2; exit 1; }
[ "${id}" = "${STUB_EXPECT_IDENTITY}" ] || { echo "Error: expected SAN value \"${id}\"" >&2; exit 1; }
echo "Verified OK" >&2
