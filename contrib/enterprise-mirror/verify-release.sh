#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Verifies a myriarch release in one of two modes. Also meant for operators (needs only bash, curl and openssl or cosign).
#
# Key mode (releases that a mirror re-signed with its own key): the signature of checksums.txt (openssl AND, if present,
# cosign) and optionally every file against checksums.txt and its own .sig.
#
#   contrib/enterprise-mirror/verify-release.sh --public-key cosign.pub --base-url <mirror>/myriarch-releases/v0.1.0 [--assets]
#   contrib/enterprise-mirror/verify-release.sh --public-key cosign.pub --dir <directory-with-files>                [--assets]
#   contrib/enterprise-mirror/verify-release.sh --public-key cosign.pub --dist dist --version 0.1.0                   [--assets]
#
# Keyless mode (the project's own releases, mirrored unchanged): the Sigstore bundle checksums.txt.sigstore.json is verified
# with cosign 3.0.4 or newer against the identity of release.yml at the tag of the version, and optionally every file
# against checksums.txt. There are no per-file .sig files.
#
#   contrib/enterprise-mirror/verify-release.sh --keyless --version 0.1.0 --base-url <mirror>/myriarch-releases/v0.1.0 [--assets] [--trusted-root FILE]
#   contrib/enterprise-mirror/verify-release.sh --keyless --version 0.1.0 --dir <directory-with-files>                [--assets] [--trusted-root FILE]
#
#   --public-key F   key mode: public key of the mirror (exactly one of --public-key and --keyless)
#   --keyless        keyless mode (needs --version; --dist is not available)
#   --version V      keyless mode: the release to verify, X.Y.Z or X.Y.Z-rc.N (a leading v is accepted)
#   --trusted-root F keyless mode: Sigstore trusted_root.json for hosts without access to the Sigstore infrastructure
#                    (myriarch-updater fetch-trusted-root --out trusted_root.json)
#   --base-url URL   directory of the release (checksums.txt and its signature live below it)
#   --dir DIR        local directory (flat, upload names)
#   --dist DIR       goreleaser dist (mapped to upload names with --version, see ci/lib.sh); key mode only
#   --assets         additionally verify all files listed in checksums.txt that are available
#                    (a mirror holds only some of the files; missing ones are skipped)
#
# Environment (--base-url only): VERIFY_AUTH_HEADER ("Authorization: Bearer ..."), CURL_CA_FILE
# (additional CA). Proxy: HTTPS_PROXY/NO_PROXY. Exit 0 = everything valid.
#
# Stable error tokens (in square brackets in the message): [signature_invalid], [checksum_mismatch], [cosign_missing],
# [cosign_outdated].
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=ci/lib.sh
. "${HERE}/../../ci/lib.sh"

# The identity of the project's releases (ADR-0029, ADR-0035): release.yml of this repository at the tag of the version. The
# repository is pinned to internal/brand/brand.go by the tests of this directory.
readonly RELEASE_REPOSITORY="myriarch/myriarch"
readonly RELEASE_WORKFLOW=".github/workflows/release.yml"
readonly RELEASE_OIDC_ISSUER="https://token.actions.githubusercontent.com"
readonly MIN_COSIGN_VERSION="3.0.4"   # as the installers; older versions have known verification flaws

key=""; base=""; dir=""; dist=""; version=""; assets=0; keyless=0; trusted_root=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --public-key) key="${2:?}"; shift 2 ;;
    --keyless)    keyless=1; shift ;;
    --trusted-root) trusted_root="${2:?}"; shift 2 ;;
    --base-url)   base="${2:?}"; base="${base%/}"; shift 2 ;;
    --dir)        dir="${2:?}"; shift 2 ;;
    --dist)       dist="${2:?}"; shift 2 ;;
    --version)    version="${2:?}"; shift 2 ;;
    --assets)     assets=1; shift ;;
    -h|--help)    sed -n '2,36p' "$0"; exit 0 ;;
    *)            die "unknown option: $1" ;;
  esac
done
if [ "${keyless}" = 1 ]; then
  [ -z "${key}" ] || die "--keyless and --public-key exclude each other: give exactly one"
  [ -z "${dist}" ] || die "--keyless works on a release directory (--dir) or a mirror (--base-url), not on --dist"
  [ -n "${version}" ] || die "--keyless needs --version (the release is verified against the identity of its tag)"
  version="${version#v}"
  [[ "${version}" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$ ]] || die "invalid --version '${version}': expected X.Y.Z or X.Y.Z-rc.N"
  [ -z "${trusted_root}" ] || [ -r "${trusted_root}" ] || die "--trusted-root is not readable: ${trusted_root}"
