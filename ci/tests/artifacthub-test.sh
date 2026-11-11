#!/usr/bin/env bash
# shellcheck disable=SC2016  # the mutations below are literal workflow text with ${{ }} expressions: nothing in them is meant to expand
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the Artifact Hub upload (ci/release/push-artifacthub.sh and .github/workflows/artifacthub.yml; ADR-0035). No network, no
# registry, no port, no real crane:
#  1. the layout: the script builds the OCI layout of the artifact in a directory (--layout-out). The file has exactly one key, the media
#     types are those of the Artifact Hub documentation, every blob matches its digest, and the same ID gives the same digest, whichever chart is named (the file does not name the chart).
#  2. the input checks: the ID is a UUID in lower case and nothing else (upper case, empty, too long, a line break, YAML characters, a
#     missing option); --chart-name is exactly myriarch or myriarch-collector (chart_name for anything else, usage error when missing).
#  3. the upload with a stand-in for crane, for the server chart and for the collector chart: a published chart is required (a version tag,
#     release candidates included), the target is the fixed repository of the chosen chart and the fixed tag whatever the environment
#     says, no version tag is written, a wrong workflow ref, a failed push and a wrong digest read back are refused, and a second run
#     overwrites only the tag artifacthub.io. The other chart repository is only read; an ID that it carries already is refused
#     (id_in_use), and so is an unreadable list or manifest of it.
#  4. the workflow: workflow_dispatch only with the inputs chart (a required choice of the two names, no default) and repository_id, contents: read on top, one job with the environment release and
#     packages: write as its only extra scope, pinned actions, no cancellation, no secret, no input in a run text, no Go or Node in the
#     privileged job, the entry in ci/workflow-permissions.txt, and the policy check (ci/check-workflows.py) accepts it and refuses
#     mutated copies.
set -uo pipefail
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
need_cmd python3
# the CI runner sets this: the script appends to it, and a test must not write into the summary of the run
unset GITHUB_STEP_SUMMARY

REL="${ROOT}/ci/release"
SCRIPT_AH="${REL}/push-artifacthub.sh"
WORKFLOW="${ROOT}/.github/workflows/artifacthub.yml"
W="${TEST_TMP}"
ID=123e4567-e89b-42d3-a456-426614174000
CHART=ghcr.io/myriarch/charts/myriarch
eq() { if [ "$2" = "$3" ]; then ok "$1"; else fail "$1 (expected '$2', got '$3')"; fi; }
ah() { ( unset DOCKER_CONFIG; "$SCRIPT_AH" "$@" ); }

# ==== 1. the layout ===========================================================================================================
expect_ok "layout: a valid ID" ah --chart-name myriarch --repository-id "${ID}" --layout-out "${W}/lay1"
DIG1="$(printf '%s\n' "${OUT}" | sed -n 's/^artifacthub-digest=//p')"
python3 -I - "${W}/lay1" "${ID}" "${DIG1}" <<'PY' && ok "layout: file, media types and digests" || fail "layout: the layout is wrong"
import hashlib, json, os, sys

lay, rid, dig = sys.argv[1:4]


def blob(d):
    assert d["digest"].startswith("sha256:"), d
    data = open(os.path.join(lay, "blobs", "sha256", d["digest"][7:]), "rb").read()
    assert hashlib.sha256(data).hexdigest() == d["digest"][7:] and len(data) == d["size"], d
    return data


assert json.load(open(os.path.join(lay, "oci-layout"))) == {"imageLayoutVersion": "1.0.0"}
index = json.load(open(os.path.join(lay, "index.json")))
assert len(index["manifests"]) == 1 and index["manifests"][0]["digest"] == dig, index
m = json.loads(blob(index["manifests"][0]))
assert m["schemaVersion"] == 2 and m["mediaType"] == "application/vnd.oci.image.manifest.v1+json", m
assert set(m) == {"schemaVersion", "mediaType", "config", "layers"}, sorted(m)
assert m["config"]["mediaType"] == "application/vnd.cncf.artifacthub.config.v1+yaml" and blob(m["config"]) == b"", m["config"]
assert len(m["layers"]) == 1, m["layers"]
assert m["layers"][0]["mediaType"] == "application/vnd.cncf.artifacthub.repository-metadata.layer.v1.yaml", m["layers"]
assert blob(m["layers"][0]) == ("repositoryID: %s\n" % rid).encode()
# exactly the manifest, the config, the layer: no other blob
assert len(os.listdir(os.path.join(lay, "blobs", "sha256"))) == 3
PY
eq "layout: the file has exactly one line and one key" "1 repositoryID" "$(f="${W}/lay1/blobs/sha256/$(python3 -I -c 'import hashlib,sys; print(hashlib.sha256(("repositoryID: %s\n" % sys.argv[1]).encode()).hexdigest())' "${ID}")"; printf '%s %s' "$(wc -l < "${f}" | tr -d ' ')" "$(sed -n 's/^\([A-Za-z]*\):.*/\1/p' "${f}" | sort -u | tr '\n' ' ' | sed 's/ $//')")"
! grep -rIl -e owners -e name -e email "${W}/lay1/blobs" >/dev/null 2>&1 && ok "layout: no owners, name or e-mail in any blob" || fail "layout: a blob names owners, a name or an e-mail"
expect_ok "layout: the same ID again" ah --chart-name myriarch --repository-id "${ID}" --layout-out "${W}/lay2"
eq "layout: the same ID gives the same digest" "${DIG1}" "$(printf '%s\n' "${OUT}" | sed -n 's/^artifacthub-digest=//p')"
expect_ok "layout: another ID" ah --chart-name myriarch --repository-id 00000000-0000-0000-0000-000000000000 --layout-out "${W}/lay3"
[ "$(printf '%s\n' "${OUT}" | sed -n 's/^artifacthub-digest=//p')" != "${DIG1}" ] && ok "layout: another ID gives another digest" || fail "layout: another ID gives the same digest"
expect_ok "layout: the same ID for the collector chart" ah --chart-name myriarch-collector --repository-id "${ID}" --layout-out "${W}/lay4"
eq "layout: the file does not name the chart, so the digest is the same" "${DIG1}" "$(printf '%s\n' "${OUT}" | sed -n 's/^artifacthub-digest=//p')"
expect_fail "layout: a directory with files is refused" "layout_invalid.*not an empty directory" ah --chart-name myriarch --repository-id "${ID}" --layout-out "${W}/lay1"

