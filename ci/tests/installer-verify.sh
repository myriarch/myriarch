#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail" is intentional in these tests (ok/fail are counters and never fail)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests the signature verification of our binaries in deploy/scripts/install-linux.sh (and, when pwsh is available,
# install-windows.ps1) in staging mode (--root) against a fake release with file:// URLs. Needs only bash, openssl,
# tar, awk; with cosign in PATH it additionally covers a real cosign signature (format contract) and the cosign fallback.
# Failures are asserted on the stable error codes of the installer ("[<code>]"), never on message prose.
#
#   ci/tests/installer-verify.sh          exit 0 = all cases as expected
set -uo pipefail
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"

INSTALLER="${ROOT}/deploy/scripts/install-linux.sh"
TEMPLATES="${ROOT}/deploy"
need_cmd openssl tar awk

W="${TEST_TMP}"
REL="${W}/release"            # our myriarch release (updater, checksums.txt, .sig)
MIRROR="${W}/mirror"          # collector mirror in the layout <base>/<tag>/<asset>
mkdir -p "${REL}" "${MIRROR}/v0.161.0" "${MIRROR}/cmd/opampsupervisor/v0.161.0"

# ---- Key pair (test only) ----------------------------------------------------------------------
openssl ecparam -name prime256v1 -genkey -noout -out "${W}/key.pem" 2>/dev/null
openssl ec -in "${W}/key.pem" -pubout -out "${W}/cosign.pub" 2>/dev/null
openssl ecparam -name prime256v1 -genkey -noout -out "${W}/other-key.pem" 2>/dev/null
openssl ec -in "${W}/other-key.pem" -pubout -out "${W}/other.pub" 2>/dev/null

sign_blob() { # file key -> file.sig (base64 of the ASN.1 DER ECDSA signature over SHA-256)
  openssl dgst -sha256 -sign "$2" -out "$1.der" "$1" && openssl base64 -A -in "$1.der" -out "$1.sig" && rm -f "$1.der"
}

# ---- Fake collector mirror (like internal/updater/deploy_test.go) --------------------------------------
stage="${W}/stage"; mkdir -p "${stage}"
printf '#!/bin/sh\necho "otelcol-contrib version 0.161.0"\n' > "${stage}/otelcol-contrib"; chmod 0755 "${stage}/otelcol-contrib"
tar -czf "${MIRROR}/v0.161.0/otelcol-contrib_0.161.0_linux_amd64.tar.gz" -C "${stage}" otelcol-contrib
printf '#!/bin/sh\necho supervisor\n' > "${MIRROR}/cmd/opampsupervisor/v0.161.0/opampsupervisor_0.161.0_linux_amd64"
for f in "${MIRROR}/v0.161.0/otelcol-contrib_0.161.0_linux_amd64.tar.gz" "${MIRROR}/cmd/opampsupervisor/v0.161.0/opampsupervisor_0.161.0_linux_amd64"; do
  sha256_of "${f}" | tr -d '\n' > "${f}.sha256"
done

# ---- Fake release: updater + checksums.txt in goreleaser format ----------------------------------------
UPDATER_NAME="myriarch-updater_9.9.9_linux_amd64"
printf '#!/bin/sh\necho fake-updater\n' > "${REL}/${UPDATER_NAME}"; chmod 0755 "${REL}/${UPDATER_NAME}"
printf 'fake windows binary\n' > "${REL}/myriarch-updater_9.9.9_windows_amd64.exe"
printf 'fake archive\n' > "${REL}/myriarch-updater_9.9.9_linux_amd64.tar.gz"
UPDATER_SHA="$(sha256_of "${REL}/${UPDATER_NAME}")"
make_checksums() { # target-dir
  (cd "$1" && for f in myriarch-updater_9.9.9_linux_amd64 myriarch-updater_9.9.9_linux_amd64.tar.gz myriarch-updater_9.9.9_windows_amd64.exe; do
    printf '%s  %s\n' "$(sha256_of "${f}")" "${f}"; done > checksums.txt)
}
make_checksums "${REL}"
sign_blob "${REL}/checksums.txt" "${W}/key.pem"

