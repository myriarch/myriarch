#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Binds the draft release to what verify.sh verified, sets the image tags and publishes the release (job `finalize`, after the second
# approval of the environment `release`, ADR-0035). Privileged (contents: write, packages: write): it is called with a PATH that has no
# go, node, pnpm, make or goreleaser.
#
#   GH_TOKEN=... TOOLS_BIN=... RELEASE_COMMIT=... RELEASE_PRERELEASE=... RELEASE_LATEST=... IMAGE_TAGS=... SERVER_INDEX_DIGEST=... \
#   UPDATER_INDEX_DIGEST=... CHART_DIGEST=... GITHUB_RUN_ATTEMPT=... ci/release/finalize.sh --tag TAG --release-id ID --verified DIR
#
#   --verified DIR  the artifact of the verify job: verified-manifest.txt, verified-images.txt, checksums.txt
#
# Order (nothing is tagged or published before the checks 1 to 4 and the planning of 5 passed)
#   1. inputs       ID is a number; RELEASE_COMMIT is the commit of the tag; verified-manifest.txt has the 42 names of assets.json with well-formed lines; verified-images.txt has
#                   six lines whose index digests equal the two environment digests [verified_mismatch]; IMAGE_TAGS are the tags of TAG
#                   (X.Y.Z-rc.N, or X.Y.Z and X.Y) and none is latest [image_tag_latest]; RELEASE_PRERELEASE and RELEASE_LATEST fit TAG
#   2. visibility   all six image digests and the digest of the chart (CHART_DIGEST, a well-formed digest [verified_mismatch]) are read
#                   WITHOUT credentials (a fresh empty DOCKER_CONFIG for that one crane call): ghcr.io makes a new package private,
#                   and an organization owner switches it to public by hand [ghcr_not_public]
#   3. the release  release ID has the tag, was created by github-actions[bot], is named TAG and has the expected prerelease flag
#                   [draft_foreign]; it is the only release of the tag [draft_ambiguous]; it is a draft, or - only in a re-run
#                   (GITHUB_RUN_ATTEMPT > 1) after it was published by this job - published with the same assets [release_not_draft]
#   4. assets       the names equal the manifest; each asset is uploaded [asset_state] by github-actions[bot] [asset_foreign], has the
#                   size [asset_size_mismatch] and the SHA-256 digest of the manifest [asset_digest_missing] [asset_digest_mismatch];
#                   refs/tags/TAG exists and resolves (an annotated tag is dereferenced) to RELEASE_COMMIT [tag_moved]
#   5. image tags   per repository from `crane ls`: an immutable tag (X.Y.Z-rc.N, X.Y.Z) is set when absent, kept when it already
#                   points at the index, and a conflict otherwise [image_tag_conflict]; the moving tag X.Y (final tags only) is set when
#                   absent, re-set when its image has a lower version, left alone (and logged) when its version is higher, and a
#                   conflict when unreadable; a repository with a tag latest is refused. After tagging, `crane digest` of every tag
#                   that was set or kept equals the index [image_tag_mismatch]
#   6. publish      directly before: the release is read by ID again and checks 3 and 4 are repeated, the uniqueness of the release of
#                   the tag [draft_ambiguous] included (the draft may have changed while the image tags were set), and the tag is
#                   resolved again [tag_moved]. Then
#                   gh api -X PATCH repos/<repository>/releases/ID -F draft=false -f make_latest=<RELEASE_LATEST> [publish_failed]
#   7. post checks  (each retried up to 10 times, RELEASE_RETRY_DELAY seconds apart, default 15: the release attestation appears shortly
#                   after publication) gh release view: not a draft, the same ID, the prerelease flag, immutable [release_not_immutable];
#                   the release is read by ID once more, it is the only release of the tag and published [release_state] [draft_ambiguous],
#                   and check 4 is repeated on the published, now immutable release [release_assets_changed: the run must say that the
#                   release is immutable and wrong]; gh release verify
#                   [release_attestation]; gh release verify-asset on checksums.txt [release_asset_attestation]
#   8. stdout       release-url=https://github.com/<repository>/releases/tag/TAG
#
# Exit status: 0 published and checked, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="finalize.sh --tag TAG --release-id ID --verified DIR"
BOT="github-actions[bot]"
tag="" rid="" verified=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag) need_value "$1" "$#"; tag="$2"; shift 2 ;;
    --release-id) need_value "$1" "$#"; rid="$2"; shift 2 ;;
    --verified) need_value "$1" "$#"; verified="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
    *) usage_err "${USAGE}" ;;
  esac
