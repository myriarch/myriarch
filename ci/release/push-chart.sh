#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Pushes a chart of the release (the server chart or the collector chart) to ghcr.io BY DIGEST and signs it keyless (step of the job `push-chart`, ADR-0035). Privileged (id-token: write,
# packages: write). It runs strictly before the provenance attestation of the same digest (actions/attest), like sign-image.sh. Like the
# images, the chart gets no tag here: the version tag is added by finalize.sh after the second approval, with `crane tag`.
#
#   TOOLS_BIN=... GITHUB_WORKFLOW_REF=... ci/release/push-chart.sh [--chart-name NAME] --layout DIR --digest CHART_DIGEST --tag TAG --commit SHA
#
#   --chart-name  myriarch (default): RELEASE_CHART, the server chart; myriarch-collector: RELEASE_CHART_COLLECTOR [chart_name]
#   --layout  the OCI layout that `ci/release/chart-package.sh layout` wrote (job `chart`): one chart manifest [layout_invalid]
#   --digest  the digest of that manifest, which is the digest of the chart in the registry [layout_invalid]
#
# 1. guard    GITHUB_WORKFLOW_REF names .github/workflows/release.yml of the release repository at a version tag (ADR-0035 freezes this
#             identity): a guard against a push from a laptop, not a security boundary [workflow_ref]
# 2. layout   index.json holds exactly the manifest --digest, and the manifest, its config and its single chart layer are present with the
#             right size and SHA-256 [layout_invalid]
# 3. push     `crane push LAYOUT RELEASE_CHART@DIGEST` with the credentials of $HOME/.docker/config.json (ci/release/registry-login.sh). A
#             reference with a digest sets no tag. The digest is read back with `crane digest`, the manifest the registry serves must be
#             the bytes of the layout, and `crane validate --remote` checks the whole artifact [push_failed] [chart_digest_mismatch]
# 4. sign     `cosign sign --yes REPO@DIGEST` with the ambient OIDC token, then `cosign verify` with the exact identity of the run (the same
#             claims as sign-image.sh) [sign_failed] [signature_invalid]
#
# helm is not needed here: the chart was packaged, rendered and checked in the job `chart`, which has no write access, and the layout
# reaches this job as an artifact whose SHA-256 was compared on unpacking. verify-chart.sh renders the pushed chart again.
#
# stdout: chart-digest=<digest> (a GITHUB_OUTPUT line). Everything else goes to stderr. Pushing the same digest again changes nothing.
# Exit status: 0 ok, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="push-chart.sh [--chart-name NAME] --layout DIR --digest CHART_DIGEST --tag TAG --commit SHA (environment: TOOLS_BIN, GITHUB_WORKFLOW_REF)"
layout="" digest="" tag="" commit="" name="myriarch"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --chart-name) need_value "$1" "$#"; name="$2"; shift 2 ;;
    --layout) need_value "$1" "$#"; layout="$2"; shift 2 ;;
    --digest) need_value "$1" "$#"; digest="$2"; shift 2 ;;
    --tag) need_value "$1" "$#"; tag="$2"; shift 2 ;;
    --commit) need_value "$1" "$#"; commit="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
    *) usage_err "${USAGE}" ;;
  esac
done
[ -n "${layout}" ] && [ -n "${digest}" ] && [ -n "${tag}" ] && [ -n "${commit}" ] || usage_err "${USAGE}"
load_identity
case "${name}" in
  myriarch) repo="${RELEASE_CHART}" ;;
  myriarch-collector) repo="${RELEASE_CHART_COLLECTOR}" ;;
  *) err chart_name "--chart-name must be myriarch or myriarch-collector" ;;
esac
check_match tag_invalid "--tag" "${tag}" "${TAG_RE}"
check_match commit_invalid "--commit" "${commit}" "${COMMIT_RE}"
check_match digest_invalid "--digest" "${digest}" "${DIGEST_RE}"
check_text layout_invalid "--layout" "${layout}"
[ -d "${layout}" ] || err layout_invalid "${layout} is not a directory"
need_tool cosign crane
[ -n "${HOME:-}" ] && [ -f "${HOME}/.docker/config.json" ] || err push_failed "${HOME:-HOME}/.docker/config.json does not exist: run ci/release/registry-login.sh login first"