base_args=(--opamp-endpoint wss://fleet.example.com/v1/opamp --token myre_abcDEF123 --site-type office --site-id OFFICE1
  --collector-version 0.161.0 --with-updater)
run_install() { # root extra-args...   (lab mode for mirror/file://, collector Sigstore skipped)
  local root="$1"; shift
  bash "${INSTALLER}" --root "${root}" --arch amd64 --templates-dir "${TEMPLATES}" --insecure --verify-signatures skip \
    --release-base-url "file://${MIRROR}" "${base_args[@]}" --updater-url "file://${REL}/${UPDATER_NAME}" "$@"
}
fresh() { mktemp -d "${W}/root.XXXXXX"; }
signed_args() { printf '%s\n' --updater-checksums-url "file://${REL}/checksums.txt" --signing-key "${W}/cosign.pub"; }

echo "install-linux.sh: signature verification of checksums.txt (openssl)"
t_valid() { local r; r="$(fresh)"; run_install "${r}" --updater-checksums-url "file://${REL}/checksums.txt" --signing-key "${W}/cosign.pub" \
  && cmp -s "${REL}/${UPDATER_NAME}" "${r}/opt/myriarch/updater/myriarch-updater"; }
expect_ok "valid signature: updater is installed" t_valid

t_valid_and_sha() { run_install "$(fresh)" --updater-checksums-url "file://${REL}/checksums.txt" --signing-key "${W}/cosign.pub" --updater-sha256 "${UPDATER_SHA}"; }
expect_ok "signature AND matching --updater-sha256 are allowed" t_valid_and_sha

t_sig_url() { cp "${REL}/checksums.txt.sig" "${W}/elsewhere.sig"; run_install "$(fresh)" --updater-checksums-url "file://${REL}/checksums.txt" \
  --updater-checksums-sig-url "file://${W}/elsewhere.sig" --signing-key "${W}/cosign.pub"; }
expect_ok "--updater-checksums-sig-url (signature at a different location)" t_sig_url

t_sha_only() { run_install "$(fresh)" --updater-sha256 "${UPDATER_SHA}"; }
expect_ok "--updater-sha256 only (previous behavior)" t_sha_only

# Negative cases: never install anything
no_install() { [ ! -e "$1/etc/myriarch/supervisor.yaml" ] && [ ! -e "$1/opt/myriarch/updater/myriarch-updater" ]; }

