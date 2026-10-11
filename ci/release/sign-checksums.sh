#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Signs checksums.txt of a release keyless and prepares the provenance asset (job `sign-blobs` of the release workflow, ADR-0035).
# Privileged: it runs with id-token: write, so it is called with a PATH that has no go, node, pnpm, make or goreleaser.
#
#   ci/release/sign-checksums.sh sign --assets DIR --out DIR --tag TAG --commit SHA
#   ci/release/sign-checksums.sh provenance --bundle FILE --assets DIR --out DIR --version V
#
# sign        `cosign sign-blob --yes --bundle OUT/checksums.txt.sigstore.json DIR/checksums.txt` with the ambient GitHub Actions OIDC
#             token and the default signing configuration (no --key, no --tlog-upload, no --use-signing-config=false), then at once
#             `verify-blob.sh --mode exact` with the identity of identity.env, TAG and SHA. OUT must be new or empty, and the bundle
#             must not exist [bundle_exists]. A failed signing is [sign_failed], a failed verification [signature_invalid]; the job
#             fails and nothing is uploaded.
# provenance  Takes the bundle that actions/attest wrote (a Sigstore bundle, one JSON line), checks its structure and copies it to
#             OUT/myriarch_V_provenance.intoto.jsonl (an existing target is [provenance_exists]). The checks (python3 -I): exactly one
#             non-empty line; a JSON object with mediaType application/vnd.dev.sigstore.bundle.v0.N+json (N from 3 on); a dsseEnvelope
#             with payloadType application/vnd.in-toto+json; a statement with predicateType https://slsa.dev/provenance/v1 whose
#             subjects (name and digest.sha256) are exactly the lines of DIR/checksums.txt [provenance_invalid]. The signature is not
#             checked here: verify.sh does that with `gh attestation verify`.
#
# Exit status: 0 ok, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="sign-checksums.sh sign --assets DIR --out DIR --tag TAG --commit SHA | provenance --bundle FILE --assets DIR --out DIR --version V"

sign() {
  local assets="" out="" tag="" commit=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --assets) need_value "$1" "$#"; assets="$2"; shift 2 ;;
      --out) need_value "$1" "$#"; out="$2"; shift 2 ;;
      --tag) need_value "$1" "$#"; tag="$2"; shift 2 ;;
      --commit) need_value "$1" "$#"; commit="$2"; shift 2 ;;
      *) usage_err "${USAGE}" ;;
    esac
  done
  [ -n "${assets}" ] && [ -n "${out}" ] && [ -n "${tag}" ] && [ -n "${commit}" ] || usage_err "${USAGE}"
  load_identity
  check_match tag_invalid "--tag" "${tag}" "${TAG_RE}"
  check_match commit_invalid "--commit" "${commit}" "${COMMIT_RE}"
  check_text file_missing "--assets" "${assets}"
  check_text file_missing "--out" "${out}"
  [ -f "${assets}/checksums.txt" ] || err file_missing "${assets}/checksums.txt is missing"
  local bundle="${out}/checksums.txt.sigstore.json"
  [ ! -e "${bundle}" ] || err bundle_exists "${bundle} exists already"
  if [ -d "${out}" ] && [ -n "$(ls -A "${out}")" ]; then err out_not_empty "${out} exists and is not empty"; fi
  need_tool cosign
  mkdir -p "${out}"
  info "signing ${assets}/checksums.txt keyless"
  "${TOOLS_BIN}/cosign" sign-blob --yes --bundle "${bundle}" "${assets}/checksums.txt" >&2 || err sign_failed "cosign sign-blob failed"
  [ -s "${bundle}" ] || err sign_failed "cosign sign-blob wrote no bundle"
  "${RELEASE_LIB_DIR}/verify-blob.sh" --mode exact --bundle "${bundle}" --repository "${RELEASE_REPOSITORY}" --workflow "${RELEASE_WORKFLOW}" \
    --tag "${tag}" --commit "${commit}" "${assets}/checksums.txt"
  info "checksums.txt is signed and verified for ${tag}"
}