# --- 1. guard ----------------------------------------------------------------------------------------------------------------
want_ref="$(printf '%s' "${RELEASE_REPOSITORY}/${RELEASE_WORKFLOW}@refs/tags/${tag}")"
[ "${GITHUB_WORKFLOW_REF:-}" = "${want_ref}" ] \
  || err workflow_ref "refusing to write to ${repo}: GITHUB_WORKFLOW_REF is '${GITHUB_WORKFLOW_REF:-unset}', expected '${want_ref}'"

# --- 2. layout ---------------------------------------------------------------------------------------------------------------
python3 -I - "${layout}" "${digest}" <<'PY' || exit 1
import hashlib, json, os, sys

layout, want = sys.argv[1:3]
CHART = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"


def fail(msg):
    print("[push-chart] ERROR [layout_invalid] " + msg, file=sys.stderr)
    sys.exit(1)


def read(path):
    try:
        with open(path, "rb") as f:
            return f.read()
    except OSError as e:
        fail("cannot read %s: %s" % (path, e.strerror))


def blob(desc):
    d = str(desc.get("digest", ""))
    if len(d) != 71 or not d.startswith("sha256:") or any(c not in "0123456789abcdef" for c in d[7:]):
        fail("unsupported digest '%s'" % d)
    data = read(os.path.join(layout, "blobs", "sha256", d[7:]))
    if hashlib.sha256(data).hexdigest() != d[7:] or len(data) != desc.get("size"):
        fail("blob %s does not match its digest or size" % d)
    return data


try:
    marker = json.loads(read(os.path.join(layout, "oci-layout")))
    index = json.loads(read(os.path.join(layout, "index.json")))
except ValueError:
    fail("oci-layout or index.json is not JSON")
if marker != {"imageLayoutVersion": "1.0.0"}:
    fail("oci-layout is not an OCI image layout 1.0.0")
manifests = index.get("manifests") or []
if len(manifests) != 1 or manifests[0].get("digest") != want or manifests[0].get("mediaType") != "application/vnd.oci.image.manifest.v1+json":
    fail("index.json must hold exactly the image manifest %s" % want)
manifest = json.loads(blob(manifests[0]))
blob(manifest.get("config") or {})
layers = manifest.get("layers") or []
if len(layers) != 1 or layers[0].get("mediaType") != CHART:
    fail("the chart manifest must have exactly one chart content layer")
blob(layers[0])
PY

# --- 3. push -----------------------------------------------------------------------------------------------------------------
errfile="$(mktemp "${TMPDIR:-/tmp}/myriarch-push-chart.XXXXXX")"
trap 'rm -f "${errfile}" "${errfile}.manifest"' EXIT
info "pushing ${layout} to ${repo}@${digest} (no tag)"
"${TOOLS_BIN}/crane" push "${layout}" "${repo}@${digest}" > /dev/null 2> "${errfile}" \
  || err push_failed "crane push failed: $(grep -v '^[[:space:]]*$' "${errfile}" | tail -n 1 | cut -c 1-300)"
got="$("${TOOLS_BIN}/crane" digest "${repo}@${digest}" 2> "${errfile}")" || err chart_digest_mismatch "crane could not read ${repo}@${digest} back"
[ "${got}" = "${digest}" ] || err chart_digest_mismatch "the registry reports ${got} for ${repo}@${digest}"
"${TOOLS_BIN}/crane" manifest "${repo}@${digest}" > "${errfile}.manifest" 2> "${errfile}" || err chart_digest_mismatch "crane could not read the manifest of ${repo}@${digest}"
[ "sha256:$(sha256_of "${errfile}.manifest")" = "${digest}" ] || err chart_digest_mismatch "the manifest the registry serves for ${repo}@${digest} hashes to another digest"
"${TOOLS_BIN}/crane" validate --remote "${repo}@${digest}" >&2 || err chart_digest_mismatch "the chart ${repo}@${digest} in the registry is incomplete or invalid"

# --- 4. sign -----------------------------------------------------------------------------------------------------------------
info "signing ${repo}@${digest}"
"${TOOLS_BIN}/cosign" sign --yes "${repo}@${digest}" >&2 || err sign_failed "cosign sign failed for ${repo}@${digest}"
verify_image_signature "${repo}" "${digest}" "${tag}" "${commit}"
info "verified the signature of ${repo}@${digest}"
printf 'chart-digest=%s\n' "${digest}"
