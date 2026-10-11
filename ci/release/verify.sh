#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Verifies a release as a whole BEFORE it is published (job `verify` of the release workflow, ADR-0035). Nothing here changes the
# release or the registry; the job holds read scopes only. What it proves is what finalize.sh later binds the draft to.
#
#   GH_TOKEN=... TOOLS_BIN=... RELEASE_TAG=... RELEASE_COMMIT=... SERVER_INDEX_DIGEST=... SERVER_AMD64_DIGEST=... \
#   SERVER_ARM64_DIGEST=... UPDATER_INDEX_DIGEST=... UPDATER_AMD64_DIGEST=... UPDATER_ARM64_DIGEST=... \
#     ci/release/verify.sh --assets DIR --signatures DIR --out DIR
#
# The registry login (ci/release/registry-login.sh login) has been done before: the packages are still private. OUT must be new or empty.
#
#   1. files        DIR and the signatures directory together are the 42 files of stage `final` (verify-dist.sh)
#   2. signature    checksums.txt against checksums.txt.sigstore.json with verify-blob.sh in the modes exact (tag, commit), pattern and
#                   bound, all with the values of identity.env                                                [signature_invalid]
#   3. provenance   `gh attestation verify <file> --bundle <provenance asset> --repo <repository> --cert-identity <exact>
#                   --source-ref refs/tags/<tag> --source-digest <commit> --deny-self-hosted-runners` for each of the 39 listed files,
#                   and for myriarch-updater_<v>_linux_amd64 and its .tar.gz once more without --bundle (the copy in GitHub's
#                   attestation API) and with --signer-workflow instead of --cert-identity     [provenance_invalid]
#   4. images       for the server and the updater image:
#                   - the index (crane manifest REPO@INDEX): its SHA-256 is INDEX, an OCI image index with exactly the manifests
#                     linux/amd64 and linux/arm64 (the two platform digests), annotations source, version and revision [image_index]
#                   - each platform manifest has exactly one layer; its config has user 65532:65532, the entrypoint of the image,
#                     the labels version, revision and source, and one diff id                                            [image_config]
#                   - cosign verify with the exact identity on the index and both platform digests                  [signature_invalid]
#                   - gh attestation verify oci://REPO@INDEX --bundle-from-oci (provenance) and, for each platform digest, with
#                     --predicate-type https://spdx.dev/Document/v2.3 (SBOM)                                          [attestation_invalid]
#                   - crane ls REPO: no tag latest [image_tag_latest]; every tag is sha256-<64 hex> or a version tag [image_tag_unexpected]
#   5. output       OUT/verified-manifest.txt (<sha256>  <size>  <name> for the 42 files, sorted by name), OUT/verified-images.txt
#                   (<repository> <index|linux/amd64|linux/arm64> <digest>, six lines) and OUT/checksums.txt. A summary goes to
#                   GITHUB_STEP_SUMMARY when it is set.
#
# Exit status: 0 verified, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="verify.sh --assets DIR --signatures DIR --out DIR"
assets="" signatures="" out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --assets) need_value "$1" "$#"; assets="$2"; shift 2 ;;
    --signatures) need_value "$1" "$#"; signatures="$2"; shift 2 ;;
    --out) need_value "$1" "$#"; out="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
    *) usage_err "${USAGE}" ;;
  esac
done
[ -n "${assets}" ] && [ -n "${signatures}" ] && [ -n "${out}" ] || usage_err "${USAGE}"
load_identity
tag="${RELEASE_TAG:-}"
commit="${RELEASE_COMMIT:-}"
check_match tag_invalid "RELEASE_TAG" "${tag}" "${TAG_RE}"
check_match commit_invalid "RELEASE_COMMIT" "${commit}" "${COMMIT_RE}"
for v in SERVER_INDEX_DIGEST SERVER_AMD64_DIGEST SERVER_ARM64_DIGEST UPDATER_INDEX_DIGEST UPDATER_AMD64_DIGEST UPDATER_ARM64_DIGEST; do
  check_match image_digest_invalid "${v}" "${!v:-}" "${DIGEST_RE}"