done
[ -n "${tag}" ] && [ -n "${rid}" ] && [ -n "${verified}" ] || usage_err "${USAGE}"
load_identity
check_match tag_invalid "--tag" "${tag}" "${TAG_RE}"
check_match release_id_invalid "--release-id" "${rid}" '^[1-9][0-9]*$'
check_text verified_mismatch "--verified" "${verified}"
need_gh
need_tool crane
repo="${RELEASE_REPOSITORY}"
version="${tag#v}"
base="${version%%-*}"
commit="${RELEASE_COMMIT:-}"
check_match commit_invalid "RELEASE_COMMIT" "${commit}" "${COMMIT_RE}"
attempt="${GITHUB_RUN_ATTEMPT:-1}"
check_match run_attempt_invalid "GITHUB_RUN_ATTEMPT" "${attempt}" '^[1-9][0-9]*$'
delay="${RELEASE_RETRY_DELAY:-15}"
check_match retry_delay_invalid "RELEASE_RETRY_DELAY" "${delay}" '^[0-9]{1,3}$'

# --- 1. inputs ---------------------------------------------------------------------------------------------------------------
kind=final
want_tags="${base},${base%.*}"
case "${version}" in *-rc.*) kind=rc; want_tags="${version}" ;; esac
prerelease="${RELEASE_PRERELEASE:-}"
latest="${RELEASE_LATEST:-}"
case "${prerelease}" in true|false) ;; *) err prerelease_invalid "RELEASE_PRERELEASE must be true or false" ;; esac
case "${latest}" in true|false) ;; *) err latest_invalid "RELEASE_LATEST must be true or false" ;; esac
if [ "${kind}" = rc ]; then
  [ "${prerelease}" = true ] && [ "${latest}" = false ] || err prerelease_invalid "${tag} is a release candidate: RELEASE_PRERELEASE=true and RELEASE_LATEST=false"
else
  [ "${prerelease}" = false ] || err prerelease_invalid "${tag} is a final tag: RELEASE_PRERELEASE=false"
fi
image_tags="${IMAGE_TAGS:-}"
check_match image_tags_invalid "IMAGE_TAGS" "${image_tags}" '^[0-9A-Za-z.,-]+$'
case ",${image_tags}," in *,latest,*) err image_tag_latest "IMAGE_TAGS contains latest, which must not exist during v0.x" ;; esac
[ "${image_tags}" = "${want_tags}" ] || err image_tags_invalid "IMAGE_TAGS '${image_tags}' is not '${want_tags}', the tags of ${tag}"
server_index="${SERVER_INDEX_DIGEST:-}"
updater_index="${UPDATER_INDEX_DIGEST:-}"
check_match verified_mismatch "SERVER_INDEX_DIGEST" "${server_index}" "${DIGEST_RE}"
check_match verified_mismatch "UPDATER_INDEX_DIGEST" "${updater_index}" "${DIGEST_RE}"
chart_digest="${CHART_DIGEST:-}"
check_match verified_mismatch "CHART_DIGEST" "${chart_digest}" "${DIGEST_RE}"

manifest="${verified}/verified-manifest.txt"
images="${verified}/verified-images.txt"
[ -f "${manifest}" ] && [ -f "${images}" ] && [ -f "${verified}/checksums.txt" ] || err verified_mismatch "${verified} lacks verified-manifest.txt, verified-images.txt or checksums.txt"
want_names=" $(asset_set "${version}" names | tr '\n' ' ')"
m_names=" "
m_count=0
while IFS= read -r line || [ -n "${line}" ]; do
  [[ "${line}" =~ ^([0-9a-f]{64})\ \ ([0-9]+)\ \ ([A-Za-z0-9][A-Za-z0-9._-]*)$ ]] || err verified_mismatch "verified-manifest.txt has a malformed line"
  case "${want_names}" in *" ${BASH_REMATCH[3]} "*) ;; *) err verified_mismatch "verified-manifest.txt lists ${BASH_REMATCH[3]}, which is not a release file" ;; esac
  case "${m_names}" in *" ${BASH_REMATCH[3]} "*) err verified_mismatch "verified-manifest.txt lists ${BASH_REMATCH[3]} twice" ;; esac
  m_names="${m_names}${BASH_REMATCH[3]} "
  m_count=$((m_count + 1))