else
  [ -z "${trusted_root}" ] || die "--trusted-root belongs to --keyless"
  [ -n "${key}" ] || die "give exactly one of --public-key (key mode) and --keyless"
  [ -r "${key}" ] || die "--public-key is missing or not readable"
fi
sources=0; for v in "${base}" "${dir}" "${dist}"; do [ -n "${v}" ] && sources=$((sources + 1)); done
[ "${sources}" = 1 ] || die "give exactly one source: --base-url, --dir or --dist"
if [ "${keyless}" = 1 ]; then need_cmd curl; else need_cmd openssl; fi
work="$(mktemp -d)"; trap 'rm -rf "${work}"' EXIT

if [ -n "${dist}" ]; then
  [ -n "${version}" ] || die "--dist needs --version"
  dir="${work}/stage"; stage_release_assets "${dist}" "${version#v}" "${dir}" "${SCOPE:-all}"
fi

# Fetch with manual redirect handling: the auth header (VERIFY_AUTH_HEADER, e.g. "Authorization: Bearer ...") goes only to
# the origin of the first URL, NEVER to another host (artifact stores often redirect downloads to object storage; curl
# would forward custom headers there).
curl_get() { # url dest -> 0 found, 1 not found (404); otherwise abort
  local url="$1" dest="$2" code a hops=0 origin cur loc hdr="${work}/curl-headers"
  local ca=()
  while IFS= read -r a; do if [ -n "${a}" ]; then ca+=("${a}"); fi; done < <(curl_ca_args)
  origin="$(printf '%s' "${url}" | sed -n 's|^\(https\{0,1\}://[^/]*\).*|\1|p')"
  while :; do
    cur="$(printf '%s' "${url}" | sed -n 's|^\(https\{0,1\}://[^/]*\).*|\1|p')"
    code="$( { if [ -n "${VERIFY_AUTH_HEADER:-}" ] && [ "${cur}" = "${origin}" ]; then printf 'header = "%s"\n' "${VERIFY_AUTH_HEADER//\"/\\\"}"; fi; } \
      | curl -sS -K - --retry 3 --connect-timeout 20 ${ca[@]+"${ca[@]}"} -D "${hdr}" -o "${dest}" -w '%{http_code}' "${url}")" \
      || die "download failed: ${url}"
    case "${code}" in
      200) return 0 ;;
      404) return 1 ;;
      301|302|303|307|308)
        hops=$((hops + 1)); [ "${hops}" -le 5 ] || die "too many redirects: ${url}"
        loc="$(tr -d '\r' < "${hdr}" | awk 'tolower($1)=="location:" {print $2; exit}')"
        [ -n "${loc}" ] || die "redirect without Location: ${url}"
        case "${loc}" in /*) url="${cur}${loc}" ;; *) url="${loc}" ;; esac ;;
      *) die "download of ${url}: HTTP ${code}" ;;
    esac
  done
}

fetch() { # name dest -> 0/1 (not present)
  if [ -n "${base}" ]; then curl_get "${base}/$1" "$2"
  elif [ -e "${dir}/$1" ]; then cp -L "${dir}/$1" "$2"
  else return 1
  fi
}

verify_sig() { # file sig-file label
  local der="${work}/sig.der"
  openssl base64 -d -A -in "$2" -out "${der}" 2>/dev/null || die "[signature_invalid] $3: the signature is not Base64"
  openssl dgst -sha256 -verify "${key}" -signature "${der}" "$1" >/dev/null 2>&1 || die "[signature_invalid] $3: signature INVALID (openssl)"
  if have cosign; then
    cosign verify-blob --key "${key}" --signature "$2" --insecure-ignore-tlog=true "$1" >/dev/null 2>&1 || die "[signature_invalid] $3: signature INVALID (cosign)"
  fi
}

# Succeeds when the cosign at $1 is MIN_COSIGN_VERSION or newer (a pre-release of the minimum is too old); otherwise prints the
# reason. Same rule as the installers.
cosign_version_problem() { # path
  local out ver
  out="$("$1" version 2>&1)" || { printf 'cosign "%s version" failed' "$1"; return 1; }
  ver="$(printf '%s\n' "${out}" | awk '/^GitVersion:/ { print $2; exit }')"
  ver="${ver#v}"
  if ! [[ "${ver}" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)(-[^+]+)?(\+.*)?$ ]]; then
    printf 'cosign "%s" reports no usable version' "$1"; return 1
  fi
  local want="${MIN_COSIGN_VERSION}" ma="${BASH_REMATCH[1]}" mi="${BASH_REMATCH[2]}" pa="${BASH_REMATCH[3]}" pre="${BASH_REMATCH[4]}"
  local wma="${want%%.*}" rest="${want#*.}"
  local wmi="${rest%%.*}" wpa="${rest#*.}"
  if [ -n "${pre}" ] && [ "${ma}.${mi}.${pa}" = "${want}" ]; then
    printf 'cosign %s.%s.%s%s is a pre-release of the minimum' "${ma}" "${mi}" "${pa}" "${pre}"; return 1
  fi
  if [ "${ma}" -gt "${wma}" ] \
    || { [ "${ma}" -eq "${wma}" ] && [ "${mi}" -gt "${wmi}" ]; } \
    || { [ "${ma}" -eq "${wma}" ] && [ "${mi}" -eq "${wmi}" ] && [ "${pa}" -ge "${wpa}" ]; }; then
    return 0
  fi
  printf 'cosign %s.%s.%s is too old' "${ma}" "${mi}" "${pa}"; return 1
}

# Keyless: cosign verify-blob of checksums.txt with the bundle against the exact identity of the tag of the version. The
# identity is not a pattern: a signature of the same workflow for another tag is genuine and must not pass.
verify_keyless() { # checksums.txt bundle
  local identity="https://github.com/${RELEASE_REPOSITORY}/${RELEASE_WORKFLOW}@refs/tags/v${version}" problem err
  have cosign || die "[cosign_missing] cosign ${MIN_COSIGN_VERSION} or newer is required for --keyless (ci/install-tools.sh cosign)"
  problem="$(cosign_version_problem "$(command -v cosign)")" || die "[cosign_outdated] ${problem}: cosign ${MIN_COSIGN_VERSION} or newer is required"
  local args=(verify-blob --bundle "$2" --certificate-identity "${identity}" --certificate-oidc-issuer "${RELEASE_OIDC_ISSUER}")
  if [ -n "${trusted_root}" ]; then args+=(--trusted-root "${trusted_root}"); fi
  err="${work}/cosign.err"
  cosign "${args[@]}" "$1" >/dev/null 2>"${err}" \
    || die "[signature_invalid] checksums.txt: the bundle does not verify against ${identity} ($(tail -n 1 "${err}" | cut -c1-200))"
}

mkdir -p "${work}/in"
fetch checksums.txt "${work}/in/checksums.txt" || die "checksums.txt not found (${base:-${dir}})"
if [ "${keyless}" = 1 ]; then
  fetch checksums.txt.sigstore.json "${work}/in/checksums.txt.sigstore.json" || die "checksums.txt.sigstore.json not found (${base:-${dir}})"
  verify_keyless "${work}/in/checksums.txt" "${work}/in/checksums.txt.sigstore.json"
  log "checksums.txt: bundle valid for the identity of release.yml at v${version}"
else
  fetch checksums.txt.sig "${work}/in/checksums.txt.sig" || die "checksums.txt.sig not found (${base:-${dir}})"
  verify_sig "${work}/in/checksums.txt" "${work}/in/checksums.txt.sig" "checksums.txt"
  log "checksums.txt: signature valid ($(have cosign && echo 'openssl + cosign' || echo 'openssl'))"
fi

if [ "${assets}" = 1 ]; then
  checked=0; skipped=0
  while read -r hash name; do
    name="${name#\*}"
    [[ "${hash}" =~ ^[0-9a-fA-F]{64}$ ]] && [ -n "${name}" ] || continue
    [[ "${name}" != */* && "${name}" != ..* ]] || die "invalid file name in checksums.txt: ${name}"
    f="${work}/in/${name}"
    if ! fetch "${name}" "${f}"; then skipped=$((skipped + 1)); continue; fi
    [ "$(sha256_of "${f}")" = "$(printf '%s' "${hash}" | tr 'A-F' 'a-f')" ] || die "[checksum_mismatch] ${name}: SHA-256 does not match checksums.txt"
    if [ "${keyless}" = 0 ]; then
      fetch "${name}.sig" "${f}.sig" || die "${name}: ${name}.sig is missing"
      verify_sig "${f}" "${f}.sig" "${name}"
    fi
    checked=$((checked + 1))
    rm -f "${f}" "${f}.sig"
  done < "${work}/in/checksums.txt"
  [ "${checked}" -gt 0 ] || die "none of the files listed in checksums.txt was available"
  if [ "${keyless}" = 1 ]; then
    log "${checked} files verified (SHA-256 against the signed checksums.txt), ${skipped} not present (skipped)"
  else
    log "${checked} files verified (SHA-256 + signature), ${skipped} not present (skipped)"
  fi
fi
log "OK: the release is valid"
