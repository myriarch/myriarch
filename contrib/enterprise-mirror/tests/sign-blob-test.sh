#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail" is intended in the tests (ok/fail are counters and never fail)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Format contract of the signatures: contrib/enterprise-mirror/sign-blob.sh (cosign sign-blob with a key, without a transparency log) creates a
# .sig that `openssl dgst -sha256 -verify cosign.pub -signature <(openssl base64 -d -A -in x.sig) x` confirms.
# Needs cosign (pinned version, ci/install-tools.sh cosign) and openssl. Errors are matched by their stable tokens.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIRROR_DIR="$(cd "${HERE}/.." && pwd)"
# shellcheck source=ci/tests/lib-test.sh
. "${HERE}/../../../ci/tests/lib-test.sh"
need_cmd cosign openssl
W="${TEST_TMP}"
export COSIGN_PASSWORD=testpw
mkdir -p "${W}/k1" "${W}/k2"
( cd "${W}/k1" && cosign generate-key-pair >/dev/null 2>&1 )
( cd "${W}/k2" && COSIGN_PASSWORD=pw2 cosign generate-key-pair >/dev/null 2>&1 )
printf 'content of the file\n' > "${W}/x.txt"
log "cosign: $(cosign version 2>/dev/null | sed -n 's/^GitVersion: *//p')"

verify_openssl() { # file sig pub
  openssl dgst -sha256 -verify "$3" -signature <(openssl base64 -d -A -in "$2") "$1"
}

for mode in legacy bundle auto; do
  t_sign() { COSIGN_SIGN_MODE="${mode}" COSIGN_KEY="${W}/k1/cosign.key" COSIGN_REPO_PUBLIC_KEY=/nonexistent "${MIRROR_DIR}/sign-blob.sh" "${W}/x.txt" "${W}/x.${mode}.sig"; }
  expect_ok "mode ${mode}: signature created" t_sign
  if OUT="$(verify_openssl "${W}/x.txt" "${W}/x.${mode}.sig" "${W}/k1/cosign.pub" 2>&1)" && printf '%s' "${OUT}" | grep -q 'Verified OK'; then ok "mode ${mode}: openssl confirms 'Verified OK'"; else fail "mode ${mode}: openssl verification: ${OUT}"; fi
  if verify_openssl "${W}/x.txt" "${W}/x.${mode}.sig" "${W}/k2/cosign.pub" >/dev/null 2>&1; then fail "mode ${mode}: foreign key accepted"; else ok "mode ${mode}: foreign key is rejected"; fi
done

printf 'other file\n' > "${W}/y.txt"
if verify_openssl "${W}/y.txt" "${W}/x.auto.sig" "${W}/k1/cosign.pub" >/dev/null 2>&1; then fail "the signature is valid for another file"; else ok "the signature is bound to the file content"; fi
if cosign verify-blob --key "${W}/k1/cosign.pub" --signature "${W}/x.auto.sig" --insecure-ignore-tlog=true "${W}/x.txt" >/dev/null 2>&1; then ok "cosign verify-blob confirms the same .sig"; else fail "cosign verify-blob failed"; fi
[ "$(wc -l < "${W}/x.auto.sig" | tr -d ' ')" -le 1 ] && ok ".sig is a single Base64 line" || fail ".sig has several lines"

t_mismatch() { COSIGN_KEY="${W}/k2/cosign.key" COSIGN_PASSWORD=pw2 COSIGN_REPO_PUBLIC_KEY="${W}/k1/cosign.pub" "${MIRROR_DIR}/sign-blob.sh" "${W}/x.txt" "${W}/z.sig"; }
expect_fail "key does not match the committed cosign.pub" "key_mismatch" t_mismatch
[ ! -e "${W}/z.sig" ] && ok "  ... and no signature was written" || fail "signature written despite the key conflict"
t_match() { COSIGN_KEY="${W}/k1/cosign.key" COSIGN_REPO_PUBLIC_KEY="${W}/k1/cosign.pub" "${MIRROR_DIR}/sign-blob.sh" "${W}/x.txt" "${W}/m.sig"; }
expect_ok "key matches the committed cosign.pub" t_match
t_pw() { COSIGN_PASSWORD=wrong COSIGN_KEY="${W}/k1/cosign.key" COSIGN_REPO_PUBLIC_KEY=/nonexistent "${MIRROR_DIR}/sign-blob.sh" "${W}/x.txt" "${W}/p.sig"; }
expect_fail "wrong password" "sign_failed" t_pw
t_nokey() { env -u COSIGN_KEY -u COSIGN_PRIVATE_KEY "${MIRROR_DIR}/sign-blob.sh" "${W}/x.txt" "${W}/q.sig"; }
expect_fail "without a key" "COSIGN_KEY" t_nokey

# Transparency log: no upload (signing works without any network access)
t_offline() { HTTPS_PROXY=http://127.0.0.1:9 HTTP_PROXY=http://127.0.0.1:9 COSIGN_SIGN_MODE=legacy COSIGN_KEY="${W}/k1/cosign.key" COSIGN_REPO_PUBLIC_KEY=/nonexistent "${MIRROR_DIR}/sign-blob.sh" "${W}/x.txt" "${W}/o.sig"; }
expect_ok "signing without network access (no transparency log upload, no TUF)" t_offline

finish_tests "sign-blob-test"
