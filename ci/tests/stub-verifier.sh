#!/bin/sh
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test stub for "cosign" and "myriarch-updater verify-bundle" (ci/tests/installer-verify.sh and .ps1). It records its
# arguments in $STUB_LOG and emulates what matters for the installers:
#   - "version" prints a cosign version line ($STUB_COSIGN_VERSION, default v3.1.3);
#   - for checksums.txt the identity regexp that the installer passes must match $STUB_SAN (the certificate identity of the
#     bundle), the issuer must be GitHub's, a verify-bundle call must carry --version and $STUB_SAN must end with that tag,
#     and the file must hash to $STUB_GOOD_SHA, so that a changed file fails like a real signature check;
#   - any other file (collector, supervisor) always passes.
if [ "$1" = version ]; then printf 'GitVersion:    %s\n' "${STUB_COSIGN_VERSION:-v3.1.3}"; exit 0; fi
{ printf 'CALL'; for a in "$@"; do printf ' %s' "$a"; done; printf '\n'; } >> "${STUB_LOG}"
mode="$1"; shift
file=""; re=""; iss=""; ver=""
while [ "$#" -gt 0 ]; do
  case "${mode}:$1" in
    verify-blob:--certificate-identity-regexp|verify-bundle:--identity-regexp) re="$2"; shift 2 ;;
    verify-blob:--certificate-oidc-issuer|verify-bundle:--issuer) iss="$2"; shift 2 ;;
    verify-bundle:--artifact) file="$2"; shift 2 ;;
    verify-bundle:--version) ver="$2"; shift 2 ;;
    *:--bundle|*:--trusted-root) shift 2 ;;
    verify-blob:*) file="$1"; shift ;;
    *) shift ;;
  esac
done
case "$(basename "${file}")" in checksums.txt) ;; *) exit 0 ;; esac
[ "${iss}" = "https://token.actions.githubusercontent.com" ] || exit 1
printf '%s\n' "${STUB_SAN}" | grep -Eq -- "${re}" || exit 1
if [ -n "${ver}" ]; then case "${STUB_SAN}" in *"@refs/tags/${ver}") ;; *) exit 1 ;; esac; fi
if [ -n "${STUB_GOOD_SHA:-}" ]; then
  got="$(sha256sum "${file}" 2>/dev/null || shasum -a 256 "${file}")"
  [ "${got%% *}" = "${STUB_GOOD_SHA}" ] || exit 1
fi
exit 0
