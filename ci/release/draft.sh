#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Creates the DRAFT release of a tag with the complete file set (job `draft` of the release workflow, ADR-0035). A draft is invisible to
# the public and stays editable; it is published only by finalize.sh after the verification and the second approval. Privileged
# (contents: write): it is called with a PATH that has no go, node, pnpm, make or goreleaser.
#
#   GH_TOKEN=... TOOLS_BIN=... RELEASE_PRERELEASE=true|false NOTES_START_TAG=<tag or empty> \
#     ci/release/draft.sh --tag TAG --commit SHA --notes FILE --assets DIR --signatures DIR
#
#   --commit      the commit of the tag (guard output). It is the target of the release, so that GitHub would create a missing tag
#                 of the published draft at this commit and nowhere else; finalize.sh checks the tag again before it publishes.
#   --assets      the 40 files of stage `unsigned` (release-assets artifact); --signatures: checksums.txt.sigstore.json and the
#                 provenance file. verify-dist.sh checks the union as stage `final` (42 files) first.
#
# Steps
#   1. verify-dist.sh over a directory of links to the union of both directories, --stage final
#   2. lists the releases (gh api --paginate --slurp) and looks at those with the tag:
#        a published one      -> [release_exists]: a published tag is never released again, tag the next rc
#        a draft whose author is not github-actions[bot] -> [draft_foreign]: delete or rename it by hand, then re-run
#        drafts of github-actions[bot] (left by an earlier attempt) -> deleted by id (gh api -X DELETE), each logged
#   3. gh release create TAG --repo <repository> --draft --verify-tag --target SHA --title TAG --notes-file FILE --generate-notes
#        [--notes-start-tag NOTES_START_TAG] [--prerelease] <the 42 files sorted by name>      [create_failed]
#      NOTES_START_TAG is only used when that tag has a published (not draft) release: guard.sh names the highest tag below the
#      current one, which has no release when an earlier attempt failed before publication (a tag is never reused). Without a
#      published release the option is left out and the log says so, and GitHub picks the start of the generated change list itself.
#      --prerelease exactly when RELEASE_PRERELEASE=true; never --latest. The notes of FILE come first, then GitHub's generated list.
#   4. reads the list again: exactly one draft of the tag by github-actions[bot] [draft_ambiguous] [draft_missing], with exactly the
#      42 names [asset_missing] [asset_unexpected] and the requested prerelease flag [prerelease_mismatch]
# stdout: release-id=<REST id of the draft> (a number [release_id_invalid]).
#
# Exit status: 0 ok, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="draft.sh --tag TAG --commit SHA --notes FILE --assets DIR --signatures DIR"
BOT="github-actions[bot]"
tag="" commit="" notes="" assets="" signatures=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag) need_value "$1" "$#"; tag="$2"; shift 2 ;;
    --commit) need_value "$1" "$#"; commit="$2"; shift 2 ;;
    --notes) need_value "$1" "$#"; notes="$2"; shift 2 ;;
    --assets) need_value "$1" "$#"; assets="$2"; shift 2 ;;
    --signatures) need_value "$1" "$#"; signatures="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
    *) usage_err "${USAGE}" ;;
  esac
done
[ -n "${tag}" ] && [ -n "${commit}" ] && [ -n "${notes}" ] && [ -n "${assets}" ] && [ -n "${signatures}" ] || usage_err "${USAGE}"
load_identity
check_match tag_invalid "--tag" "${tag}" "${TAG_RE}"
check_match commit_invalid "--commit" "${commit}" "${COMMIT_RE}"
for v in "${notes}" "${assets}" "${signatures}"; do check_text file_missing "a path argument" "${v}"; done
[ -f "${notes}" ] || err file_missing "the notes file ${notes} does not exist"
[ -d "${assets}" ] && [ -d "${signatures}" ] || err file_missing "--assets and --signatures must be directories"
prerelease="${RELEASE_PRERELEASE:-}"
case "${prerelease}" in true|false) ;; *) err prerelease_invalid "RELEASE_PRERELEASE must be true or false" ;; esac
case "${tag}" in
  *-rc.*) [ "${prerelease}" = true ] || err prerelease_invalid "${tag} is a release candidate and needs RELEASE_PRERELEASE=true" ;;
  *) [ "${prerelease}" = false ] || err prerelease_invalid "${tag} is a final tag and needs RELEASE_PRERELEASE=false" ;;
esac
start_tag="${NOTES_START_TAG:-}"
if [ -n "${start_tag}" ]; then check_match notes_start_tag_invalid "NOTES_START_TAG" "${start_tag}" "${TAG_RE}"; fi
need_gh
version="${tag#v}"
repo="${RELEASE_REPOSITORY}"