done < "${manifest}"
[ "${m_count}" -eq "$(printf '%s' "${want_names}" | wc -w | tr -d ' ')" ] || err verified_mismatch "verified-manifest.txt does not list all release files"

image_lines=""
i_count=0
while IFS= read -r line || [ -n "${line}" ]; do
  [[ "${line}" =~ ^(ghcr\.io/[a-z0-9._/-]+)\ (index|linux/amd64|linux/arm64)\ (sha256:[0-9a-f]{64})$ ]] || err verified_mismatch "verified-images.txt has a malformed line"
  image_lines="${image_lines}${BASH_REMATCH[1]} ${BASH_REMATCH[2]} ${BASH_REMATCH[3]}
"
  i_count=$((i_count + 1))
done < "${images}"
[ "${i_count}" -eq 6 ] || err verified_mismatch "verified-images.txt must have six lines"
image_digest() { # repository platform
  printf '%s' "${image_lines}" | awk -v r="$1" -v p="$2" '$1 == r && $2 == p { print $3 }'
}
for pair in "${RELEASE_IMAGE_SERVER}:${server_index}" "${RELEASE_IMAGE_UPDATER}:${updater_index}"; do
  r="${pair%%:*}"
  d="${pair#*:}"
  [ "$(image_digest "${r}" index)" = "${d}" ] || err verified_mismatch "the verified index digest of ${r} is not the digest of this run"
  for p in linux/amd64 linux/arm64; do
    [[ "$(image_digest "${r}" "${p}")" =~ ${DIGEST_RE} ]] || err verified_mismatch "verified-images.txt has no ${p} digest for ${r}"
  done
done

# --- 2. anonymous read of all six digests ------------------------------------------------------------------------------------
anon="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-finalize-anon.XXXXXX")"
work="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-finalize.XXXXXX")"
trap 'rm -rf "${anon}" "${work}"' EXIT
while read -r r p d; do
  [ -n "${r}" ] || continue
  if ! DOCKER_CONFIG="${anon}" "${TOOLS_BIN}/crane" manifest "${r}@${d}" > /dev/null 2> "${work}/anon.err"; then
    err ghcr_not_public "${r} cannot be read without credentials: ghcr.io makes a package private on its first publish. An organization owner sets ${r} to public (package settings, \"Change visibility\"; this cannot be undone), then re-runs this job with \"Re-run failed jobs\"."
  fi
done <<EOT
${image_lines}
EOT
if ! DOCKER_CONFIG="${anon}" "${TOOLS_BIN}/crane" manifest "${RELEASE_CHART}@${chart_digest}" > /dev/null 2> "${work}/anon.err"; then
  err ghcr_not_public "${RELEASE_CHART} cannot be read without credentials: ghcr.io makes a package private on its first publish. An organization owner sets ${RELEASE_CHART} to public (package settings, \"Change visibility\"; this cannot be undone), then re-runs this job with \"Re-run failed jobs\"."
fi
info "all six image digests and the chart digest are readable without credentials"

