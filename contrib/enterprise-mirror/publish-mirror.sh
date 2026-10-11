#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Copies the host artifacts of a release into the mirror repository myriarch-releases (Artifactory or Nexus) so that
# installers and myriarch-updaters on the hosts can download them without credentials and without internet
# access. Two modes:
#
#   contrib/enterprise-mirror/publish-mirror.sh [--dist dist] [--version X.Y.Z] [--dry-run]          key mode (default)
#   contrib/enterprise-mirror/publish-mirror.sh --keyless --dir DIR --version X.Y.Z[-rc.N] [--dry-run] [--trusted-root FILE]
#
# Key mode: releases that the organization re-signed with its own key. Uploads to ${MIRROR_REPO_URL}/v<version>/ (layout like
# GitHub releases):
#   myriarch-updater_<ver>_<os>_<arch>[.exe]   bare binary (target of install-*.sh --updater-url)
#   myriarch-updater_<ver>_<os>_<arch>.tar.gz|.zip, myrctl_<ver>_<os>_<arch>.tar.gz|.zip
#   checksums.txt, checksums.txt.sig, cosign.pub and the .sig of every uploaded file
# Order: binaries/archives first, then checksums.txt, checksums.txt.sig last (the signature is the "ready
# marker": an installer that finds it also finds all files).
#
# Keyless mode: the project's own release, mirrored unchanged. DIR holds the files that you downloaded from the GitHub release
# of the tag (flat, upload names). The script first runs verify-release.sh --keyless --assets on DIR (the bundle against the
# identity of release.yml at the tag, then the SHA-256 of every file against checksums.txt), then uploads the files of the host
# scope that checksums.txt lists and DIR holds, unchanged: the bare myriarch-updater binaries and archives and the myrctl
# archives (SCOPE=all adds every other file that checksums.txt lists and the provenance file), then checksums.txt, then
# checksums.txt.sigstore.json last (the ready marker). There is no cosign.pub and no .sig file in this mode, and a rewritten
# checksums.txt would no longer match the bundle.
#
# Immutable (both modes): a file that already exists with the same SHA-256 is skipped (so the script can be repeated); with a
# different SHA-256 the script aborts (never overwrites). Before the upload all signatures are verified
# (contrib/enterprise-mirror/verify-release.sh); after the upload every file is fetched again and compared.
#
# Environment:
#   MIRROR_REPO_URL   for example https://artifactory.example.com/artifactory/myriarch-releases
#                     or https://nexus.example.com/repository/myriarch-releases (raw hosted repository)
#   MIRROR_TYPE       artifactory | nexus
#   MIRROR_TOKEN      artifactory: access token (Bearer)
#   MIRROR_USER, MIRROR_PASSWORD   nexus: basic authentication
#   MIRROR_CA_FILE    optional: extra CA file (otherwise the system certificates)
#   MIRROR_ALLOW_HTTP=1  for local tests only (credentials would otherwise travel in clear text)
#   COSIGN_PUBLIC_KEY path of the public key (key mode; default: deploy/release/cosign.pub)
#   SCOPE             keyless mode: host (default) or all
# Credentials never go on the command line (curl configuration through stdin); proxy: HTTPS_PROXY/NO_PROXY.
#
# Stable error tokens (in square brackets in the message): [mirror_https_required], [mirror_type_invalid],
# [mirror_access_denied], [mirror_content_differs], [mirror_readback_mismatch], [scope_invalid].
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=ci/lib.sh
. "${HERE}/../../ci/lib.sh"

dist="${ROOT}/dist"; version=""; dry=0; keyless=0; dir=""; trusted_root=""; dist_given=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dist)    dist="${2:?}"; dist_given=1; shift 2 ;;
    --version) version="${2:?}"; shift 2 ;;
    --dry-run) dry=1; shift ;;
    --keyless) keyless=1; shift ;;
    --dir)     dir="${2:?}"; shift 2 ;;
    --trusted-root) trusted_root="${2:?}"; shift 2 ;;
    -h|--help) sed -n '2,45p' "$0"; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done