# ==== 2. the ID check =========================================================================================================
for bad in "123E4567-E89B-42D3-A456-426614174000" "" "123e4567-e89b-42d3-a456-4266141740000" "123e4567-e89b-42d3-a456-42661417400" \
           "123e4567e89b42d3a456426614174000" " 123e4567-e89b-42d3-a456-426614174000" "123e4567-e89b-42d3-a456-426614174000 " \
           "123e4567-e89b-42d3-a456-42661417400g" "x: y" '"123e4567-e89b-42d3-a456-426614174000"' "{a: b}" "123e4567-e89b-42d3-a456-426614174000#c" \
           "123e4567-e89b-42d3-a456-426614174000
owners: [x]" "123e4567-e89b-42d3-a456-426614174000
" $'123e4567-e89b-42d3-a456-426614174000\r' ; do
  label="$(printf '%s' "${bad}" | head -c 24 | tr -c 'A-Za-z0-9-' '_')"
  expect_fail "id: refused '${label}'" "id_invalid" ah --chart-name myriarch --repository-id "${bad}" --layout-out "${W}/bad-$RANDOM"
done
for badc in "" "Myriarch" "myriarch " " myriarch" $'myriarch-collector\n' "collector" "ghcr.io/myriarch/charts/myriarch" "myriarch/../myriarch-collector" "myriarch,myriarch-collector"; do
  label="$(printf '%s' "${badc}" | head -c 24 | tr -c 'A-Za-z0-9-' '_')"
  expect_fail "chart: refused '${label}'" "chart_name" ah --chart-name "${badc}" --repository-id "${ID}" --layout-out "${W}/badc-$RANDOM"
done
expect_fail "chart: a missing --chart-name is a usage error" "usage:" ah --repository-id "${ID}"
expect_fail "chart: a missing --chart-name is a usage error with --layout-out" "usage:" ah --repository-id "${ID}" --layout-out "${W}/badc-$RANDOM"
expect_fail "chart: the option without a value" "usage:" ah --repository-id "${ID}" --chart-name
expect_fail "id: no option" "usage:" ah
expect_fail "id: the option without a value" "usage:" ah --chart-name myriarch --repository-id
expect_fail "id: an owners option does not exist" "usage:" ah --chart-name myriarch --repository-id "${ID}" --owners x
expect_fail "id: a repository option does not exist" "usage:" ah --chart-name myriarch --repository-id "${ID}" --repository ghcr.io/example/other
expect_fail "id: a tag option does not exist" "usage:" ah --chart-name myriarch --repository-id "${ID}" --tag v1.0.0

# ==== 3. the upload with a stand-in for crane ==================================================================================
S="${W}/stubs"; SB="${W}/stubbin"; HOME_DIR="${W}/home"
mkdir -p "${S}" "${SB}" "${HOME_DIR}/.docker"
printf '{"auths":{"ghcr.io":{"auth":"eDp5"}}}\n' > "${HOME_DIR}/.docker/config.json"
cat > "${SB}/crane" <<'STUB'
#!/usr/bin/env bash
set -u
d="${STUB_DIR:?}"
if [ "${1:-}" = version ]; then echo v0.22.1; exit 0; fi
{ printf '%s ' "$@"; echo; } >> "${d}/calls.log"
case "$1" in
  ls) k="$(basename "$2")"; if [ -f "${d}/ls.fail.${k}" ]; then echo "Error: name unknown" >&2; exit 1; fi; cat "${d}/tags.${k}" 2>/dev/null ;;
  push) if [ -f "${d}/push.fail" ]; then echo "Error: denied" >&2; exit 1; fi; rm -rf "${d}/pushed"; cp -R "$2" "${d}/pushed" ;;
  digest)
    if [ -f "${d}/digest.override" ]; then cat "${d}/digest.override"; exit 0; fi
    sed -n 's/.*"digest":"\(sha256:[0-9a-f]*\)".*/\1/p' "${d}/pushed/index.json" ;;
  manifest)
    k="$(basename "${2%%:*}")"
    if [ -f "${d}/omanifest.fail.${k}" ]; then echo "Error: denied" >&2; exit 1; fi
    if [ -f "${d}/omanifest.${k}" ]; then cat "${d}/omanifest.${k}"; exit 0; fi
    h="$(sed -n 's/.*"digest":"sha256:\([0-9a-f]*\)".*/\1/p' "${d}/pushed/index.json")"
    if [ -f "${d}/manifest.override" ]; then cat "${d}/manifest.override"; else cat "${d}/pushed/blobs/sha256/${h}"; fi ;;