provenance() {
  local bundle="" assets="" out="" version=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --bundle) need_value "$1" "$#"; bundle="$2"; shift 2 ;;
      --assets) need_value "$1" "$#"; assets="$2"; shift 2 ;;
      --out) need_value "$1" "$#"; out="$2"; shift 2 ;;
      --version) need_value "$1" "$#"; version="$2"; shift 2 ;;
      *) usage_err "${USAGE}" ;;
    esac
  done
  [ -n "${bundle}" ] && [ -n "${assets}" ] && [ -n "${out}" ] && [ -n "${version}" ] || usage_err "${USAGE}"
  check_match version_invalid "--version" "${version}" "${VERSION_RE}"
  check_text file_missing "--bundle" "${bundle}"
  check_text file_missing "--assets" "${assets}"
  check_text file_missing "--out" "${out}"
  [ -f "${bundle}" ] || err file_missing "the attestation bundle ${bundle} does not exist"
  [ -f "${assets}/checksums.txt" ] || err file_missing "${assets}/checksums.txt is missing"
  local target="${out}/myriarch_${version}_provenance.intoto.jsonl"
  [ ! -e "${target}" ] || err provenance_exists "${target} exists already"
  python3 -I - "${bundle}" "${assets}/checksums.txt" <<'PY' || err provenance_invalid "the attestation bundle does not describe the files of checksums.txt as SLSA build provenance"
import base64, binascii, json, re, sys

bundle_path, sums_path = sys.argv[1:3]


def fail(msg):
    print("[sign-checksums] %s" % msg, file=sys.stderr)
    sys.exit(1)


text = open(bundle_path, encoding="utf-8").read()
lines = [l for l in text.split("\n") if l.strip()]
if len(lines) != 1:
    fail("the bundle must have exactly one non-empty line, it has %d" % len(lines))
try:
    bundle = json.loads(lines[0])
except ValueError:
    fail("the bundle is not JSON")
if not isinstance(bundle, dict):
    fail("the bundle is not a JSON object")
if not re.match(r"^application/vnd\.dev\.sigstore\.bundle\.v0\.([3-9]|[1-9][0-9]+)\+json$", str(bundle.get("mediaType"))):
    fail("unexpected bundle media type")
env = bundle.get("dsseEnvelope")
if not isinstance(env, dict) or env.get("payloadType") != "application/vnd.in-toto+json":
    fail("the bundle has no in-toto DSSE envelope")
try:
    statement = json.loads(base64.b64decode(env["payload"], validate=True))
except (KeyError, TypeError, ValueError, binascii.Error):
    fail("the DSSE payload is not a base64 JSON statement")
if not isinstance(statement, dict) or statement.get("predicateType") != "https://slsa.dev/provenance/v1":
    fail("the statement is not SLSA provenance v1")
got = []
for s in statement.get("subject") or []:
    if not isinstance(s, dict) or not isinstance(s.get("name"), str) or not isinstance(s.get("digest"), dict) or not isinstance(s["digest"].get("sha256"), str):
        fail("a subject has no name or sha256 digest")
    got.append((s["name"], s["digest"]["sha256"]))
want = []
for line in open(sums_path, encoding="utf-8").read().split("\n"):
    if not line:
        continue
    m = re.match(r"^([0-9a-f]{64})  ([^/ ]+)$", line)
    if not m:
        fail("checksums.txt has a malformed line")
    want.append((m.group(2), m.group(1)))
if not want:
    fail("checksums.txt lists nothing")
if sorted(got) != sorted(want):
    missing = sorted(set(want) - set(got))
    extra = sorted(set(got) - set(want))
    fail("the subjects differ from checksums.txt: %d missing, %d extra, %d entries against %d" % (len(missing), len(extra), len(got), len(want)))
PY
  mkdir -p "${out}"
  cp "${bundle}" "${target}"
  info "the provenance asset is ${target}"
}

[ "$#" -ge 1 ] || usage_err "${USAGE}"
cmd="$1"
shift
case "${cmd}" in
  sign) sign "$@" ;;
  provenance) provenance "$@" ;;
  -h|--help|help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
  *) usage_err "${USAGE}" ;;
esac