TAMPER_ROOT="$(fresh)"
t_tampered() { local d; d="${W}/tampered"; mkdir -p "${d}"; cp "${REL}"/* "${d}/"
  sed 's/^./0/' "${REL}/checksums.txt" > "${d}/checksums.txt"   # first character of every line changed
  run_install "${TAMPER_ROOT}" --updater-checksums-url "file://${d}/checksums.txt" --signing-key "${W}/cosign.pub"; }
expect_fail "tampered checksums.txt is rejected" '\[signature_invalid\]' t_tampered
if no_install "${TAMPER_ROOT}"; then ok "  ... and nothing was installed"; else fail "something was installed after a tampered checksums.txt"; fi

t_wrong_key() { run_install "$(fresh)" --updater-checksums-url "file://${REL}/checksums.txt" --signing-key "${W}/other.pub"; }
expect_fail "wrong key is rejected" '\[signature_invalid\]' t_wrong_key

t_missing_line() { local d="${W}/missing"; mkdir -p "${d}"; grep -v "  ${UPDATER_NAME}\$" "${REL}/checksums.txt" > "${d}/checksums.txt"
  sign_blob "${d}/checksums.txt" "${W}/key.pem"; run_install "$(fresh)" --updater-checksums-url "file://${d}/checksums.txt" --signing-key "${W}/cosign.pub"; }
expect_fail "validly signed checksums.txt without a line for the updater" '\[checksum_entry_count\].*\b0\b' t_missing_line

t_dup_line() { local d="${W}/dup"; mkdir -p "${d}"; { cat "${REL}/checksums.txt"; grep "  ${UPDATER_NAME}\$" "${REL}/checksums.txt"; } > "${d}/checksums.txt"
  sign_blob "${d}/checksums.txt" "${W}/key.pem"; run_install "$(fresh)" --updater-checksums-url "file://${d}/checksums.txt" --signing-key "${W}/cosign.pub"; }
expect_fail "duplicate line for the updater" '\[checksum_entry_count\].*\b2\b' t_dup_line

t_conflict() { run_install "$(fresh)" --updater-checksums-url "file://${REL}/checksums.txt" --signing-key "${W}/cosign.pub" --updater-sha256 "$(printf '0%.0s' $(seq 64))"; }
expect_fail "--updater-sha256 contradicts checksums.txt (hash conflict)" '\[checksum_conflict\]' t_conflict

t_binary_tampered() { local d="${W}/bin-tampered"; mkdir -p "${d}"; cp "${REL}"/* "${d}/"; printf 'evil\n' >> "${d}/${UPDATER_NAME}"
  bash "${INSTALLER}" --root "$(fresh)" --arch amd64 --templates-dir "${TEMPLATES}" --insecure --verify-signatures skip --release-base-url "file://${MIRROR}" \
    "${base_args[@]}" --updater-url "file://${d}/${UPDATER_NAME}" --updater-checksums-url "file://${d}/checksums.txt" --signing-key "${W}/cosign.pub"; }
expect_fail "updater binary does not match the signed checksum" '\[checksum_mismatch\]' t_binary_tampered

t_no_sig() { local d="${W}/nosig"; mkdir -p "${d}"; cp "${REL}/checksums.txt" "${d}/"; run_install "$(fresh)" --updater-checksums-url "file://${d}/checksums.txt" --signing-key "${W}/cosign.pub"; }
expect_fail "missing signature file" '\[download_failed\]' t_no_sig

t_bad_b64() { local d="${W}/badb64"; mkdir -p "${d}"; cp "${REL}/checksums.txt" "${d}/"; printf '!!!not-base64!!!\n' > "${d}/checksums.txt.sig"
  run_install "$(fresh)" --updater-checksums-url "file://${d}/checksums.txt" --signing-key "${W}/cosign.pub"; }
expect_fail "signature is not base64" '\[signature_malformed\]|\[signature_invalid\]' t_bad_b64

t_no_key() { run_install "$(fresh)" --updater-checksums-url "file://${REL}/checksums.txt"; }
expect_fail "--updater-checksums-url without --signing-key" '\[option_conflict\].*--signing-key' t_no_key

t_key_only() { run_install "$(fresh)" --signing-key "${W}/cosign.pub" --updater-sha256 "${UPDATER_SHA}"; }
expect_fail "--signing-key without --updater-checksums-url" '\[option_conflict\].*--updater-checksums-url' t_key_only

t_sig_url_only() { run_install "$(fresh)" --updater-checksums-sig-url "file://${REL}/checksums.txt.sig" --updater-sha256 "${UPDATER_SHA}"; }
expect_fail "--updater-checksums-sig-url without --updater-checksums-url" '\[option_conflict\].*--updater-checksums-url' t_sig_url_only

t_key_unreadable() { run_install "$(fresh)" --updater-checksums-url "file://${REL}/checksums.txt" --signing-key "${W}/does-not-exist.pub"; }
expect_fail "signing key file is not readable" '\[file_unreadable\]' t_key_unreadable

t_without_updater() { bash "${INSTALLER}" --root "$(fresh)" --arch amd64 --templates-dir "${TEMPLATES}" --insecure --verify-signatures skip \
  --release-base-url "file://${MIRROR}" --opamp-endpoint wss://x/v1/opamp --token myre_x --site-type office --site-id 1 --collector-version 0.161.0 \
  --signing-key "${W}/cosign.pub"; }
expect_fail "--signing-key without --with-updater" '\[option_conflict\].*--with-updater' t_without_updater

# Mandatory without --insecure: neither hash nor checksums URL => abort (the https requirement for the updater URL comes first, hence an https URL)
t_mandatory() { bash "${INSTALLER}" --root "$(fresh)" --arch amd64 --templates-dir "${TEMPLATES}" --verify-signatures require \
  --release-base-url "https://mirror.example.com/r" --opamp-endpoint wss://x/v1/opamp --token myre_x --site-type office --site-id 1 \
  --collector-version 0.161.0 --with-updater --updater-url "https://mirror.example.com/${UPDATER_NAME}"; }
expect_fail "a hash source is mandatory without --insecure" '\[updater_hash_source_missing\]' t_mandatory

t_http_checksums() { bash "${INSTALLER}" --root "$(fresh)" --arch amd64 --templates-dir "${TEMPLATES}" \
  --release-base-url "https://mirror.example.com/r" --opamp-endpoint wss://x/v1/opamp --token myre_x --site-type office --site-id 1 \
  --collector-version 0.161.0 --with-updater --updater-url "https://mirror.example.com/${UPDATER_NAME}" \
  --updater-checksums-url "http://mirror.example.com/checksums.txt" --signing-key "${W}/cosign.pub"; }
expect_fail "checksums.txt only over https (without --insecure)" '\[https_required\]' t_http_checksums

# ---- Identity binding, transport and ownership ---------------------------------------------------------
echo "install-linux.sh: certificate identity bound to the requested version, https-only transport, ownership"
mkdir -p "${MIRROR}/cmd/opampsupervisor/v0.160.0"
printf '#!/bin/sh\necho supervisor\n' > "${MIRROR}/cmd/opampsupervisor/v0.160.0/opampsupervisor_0.160.0_linux_amd64"
sha256_of "${MIRROR}/cmd/opampsupervisor/v0.160.0/opampsupervisor_0.160.0_linux_amd64" | tr -d '\n' > "${MIRROR}/cmd/opampsupervisor/v0.160.0/opampsupervisor_0.160.0_linux_amd64.sha256"
for b in "${MIRROR}/v0.161.0/otelcol-contrib_0.161.0_linux_amd64.tar.gz" "${MIRROR}/cmd/opampsupervisor/v0.160.0/opampsupervisor_0.160.0_linux_amd64"; do
  printf '{}\n' > "${b}.sigstore.json"
done
plain_args=(--arch amd64 --templates-dir "${TEMPLATES}" --insecure --release-base-url "file://${MIRROR}"
  --opamp-endpoint wss://fleet.example.com/v1/opamp --token myre_abcDEF123 --site-type office --site-id OFFICE1 --collector-version 0.161.0)

STUBS="${W}/stubs"; mkdir -p "${STUBS}"
cat > "${STUBS}/cosign" <<'STUB'
#!/bin/sh
printf '%s\n' "$@" >> "${COSIGN_LOG}"
exit 0
STUB
chmod 0755 "${STUBS}/cosign"
t_identity_binding() {
  local log="${W}/cosign.log" r; r="$(fresh)"; : > "${log}"
  PATH="${STUBS}:${PATH}" COSIGN_LOG="${log}" bash "${INSTALLER}" --root "${r}" "${plain_args[@]}" --supervisor-version 0.160.0 --verify-signatures require || return 1
  local n; n="$(grep -c -- '^--certificate-identity-regexp$' "${log}")"
  [ "${n}" = 2 ] || { echo "cosign was called ${n} times, want 2"; return 1; }
  # the value after every --certificate-identity-regexp ends with the tag of the requested version
  awk '/^--certificate-identity-regexp$/ {getline; print}' "${log}" > "${W}/identities.txt"
  grep -Fq -- '/base-release\.yaml@refs/tags/v0\.161\.0$' "${W}/identities.txt" || { echo "collector identity not bound to the version"; cat "${W}/identities.txt"; return 1; }
  grep -Fq -- '/base-binary-release\.yaml@refs/tags/v0\.160\.0$' "${W}/identities.txt" || { echo "supervisor identity not bound to the version"; cat "${W}/identities.txt"; return 1; }
  ! grep -Fq -- 'v[0-9]+' "${W}/identities.txt" || { echo "a generic tag pattern was passed"; return 1; }
}
expect_ok "cosign receives identities bound to the collector and supervisor versions" t_identity_binding

# the identity pattern matches exactly the tag of the version (and not 0.1610 or 10.161.0)
t_identity_regex() {
  local id; id="$(bash -c 'eval "$(sed -n "/^cert_identity()/,/^}/p" "$1")"; cert_identity base-release 0.161.0' _ "${INSTALLER}")"
  printf 'https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/base-release.yaml@refs/tags/v0.161.0\n' | grep -Eq -- "${id}" || return 1
  ! printf 'https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/base-release.yaml@refs/tags/v0.1610\n' | grep -Eq -- "${id}" || return 1
  ! printf 'https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/base-release.yaml@refs/tags/v10.161.0\n' | grep -Eq -- "${id}"
}
expect_ok "the identity pattern matches only the requested tag" t_identity_regex

# wget cannot restrict redirects to https: without --insecure curl is required
NOCURL="${W}/bin-no-curl"; mkdir -p "${NOCURL}"
for c in bash sh tar install cmp awk sed tr cut grep mktemp date id uname sha256sum shasum mv cp ln rm mkdir dirname basename cat head readlink chmod sort wc sleep seq env timeout openssl; do
  p="$(command -v "${c}" 2>/dev/null || true)"; [ -n "${p}" ] && ln -sf "${p}" "${NOCURL}/${c}"
done
cat > "${NOCURL}/wget" <<'STUB'
#!/bin/sh
printf '%s\n' "$@" >> "${WGET_LOG}"
exit 1
STUB
chmod 0755 "${NOCURL}/wget"
t_wget_refused() { PATH="${NOCURL}" bash "${INSTALLER}" --root "$(fresh)" --arch amd64 --templates-dir "${TEMPLATES}" --verify-signatures require \
  --release-base-url "https://mirror.example.com/r" --opamp-endpoint wss://x/v1/opamp --token myre_x --site-type office --site-id 1 --collector-version 0.161.0; }
expect_fail "wget without curl is refused without --insecure" '\[tool_missing\]' t_wget_refused
t_wget_lab() { : > "${W}/wget.log"; WGET_LOG="${W}/wget.log" PATH="${NOCURL}" bash "${INSTALLER}" --root "$(fresh)" "${plain_args[@]}" --verify-signatures skip; }
expect_fail "with --insecure wget is used for labs" '\[download_failed\]' t_wget_lab
grep -Fxq -- '--secure-protocol=TLSv1_2' "${W}/wget.log" && ok "  ... with a minimum TLS version" || fail "wget was not called with --secure-protocol=TLSv1_2"

# extraction: owner and mode never come from the archive
TARSTUB="${W}/tarstub"; mkdir -p "${TARSTUB}"
cat > "${TARSTUB}/tar" <<STUB
#!/bin/sh
printf '%s\n' "\$@" >> "\${TAR_LOG}"
exec "$(command -v tar)" "\$@"
STUB
chmod 0755 "${TARSTUB}/tar"
t_tar_flags() { : > "${W}/tar.log"; PATH="${TARSTUB}:${PATH}" TAR_LOG="${W}/tar.log" bash "${INSTALLER}" --root "$(fresh)" "${plain_args[@]}" --verify-signatures skip \
  && grep -Fxq -- '--no-same-owner' "${W}/tar.log" && grep -Fxq -- '--no-same-permissions' "${W}/tar.log"; }
expect_ok "tar is called with --no-same-owner and --no-same-permissions" t_tar_flags

if [ "$(id -u)" = 0 ] && tar --help 2>&1 | grep -q -- '--owner'; then
  ROOT_STAGE="${W}/owner-stage"; mkdir -p "${ROOT_STAGE}"
  cp "${stage}/otelcol-contrib" "${ROOT_STAGE}/"
  tar -czf "${W}/owner.tar.gz" -C "${ROOT_STAGE}" --owner=4242 --group=4242 otelcol-contrib
  OWN_MIRROR="${W}/owner-mirror"; mkdir -p "${OWN_MIRROR}/v0.161.0" "${OWN_MIRROR}/cmd/opampsupervisor/v0.161.0"
  cp "${W}/owner.tar.gz" "${OWN_MIRROR}/v0.161.0/otelcol-contrib_0.161.0_linux_amd64.tar.gz"
  cp "${MIRROR}/cmd/opampsupervisor/v0.161.0/"* "${OWN_MIRROR}/cmd/opampsupervisor/v0.161.0/"
  sha256_of "${OWN_MIRROR}/v0.161.0/otelcol-contrib_0.161.0_linux_amd64.tar.gz" | tr -d '\n' > "${OWN_MIRROR}/v0.161.0/otelcol-contrib_0.161.0_linux_amd64.tar.gz.sha256"
  t_owner() { local r; r="$(fresh)"; bash "${INSTALLER}" --root "${r}" --arch amd64 --templates-dir "${TEMPLATES}" --insecure --release-base-url "file://${OWN_MIRROR}" \
    --opamp-endpoint wss://x/v1/opamp --token myre_x --site-type office --site-id 1 --collector-version 0.161.0 --verify-signatures skip \
    && [ "$(stat -c '%u:%g' "${r}/opt/myriarch/collector/versions/0.161.0/otelcol-contrib")" = "0:0" ]; }
  expect_ok "an archive with a foreign owner yields a binary owned by root" t_owner
else
  echo "(not running as root with GNU tar: the ownership case is skipped)"
fi

# the CA bundle is copied into the protected configuration directory and referenced there
printf -- '-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n' > "${W}/ca.pem"
t_ca_copy() { local r; r="$(fresh)"; bash "${INSTALLER}" --root "${r}" "${plain_args[@]}" --verify-signatures skip --ca-file "${W}/ca.pem" --with-updater \
  --updater-url "file://${REL}/${UPDATER_NAME}" --updater-sha256 "${UPDATER_SHA}" || return 1
  grep -Fq '/etc/myriarch/ca.pem' "${r}/etc/myriarch/supervisor.yaml" && grep -Fq '/etc/myriarch/ca.pem' "${r}/etc/myriarch/updater.yaml" \
    && ! grep -Fq "${W}" "${r}/etc/myriarch/supervisor.yaml" "${r}/etc/myriarch/updater.yaml" \
    && cmp -s "${W}/ca.pem" "${r}/etc/myriarch/ca.pem" && [ "$(file_mode "${r}/etc/myriarch/ca.pem")" = 644 ]; }
file_mode() { stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1"; }
expect_ok "--ca-file is copied to /etc/myriarch/ca.pem (0644) and referenced there" t_ca_copy
t_ca_relative() { (cd "${W}" && bash "${INSTALLER}" --root "$(fresh)" "${plain_args[@]}" --verify-signatures skip --ca-file ca.pem); }
expect_fail "a relative --ca-file is refused" '\[option_invalid\]' t_ca_relative

# ---- cosign fallback (no openssl in PATH) + real cosign signature (format contract) -------------------
if have cosign; then
  echo "install-linux.sh: real cosign signature (contrib/enterprise-mirror/sign-blob.sh) and cosign fallback"
  mkdir -p "${W}/cosign-keys"
  ( cd "${W}/cosign-keys" && COSIGN_PASSWORD=testpw cosign generate-key-pair >/dev/null 2>&1 )
  cp "${REL}/checksums.txt" "${W}/cosign-checksums.txt"
  COSIGN_PASSWORD=testpw COSIGN_KEY="${W}/cosign-keys/cosign.key" COSIGN_REPO_PUBLIC_KEY=/nonexistent \
    "${ROOT}/contrib/enterprise-mirror/sign-blob.sh" "${W}/cosign-checksums.txt" "${W}/cosign-checksums.txt.sig" >/dev/null 2>&1
  C="${W}/cosign-rel"; mkdir -p "${C}"; cp "${REL}/${UPDATER_NAME}" "${C}/"; cp "${W}/cosign-checksums.txt" "${C}/checksums.txt"; cp "${W}/cosign-checksums.txt.sig" "${C}/checksums.txt.sig"
  t_cosign_sig() { run_install "$(fresh)" --updater-checksums-url "file://${C}/checksums.txt" --signing-key "${W}/cosign-keys/cosign.pub"; }
  expect_ok "a .sig produced by cosign is accepted by the installer (openssl)" t_cosign_sig

  # PATH without openssl: the installer falls back to cosign verify-blob
  nossl="${W}/bin-no-openssl"; mkdir -p "${nossl}"
  for c in bash sh curl wget tar install cmp awk sed tr cut grep mktemp date id uname sha256sum shasum mv cp ln rm mkdir dirname basename cat head readlink chmod sort wc sleep seq env cosign; do
    p="$(command -v "${c}" 2>/dev/null || true)"; [ -n "${p}" ] && ln -sf "${p}" "${nossl}/${c}"
  done
  t_cosign_fallback() { PATH="${nossl}" run_install "$(fresh)" --updater-checksums-url "file://${C}/checksums.txt" --signing-key "${W}/cosign-keys/cosign.pub"; }
  expect_ok "without openssl the installer verifies with cosign verify-blob" t_cosign_fallback
  t_cosign_fallback_bad() { PATH="${nossl}" run_install "$(fresh)" --updater-checksums-url "file://${REL}/checksums.txt" --signing-key "${W}/cosign-keys/cosign.pub"; }
  expect_fail "cosign fallback rejects a foreign signature" '\[signature_invalid\]' t_cosign_fallback_bad

  # without openssl AND without cosign: abort
  rm -f "${nossl}/cosign"
  t_no_verifier() { PATH="${nossl}" run_install "$(fresh)" --updater-checksums-url "file://${REL}/checksums.txt" --signing-key "${W}/cosign.pub"; }
  expect_fail "neither openssl nor cosign: abort" '\[signature_verifier_missing\]' t_no_verifier
else
  echo "(cosign not in PATH: real cosign signature and fallback skipped; ci/install-tools.sh cosign)"
fi

# ---- Windows (PowerShell) ---------------------------------------------------------------------------
if have pwsh && [ -f "${CI_DIR}/tests/installer-verify.ps1" ]; then
  echo "install-windows.ps1: signature verification (pwsh)"
  expect_ok "install-windows.ps1 (pwsh)" pwsh -NoProfile -NonInteractive -File "${CI_DIR}/tests/installer-verify.ps1" \
    -Installer "${ROOT}/deploy/scripts/install-windows.ps1" -Templates "${TEMPLATES}" -Work "${W}/win" -SignBlob "${ROOT}/contrib/enterprise-mirror/sign-blob.sh"
else
  echo "(pwsh not in PATH: install-windows.ps1 skipped)"
fi

finish_tests "installer-verify"
