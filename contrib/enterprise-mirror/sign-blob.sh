#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Signs a file with cosign (key, ECDSA P-256), WITHOUT an entry in a transparency log, and immediately re-verifies the
# signature with openssl (format contract).
#
#   contrib/enterprise-mirror/sign-blob.sh <file> <signature-output>
#
# Result: <signature-output> contains the Base64 encoding of an ASN.1 DER ECDSA signature over the SHA-256 of the file:
#   openssl dgst -sha256 -verify cosign.pub -signature <(openssl base64 -d -A -in <file>.sig) <file>
#
# Environment:
#   COSIGN_KEY          path of the key file (cosign.key), KMS URI (awskms://, gcpkms://, azurekms://,
#                       hashivault://) or env://NAME. Alias: COSIGN_PRIVATE_KEY (same meaning).
#   COSIGN_PASSWORD     password of the key file (not needed with a KMS)
#   COSIGN_REPO_PUBLIC_KEY  location of the committed key (default: deploy/release/cosign.pub; for tests)
#   COSIGN_PUBLIC_KEY   public key for the re-verification (default: deploy/release/cosign.pub, otherwise derived from
#                       the signing key). If deploy/release/cosign.pub exists, the signing key MUST match it
#                       (protection against a wrong or rotated key).
#   COSIGN_SIGN_MODE   auto (default) | legacy | bundle
#                       legacy: --output-signature (cosign v2/v3, marked deprecated in v3 but functional)
#                       bundle: empty signing config + --bundle, the signature is read from the bundle
#                       auto:   legacy, bundle on failure (in case a future cosign version removes the flags)
#
# Stable error tokens (in square brackets in the message): [sign_failed], [key_mismatch], [signature_verify_failed].
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=ci/lib.sh
. "${HERE}/../../ci/lib.sh"

[ "$#" -eq 2 ] || die "usage: contrib/enterprise-mirror/sign-blob.sh <file> <signature-output>"
artifact="$1"; sigfile="$2"
[ -f "${artifact}" ] || die "file not found: ${artifact}"
key="${COSIGN_KEY:-${COSIGN_PRIVATE_KEY:-}}"
[ -n "${key}" ] || die "COSIGN_KEY (or COSIGN_PRIVATE_KEY) is not set"
need_cmd cosign openssl
mode="${COSIGN_SIGN_MODE:-auto}"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

sign_legacy() {
  cosign sign-blob --yes --key "${key}" --use-signing-config=false --new-bundle-format=false --tlog-upload=false \
    --output-signature "${sigfile}" "${artifact}" >"${work}/legacy.log" 2>&1
}

sign_bundle() {
  cosign signing-config create --out "${work}/signing-config.json" >"${work}/bundle.log" 2>&1 || return 1
  cosign sign-blob --yes --key "${key}" --signing-config "${work}/signing-config.json" --bundle "${work}/bundle.json" \
    "${artifact}" >>"${work}/bundle.log" 2>&1 || return 1
  # messageSignature.signature in the bundle is the same Base64 DER signature ("signature" occurs only there)
  tr -d '\n\t ' < "${work}/bundle.json" | sed -n 's/.*"signature":"\([^"]*\)".*/\1/p' | head -n 1 > "${sigfile}"
  [ -s "${sigfile}" ]
}

rm -f "${sigfile}"
case "${mode}" in
  legacy) sign_legacy || { cat "${work}/legacy.log" >&2; die "[sign_failed] cosign sign-blob failed" ; } ;;
  bundle) sign_bundle || { cat "${work}/bundle.log" >&2; die "[sign_failed] cosign sign-blob (bundle) failed" ; } ;;
  auto)
    if ! sign_legacy; then
      warn "cosign sign-blob with --output-signature failed, trying bundle mode"
      rm -f "${sigfile}"
      if ! sign_bundle; then
        echo "--- legacy:" >&2; cat "${work}/legacy.log" >&2
        echo "--- bundle:" >&2; cat "${work}/bundle.log" >&2
        die "[sign_failed] cosign sign-blob failed (check the key, COSIGN_PASSWORD, KMS access)"
      fi
    fi ;;
  *) die "COSIGN_SIGN_MODE: auto, legacy or bundle" ;;
esac

# --- re-verification with openssl (exactly what installers and operators do) ------------------------------
repo_pub="${COSIGN_REPO_PUBLIC_KEY:-${ROOT}/deploy/release/cosign.pub}"
derived="${work}/derived.pub"
cosign public-key --key "${key}" --outfile "${derived}" >/dev/null 2>&1 || die "the public key cannot be derived from COSIGN_KEY (COSIGN_PASSWORD?)"
if [ -f "${repo_pub}" ] && ! cmp -s <(tr -d '\r' < "${repo_pub}") <(tr -d '\r' < "${derived}"); then
  rm -f "${sigfile}"
  die "[key_mismatch] COSIGN_KEY does not match deploy/release/cosign.pub: wrong or rotated key? (rotation: deploy/release/README.md)"
fi
pub="${COSIGN_PUBLIC_KEY:-${derived}}"
openssl base64 -d -A -in "${sigfile}" -out "${work}/sig.der" || die "the signature is not Base64"
if ! openssl dgst -sha256 -verify "${pub}" -signature "${work}/sig.der" "${artifact}" >/dev/null 2>&1; then
  rm -f "${sigfile}"
  die "[signature_verify_failed] re-verification with openssl failed: ${sigfile} does not follow the format contract"
fi
log "signed: $(basename "${artifact}") -> $(basename "${sigfile}")"