# --- 3. the release ----------------------------------------------------------------------------------------------------------
rel=""
trap 'rm -rf "${anon}" "${work}"; [ -z "${rel}" ] || rm -f "${rel}"' EXIT
read_release() { # what: reads the release by ID into the file ${rel}
  [ -z "${rel}" ] || rm -f "${rel}"
  rel="$(gh_json "$1" api "repos/${repo}/releases/${rid}")"
}
# release_draft_flag: the release in ${rel} is the release that the workflow created for TAG; prints true (draft) or false (published)
release_draft_flag() {
  python3 -I - "${rel}" "${tag}" "${prerelease}" "${BOT}" <<'PY' || exit 1
import json, sys

path, tag, prerelease, bot = sys.argv[1:5]
try:
    r = json.load(open(path, encoding="utf-8"))
    assert isinstance(r, dict)
except (OSError, ValueError, AssertionError):
    print("[finalize] ERROR [draft_foreign] the release is not readable JSON", file=sys.stderr)
    sys.exit(1)
ok = (r.get("tag_name") == tag and (r.get("author") or {}).get("login") == bot and r.get("name") == tag
      and str(bool(r.get("prerelease"))).lower() == prerelease and isinstance(r.get("draft"), bool))
if not ok:
    print("[finalize] ERROR [draft_foreign] the release is not the draft of %s that the release workflow created" % tag, file=sys.stderr)
    sys.exit(1)
print(str(r["draft"]).lower())
PY
}
# check_single_release: release ${rid} is the only release of TAG (drafts included); repeated at every re-read of the release
check_single_release() {
  local rows n=0 id _
  rows="$(release_rows "${tag}")"
  while IFS='|' read -r id _ _ _ _; do
    [ -n "${id}" ] || continue
    n=$((n + 1))
    [ "${id}" = "${rid}" ] || err draft_ambiguous "tag ${tag} has another release (${id}) besides ${rid}"
  done <<EOT
${rows}
EOT
  [ "${n}" -eq 1 ] || err draft_ambiguous "tag ${tag} has ${n} releases, expected exactly release ${rid}"
}
read_release "read release ${rid}"
# The flag is captured on its own line: a refusal of release_draft_flag ([draft_foreign]) ends the script here, instead of its lost output
# being compared with true or false and reported as another code.
draft="$(release_draft_flag)" || exit 1
check_single_release
published=0
if [ "${draft}" = false ]; then
  if [ "${attempt}" -le 1 ]; then
    err release_not_draft "release ${rid} of ${tag} is already published; it was published outside this job (never publish the draft by hand)"
  fi
  published=1
  info "release ${rid} is already published (re-run ${attempt}); continuing with the tags and the checks"
fi

# --- 4. the assets and the tag -----------------------------------------------------------------------------------------------
# check_assets [code]: the assets of the release in ${rel} are exactly the verified files. With a code, every refusal carries that code
# (used after the publication, when the release is immutable and a difference cannot be undone).
check_assets() {
  python3 -I - "${rel}" "${manifest}" "${BOT}" "${1:-}" <<'PY' || exit 1
import json, re, sys

path, manifest_path, bot, override = sys.argv[1:5]


def fail(code, msg):
    if override:
        print("[finalize] ERROR [%s] the release is published and immutable, but its files differ from the verified files (%s: %s). "
              "Do not use this release; publish a new tag." % (override, code, msg), file=sys.stderr)
    else:
        print("[finalize] ERROR [%s] %s" % (code, msg), file=sys.stderr)
    sys.exit(1)


r = json.load(open(path, encoding="utf-8"))
want = {}
for line in open(manifest_path, encoding="utf-8").read().split("\n"):
    m = re.match(r"^([0-9a-f]{64})  ([0-9]+)  (.+)$", line)
    if m:
        want[m.group(3)] = (m.group(1), int(m.group(2)))
assets = r.get("assets") or []
names = [a.get("name") for a in assets]
for n in sorted(set(want) - set(names)):
    fail("asset_missing", "the release lacks %s" % n)
for n in sorted(set(names) - set(want)):
    fail("asset_unexpected", "the release has the file %s, which was not verified" % n)
if len(names) != len(set(names)):
    fail("asset_unexpected", "the release has a file name twice")
for a in assets:
    n = a["name"]
    if a.get("state") != "uploaded":
        fail("asset_state", "%s is in the state '%s'" % (n, a.get("state")))
    if (a.get("uploader") or {}).get("login") != bot:
        fail("asset_foreign", "%s was not uploaded by %s" % (n, bot))
    if a.get("size") != want[n][1]:
        fail("asset_size_mismatch", "%s has %s bytes, verified: %d" % (n, a.get("size"), want[n][1]))
    d = a.get("digest")
    if not isinstance(d, str) or not re.match(r"^sha256:[0-9a-f]{64}$", d):
        fail("asset_digest_missing", "%s has no SHA-256 digest in the release" % n)
    if d != "sha256:" + want[n][0]:
        fail("asset_digest_mismatch", "%s differs from the verified file" % n)
PY
}
# check_tag: refs/tags/TAG is a tag of RELEASE_COMMIT (an annotated tag is dereferenced, at most three levels). GitHub creates a missing
# tag of a published draft at its target, so a tag that was moved or re-created would otherwise publish another commit than the one that
# was signed and verified.
tag_object() { # file: "<type> <sha>" of the object of a ref or an annotated tag
  python3 -I -c '
import json, re, sys

try:
    o = json.load(open(sys.argv[1], encoding="utf-8"))["object"]
    assert o["type"] in ("commit", "tag") and re.match(r"^[0-9a-f]{40}$", o["sha"])
except (OSError, ValueError, KeyError, TypeError, AssertionError):
    sys.exit(1)
print(o["type"], o["sha"])
' "$1"
}
check_tag() {
  local f line otype osha _
  f="$(GH_JSON_NOT_FOUND=tag_moved gh_json "resolve the tag ${tag}" api "repos/${repo}/git/ref/tags/${tag}")"
  line="$(tag_object "${f}")" || { rm -f "${f}"; err tag_moved "the ref of the tag ${tag} is not a commit or a tag object"; }
  rm -f "${f}"
  for _ in 1 2 3; do
    otype="${line%% *}"
    osha="${line#* }"
    [ "${otype}" = tag ] || break
    f="$(GH_JSON_NOT_FOUND=tag_moved gh_json "read the tag object ${osha}" api "repos/${repo}/git/tags/${osha}")"
    line="$(tag_object "${f}")" || { rm -f "${f}"; err tag_moved "the tag object of ${tag} is not a commit or a tag object"; }
    rm -f "${f}"
  done
  otype="${line%% *}"
  osha="${line#* }"
  [ "${otype}" = commit ] && [ "${osha}" = "${commit}" ] || err tag_moved "the tag ${tag} resolves to ${osha}, not to ${commit}, the commit that was signed and verified"
}
check_assets
info "the ${m_count} assets of release ${rid} are the verified files"
check_tag
info "the tag ${tag} points at ${commit}"

