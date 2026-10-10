#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Verifies a myriarch release: the signature of checksums.txt (openssl AND, if present, cosign) and optionally every
# file against checksums.txt and its own .sig. Also meant for operators (needs only bash, curl, openssl).
#
#   contrib/enterprise-mirror/verify-release.sh --public-key cosign.pub --base-url <mirror>/myriarch-releases/v0.1.0 [--assets]
#   contrib/enterprise-mirror/verify-release.sh --public-key cosign.pub --dir <directory-with-files>                [--assets]
#   contrib/enterprise-mirror/verify-release.sh --public-key cosign.pub --dist dist --version 0.1.0                   [--assets]
#
#   --base-url URL   directory of the release (checksums.txt and checksums.txt.sig live below it)
#   --dir DIR        local directory (flat, upload names)
#   --dist DIR       goreleaser dist (mapped to upload names with --version, see ci/lib.sh)
#   --assets         additionally verify all files listed in checksums.txt that are available
#                    (a mirror holds only some of the files; missing ones are skipped)
#
# Environment (--base-url only): VERIFY_AUTH_HEADER ("Authorization: Bearer ..."), CURL_CA_FILE
# (additional CA). Proxy: HTTPS_PROXY/NO_PROXY. Exit 0 = everything valid.
#
# Stable error tokens (in square brackets in the message): [signature_invalid], [checksum_mismatch].
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=ci/lib.sh
. "${HERE}/../../ci/lib.sh"

key=""; base=""; dir=""; dist=""; version=""; assets=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --public-key) key="${2:?}"; shift 2 ;;
    --base-url)   base="${2:?}"; base="${base%/}"; shift 2 ;;
    --dir)        dir="${2:?}"; shift 2 ;;
    --dist)       dist="${2:?}"; shift 2 ;;
    --version)    version="${2:?}"; shift 2 ;;
    --assets)     assets=1; shift ;;
    -h|--help)    sed -n '2,18p' "$0"; exit 0 ;;
    *)            die "unknown option: $1" ;;
  esac
done
[ -r "${key}" ] || die "--public-key is missing or not readable"
sources=0; for v in "${base}" "${dir}" "${dist}"; do [ -n "${v}" ] && sources=$((sources + 1)); done
[ "${sources}" = 1 ] || die "give exactly one source: --base-url, --dir or --dist"
need_cmd openssl
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

mkdir -p "${work}/in"
fetch checksums.txt "${work}/in/checksums.txt" || die "checksums.txt not found (${base:-${dir}})"
fetch checksums.txt.sig "${work}/in/checksums.txt.sig" || die "checksums.txt.sig not found (${base:-${dir}})"
verify_sig "${work}/in/checksums.txt" "${work}/in/checksums.txt.sig" "checksums.txt"
log "checksums.txt: signature valid ($(have cosign && echo 'openssl + cosign' || echo 'openssl'))"

if [ "${assets}" = 1 ]; then
  checked=0; skipped=0
  while read -r hash name; do
    name="${name#\*}"
    [[ "${hash}" =~ ^[0-9a-fA-F]{64}$ ]] && [ -n "${name}" ] || continue
    [[ "${name}" != */* && "${name}" != ..* ]] || die "invalid file name in checksums.txt: ${name}"
    f="${work}/in/${name}"
    if ! fetch "${name}" "${f}"; then skipped=$((skipped + 1)); continue; fi
    [ "$(sha256_of "${f}")" = "$(printf '%s' "${hash}" | tr 'A-F' 'a-f')" ] || die "[checksum_mismatch] ${name}: SHA-256 does not match checksums.txt"
    fetch "${name}.sig" "${f}.sig" || die "${name}: ${name}.sig is missing"
    verify_sig "${f}" "${f}.sig" "${name}"
    checked=$((checked + 1))
    rm -f "${f}"
  done < "${work}/in/checksums.txt"
  [ "${checked}" -gt 0 ] || die "none of the files listed in checksums.txt was available"
  log "${checked} files verified (SHA-256 + signature), ${skipped} not present (skipped)"
fi
log "OK: the release is valid"