esac
exit 0
STUB
chmod +x "${SB}/crane"
RB="${W}/restricted"; mkdir -p "${RB}"
for t in bash sh env python3 sed awk grep mktemp cat cp rm mkdir chmod ls head tail cut tr wc sort basename dirname sha256sum shasum; do
  p="$(command -v "${t}" 2>/dev/null || true)"; [ -n "${p}" ] || continue
  printf '#!/bin/sh\nexec "%s" "$@"\n' "${p}" > "${RB}/${t}"; chmod +x "${RB}/${t}"
done
REF_WF="myriarch/myriarch/.github/workflows/artifacthub.yml@refs/tags/v0.2.0"
CHN=myriarch; CHART=ghcr.io/myriarch/charts/myriarch; OTH=myriarch-collector; OTHREPO=ghcr.io/myriarch/charts/myriarch-collector
export CHN
reset() { rm -rf "${S}"; mkdir -p "${S}"; printf '0.2.0-rc.1\n' > "${S}/tags.${CHN}"; }
calls() { cat "${S}/calls.log" 2>/dev/null || true; }
count() { calls | grep -Ec -- "$1" || true; }
run_ah() { ( export PATH="${RB}" TOOLS_BIN="${SB}" STUB_DIR="${S}" HOME="${HOME_DIR}" GITHUB_WORKFLOW_REF="${REF_WF}"; unset DOCKER_CONFIG; "$SCRIPT_AH" --chart-name "${CHN}" --repository-id "${ID}" ); }
# the manifest that the script writes for an ID: what the other chart repository would serve if it carried that ID
omanifest() { # <ID> <file>
  local lay="${W}/om-$RANDOM"
  ah --chart-name myriarch --repository-id "$1" --layout-out "${lay}" > /dev/null 2>&1
  cat "${lay}/blobs/sha256/$(sed -n 's/.*"digest":"sha256:\([0-9a-f]*\)".*/\1/p' "${lay}/index.json")" > "$2"
}

for CHN in myriarch myriarch-collector; do
export CHN
if [ "${CHN}" = myriarch ]; then CHART=ghcr.io/myriarch/charts/myriarch; OTH=myriarch-collector; OTHREPO=ghcr.io/myriarch/charts/myriarch-collector
else CHART=ghcr.io/myriarch/charts/myriarch-collector; OTH=myriarch; OTHREPO=ghcr.io/myriarch/charts/myriarch; fi
reset
expect_ok "upload (${CHN}): a chart with a release candidate tag" run_ah
eq "upload (${CHN}): the output line" "artifacthub-digest=${DIG1}" "$(printf '%s\n' "${OUT}" | grep '^artifacthub-digest=')"
eq "upload (${CHN}): one push to the fixed repository and tag" "1" "$(count "^push .* ${CHART}:artifacthub.io \$")"
eq "upload (${CHN}): no other push" "1" "$(count '^push ')"
eq "upload (${CHN}): the tag list of the fixed repository was read" "1" "$(count "^ls ${CHART} \$")"
eq "upload (${CHN}): no tag command and no push by digest or version" "0" "$(count '^tag |^push .*(@|:v?[0-9])')"
expect_ok "upload (${CHN}): a second run overwrites the tag only" run_ah
eq "upload (${CHN}): two pushes, both to artifacthub.io" "2" "$(count "^push .* ${CHART}:artifacthub.io \$")"
reset; printf '0.2.0\n' > "${S}/tags.${CHN}"
expect_ok "upload (${CHN}): a chart with a final version tag" run_ah
reset; printf 'latest\nartifacthub.io\nsha256-abc.sig\nv0.2.0\n0.2\n0.2.0-rc.0\n' > "${S}/tags.${CHN}"
expect_fail "upload (${CHN}): no version tag (other tags, v-prefix, short, rc.0) is no published chart" "chart_not_published" run_ah
eq "upload (${CHN}): nothing is pushed for an unpublished chart" "0" "$(count '^push ')"
reset; : > "${S}/tags.${CHN}"
expect_fail "upload (${CHN}): an empty tag list" "chart_not_published" run_ah
reset; touch "${S}/ls.fail.${CHN}"
expect_fail "upload (${CHN}): the tag list cannot be read" "chart_unreadable" run_ah
eq "upload (${CHN}): nothing is pushed when the list fails" "0" "$(count '^push ')"
reset; touch "${S}/push.fail"
expect_fail "upload (${CHN}): the push fails" "push_failed" run_ah
reset; printf 'sha256:%s' "$(printf 'e5%.0s' $(seq 32))" > "${S}/digest.override"
expect_fail "upload (${CHN}): the registry reports another digest" "artifact_mismatch" run_ah
reset; printf '{}' > "${S}/manifest.override"
expect_fail "upload (${CHN}): the registry serves another manifest" "artifact_mismatch.*hashes to another digest" run_ah
reset
expect_fail "upload (${CHN}): a workflow ref of another workflow" "workflow_ref" env GITHUB_WORKFLOW_REF="myriarch/myriarch/.github/workflows/release.yml@refs/tags/v0.2.0" bash -c '
  export PATH="$1" TOOLS_BIN="$2" STUB_DIR="$3" HOME="$4"; exec "$5" --chart-name "${CHN}" --repository-id "$6"' _ "${RB}" "${SB}" "${S}" "${HOME_DIR}" "${SCRIPT_AH}" "${ID}"