# --- 5. image tags: plan everything, then tag -------------------------------------------------------------------------------
# lower|equal|higher|invalid: the version of the image behind the moving tag against this release
compare_label() { # label-or-empty
  python3 -I - "$1" "${version}" <<'PY'
import re, sys

def key(v):
    m = re.match(r"^(\d+)\.(\d+)\.(\d+)(?:-rc\.(\d+))?$", v)
    if not m:
        return None
    x, y, z, rc = int(m.group(1)), int(m.group(2)), int(m.group(3)), m.group(4)
    return (x, y, z, 0, int(rc)) if rc else (x, y, z, 1, 0)

a, b = key(sys.argv[1]), key(sys.argv[2])
print("invalid" if a is None or b is None else "lower" if a < b else "higher" if a > b else "equal")
PY
}
plan="" # lines: repository|index|tag|action (set|keep|stays)
plan_line() { plan="${plan}$1|$2|$3|$4
"; }
plan_repo() { # repository index
  local r="$1" idx="$2" existing t cur label rel_cmp tags_arr
  existing="$("${TOOLS_BIN}/crane" ls "$r" 2> "${work}/crane.err")" || err image_tag_conflict "crane could not list the tags of ${r}"
  if printf '%s\n' "${existing}" | grep -Fxq latest; then
    err image_tag_latest "${r} has the tag latest, which must not exist during v0.x"
  fi
  IFS=, read -r -a tags_arr <<< "${image_tags}"
  for t in "${tags_arr[@]}"; do
    if ! printf '%s\n' "${existing}" | grep -Fxq -- "${t}"; then
      plan_line "${r}" "${idx}" "${t}" set
      continue
    fi
    cur="$("${TOOLS_BIN}/crane" digest "${r}:${t}" 2> "${work}/crane.err")" || err image_tag_conflict "crane could not read ${r}:${t}"
    if [ "${cur}" = "${idx}" ]; then
      plan_line "${r}" "${idx}" "${t}" keep
    elif [[ "${t}" =~ ^[0-9]+\.[0-9]+$ ]]; then
      label="$("${TOOLS_BIN}/crane" config "${r}:${t}" 2> "${work}/crane.err" | python3 -I -c '
import json, sys
try:
    print(json.load(sys.stdin)["config"]["Labels"]["org.opencontainers.image.version"])
except Exception:
    print("")
')" || label=""
      rel_cmp="$(compare_label "${label}")"
      case "${rel_cmp}" in
        lower) plan_line "${r}" "${idx}" "${t}" set ;;
        higher) plan_line "${r}" "${idx}" "${t}" stays; info "${r}:${t} stays at ${label}, which is newer than ${version}" ;;
        *) err image_tag_conflict "${r}:${t} points to an image whose version label is '${label}'; it is not older than ${version}" ;;
      esac
    else
      err image_tag_conflict "${r}:${t} points to ${cur}, not to ${idx}: an immutable tag is never moved"
    fi
  done
}
plan_repo "${RELEASE_IMAGE_SERVER}" "${server_index}"
plan_repo "${RELEASE_IMAGE_UPDATER}" "${updater_index}"
while IFS='|' read -r r idx t action; do
  [ -n "${r}" ] || continue
  if [ "${action}" = set ]; then
    info "tagging ${r}@${idx} as ${t}"
    "${TOOLS_BIN}/crane" tag "${r}@${idx}" "${t}" > /dev/null 2> "${work}/crane.err" || err image_tag_conflict "crane could not set ${r}:${t}"
  fi