done
for v in "${assets}" "${signatures}" "${out}"; do check_text file_missing "a path argument" "${v}"; done
[ -d "${assets}" ] && [ -d "${signatures}" ] || err file_missing "--assets and --signatures must be directories"
if [ -d "${out}" ] && [ -n "$(ls -A "${out}")" ]; then err out_not_empty "${out} exists and is not empty"; fi
need_gh
need_tool cosign crane
version="${tag#v}"
repo="${RELEASE_REPOSITORY}"
provenance_asset="myriarch_${version}_provenance.intoto.jsonl"
label_source="$(sed -n 's/^LABEL_SOURCE=//p' "${ROOT}/ci/image-labels.env" | head -n 1)"
[ -n "${label_source}" ] || err image_index "LABEL_SOURCE is missing in ci/image-labels.env"

work="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-verify.XXXXXX")"
trap 'rm -rf "${work}"' EXIT
files="${work}/files"
mkdir -p "${files}"
for d in "${assets}" "${signatures}"; do
  for f in "${d}"/* "${d}"/.[!.]*; do
    [ -e "${f}" ] || [ -L "${f}" ] || continue
    name="$(basename "${f}")"
    if [ -L "${f}" ] || [ ! -f "${f}" ]; then err asset_not_regular "${name} is not a regular file"; fi
    [ ! -e "${files}/${name}" ] || err asset_unexpected "${name} is in both directories"
    ln "${f}" "${files}/${name}" 2>/dev/null || cp "${f}" "${files}/${name}"
  done
done

# --- 1. files ----------------------------------------------------------------------------------------------------------------
"${RELEASE_LIB_DIR}/verify-dist.sh" --dir "${files}" --version "${version}" --stage final

# --- 2. the signature of checksums.txt ---------------------------------------------------------------------------------------
for mode in exact pattern bound; do
  "${RELEASE_LIB_DIR}/verify-blob.sh" --mode "${mode}" --bundle "${files}/checksums.txt.sigstore.json" --repository "${RELEASE_REPOSITORY}" \
    --workflow "${RELEASE_WORKFLOW}" --tag "${tag}" --commit "${commit}" "${files}/checksums.txt"
done

# --- 3. provenance of the files ----------------------------------------------------------------------------------------------
# attest <code> <description> <gh attestation verify arguments...>
attest() {
  local code="$1" what="$2" errfile last
  shift 2
  errfile="${work}/gh.err"
  if ! "${TOOLS_BIN}/gh" attestation verify "$@" > /dev/null 2> "${errfile}"; then
    last="$(grep -v '^[[:space:]]*$' "${errfile}" | tail -n 1 | cut -c 1-300 || true)"
    err "${code}" "gh attestation verify failed for ${what}: ${last:-no message}"
  fi
}
identity="$(identity_exact "${tag}")"
n=0
while IFS= read -r name; do
  attest provenance_invalid "${name}" "${files}/${name}" --bundle "${files}/${provenance_asset}" --repo "${repo}" \
    --cert-identity "${identity}" --source-ref "refs/tags/${tag}" --source-digest "${commit}" --deny-self-hosted-runners
  n=$((n + 1))
done < <(asset_set "${version}" listed)
for name in "myriarch-updater_${version}_linux_amd64" "myriarch-updater_${version}_linux_amd64.tar.gz"; do
  attest provenance_invalid "${name} (attestation API)" "${files}/${name}" --repo "${repo}" \
    --signer-workflow "$(signer_workflow "${tag}")" --source-ref "refs/tags/${tag}" --source-digest "${commit}" --deny-self-hosted-runners
done
info "provenance verified for ${n} files and for two files through the attestation API"

# --- 4. images ---------------------------------------------------------------------------------------------------------------
# image_check <repository> <kind: server|updater> <index> <amd64> <arm64>
image_check() {
  local repository="$1" kind="$2" index="$3" amd64="$4" arm64="$5" entrypoint tags t d pl
  case "${kind}" in server) entrypoint="/usr/local/bin/myriarchd" ;; *) entrypoint="/usr/local/bin/myriarch-updater" ;; esac
  "${TOOLS_BIN}/crane" manifest "${repository}@${index}" > "${work}/index.json" 2> "${work}/crane.err" \
    || err image_index "crane could not read ${repository}@${index}"
  python3 -I - "${work}/index.json" "${index}" "${amd64}" "${arm64}" "${label_source}" "${version}" "${commit}" <<'PY' || exit 1
import hashlib, json, sys

path, index, amd64, arm64, source, version, commit = sys.argv[1:8]


def fail(msg):
    print("[verify] ERROR [image_index] %s" % msg, file=sys.stderr)
    sys.exit(1)


raw = open(path, "rb").read()
if "sha256:" + hashlib.sha256(raw).hexdigest() != index:
    fail("the index the registry serves does not hash to %s" % index)
try:
    doc = json.loads(raw)
except ValueError:
    fail("the index is not JSON")
if doc.get("mediaType") != "application/vnd.oci.image.index.v1+json":
    fail("%s is not an OCI image index" % index)
platforms = {}
for m in doc.get("manifests") or []:
    p = m.get("platform") or {}
    key = "%s/%s" % (p.get("os"), p.get("architecture"))
    if key in platforms:
        fail("the index lists %s twice" % key)
    platforms[key] = m.get("digest")
if platforms != {"linux/amd64": amd64, "linux/arm64": arm64}:
    fail("the index has the platforms %s, expected exactly linux/amd64 and linux/arm64 with the digests of this run" % ", ".join(sorted(platforms)))
ann = doc.get("annotations") or {}
for key, want in (("org.opencontainers.image.source", source), ("org.opencontainers.image.version", version), ("org.opencontainers.image.revision", commit)):
    if ann.get(key) != want:
        fail("the index annotation %s is '%s', expected '%s'" % (key, ann.get(key), want))
PY
  for pl in "linux/amd64:${amd64}" "linux/arm64:${arm64}"; do
    d="${pl#*:}"
    "${TOOLS_BIN}/crane" manifest "${repository}@${d}" > "${work}/manifest.json" 2> "${work}/crane.err" || err image_config "crane could not read ${repository}@${d}"
    "${TOOLS_BIN}/crane" config "${repository}@${d}" > "${work}/config.json" 2> "${work}/crane.err" || err image_config "crane could not read the config of ${repository}@${d}"
    python3 -I - "${work}/manifest.json" "${work}/config.json" "${d}" "${entrypoint}" "${label_source}" "${version}" "${commit}" <<'PY' || exit 1
import hashlib, json, sys

mpath, cpath, digest, entrypoint, source, version, commit = sys.argv[1:8]


def fail(msg):
    print("[verify] ERROR [image_config] %s" % msg, file=sys.stderr)
    sys.exit(1)


raw = open(mpath, "rb").read()
if "sha256:" + hashlib.sha256(raw).hexdigest() != digest:
    fail("the manifest the registry serves does not hash to %s" % digest)
try:
    manifest = json.loads(raw)
    config = json.load(open(cpath, encoding="utf-8"))
except ValueError:
    fail("a manifest or config is not JSON")
if len(manifest.get("layers") or []) != 1:
    fail("%s has %d layers, expected exactly one" % (digest, len(manifest.get("layers") or [])))
c = config.get("config") or {}
if c.get("User") != "65532:65532":
    fail("%s runs as '%s', expected 65532:65532" % (digest, c.get("User")))
if c.get("Entrypoint") != [entrypoint]:
    fail("%s has the entrypoint %s, expected [%s]" % (digest, c.get("Entrypoint"), entrypoint))
labels = c.get("Labels") or {}
for key, want in (("org.opencontainers.image.version", version), ("org.opencontainers.image.revision", commit), ("org.opencontainers.image.source", source)):
    if labels.get(key) != want:
        fail("%s has the label %s='%s', expected '%s'" % (digest, key, labels.get(key), want))
if len((config.get("rootfs") or {}).get("diff_ids") or []) != 1:
    fail("%s has not exactly one diff id" % digest)
PY
  done
  for d in "${index}" "${amd64}" "${arm64}"; do
    verify_image_signature "${repository}" "${d}" "${tag}" "${commit}"
  done
  attest attestation_invalid "the provenance of ${repository}@${index}" "oci://${repository}@${index}" --repo "${repo}" --bundle-from-oci \
    --cert-identity "${identity}" --source-ref "refs/tags/${tag}" --source-digest "${commit}" --deny-self-hosted-runners
  for d in "${amd64}" "${arm64}"; do
    attest attestation_invalid "the SBOM of ${repository}@${d}" "oci://${repository}@${d}" --repo "${repo}" --bundle-from-oci \
      --predicate-type https://spdx.dev/Document/v2.3 \
      --cert-identity "${identity}" --source-ref "refs/tags/${tag}" --source-digest "${commit}" --deny-self-hosted-runners
  done
  tags="$("${TOOLS_BIN}/crane" ls "${repository}" 2> "${work}/crane.err")" || err image_tag_unexpected "crane could not list the tags of ${repository}"
  while IFS= read -r t; do
    [ -n "${t}" ] || continue
    if [ "${t}" = latest ]; then err image_tag_latest "${repository} has the tag latest, which must not exist during v0.x"; fi
    [[ "${t}" =~ ^sha256-[0-9a-f]{64}$ ]] && continue
    [[ "${t}" =~ ^[0-9]+\.[0-9]+(\.[0-9]+(-rc\.[1-9][0-9]*)?)?$ ]] && continue
    err image_tag_unexpected "${repository} has the tag '${t}', which is neither a version tag nor a sha256-<hex> signature tag"
  done <<EOT
${tags}
EOT
  info "${repository}: index, platforms, signatures, attestations and tags verified"
}
image_check "${RELEASE_IMAGE_SERVER}" server "${SERVER_INDEX_DIGEST}" "${SERVER_AMD64_DIGEST}" "${SERVER_ARM64_DIGEST}"
image_check "${RELEASE_IMAGE_UPDATER}" updater "${UPDATER_INDEX_DIGEST}" "${UPDATER_AMD64_DIGEST}" "${UPDATER_ARM64_DIGEST}"

# --- 5. output ---------------------------------------------------------------------------------------------------------------
mkdir -p "${out}"
while IFS= read -r name; do
  printf '%s  %s  %s\n' "$(sha256_of "${files}/${name}")" "$(wc -c < "${files}/${name}" | tr -d ' ')" "${name}"
done < <(asset_set "${version}" names) > "${out}/verified-manifest.txt"
{
  printf '%s index %s\n' "${RELEASE_IMAGE_SERVER}" "${SERVER_INDEX_DIGEST}"
  printf '%s linux/amd64 %s\n' "${RELEASE_IMAGE_SERVER}" "${SERVER_AMD64_DIGEST}"
  printf '%s linux/arm64 %s\n' "${RELEASE_IMAGE_SERVER}" "${SERVER_ARM64_DIGEST}"
  printf '%s index %s\n' "${RELEASE_IMAGE_UPDATER}" "${UPDATER_INDEX_DIGEST}"
  printf '%s linux/amd64 %s\n' "${RELEASE_IMAGE_UPDATER}" "${UPDATER_AMD64_DIGEST}"
  printf '%s linux/arm64 %s\n' "${RELEASE_IMAGE_UPDATER}" "${UPDATER_ARM64_DIGEST}"
} > "${out}/verified-images.txt"
cp "${files}/checksums.txt" "${out}/checksums.txt"
summary "## Verified release ${tag}" "" \
  "- commit \`${commit}\`" \
  "- $(wc -l < "${out}/verified-manifest.txt" | tr -d ' ') files, signature and provenance verified for the identity \`${identity}\`" \
  "- \`${RELEASE_IMAGE_SERVER}@${SERVER_INDEX_DIGEST}\`" \
  "- \`${RELEASE_IMAGE_UPDATER}@${UPDATER_INDEX_DIGEST}\`"
info "release ${tag} verified: $(wc -l < "${out}/verified-manifest.txt" | tr -d ' ') files and both images"