if [ "${keyless}" = 1 ]; then
  [ "${dist_given}" = 0 ] || die "--keyless takes the release files from --dir, not from --dist"
  [ -n "${dir}" ] || die "--keyless needs --dir (the directory with the downloaded release files)"
  [ -d "${dir}" ] || die "directory is missing: ${dir}"
  [ -n "${version}" ] || die "--keyless needs --version"
  version="${version#v}"
  [[ "${version}" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$ ]] || die "invalid --version '${version}': expected X.Y.Z or X.Y.Z-rc.N"
  case "${SCOPE:-host}" in host|all) ;; *) die "[scope_invalid] SCOPE: host or all" ;; esac
else
  [ -z "${dir}" ] || die "--dir belongs to --keyless"
  [ -z "${trusted_root}" ] || die "--trusted-root belongs to --keyless"
fi

need_env MIRROR_REPO_URL MIRROR_TYPE
repo="${MIRROR_REPO_URL%/}"
case "${repo}" in
  https://?*) ;;
  http://?*) [ "${MIRROR_ALLOW_HTTP:-0}" = 1 ] || die "[mirror_https_required] MIRROR_REPO_URL must be https:// (avoid credentials in clear text)" ;;
  *) die "[mirror_https_required] MIRROR_REPO_URL must start with https://" ;;
esac
case "${MIRROR_TYPE}" in
  artifactory) need_env MIRROR_TOKEN ;;
  nexus)       need_env MIRROR_USER MIRROR_PASSWORD ;;
  *) die "[mirror_type_invalid] MIRROR_TYPE: artifactory or nexus" ;;
esac
need_cmd curl openssl
if [ "${keyless}" = 0 ]; then
  [ -d "${dist}" ] || die "dist directory is missing: ${dist}"
  if [ -z "${version}" ]; then version="$(release_version)"; fi
  [[ "${version}" =~ ^[0-9A-Za-z][0-9A-Za-z.+-]*$ ]] || die "invalid version: ${version}"
  pub="${COSIGN_PUBLIC_KEY:-${ROOT}/deploy/release/cosign.pub}"
  [ -r "${pub}" ] || die "public key is missing: ${pub} (contrib/enterprise-mirror/signing.md)"
fi

