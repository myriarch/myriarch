#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Uploads the Artifact Hub repository metadata file of the server chart to ghcr.io (step of the job `publish` of
# .github/workflows/artifacthub.yml, ADR-0035). Privileged (packages: write), started by the board by hand after the chart is published.
#
#   TOOLS_BIN=... GITHUB_WORKFLOW_REF=... ci/release/push-artifacthub.sh --repository-id ID
#   ci/release/push-artifacthub.sh --repository-id ID --layout-out DIR      (builds the OCI layout only: no tool, no network, no guard)
#
# Artifact Hub shows a chart repository as "Verified publisher" when the repository holds, under the tag `artifacthub.io`, an OCI
# artifact with the file artifacthub-repo.yml. The media types are those of https://artifacthub.io/docs/topics/repositories/helm-charts/
# (section OCI, `oras push ... --config /dev/null:application/vnd.cncf.artifacthub.config.v1+yaml ...`; read on 2026-10-23):
#   config  application/vnd.cncf.artifacthub.config.v1+yaml (an empty blob, like `--config /dev/null`)
#   layer   application/vnd.cncf.artifacthub.repository-metadata.layer.v1.yaml (the file)
#
# The target is fixed in this script: the repository RELEASE_CHART of ci/release/identity.env and the tag artifacthub.io. Neither the
# repository nor the tag is an input, and the script never writes a version tag. The only input is the repository ID, which Artifact Hub
# shows on the repository card of the control panel.
#
# The file is public. It holds exactly one key, repositoryID, and nothing else: no owners, no names, no addresses. An `owners` entry
# would need a decision of the board and a change of its own.
#
# 1. id       the ID is a UUID in lower case, as a whole string [id_invalid]
# 2. layout   the OCI layout of the artifact (config, one layer, manifest) is written to a temporary directory, or to --layout-out
# 3. guard    GITHUB_WORKFLOW_REF names .github/workflows/artifacthub.yml of the release repository at a release tag refs/tags/vX.Y.Z
#             or refs/tags/vX.Y.Z-rc.N (the environment `release` admits only such tags); a branch, including main, and any other tag
#             are refused. A guard against a push from a laptop, not a security boundary [workflow_ref]
# 4. chart    the chart repository holds at least one version tag (`crane ls`): the chart is published [chart_unreadable] [chart_not_published]
# 5. push     `crane push LAYOUT RELEASE_CHART:artifacthub.io` with the credentials of $HOME/.docker/config.json (ci/release/registry-login.sh),
#             then the manifest the registry serves for the tag must be the bytes of the layout [push_failed] [artifact_mismatch]
#
# Running it again overwrites the tag artifacthub.io and nothing else (the same ID gives the same digest).
# stdout: artifacthub-digest=<digest> (a GITHUB_OUTPUT line). Everything else goes to stderr. Exit status: 0 ok, 1 refused or failed,
# 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="push-artifacthub.sh --repository-id ID [--layout-out DIR] (environment: TOOLS_BIN, GITHUB_WORKFLOW_REF)"
TAG_ARTIFACTHUB="artifacthub.io"
WORKFLOW_ARTIFACTHUB=".github/workflows/artifacthub.yml"
ID_RE='^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
id="" out="" have_id=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --repository-id) need_value "$1" "$#"; id="$2"; have_id=1; shift 2 ;;
    --layout-out) need_value "$1" "$#"; out="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
    *) usage_err "${USAGE}" ;;
  esac
done
[ "${have_id}" = 1 ] || usage_err "${USAGE}"
[ -n "${id}" ] || err id_invalid "the repository ID is empty"
load_identity

# --- 1. id -------------------------------------------------------------------------------------------------------------------
check_match id_invalid "--repository-id" "${id}" "${ID_RE}"

# --- 2. layout ---------------------------------------------------------------------------------------------------------------
work=""
cleanup() { [ -z "${work}" ] || rm -rf "${work}"; }
trap cleanup EXIT
if [ -n "${out}" ]; then
  check_text layout_invalid "--layout-out" "${out}"
  if [ -e "${out}" ]; then [ -d "${out}" ] && [ -z "$(ls -A "${out}")" ] || err layout_invalid "${out} exists and is not an empty directory"; fi
  layout="${out}"
else
  work="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-artifacthub.XXXXXX")"
  layout="${work}/layout"
fi
mkdir -p "${layout}"
digest="$(python3 -I - "${layout}" "${id}" <<'PY'
import hashlib, json, os, sys

layout, repo_id = sys.argv[1:3]
CONFIG = "application/vnd.cncf.artifacthub.config.v1+yaml"
LAYER = "application/vnd.cncf.artifacthub.repository-metadata.layer.v1.yaml"
MANIFEST = "application/vnd.oci.image.manifest.v1+json"