for bad in refs/heads/main refs/heads/x refs/tags/v0.2 refs/tags/0.2.0 refs/tags/v0.2.0-rc.0 refs/tags/v0.2.0-beta refs/tags/nightly 'refs/tags/v0.2.0/x'; do
  expect_fail "upload (${CHN}): a workflow ref at ${bad} is refused" "workflow_ref" env GITHUB_WORKFLOW_REF="myriarch/myriarch/.github/workflows/artifacthub.yml@${bad}" bash -c '
    export PATH="$1" TOOLS_BIN="$2" STUB_DIR="$3" HOME="$4"; exec "$5" --chart-name "${CHN}" --repository-id "$6"' _ "${RB}" "${SB}" "${S}" "${HOME_DIR}" "${SCRIPT_AH}" "${ID}"
done
expect_fail "upload (${CHN}): a workflow ref of another repository" "workflow_ref" env GITHUB_WORKFLOW_REF="example/other/.github/workflows/artifacthub.yml@refs/tags/v0.2.0" bash -c '
  export PATH="$1" TOOLS_BIN="$2" STUB_DIR="$3" HOME="$4"; exec "$5" --chart-name "${CHN}" --repository-id "$6"' _ "${RB}" "${SB}" "${S}" "${HOME_DIR}" "${SCRIPT_AH}" "${ID}"
reset
expect_ok "upload (${CHN}): a release candidate tag is accepted" env GITHUB_WORKFLOW_REF="myriarch/myriarch/.github/workflows/artifacthub.yml@refs/tags/v0.2.0-rc.1" bash -c '
  export PATH="$1" TOOLS_BIN="$2" STUB_DIR="$3" HOME="$4"; exec "$5" --chart-name "${CHN}" --repository-id "$6"' _ "${RB}" "${SB}" "${S}" "${HOME_DIR}" "${SCRIPT_AH}" "${ID}"
reset
expect_fail "upload (${CHN}): no workflow ref" "workflow_ref" env -u GITHUB_WORKFLOW_REF bash -c '
  export PATH="$1" TOOLS_BIN="$2" STUB_DIR="$3" HOME="$4"; exec "$5" --chart-name "${CHN}" --repository-id "$6"' _ "${RB}" "${SB}" "${S}" "${HOME_DIR}" "${SCRIPT_AH}" "${ID}"
eq "upload (${CHN}): nothing is pushed after a refused workflow ref" "0" "$(count '^push ')"
# the target does not move with the environment
reset
( export PATH="${RB}" TOOLS_BIN="${SB}" STUB_DIR="${S}" HOME="${HOME_DIR}" GITHUB_WORKFLOW_REF="${REF_WF}" RELEASE_CHART=ghcr.io/example/other RELEASE_CHART_COLLECTOR=ghcr.io/example/other RELEASE_TAG=v9.9.9 ARTIFACTHUB_TAG=v9.9.9 TAG_ARTIFACTHUB=v9.9.9 CHART=ghcr.io/example/other
  "$SCRIPT_AH" --chart-name "${CHN}" --repository-id "${ID}" > /dev/null 2>&1 )
