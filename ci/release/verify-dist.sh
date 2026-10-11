#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Checks that a directory holds exactly the files of a release and that checksums.txt describes them (ADR-0035). The expected names
# come from deploy/release/assets.json: 42 names in stage `final`, 40 in stage `unsigned` (without the signature of checksums.txt
# and the provenance file).
#
#   ci/release/verify-dist.sh --dir DIR --version V --stage unsigned|final [--assets-file FILE]
#
# It refuses
#   - an expected name that is missing [asset_missing]
#   - any other entry in DIR, also a directory or a hidden file [asset_unexpected]
#   - an expected name that is a link, a directory or another non-regular file [asset_not_regular]
#   - a line of checksums.txt that is not "<64 hex>  <name>" without a slash or a space (also a name twice) [checksums_malformed]
#   - a listed name that is not one of the 39 archive, binary, SBOM and license files [checksums_unexpected]
#   - one of the 39 that is not listed [checksums_incomplete]
#   - a hash that differs from the file [checksum_mismatch] (names the file)
#   - with --stage final: checksums.txt.sigstore.json or the provenance file empty [asset_empty] (missing is [asset_missing]; neither
#     is listed in checksums.txt, which would be [checksums_unexpected])
# stdout stays empty; stderr: "[verify-dist] ok: 42 assets, 39 checksums" (or 40 assets).
# V is X.Y.Z, X.Y.Z-rc.N or, for the rehearsal, X.Y.Z-snapshot.
#
# Exit status: 0 ok, 1 refused, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="verify-dist.sh --dir DIR --version V --stage unsigned|final [--assets-file FILE]"
dir="" version="" stage="" assets_file=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dir) need_value "$1" "$#"; dir="$2"; shift 2 ;;
    --version) need_value "$1" "$#"; version="$2"; shift 2 ;;
    --stage) need_value "$1" "$#"; stage="$2"; shift 2 ;;
    --assets-file) need_value "$1" "$#"; assets_file="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
    *) usage_err "${USAGE}" ;;
  esac
done
[ -n "${dir}" ] && [ -n "${version}" ] && [ -n "${stage}" ] || usage_err "${USAGE}"
case "${stage}" in unsigned|final) ;; *) usage_err "${USAGE}" ;; esac
check_match version_invalid "--version" "${version}" "${SNAPSHOT_VERSION_RE}"
check_text asset_unexpected "--dir" "${dir}"
[ -d "${dir}" ] || err asset_missing "${dir} is not a directory"

if [ "${stage}" = final ]; then set_mode=names; else set_mode=unsigned; fi
expected=" "
while IFS= read -r name; do expected="${expected}${name} "; done < <(asset_set "${version}" "${set_mode}" ${assets_file:+"${assets_file}"})
listed=" "
while IFS= read -r name; do listed="${listed}${name} "; done < <(asset_set "${version}" listed ${assets_file:+"${assets_file}"})
n_expected="$(printf '%s' "${expected}" | wc -w | tr -d ' ')"
n_listed="$(printf '%s' "${listed}" | wc -w | tr -d ' ')"

# --- the entries of DIR ------------------------------------------------------------------------------------------------------
seen=" "
while IFS= read -r -d '' path; do
  name="${path##*/}"
  case "${name}" in *[[:cntrl:]]*) err asset_unexpected "an entry of ${dir} has a control character in its name" ;; esac
  case "${expected}" in
    *" ${name} "*)
      if [ -L "${path}" ] || [ ! -f "${path}" ]; then err asset_not_regular "${name} is not a regular file (a link, a directory or a special file)"; fi
      seen="${seen}${name} "
      ;;
    *) err asset_unexpected "${name} is not part of the ${stage} release files" ;;
  esac
done < <(find "${dir}" -mindepth 1 -maxdepth 1 -print0)
for name in ${expected}; do
  case "${seen}" in *" ${name} "*) ;; *) err asset_missing "${name} is missing in ${dir}" ;; esac
done

# --- checksums.txt ------------------------------------------------------------------------------------------------------------
sums_seen=" "
n_sums=0
while IFS= read -r line || [ -n "${line}" ]; do
  case "${line}" in *[[:cntrl:]]*) err checksums_malformed "a line of checksums.txt has a control character" ;; esac
  [[ "${line}" =~ ^([0-9a-f]{64})\ \ ([^/\ ]+)$ ]] || err checksums_malformed "a line of checksums.txt is not '<64 hex>  <name>'"
  hash="${BASH_REMATCH[1]}"
  name="${BASH_REMATCH[2]}"
  case "${listed}" in *" ${name} "*) ;; *) err checksums_unexpected "checksums.txt lists ${name}, which is not one of the ${n_listed} checksummed files" ;; esac
  case "${sums_seen}" in *" ${name} "*) err checksums_malformed "checksums.txt lists ${name} twice" ;; esac
  sums_seen="${sums_seen}${name} "
  [ "$(sha256_of "${dir}/${name}")" = "${hash}" ] || err checksum_mismatch "${name} differs from the hash in checksums.txt"
  n_sums=$((n_sums + 1))
done < "${dir}/checksums.txt"
for name in ${listed}; do
  case "${sums_seen}" in *" ${name} "*) ;; *) err checksums_incomplete "checksums.txt does not list ${name}" ;; esac
done

if [ "${stage}" = final ]; then
  for name in checksums.txt.sigstore.json "myriarch_${version}_provenance.intoto.jsonl"; do
    [ -s "${dir}/${name}" ] || err asset_empty "${name} is empty"
  done
fi
info "ok: ${n_expected} assets, ${n_sums} checksums"