def put(data):
    h = hashlib.sha256(data).hexdigest()
    with open(os.path.join(layout, "blobs", "sha256", h), "wb") as f:
        f.write(data)
    return {"digest": "sha256:" + h, "size": len(data)}


os.makedirs(os.path.join(layout, "blobs", "sha256"))
with open(os.path.join(layout, "oci-layout"), "w") as f:
    f.write('{"imageLayoutVersion":"1.0.0"}')
config = dict(mediaType=CONFIG, **put(b""))
layer = dict(mediaType=LAYER, **put(("repositoryID: %s\n" % repo_id).encode()))
manifest = {"schemaVersion": 2, "mediaType": MANIFEST, "config": config, "layers": [layer]}
desc = dict(mediaType=MANIFEST, **put(json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode()))
with open(os.path.join(layout, "index.json"), "w") as f:
    json.dump({"schemaVersion": 2, "manifests": [desc]}, f, sort_keys=True, separators=(",", ":"))
print(desc["digest"])
PY
)" || err layout_invalid "cannot write the OCI layout"
[[ "${digest}" =~ ${DIGEST_RE} ]] || err layout_invalid "the layout has no manifest digest"
if [ -n "${out}" ]; then
  info "wrote the layout of ${digest} to ${out}"
  printf 'artifacthub-digest=%s\n' "${digest}"
  exit 0
fi

# --- 3. guard ----------------------------------------------------------------------------------------------------------------
need_tool crane
[ -n "${HOME:-}" ] && [ -f "${HOME}/.docker/config.json" ] || err push_failed "${HOME:-HOME}/.docker/config.json does not exist: run ci/release/registry-login.sh login first"
want_prefix="${RELEASE_REPOSITORY}/${WORKFLOW_ARTIFACTHUB}@refs/tags/"
wf_ref="${GITHUB_WORKFLOW_REF:-}"
if [ "${wf_ref#"${want_prefix}"}" = "${wf_ref}" ] || ! [[ "${wf_ref#"${want_prefix}"}" =~ ${TAG_RE} ]]; then
  err workflow_ref "refusing to write to ${RELEASE_CHART}: GITHUB_WORKFLOW_REF is '${wf_ref:-unset}', expected '${want_prefix}vX.Y.Z' (a release tag)"
fi

# --- 4. chart ----------------------------------------------------------------------------------------------------------------
tags="${work}/tags"
"${TOOLS_BIN}/crane" ls "${RELEASE_CHART}" > "${tags}" 2> "${work}/err" \
  || err chart_unreadable "crane could not list the tags of ${RELEASE_CHART}: $(grep -v '^[[:space:]]*$' "${work}/err" | tail -n 1 | cut -c 1-300)"
published=0
while IFS= read -r t || [ -n "${t}" ]; do
  case "${t}" in *[[:cntrl:]]*) continue ;; esac
  if [[ "${t}" =~ ${VERSION_RE} ]]; then published=1; break; fi
done < "${tags}"
[ "${published}" = 1 ] || err chart_not_published "${RELEASE_CHART} has no version tag yet: publish the chart (release) before the Artifact Hub metadata"

# --- 5. push -----------------------------------------------------------------------------------------------------------------
info "pushing ${layout} to ${RELEASE_CHART}:${TAG_ARTIFACTHUB}"
"${TOOLS_BIN}/crane" push "${layout}" "${RELEASE_CHART}:${TAG_ARTIFACTHUB}" > /dev/null 2> "${work}/err" \
  || err push_failed "crane push failed: $(grep -v '^[[:space:]]*$' "${work}/err" | tail -n 1 | cut -c 1-300)"
got="$("${TOOLS_BIN}/crane" digest "${RELEASE_CHART}:${TAG_ARTIFACTHUB}" 2> "${work}/err")" || err artifact_mismatch "crane could not read ${RELEASE_CHART}:${TAG_ARTIFACTHUB} back"
[ "${got}" = "${digest}" ] || err artifact_mismatch "the registry reports ${got} for ${RELEASE_CHART}:${TAG_ARTIFACTHUB}, expected ${digest}"
"${TOOLS_BIN}/crane" manifest "${RELEASE_CHART}:${TAG_ARTIFACTHUB}" > "${work}/manifest" 2> "${work}/err" || err artifact_mismatch "crane could not read the manifest of ${RELEASE_CHART}:${TAG_ARTIFACTHUB}"
[ "sha256:$(sha256_of "${work}/manifest")" = "${digest}" ] || err artifact_mismatch "the manifest the registry serves for ${RELEASE_CHART}:${TAG_ARTIFACTHUB} hashes to another digest"
info "uploaded ${digest} as ${RELEASE_CHART}:${TAG_ARTIFACTHUB}"
summary "## Artifact Hub metadata" "" "- \`${RELEASE_CHART}:${TAG_ARTIFACTHUB}\` is \`${digest}\`"
printf 'artifacthub-digest=%s\n' "${digest}"