eq "upload (${CHN}): the environment cannot change the repository or the tag" "1" "$(count "^push .* ${CHART}:artifacthub.io \$")"
eq "upload (${CHN}): nothing is written to another repository" "0" "$(count 'example/other')"
# the job summary: one block with repository, tag and digest after an upload, nothing after a refusal
SUM="${W}/summary.md"
run_ah_sum() { ( export PATH="${RB}" TOOLS_BIN="${SB}" STUB_DIR="${S}" HOME="${HOME_DIR}" GITHUB_WORKFLOW_REF="${1:-${REF_WF}}" GITHUB_STEP_SUMMARY="${SUM}"; unset DOCKER_CONFIG; "$SCRIPT_AH" --chart-name "${CHN}" --repository-id "${ID}" ); }
reset; rm -f "${SUM}"
expect_ok "summary (${CHN}): an upload" run_ah_sum
eq "summary (${CHN}): one block" "1" "$(grep -c '^## Artifact Hub metadata' "${SUM}")"
eq "summary (${CHN}): repository, tag and digest" "1" "$(grep -Fc -- "\`${CHART}:artifacthub.io\` is \`${DIG1}\`" "${SUM}")"
reset; rm -f "${SUM}"
expect_fail "summary (${CHN}): a refused workflow ref" "workflow_ref" run_ah_sum "myriarch/myriarch/.github/workflows/artifacthub.yml@refs/heads/main"
[ ! -s "${SUM}" ] && ok "summary (${CHN}): nothing is written after a refusal" || fail "summary (${CHN}): something was written after a refusal"
reset; : > "${S}/tags.${CHN}"; rm -f "${SUM}"
expect_fail "summary (${CHN}): an unpublished chart" "chart_not_published" run_ah_sum
[ ! -s "${SUM}" ] && ok "summary (${CHN}): nothing is written for an unpublished chart" || fail "summary (${CHN}): something was written for an unpublished chart"
reset; rm -rf "${HOME_DIR}/.docker"
expect_fail "upload (${CHN}): no registry login" "push_failed.*registry-login.sh" run_ah
mkdir -p "${HOME_DIR}/.docker"; printf '{"auths":{"ghcr.io":{"auth":"eDp5"}}}\n' > "${HOME_DIR}/.docker/config.json"

# the other chart repository: read only, and the same ID in both is a mix-up
omanifest "${ID}" "${W}/same-id.json"; omanifest 00000000-0000-0000-0000-000000000000 "${W}/other-id.json"
reset; printf 'artifacthub.io\n0.2.0\n' > "${S}/tags.${OTH}"; cp "${W}/same-id.json" "${S}/omanifest.${OTH}"
expect_fail "other (${CHN}): the same ID in the other repository is refused" "id_in_use" run_ah
eq "other (${CHN}): nothing is pushed" "0" "$(count '^push ')"
eq "other (${CHN}): the list of the other repository was read" "1" "$(count "^ls ${OTHREPO} \$")"
reset; printf 'artifacthub.io\n0.2.0\n' > "${S}/tags.${OTH}"; cp "${W}/other-id.json" "${S}/omanifest.${OTH}"
expect_ok "other (${CHN}): another ID in the other repository is accepted" run_ah
eq "other (${CHN}): one push, to the chosen repository" "1" "$(count "^push .* ${CHART}:artifacthub.io \$")"
eq "other (${CHN}): the other repository is only read (no push to it)" "0" "$(count "^push .* ${OTHREPO}:")"
eq "other (${CHN}): its manifest was read" "1" "$(count "^manifest ${OTHREPO}:artifacthub.io \$")"
reset; printf '0.2.0\nlatest\n' > "${S}/tags.${OTH}"
expect_ok "other (${CHN}): no artifacthub.io in the other repository is accepted" run_ah
eq "other (${CHN}): no manifest of the other repository was read" "0" "$(count "^manifest ${OTHREPO}:")"
reset; touch "${S}/ls.fail.${OTH}"
expect_fail "other (${CHN}): the list of the other repository cannot be read" "chart_unreadable.*${OTHREPO}" run_ah
eq "other (${CHN}): nothing is pushed after a failed list" "0" "$(count '^push ')"
reset; printf 'artifacthub.io\n' > "${S}/tags.${OTH}"; touch "${S}/omanifest.fail.${OTH}"
expect_fail "other (${CHN}): the manifest of the other repository cannot be read" "chart_unreadable.*${OTHREPO}:artifacthub.io" run_ah
eq "other (${CHN}): nothing is pushed after a failed manifest" "0" "$(count '^push ')"
done
CHN=myriarch; CHART=ghcr.io/myriarch/charts/myriarch; export CHN
# both repositories follow identity.env (a copy of the tree with other values), so a literal in the script is found: the privileged job writes there
IDT="${W}/idtree"; mkdir -p "${IDT}/ci"; cp -R "${REL}" "${IDT}/ci/release"; cp "${ROOT}/ci/versions.env" "${IDT}/ci/versions.env"
sed -i.bak -e 's#^RELEASE_CHART=.*#RELEASE_CHART=ghcr.io/example/server-chart#' -e 's#^RELEASE_CHART_COLLECTOR=.*#RELEASE_CHART_COLLECTOR=ghcr.io/example/collector-chart#' "${IDT}/ci/release/identity.env"
for CHN in myriarch myriarch-collector; do
  export CHN
  if [ "${CHN}" = myriarch ]; then IDREPO=ghcr.io/example/server-chart; IDOTH=ghcr.io/example/collector-chart; else IDREPO=ghcr.io/example/collector-chart; IDOTH=ghcr.io/example/server-chart; fi
  reset; printf '0.2.0\n' > "${S}/tags.server-chart"; printf '0.2.0\n' > "${S}/tags.collector-chart"
  ( export PATH="${RB}" TOOLS_BIN="${SB}" STUB_DIR="${S}" HOME="${HOME_DIR}" GITHUB_WORKFLOW_REF="${REF_WF}"; unset DOCKER_CONFIG
    "${IDT}/ci/release/push-artifacthub.sh" --chart-name "${CHN}" --repository-id "${ID}" > /dev/null 2>&1 )
  eq "identity (${CHN}): one push, to the repository that identity.env names" "1" "$(count "^push .* ${IDREPO}:artifacthub.io \$")"
  eq "identity (${CHN}): no other push" "1" "$(count '^push ')"
  eq "identity (${CHN}): the other repository of identity.env was listed" "1" "$(count "^ls ${IDOTH} \$")"
  eq "identity (${CHN}): no literal repository was used" "0" "$(count 'ghcr.io/myriarch')"
done
CHN=myriarch; export CHN
# verify-chart.sh admits the tag artifacthub.io only for exactly these media types: they must be the ones this script writes
for mt in application/vnd.cncf.artifacthub.config.v1+yaml application/vnd.cncf.artifacthub.repository-metadata.layer.v1.yaml; do
  for f in "${SCRIPT_AH}" "${REL}/verify-chart.sh"; do
    if grep -Fq -- "\"${mt}\"" "${f}"; then ok "cross-check: ${mt} is in $(basename "${f}")"; else fail "cross-check: ${mt} is not in $(basename "${f}")"; fi
  done
done
rm -f "${SB}/crane"
expect_fail "upload: no crane in TOOLS_BIN" "tool_missing" run_ah

# the constants are in the script and in nothing else that a caller controls
grep -q '^TAG_ARTIFACTHUB="artifacthub.io"$' "${SCRIPT_AH}" && ok "script: the tag is a constant" || fail "script: the tag is not a constant"
grep -q 'RELEASE_CHART_COLLECTOR' "${SCRIPT_AH}" && ! grep -q 'RELEASE_CHART=' "${SCRIPT_AH}" && ! grep -q 'RELEASE_CHART_COLLECTOR=' "${SCRIPT_AH}" && ok "script: both repositories come from identity.env" || fail "script: a repository is not taken from identity.env"
! grep -v '^[[:space:]]*#' "${SCRIPT_AH}" | grep -E 'owners|crane\" (tag|copy|cp|delete)|oras ' >/dev/null && ok "script: no owners key, no tag command, no oras" || fail "script: owners, a tag command or oras is present"

# ==== 4. the workflow ==========================================================================================================
CHECKER="${W}/check-artifacthub-workflow.py"
cat > "${CHECKER}" <<'PY'
import importlib.util
import re
import sys

workflow, policy, perms = sys.argv[1:4]
spec = importlib.util.spec_from_file_location("check_workflows", policy)
cw = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cw)
problems = []


def check(cond, message):
    if not cond:
        problems.append(message)


tx, mp, ls = cw.as_text, (lambda n: cw.as_map(n) or {}), (lambda n: cw.as_list(n) or [])
text = open(workflow, encoding="utf-8").read()
lines = text.split("\n")
root, errors = cw.MiniYaml(lines).parse()
check(not errors, "the policy reader cannot read the file: %s" % errors[:3])
top = mp(root)
code = "\n".join(l for l in lines if l.strip() and not l.strip().startswith("#"))

check(tx(top.get("name")) == "artifacthub", "name must be artifacthub")
on = mp(top.get("on"))
check(list(on) == ["workflow_dispatch"], "the only trigger is workflow_dispatch (found %s)" % list(on))
inputs = mp(mp(on.get("workflow_dispatch")).get("inputs"))
check(list(inputs) == ["chart", "repository_id"], "the inputs are exactly chart and repository_id (found %s)" % list(inputs))
check(tx(mp(inputs.get("repository_id")).get("required")) == "true", "repository_id must be required")
chart_in = mp(inputs.get("chart"))
check(tx(chart_in.get("required")) == "true", "chart must be required")
check(tx(chart_in.get("type")) == "choice", "chart must be of type choice")
check([tx(o) for o in ls(chart_in.get("options"))] == ["myriarch", "myriarch-collector"], "chart must offer exactly myriarch and myriarch-collector")
check("default" not in chart_in, "chart must have no default")
check({k: tx(v) for k, v in mp(top.get("permissions")).items()} == {"contents": "read"}, "the top-level permissions are exactly contents: read")
conc = mp(top.get("concurrency"))
check(tx(conc.get("cancel-in-progress")) == "false" and tx(conc.get("group")), "concurrency must never cancel a running upload")
check(tx(mp(mp(top.get("defaults")).get("run")).get("shell")) == "bash", "defaults.run.shell must be bash")
check(set(top) == {"name", "on", "permissions", "concurrency", "defaults", "env", "jobs"}, "unexpected top-level keys %s" % sorted(top))

jobs = mp(top.get("jobs"))
check(list(jobs) == ["tools", "publish"], "the jobs are tools and publish (found %s)" % list(jobs))
env_jobs = [j for j, b in jobs.items() if "environment" in mp(b)]
check(env_jobs == ["publish"], "exactly publish uses an environment (found %s)" % env_jobs)
check(not re.search(r"^\s*environment:[ \t]+(?!release[ \t]*$)\S", code, re.M), "the environment is the literal release")
PERMS = {"tools": {"contents": "read"}, "publish": {"contents": "read", "packages": "write"}}
for jid, job_node in jobs.items():
    job = mp(job_node)
    got = {k: tx(v) for k, v in mp(job.get("permissions")).items()}
    check(got == PERMS.get(jid), "job %s: permissions are %s" % (jid, got))
    check(tx(job.get("timeout-minutes")) is not None, "job %s: no timeout" % jid)
    check(tx(job.get("runs-on")) == "ubuntu-24.04", "job %s: runs-on must be ubuntu-24.04" % jid)
    check(tx(job.get("if")) == "github.repository == 'myriarch/myriarch' && startsWith(github.ref, 'refs/tags/v')", "job %s: the if: must test the repository and a release tag" % jid)
    check("refs/heads" not in (tx(job.get("if")) or ""), "job %s: no branch in the if:" % jid)
    check("continue-on-error" not in job and "uses" not in job and "container" not in job and "services" not in job and "strategy" not in job,
          "job %s: no continue-on-error, reusable workflow, container, services or matrix" % jid)
check([tx(n) for n in ls(mp(jobs.get("publish")).get("needs"))] == ["tools"], "publish needs exactly tools")
check(tx(mp(jobs.get("publish")).get("environment")) == "release", "publish: the environment is release")
check("environment" not in mp(jobs.get("tools")) and "packages" not in {k for k in mp(mp(jobs.get("tools")).get("permissions"))}, "tools: no environment and no packages scope")

PINS = {
    "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1", "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e",
    "actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9", "actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333",
}
for jid, job_node in jobs.items():
    for s in [mp(x) for x in ls(mp(job_node).get("steps"))]:
        name = tx(s.get("name")) or "?"
        u = tx(s.get("uses"))
        if u:
            check(u in PINS, "job %s, step '%s': %s is not one of the pinned actions" % (jid, name, u))
            if u.startswith("actions/checkout@"):
                check(tx(mp(s.get("with")).get("persist-credentials")) == "false", "job %s, step '%s': checkout without persist-credentials: false" % (jid, name))
            if jid == "publish":
                check(not u.startswith(("actions/setup-go@", "actions/setup-node@")), "publish: no Go or Node setup in the job with the write scope")
        run = tx(s.get("run"))
        if run is not None:
            check("${{" not in run, "job %s, step '%s': an expression in a run text" % (jid, name))
            check(not re.search(r"\b(go|node|npm|pnpm|make|goreleaser|curl|wget)\b", run) or jid == "tools" and "ci/release/tools.sh" in run,
                  "job %s, step '%s': a build or download command in a run text" % (jid, name))

# the input reaches the script through env: only
for n, l in enumerate(lines, 1):
    if "inputs." in l and not l.lstrip().startswith("#"):
        check(re.match(r"^          (CHART: \$\{\{ inputs\.chart \}\}|REPOSITORY_ID: \$\{\{ inputs\.repository_id \}\})$", l) is not None, "line %d: the input is used outside env: CHART or REPOSITORY_ID: %s" % (n, l.strip()))
check(code.count("inputs.repository_id") == 1, "the input repository_id is read exactly once")
check(code.count("inputs.chart") == 1, "the input chart is read exactly once")
check(not re.search(r"secrets\.|secrets:|\bGITHUB_ENV\b|\bGITHUB_PATH\b|DOCKER_CONFIG|actions/cache|id-token|attestations", code), "no secret, GITHUB_ENV, GITHUB_PATH, DOCKER_CONFIG, cache, id-token or attestations")
check(re.search(r'push-artifacthub\.sh --chart-name "\$\{CHART\}" --repository-id "\$\{REPOSITORY_ID\}"$', code, re.M) is not None, "the script is called with the quoted variables")
check("--tag" not in code and "--repository " not in code and "--owners" not in code, "the script call has no tag, repository or owners option")
entries = [l.split() for l in open(perms, encoding="utf-8") if l.split() and not l.startswith("#") and l.split()[0] == "artifacthub.yml"]
check(entries == [["artifacthub.yml", "publish", "packages"]], "ci/workflow-permissions.txt must list exactly: artifacthub.yml publish packages (found %s)" % entries)

for p in problems:
    print("FINDING: " + p)
sys.exit(1 if problems else 0)
PY
wf_check() { python3 -I "${CHECKER}" "${1:-${WORKFLOW}}" "${CI_DIR}/check-workflows.py" "${2:-${CI_DIR}/workflow-permissions.txt}"; }
expect_ok "workflow: the shape" wf_check

# policy: a tree with the real workflows, and mutated copies of artifacthub.yml
policy_tree() { # <name> <old> <new>: copy of .github and the lists, one replacement in artifacthub.yml
  local t="${W}/pol-$1"
  rm -rf "${t}"; mkdir -p "${t}/ci"
  cp -R "${ROOT}/.github" "${t}/.github"
  cp "${CI_DIR}/workflow-permissions.txt" "${CI_DIR}/workflow-secrets.txt" "${t}/ci/"
  [ -z "${2:-}" ] || python3 -I - "${t}/.github/workflows/artifacthub.yml" "$2" "$3" <<'PY'
import sys
p, old, new = sys.argv[1:4]
s = open(p, encoding="utf-8").read()
assert old in s, "mutation target not found"
open(p, "w", encoding="utf-8").write(s.replace(old, new, 1))
PY
  printf '%s' "${t}"
}
policy() { python3 -I "${CI_DIR}/check-workflows.py" --root "$1"; }
export CHECK_WORKFLOWS_GATES
CHECK_WORKFLOWS_GATES="$("${CI_DIR}/check-local.sh" --list 2>/dev/null | tr '\n' ' ')"
expect_ok "policy: ci/check-workflows.py accepts the workflow" policy "$(policy_tree plain)"
mut() { # <name> <message regex> <old> <new>: the policy refuses the mutated workflow
  local t; t="$(policy_tree "$1")" || { fail "$1: cannot build the tree"; return; }
  [ -n "${3:-}" ] && python3 -I - "${t}/.github/workflows/artifacthub.yml" "$3" "$4" <<'PY'
import sys
p, old, new = sys.argv[1:4]
s = open(p, encoding="utf-8").read()
assert old in s, "mutation target not found"
open(p, "w", encoding="utf-8").write(s.replace(old, new, 1))
PY
  expect_fail "policy: $1" "$2" policy "${t}"
}
mut "an input in a run text is refused (R7)" "R7" 'run: ci/release/push-artifacthub.sh --chart-name "${CHART}" --repository-id "${REPOSITORY_ID}"' 'run: ci/release/push-artifacthub.sh --chart-name "${CHART}" --repository-id "${{ inputs.repository_id }}"'
mut "a checkout with credentials is refused (R5)" "R5" '          persist-credentials: false
          sparse-checkout' '          sparse-checkout'
mut "a pull_request_target trigger is refused (R4)" "R4" 'on:
  workflow_dispatch:' 'on:
  pull_request_target:
  workflow_dispatch:'
mut "an unpinned action is refused (R1)" "R1" 'actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333 # v8.0.2' 'actions/download-artifact@v8'
mut "a job without a timeout is refused (R6)" "R6" '    timeout-minutes: 10
' ''
mut "a write scope that is not listed is refused (R3)" "R3" '      packages: write # write' '      packages: write # write
      contents: write # write'
t="$(policy_tree noentry)"; grep -v '^artifacthub.yml publish packages$' "${t}/ci/workflow-permissions.txt" > "${t}/ci/wp" && mv "${t}/ci/wp" "${t}/ci/workflow-permissions.txt"
expect_fail "policy: a missing line in ci/workflow-permissions.txt is refused (R3)" "R3" policy "${t}"

# the static checks of part 4 bite
mutwf() { # <name> <finding regex> <old> <new>
  python3 -I - "${WORKFLOW}" "${W}/wf-$1.yml" "$3" "$4" <<'PY'
import sys
src, dst, old, new = sys.argv[1:5]
s = open(src, encoding="utf-8").read()
assert old in s, "mutation target not found"
open(dst, "w", encoding="utf-8").write(s.replace(old, new, 1))
PY
  expect_fail "workflow: $1" "$2" wf_check "${W}/wf-$1.yml"
}
mutwf "a second trigger is refused" "only trigger" 'on:
  workflow_dispatch:' 'on:
  push:
  workflow_dispatch:'
mutwf "a third input is refused" "exactly chart and repository_id" '        required: true
        type: string' '        required: true
        type: string
      tag:
        type: string'
mutwf "a default for the chart is refused" "no default" "          - myriarch
          - myriarch-collector" "          - myriarch
          - myriarch-collector
        default: myriarch"
mutwf "a chart input of type string is refused" "type choice" "        type: choice" "        type: string"
mutwf "a third chart option is refused" "exactly myriarch and myriarch-collector" "          - myriarch-collector" "          - myriarch-collector
          - other"
mutwf "the chart input in a with: is refused" "outside env" 'cache: false' 'cache: false
          go-version: ${{ inputs.chart }}'
mutwf "a second use of the chart input is refused" "read exactly once" '          CHART: ${{ inputs.chart }}' '          CHART: ${{ inputs.chart }}
          CHART2: ${{ inputs.chart }}'
mutwf "id-token is refused" "id-token" '      packages: write # write the tag' '      id-token: write
      packages: write # write the tag'
mutwf "a cancelling concurrency is refused" "never cancel" 'cancel-in-progress: false' 'cancel-in-progress: true'
mutwf "Go in the privileged job is refused" "no Go or Node setup" '      - name: Download signing-tools
        uses: actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333 # v8.0.2
        with:
          artifact-ids: ${{ needs.tools.outputs.artifact-id }}' '      - name: Set up Go
        uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
      - name: Download signing-tools
        uses: actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333 # v8.0.2
        with:
          artifact-ids: ${{ needs.tools.outputs.artifact-id }}'
mutwf "the input in a with: is refused" "outside env" 'cache: false' 'cache: false
          go-version: ${{ inputs.repository_id }}'
mutwf "a tag option in the call is refused" "no tag, repository or owners" '--repository-id "${REPOSITORY_ID}"' '--repository-id "${REPOSITORY_ID}" --tag v1.0.0'
mutwf "a secret is refused" "no secret" 'REGISTRY_TOKEN: ${{ github.token }}' 'REGISTRY_TOKEN: ${{ secrets.TOKEN }}'
mutwf "another environment is refused" "environment" 'environment: release' 'environment: production'

finish_tests "artifacthub-test"