# --- 1. the union is the final file set --------------------------------------------------------------------------------------
# a directory of the files themselves (hard links or copies): verify-dist.sh refuses links, and gh uploads these paths
real="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-draft.XXXXXX")"
trap 'rm -rf "${real}"' EXIT
for d in "${assets}" "${signatures}"; do
  for f in "${d}"/* "${d}"/.[!.]*; do
    [ -e "${f}" ] || [ -L "${f}" ] || continue
    name="$(basename "${f}")"
    if [ -L "${f}" ] || [ ! -f "${f}" ]; then err asset_not_regular "${name} is not a regular file"; fi
    [ ! -e "${real}/${name}" ] || err asset_unexpected "${name} is in both directories"
    ln "${f}" "${real}/${name}" 2>/dev/null || cp "${f}" "${real}/${name}"
  done
done
"${RELEASE_LIB_DIR}/verify-dist.sh" --dir "${real}" --version "${version}" --stage final

# --- 2. stale drafts ---------------------------------------------------------------------------------------------------------
rows="$(release_rows "${tag}")"
stale=""
while IFS='|' read -r id draft author _ _; do
  [ -n "${id}" ] || continue
  [[ "${id}" =~ ^[1-9][0-9]*$ ]] || err gh_failed "a release has an unexpected id"
  if [ "${draft}" != true ]; then
    err release_exists "tag ${tag} already has a published release; a published tag is never released again: tag the next rc"
  fi
  if [ "${author}" != "${BOT}" ]; then
    err draft_foreign "a draft of ${tag} was not created by the release workflow; delete or rename it by hand, then re-run"
  fi
  stale="${stale}${id} "
done <<EOT
${rows}
EOT
for id in ${stale}; do
  info "deleting the stale draft ${id} of ${tag} left by an earlier attempt"
  "${TOOLS_BIN}/gh" api -X DELETE "repos/${repo}/releases/${id}" > /dev/null || err gh_failed "the stale draft ${id} could not be deleted"
done

# --- 3. create ---------------------------------------------------------------------------------------------------------------
if [ -n "${start_tag}" ]; then
  start_rows="$(release_rows "${start_tag}")"
  published=0
  while IFS='|' read -r id draft _; do
    if [ -n "${id}" ] && [ "${draft}" = false ]; then published=1; fi
  done <<EOT
${start_rows}
EOT
  if [ "${published}" = 1 ]; then
    info "the generated change list starts at ${start_tag}"
  else
    info "${start_tag} has no published release (an earlier attempt failed before publication): the generated change list starts where GitHub puts it"
    start_tag=""
  fi
fi
names=()
while IFS= read -r name; do names+=("${real}/${name}"); done < <(asset_set "${version}" names)
args=(release create "${tag}" --repo "${repo}" --draft --verify-tag --target "${commit}" --title "${tag}" --notes-file "${notes}" --generate-notes)
if [ -n "${start_tag}" ]; then args+=(--notes-start-tag "${start_tag}"); fi
if [ "${prerelease}" = true ]; then args+=(--prerelease); fi
args+=("${names[@]}")
info "creating the draft release ${tag} with ${#names[@]} files"
"${TOOLS_BIN}/gh" "${args[@]}" >&2 || err create_failed "gh release create failed"

# --- 4. read back ------------------------------------------------------------------------------------------------------------
rows="$(release_rows "${tag}")"
count=0 found_id="" found_assets="" found_pre=""
while IFS='|' read -r id draft author pre list; do
  [ -n "${id}" ] || continue
  count=$((count + 1))
  if [ "${draft}" = true ] && [ "${author}" = "${BOT}" ]; then found_id="${id}"; found_assets="${list}"; found_pre="${pre}"; else count=$((count + 100)); fi
done <<EOT
${rows}
EOT
[ "${count}" -le 1 ] || err draft_ambiguous "tag ${tag} has more than one release, or one that is not a draft of the release workflow"
[ -n "${found_id}" ] || err draft_missing "the draft of ${tag} is not in the release list"
check_match release_id_invalid "the id of the draft" "${found_id}" '^[1-9][0-9]*$'
want=" $(asset_set "${version}" names | tr '\n' ' ')"
for name in ${found_assets}; do
  case "${want}" in *" ${name} "*) ;; *) err asset_unexpected "the draft has the file ${name}, which is not part of the release" ;; esac
done
for name in $(asset_set "${version}" names); do
  case " ${found_assets} " in *" ${name} "*) ;; *) err asset_missing "the draft lacks the file ${name}" ;; esac
done
[ "${found_pre}" = "${prerelease}" ] || err prerelease_mismatch "the draft has prerelease=${found_pre}, expected ${prerelease}"
info "draft ${found_id} of ${tag} holds the ${#names[@]} files"
printf 'release-id=%s\n' "${found_id}"