work="$(mktemp -d)"; trap 'rm -rf "${work}"' EXIT
stage="${work}/stage"
if [ "${keyless}" = 1 ]; then
  # The files of the scope that checksums.txt lists and DIR holds, staged as links to the downloaded files (unchanged).
  mkdir -p "${stage}"
  log "verifying the bundle and the files before the upload"
  vargs=(--keyless --dir "${dir}" --version "${version}" --assets)
  if [ -n "${trusted_root}" ]; then vargs+=(--trusted-root "${trusted_root}"); fi
  "${HERE}/verify-release.sh" "${vargs[@]}"
  srcdir="$(cd "${dir}" && pwd)"
  staged=0
  while read -r hash name; do
    name="${name#\*}"
    [[ "${hash}" =~ ^[0-9a-fA-F]{64}$ ]] && [ -n "${name}" ] || continue
    [[ "${name}" != */* && "${name}" != ..* ]] || die "invalid file name in checksums.txt: ${name}"
    case "${SCOPE:-host}" in
      host) case "${name}" in
              "myriarch-updater_${version}_"*|"myrctl_${version}_"*) ;;
              *) continue ;;
            esac
            case "${name}" in *.sbom.json) continue ;; esac ;;
    esac
    [ -f "${srcdir}/${name}" ] || { log "not in ${dir}, skipped: ${name}"; continue; }
    ln -s "${srcdir}/${name}" "${stage}/${name}"; staged=$((staged + 1))
  done < "${srcdir}/checksums.txt"
  if [ "${SCOPE:-host}" = all ] && [ -f "${srcdir}/myriarch_${version}_provenance.intoto.jsonl" ]; then
    ln -s "${srcdir}/myriarch_${version}_provenance.intoto.jsonl" "${stage}/myriarch_${version}_provenance.intoto.jsonl"; staged=$((staged + 1))
  fi
  [ "${staged}" -gt 0 ] || die "none of the files of the scope ${SCOPE:-host} that checksums.txt lists is in ${dir}"
  ln -s "${srcdir}/checksums.txt" "${stage}/checksums.txt"
  ln -s "${srcdir}/checksums.txt.sigstore.json" "${stage}/checksums.txt.sigstore.json"
else
  stage_release_assets "${dist}" "${version}" "${stage}"
  cp "${pub}" "${stage}/cosign.pub"

  log "verifying signatures before the upload"
  "${HERE}/verify-release.sh" --public-key "${pub}" --dir "${stage}" --assets
fi

cfg_escape() { local v="$1"; v="${v//\\/\\\\}"; printf '%s' "${v//\"/\\\"}"; }
auth_config() {
  case "${MIRROR_TYPE}" in
    artifactory) printf 'header = "Authorization: Bearer %s"\n' "$(cfg_escape "${MIRROR_TOKEN}")" ;;
    nexus)       printf 'user = "%s:%s"\n' "$(cfg_escape "${MIRROR_USER}")" "$(cfg_escape "${MIRROR_PASSWORD}")" ;;
  esac
}
ca_args=()
ca="${MIRROR_CA_FILE:-}"
if [ -n "${ca}" ] && [ -r "${ca}" ]; then ca_args=(--cacert "${ca}"); fi
mcurl() { auth_config | curl -sS -K - --connect-timeout 20 --retry 3 ${ca_args[@]+"${ca_args[@]}"} "$@"; }

# Result: checksum of the file in the mirror on stdout, empty if it does not exist
remote_sha256() { # url
  local out="${work}/remote.bin" code
  code="$(mcurl -o "${out}" -w '%{http_code}' "$1")" || die "download failed: $1"
  case "${code}" in
    200) sha256_of "${out}" ;;
    404) ;;
    401|403) die "[mirror_access_denied] the mirror denies access (HTTP ${code}): check MIRROR_TOKEN or MIRROR_USER/MIRROR_PASSWORD and the write permission" ;;
    *) die "the mirror answers with HTTP ${code} for $1" ;;
  esac
}

upload() { # name
  local name="$1" file="${stage}/$1" url="${repo}/v${version}/$1" local_sha remote
  local_sha="$(sha256_of "${file}")"
  remote="$(remote_sha256 "${url}")"
  if [ -n "${remote}" ]; then
    [ "${remote}" = "${local_sha}" ] || die "[mirror_content_differs] ${url} already exists with different content (mirror: ${remote}, local: ${local_sha}) - releases are immutable; create a new version"
    log "unchanged: ${name}"; UNCHANGED=$((UNCHANGED + 1)); return 0
  fi
  if [ "${dry}" = 1 ]; then log "would upload: ${name}"; return 0; fi
  local code
  if [ "${MIRROR_TYPE}" = artifactory ]; then
    code="$(mcurl -o /dev/null -w '%{http_code}' -H "X-Checksum-Sha256: ${local_sha}" -T "${file}" "${url}")" || die "upload failed: ${name}"
  else
    code="$(mcurl -o /dev/null -w '%{http_code}' --upload-file "${file}" "${url}")" || die "upload failed: ${name}"
  fi
  case "${code}" in 200|201|204) ;; *) die "upload of ${name}: HTTP ${code}" ;; esac
  remote="$(remote_sha256 "${url}")"
  [ "${remote}" = "${local_sha}" ] || die "[mirror_readback_mismatch] the read-back of ${name} does not match the local file"
  log "uploaded: ${name}"; UPLOADED=$((UPLOADED + 1))
}

UPLOADED=0; UNCHANGED=0
if [ "${keyless}" = 1 ]; then
  # everything except checksums.txt and its bundle first; checksums.txt, then the bundle (the ready marker) last
  while IFS= read -r name; do
    case "${name}" in checksums.txt|checksums.txt.sigstore.json) continue ;; esac
    upload "${name}"
  done < <(cd "${stage}" && printf '%s\n' * | LC_ALL=C sort)
  upload checksums.txt
  upload checksums.txt.sigstore.json
else
  # everything except checksums.txt/.sig and cosign.pub first, then cosign.pub, checksums.txt, checksums.txt.sig last
  while IFS= read -r name; do
    case "${name}" in checksums.txt|checksums.txt.sig|cosign.pub) continue ;; esac
    upload "${name}"
  done < <(cd "${stage}" && printf '%s\n' * | LC_ALL=C sort)
  upload cosign.pub
  upload checksums.txt
  upload checksums.txt.sig
fi
log "mirror ${repo}/v${version}/: ${UPLOADED} uploaded, ${UNCHANGED} unchanged"