done <<EOT
${plan}
EOT
while IFS='|' read -r r idx t action; do
  [ -n "${r}" ] || continue
  [ "${action}" = stays ] && continue
  [ "$("${TOOLS_BIN}/crane" digest "${r}:${t}" 2> "${work}/crane.err")" = "${idx}" ] || err image_tag_mismatch "${r}:${t} does not point to ${idx}"
done <<EOT
${plan}
EOT

# --- 6. publish --------------------------------------------------------------------------------------------------------------
if [ "${published}" = 0 ]; then
  # the draft is read again directly before the publication: the tags above took time, and the files of a draft stay editable
  read_release "read release ${rid} again"
  flag="$(release_draft_flag)" || exit 1
  [ "${flag}" = true ] || err release_not_draft "release ${rid} of ${tag} was published while this job set the image tags"
  check_single_release
  check_assets
  check_tag
  info "publishing release ${rid} of ${tag}"
  "${TOOLS_BIN}/gh" api -X PATCH "repos/${repo}/releases/${rid}" -F draft=false -f "make_latest=${latest}" > /dev/null || err publish_failed "publishing release ${rid} failed"
fi

# --- 7. post checks ----------------------------------------------------------------------------------------------------------
# retry <code> <message> <command...>: up to 10 attempts, ${delay} seconds apart
retry() {
  local code="$1" msg="$2" i
  shift 2
  for i in 1 2 3 4 5 6 7 8 9 10; do
    if "$@" > /dev/null 2> "${work}/retry.err"; then return 0; fi
    if [ "${i}" -lt 10 ]; then sleep "${delay}"; fi
  done
  err "${code}" "${msg}"
}
view_state_ok() {
  "${TOOLS_BIN}/gh" release view "${tag}" --repo "${repo}" --json tagName,isDraft,isPrerelease,isImmutable,databaseId | python3 -I -c '
import json, sys
d = json.load(sys.stdin)
ok = d.get("tagName") == sys.argv[1] and d.get("isDraft") is False and d.get("databaseId") == int(sys.argv[2]) \
    and str(d.get("isPrerelease")).lower() == sys.argv[3]
sys.exit(0 if ok else 1)
' "${tag}" "${rid}" "${prerelease}"
}
immutable_ok() {
  "${TOOLS_BIN}/gh" release view "${tag}" --repo "${repo}" --json tagName,isImmutable | python3 -I -c '
import json, sys
sys.exit(0 if json.load(sys.stdin).get("isImmutable") is True else 1)
'
}
retry release_state "release ${tag} is not published as release ${rid} with the expected pre-release flag" view_state_ok
retry release_not_immutable "release ${tag} is published but NOT immutable: enable release immutability for the organization or repository before the next tag (it applies only to later releases); this release stays mutable" immutable_ok
# the release is immutable now: its files are final. Compare them with the verified files once more.
read_release "read the published release ${rid}"
flag="$(release_draft_flag)" || exit 1
[ "${flag}" = false ] || err release_state "release ${rid} of ${tag} is not published"
check_single_release
check_assets release_assets_changed
info "the published, immutable release holds exactly the verified files"
retry release_attestation "gh release verify ${tag} failed: the release attestation is missing or invalid" \
  "${TOOLS_BIN}/gh" release verify "${tag}" --repo "${repo}"
retry release_asset_attestation "gh release verify-asset failed for checksums.txt of ${tag}" \
  "${TOOLS_BIN}/gh" release verify-asset "${tag}" "${verified}/checksums.txt" --repo "${repo}"

info "release ${tag} is published and checked"
printf 'release-url=https://github.com/%s/releases/tag/%s\n' "${repo}" "${tag}"
