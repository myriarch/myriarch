#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Stages the release files from the goreleaser dist directory under their upload names (job `package` of the release workflow and the
# release rehearsal, ADR-0035). The set of files is deploy/release/assets.json: this script produces the 40 files of stage `unsigned`;
# the signature of checksums.txt and the provenance file are added later by the signing job.
#
#   ci/release/stage-assets.sh --dist DIR --root DIR --version V --out DIR
#
#   --dist DIR     the goreleaser output (artifacts.json, metadata.json, checksums.txt, the archives and SBOMs)
#   --root DIR     the repository root: LICENSE and THIRD_PARTY_NOTICES are copied from it
#   --version V    X.Y.Z, X.Y.Z-rc.N or, for the rehearsal, X.Y.Z-snapshot; DIR/metadata.json must say the same [version_mismatch]
#   --out DIR      must not exist or be empty [out_not_empty]; receives assets/<name> (mode 0644, copies, no links) and
#                  meta/metadata.json and meta/artifacts.json
#
# The names and source paths come from `python3 -I ci/release/dist-check.py assets`. A source file that is missing or a link is
# [file_missing]. It ends with `ci/release/verify-dist.sh --stage unsigned`, which refuses an artifact that assets.json does not know
# [asset_unexpected], one that is missing [asset_missing] and a checksums.txt that does not match.
#
# Exit status: 0 ok, 1 refused, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="stage-assets.sh --dist DIR --root DIR --version V --out DIR"
dist="" root="" version="" out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dist) need_value "$1" "$#"; dist="$2"; shift 2 ;;
    --root) need_value "$1" "$#"; root="$2"; shift 2 ;;
    --version) need_value "$1" "$#"; version="$2"; shift 2 ;;
    --out) need_value "$1" "$#"; out="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
    *) usage_err "${USAGE}" ;;
  esac
done
[ -n "${dist}" ] && [ -n "${root}" ] && [ -n "${version}" ] && [ -n "${out}" ] || usage_err "${USAGE}"
check_match version_invalid "--version" "${version}" "${SNAPSHOT_VERSION_RE}"
for v in "${dist}" "${root}" "${out}"; do check_text file_missing "a path argument" "${v}"; done
[ -d "${dist}" ] || err file_missing "${dist} is not a directory"
[ -d "${root}" ] || err file_missing "${root} is not a directory"
for f in artifacts.json metadata.json; do
  [ -f "${dist}/${f}" ] && [ ! -L "${dist}/${f}" ] || err file_missing "${dist}/${f} is missing"
done
meta_version="$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8")).get("version", ""))' "${dist}/metadata.json" 2>/dev/null)" || meta_version=""
[ "${meta_version}" = "${version}" ] || err version_mismatch "${dist}/metadata.json says version '${meta_version}', not ${version}"
if [ -d "${out}" ] && [ -n "$(ls -A "${out}")" ]; then err out_not_empty "${out} exists and is not empty"; fi
[ ! -e "${out}" ] || [ -d "${out}" ] || err out_not_empty "${out} exists and is not a directory"

list="$(python3 -I "${RELEASE_LIB_DIR}/dist-check.py" assets --dist "${dist}")" || err asset_unexpected "the artifacts of ${dist} cannot be listed"
mkdir -p "${out}/assets" "${out}/meta"
copy() { # source target-name
  [ -f "$1" ] && [ ! -L "$1" ] || err file_missing "$1 is missing or not a regular file"
  [ ! -e "${out}/assets/$2" ] || err asset_unexpected "$2 occurs twice"
  cp "$1" "${out}/assets/$2"
  chmod 0644 "${out}/assets/$2"
}
count=0
while IFS="$(printf '\t')" read -r name path; do
  [ -n "${name}" ] || continue
  case "${name}" in LICENSE|THIRD_PARTY_NOTICES) err asset_unexpected "the dist directory lists ${name}, which is copied from the repository root" ;; esac
  copy "${path}" "${name}"
  count=$((count + 1))
done <<EOT
${list}
EOT
copy "${root}/LICENSE" LICENSE
copy "${root}/THIRD_PARTY_NOTICES" THIRD_PARTY_NOTICES
cp "${dist}/metadata.json" "${dist}/artifacts.json" "${out}/meta/"
info "staged ${count} files of ${dist} and 2 license files into ${out}/assets"
"${RELEASE_LIB_DIR}/verify-dist.sh" --dir "${out}/assets" --version "${version}" --stage unsigned
