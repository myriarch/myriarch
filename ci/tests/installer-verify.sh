#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail" is intentional in these tests (ok/fail are counters and never fail)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests the verification of the keyless release bundle (updater) and of the upstream bundles (collector, supervisor) in
# deploy/scripts/install-linux.sh and, when pwsh is available, install-windows.ps1, in staging mode (--root) against a
# fake release with file:// URLs. Needs only bash, tar, gzip, awk. The verifier is a stub that emulates what matters
# (identity regexp against the certificate identity of the bundle, digest of the signed file, version of cosign); with
# cosign in PATH the real cosign is additionally run offline against the committed upstream fixtures and the keyless
# bundle of checksums.txt of v0.1.0-rc.3 (internal/updater/testdata/sigstore/myriarch_0.1.0-rc.3_*); these cases are
# required when CI=true. Failures are asserted on the stable error codes of the installer ("[<code>]"), never on message
# prose.
#
#   ci/tests/installer-verify.sh          exit 0 = all cases as expected
set -uo pipefail
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"

INSTALLER="${ROOT}/deploy/scripts/install-linux.sh"
TEMPLATES="${ROOT}/deploy"
need_cmd tar gzip awk

W="${TEST_TMP}"
REL="${W}/release"            # our myriarch release (updater, checksums.txt, .sig)
MIRROR="${W}/mirror"          # collector mirror in the layout <base>/<tag>/<asset>
mkdir -p "${REL}" "${MIRROR}/v0.161.0" "${MIRROR}/cmd/opampsupervisor/v0.161.0"

# ---- Fake collector mirror (like internal/updater/deploy_test.go) --------------------------------------
stage="${W}/stage"; mkdir -p "${stage}"
printf '#!/bin/sh\necho "otelcol-contrib version 0.161.0"\n' > "${stage}/otelcol-contrib"; chmod 0755 "${stage}/otelcol-contrib"
tar -czf "${MIRROR}/v0.161.0/otelcol-contrib_0.161.0_linux_amd64.tar.gz" -C "${stage}" otelcol-contrib
printf '#!/bin/sh\necho supervisor\n' > "${MIRROR}/cmd/opampsupervisor/v0.161.0/opampsupervisor_0.161.0_linux_amd64"
for f in "${MIRROR}/v0.161.0/otelcol-contrib_0.161.0_linux_amd64.tar.gz" "${MIRROR}/cmd/opampsupervisor/v0.161.0/opampsupervisor_0.161.0_linux_amd64"; do
  sha256_of "${f}" | tr -d '\n' > "${f}.sha256"
done

# ---- Fake release: updater + checksums.txt in goreleaser format ----------------------------------------
UPDATER_VERSION="9.9.9"
UPDATER_NAME="myriarch-updater_${UPDATER_VERSION}_linux_amd64"
UPDATER_RAN="${W}/updater-ran"   # the fake updater leaves a mark when it is executed (that is: used as a verifier)
printf '#!/bin/sh\necho ran >> "%s"\necho fake-updater\n' "${UPDATER_RAN}" > "${REL}/${UPDATER_NAME}"; chmod 0755 "${REL}/${UPDATER_NAME}"
cp "${REL}/${UPDATER_NAME}" "${REL}/myriarch-updater-plain-name"
printf 'fake windows binary\n' > "${REL}/myriarch-updater_9.9.9_windows_amd64.exe"
printf 'fake archive\n' > "${REL}/myriarch-updater_9.9.9_linux_amd64.tar.gz"
UPDATER_SHA="$(sha256_of "${REL}/${UPDATER_NAME}")"
make_checksums() { # target-dir
  (cd "$1" && for f in myriarch-updater_9.9.9_linux_amd64 myriarch-updater_9.9.9_linux_amd64.tar.gz myriarch-updater_9.9.9_windows_amd64.exe myriarch-updater-plain-name; do
    printf '%s  %s\n' "$(sha256_of "${f}")" "${f}"; done > checksums.txt)
}
make_checksums "${REL}"
printf '{"bundle":"checksums.txt"}\n' > "${REL}/checksums.txt.sigstore.json"
GOOD_SUMS_SHA="$(sha256_of "${REL}/checksums.txt")"

base_args=(--opamp-endpoint wss://fleet.example.com/v1/opamp --token myre_abcDEF123 --site-type office --site-id OFFICE1
  --collector-version 0.161.0 --with-updater)
run_install() { # root extra-args...   (lab mode for mirror/file://, collector Sigstore skipped)
  local root="$1"; shift
  bash "${INSTALLER}" --root "${root}" --arch amd64 --templates-dir "${TEMPLATES}" --insecure --verify-signatures skip \
    --release-base-url "file://${MIRROR}" "${base_args[@]}" --updater-url "file://${REL}/${UPDATER_NAME}" "$@"
}
# ---- Test verifier: emulates what matters of cosign verify-blob and myriarch-updater verify-bundle -----------
# STUB_SAN is the certificate identity of the bundle. The identity regexp that the installer passes must match it (and, for
# verify-bundle, end with the tag of --version); the issuer must be GitHub's; for checksums.txt the file must hash to
# STUB_GOOD_SHA, so that a changed file fails like a real signature check. Other files (collector, supervisor) always pass.
STUBS="${W}/stubs"; mkdir -p "${STUBS}" "${STUBS}/updater"
cp "${CI_DIR}/tests/stub-verifier.sh" "${STUBS}/verifier"
chmod 0755 "${STUBS}/verifier"
cp "${STUBS}/verifier" "${STUBS}/cosign"; cp "${STUBS}/verifier" "${STUBS}/updater/myriarch-updater"
export STUB_LOG="${W}/verifier.log"
export STUB_GOOD_SHA="${GOOD_SUMS_SHA}"
GOOD_SAN="https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v${UPDATER_VERSION}"
export STUB_SAN="${GOOD_SAN}"

# upstream bundles for the collector and the supervisor (the stub accepts them)
for b in "${MIRROR}/v0.161.0/otelcol-contrib_0.161.0_linux_amd64.tar.gz" "${MIRROR}/cmd/opampsupervisor/v0.161.0/opampsupervisor_0.161.0_linux_amd64"; do
  printf '{}\n' > "${b}.sigstore.json"
done

# Restricted PATH directories: symlinks to the listed tools only, so that the installer finds nothing else. INSTALLER_TOOLS is
# what the installer needs besides a download tool; a case adds what it needs on top and leaves out what it tests the absence
# of. Keep the list complete: with a tool missing, a case fails for another reason than the one under test (GNU tar -z runs
# the external gzip, and without it a valid archive looks like one without its binary).
INSTALLER_TOOLS=(bash sh tar gzip install cmp awk sed tr cut grep mktemp date id uname sha256sum shasum mv cp ln rm mkdir dirname basename cat head readlink chmod sort wc sleep seq env)
path_dir() { # dir [tool...]: INSTALLER_TOOLS and the given tools (those the host has), without the ones named in $WITHOUT
  local dir="$1" c p; shift
  mkdir -p "${dir}"
  for c in "${INSTALLER_TOOLS[@]}" "$@"; do
    case " ${WITHOUT:-} " in *" ${c} "*) continue ;; esac
    p="$(command -v "${c}" 2>/dev/null || true)"; [ -n "${p}" ] && ln -sf "${p}" "${dir}/${c}"
  done
}
NOCOSIGN="${W}/bin-no-cosign"; path_dir "${NOCOSIGN}" curl   # a PATH without any Sigstore verifier

# release path: lab mode for file:// URLs, but with the signature check of checksums.txt and of collector and supervisor
run_release() { # root extra-args...
  local root="$1"; shift
  bash "${INSTALLER}" --root "${root}" --arch amd64 --templates-dir "${TEMPLATES}" --insecure --verify-signatures require \
    --release-base-url "file://${MIRROR}" "${base_args[@]}" --updater-url "file://${REL}/${UPDATER_NAME}" \
    --updater-checksums-url "file://${REL}/checksums.txt" --verifier "${STUBS}/cosign" "$@"
}
fresh() { mktemp -d "${W}/root.XXXXXX"; }
no_install() { [ ! -e "$1/etc/myriarch/supervisor.yaml" ] && [ ! -e "$1/opt/myriarch/updater/myriarch-updater" ]; }

echo "install-linux.sh: keyless bundle of checksums.txt (release path)"
t_valid() { local r; r="$(fresh)"; : > "${STUB_LOG}"; run_release "${r}" \
  && cmp -s "${REL}/${UPDATER_NAME}" "${r}/opt/myriarch/updater/myriarch-updater" \
  && [ "$(grep -c '^CALL verify-blob' "${STUB_LOG}")" = 3 ]; }
expect_ok "valid bundle: updater is installed and checksums.txt, collector and supervisor were verified" t_valid

t_identity_args() { : > "${STUB_LOG}"; run_release "$(fresh)" || return 1
  local call; call="$(grep -F 'checksums' "${STUB_LOG}" | head -n 1)"
  case "${call}" in *'--certificate-identity-regexp ^https://github\.com/myriarch/myriarch/\.github/workflows/release\.yml@refs/tags/v9\.9\.9$ --certificate-oidc-issuer https://token.actions.githubusercontent.com'*) ;;
    *) echo "unexpected cosign call for checksums.txt: ${call}"; return 1 ;; esac
  case "${call}" in *'--bundle '*'checksums.txt.sigstore.json '*) ;; *) echo "bundle is not <checksums>.sigstore.json: ${call}"; return 1 ;; esac
  # checksums.txt is verified first, before the updater is downloaded and before collector and supervisor
  [ "$(grep -c . "${STUB_LOG}")" = 3 ] && [ "$(head -n 1 "${STUB_LOG}" | grep -c 'checksums.txt')" = 1 ]; }
expect_ok "cosign receives the exact release identity of the updater version with GitHub's issuer, and verifies checksums.txt first" t_identity_args

t_release_regex() {
  local id; id="$(bash -c 'eval "$(sed -n "/^cert_identity_release()/,/^}/p" "$1")"; RELEASE_REPOSITORY=myriarch/myriarch; cert_identity_release "$2"' _ "${INSTALLER}" 0.1.0-rc.1)"
  [ "${id}" = '^https://github\.com/myriarch/myriarch/\.github/workflows/release\.yml@refs/tags/v0\.1\.0-rc\.1$' ] || { echo "${id}"; return 1; }
  local base='https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/'
  printf '%s\n' "${base}v0.1.0-rc.1" | grep -Eq -- "${id}" || return 1
  ! printf '%s\n' "${base}v0.1.0-rc.10" | grep -Eq -- "${id}" || return 1
  ! printf '%s\n' "${base}v0.1.0" | grep -Eq -- "${id}" || return 1
  ! printf '%s\n' "${base}v0x1x0-rc.1" | grep -Eq -- "${id}"
}
expect_ok "the release identity matches exactly the tag of the version (dots escaped, anchored)" t_release_regex

t_valid_and_sha() { run_release "$(fresh)" --updater-sha256 "${UPDATER_SHA}"; }
expect_ok "verified checksums.txt AND matching --updater-sha256 are allowed" t_valid_and_sha

t_bundle_url() { cp "${REL}/checksums.txt.sigstore.json" "${W}/elsewhere.json"; run_release "$(fresh)" --updater-bundle-url "file://${W}/elsewhere.json"; }
expect_ok "--updater-bundle-url (bundle at a different location)" t_bundle_url

t_version_flag() { run_release "$(fresh)" --updater-url "file://${REL}/myriarch-updater-plain-name" --updater-version "${UPDATER_VERSION}"; }
expect_ok "--updater-version for a file name without a version" t_version_flag
t_version_flag_v() { run_release "$(fresh)" --updater-url "file://${REL}/myriarch-updater-plain-name" --updater-version "v${UPDATER_VERSION}"; }
expect_ok "--updater-version accepts a leading v" t_version_flag_v

t_version_agree() { run_release "$(fresh)" --updater-version "${UPDATER_VERSION}"; }
expect_ok "--updater-version agrees with the version in --updater-url" t_version_agree

t_version_mismatch() { run_release "$(fresh)" --updater-version 9.9.8; }
expect_fail "--updater-version differs from the version in --updater-url" '\[option_conflict\].*9\.9\.8.*9\.9\.9' t_version_mismatch

t_version_case() { run_release "$(fresh)" --updater-version "${UPDATER_VERSION}-RC.1"; }
expect_fail "--updater-version differing only in case or pre-release is a conflict" '\[option_conflict\]' t_version_case
t_version_badpre() { run_release "$(fresh)" --updater-version "${UPDATER_VERSION}--x"; }
expect_fail "--updater-version with a pre-release part that does not start with an alphanumeric is invalid" '\[option_invalid\]' t_version_badpre

t_version_missing() { run_release "$(fresh)" --updater-url "file://${REL}/myriarch-updater-plain-name"; }
expect_fail "no version in the file name and no --updater-version" '\[option_missing\].*--updater-version' t_version_missing

t_version_invalid() { run_release "$(fresh)" --updater-version latest; }
expect_fail "--updater-version is not a version" '\[option_invalid\].*--updater-version' t_version_invalid

t_no_updater_run() { rm -f "${UPDATER_RAN}"; run_release "$(fresh)" && [ ! -e "${UPDATER_RAN}" ]; }
expect_ok "the updater that is being installed is never run as a verifier" t_no_updater_run

# a bundle with another identity must not pass, however genuine it is
SAN_REL="https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags"
for c in \
  "another tag|${SAN_REL}/v9.9.8" \
  "a longer tag (prefix of the version)|${SAN_REL}/v9.9.9.1" \
  "an rc of the version|${SAN_REL}/v9.9.9-rc.1" \
  "a branch instead of a tag|https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/heads/main" \
  "another workflow|https://github.com/myriarch/myriarch/.github/workflows/ci.yml@refs/tags/v9.9.9" \
  "another repository|https://github.com/evil/myriarch/.github/workflows/release.yml@refs/tags/v9.9.9" \
  "a repository that only starts like ours|https://github.com/myriarch/myriarch-fork/.github/workflows/release.yml@refs/tags/v9.9.9" \
  "the upstream collector workflow|https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/base-release.yaml@refs/tags/v9.9.9"; do
  title="${c%%|*}"; san="${c#*|}"
  t_wrong_identity() { local r rc; r="$(fresh)"; STUB_SAN="${san}" run_release "${r}"; rc=$?
    no_install "${r}" || { echo "the updater was installed"; return 0; }; return "${rc}"; }
  expect_fail "bundle of ${title} is rejected" '\[sigstore_verification_failed\].*checksums\.txt' t_wrong_identity
done

TAMPER_ROOT="$(fresh)"
t_tampered() { local d="${W}/tampered"; mkdir -p "${d}"; cp "${REL}"/* "${d}/"
  sed 's/^./0/' "${REL}/checksums.txt" > "${d}/checksums.txt"   # first character of every line changed
  run_release "${TAMPER_ROOT}" --updater-checksums-url "file://${d}/checksums.txt"; }
expect_fail "tampered checksums.txt is rejected" '\[sigstore_verification_failed\].*checksums\.txt' t_tampered
if no_install "${TAMPER_ROOT}"; then ok "  ... and nothing was installed"; else fail "something was installed after a tampered checksums.txt"; fi

t_updater_verifier_args() { local r; r="$(fresh)"; : > "${STUB_LOG}"
  bash "${INSTALLER}" --root "${r}" --arch amd64 --templates-dir "${TEMPLATES}" --insecure --verify-signatures require --release-base-url "file://${MIRROR}" \
    "${base_args[@]}" --updater-url "file://${REL}/${UPDATER_NAME}" --updater-checksums-url "file://${REL}/checksums.txt" --verifier "${STUBS}/updater/myriarch-updater" \
    && grep -F 'verify-bundle' "${STUB_LOG}" | grep -Fq -- '--issuer https://token.actions.githubusercontent.com' \
    && grep -F 'checksums.txt' "${STUB_LOG}" | grep -Fq -- '--version v9.9.9'; }
expect_ok "an installed myriarch-updater as --verifier gets the identity, the issuer and the version" t_updater_verifier_args

t_missing_bundle() { local d="${W}/nobundle"; mkdir -p "${d}"; cp "${REL}/checksums.txt" "${d}/"; run_release "$(fresh)" --updater-checksums-url "file://${d}/checksums.txt"; }
expect_fail "missing bundle file" '\[download_failed\]' t_missing_bundle

t_missing_line() { local d="${W}/missing"; mkdir -p "${d}"; grep -v "  ${UPDATER_NAME}\$" "${REL}/checksums.txt" > "${d}/checksums.txt"; cp "${REL}/checksums.txt.sigstore.json" "${d}/"
  STUB_GOOD_SHA="$(sha256_of "${d}/checksums.txt")" run_release "$(fresh)" --updater-checksums-url "file://${d}/checksums.txt"; }
expect_fail "genuine checksums.txt without a line for the updater" '\[checksum_entry_count\].*\b0\b' t_missing_line

t_dup_line() { local d="${W}/dup"; mkdir -p "${d}"; { cat "${REL}/checksums.txt"; grep "  ${UPDATER_NAME}\$" "${REL}/checksums.txt"; } > "${d}/checksums.txt"; cp "${REL}/checksums.txt.sigstore.json" "${d}/"
  STUB_GOOD_SHA="$(sha256_of "${d}/checksums.txt")" run_release "$(fresh)" --updater-checksums-url "file://${d}/checksums.txt"; }
expect_fail "duplicate line for the updater" '\[checksum_entry_count\].*\b2\b' t_dup_line

t_conflict() { run_release "$(fresh)" --updater-sha256 "$(printf '0%.0s' $(seq 64))"; }
expect_fail "--updater-sha256 contradicts checksums.txt (hash conflict)" '\[checksum_conflict\]' t_conflict

t_binary_tampered() { local d="${W}/bin-tampered"; mkdir -p "${d}"; cp "${REL}"/* "${d}/"; printf 'evil\n' >> "${d}/${UPDATER_NAME}"
  run_release "$(fresh)" --updater-url "file://${d}/${UPDATER_NAME}"; }
expect_fail "updater binary does not match the verified checksum" '\[checksum_mismatch\]' t_binary_tampered

echo "install-linux.sh: verifier (cosign 3.0.4 or newer, or an installed myriarch-updater), never the downloaded updater"
cosign_stub_dir() { # version -> dir with a cosign stub of that version in front of the restricted PATH
  local d="${W}/bin-cosign-$1"; path_dir "${d}" curl; cp "${STUBS}/cosign" "${d}/cosign"; printf '%s\n' "${d}"; }
t_cosign_path() { local d; d="$(cosign_stub_dir "$1")"; PATH="${d}" STUB_COSIGN_VERSION="$1" run_release_nov "$(fresh)"; }
run_release_nov() { # like run_release, but the verifier comes from PATH
  local root="$1"; shift
  bash "${INSTALLER}" --root "${root}" --arch amd64 --templates-dir "${TEMPLATES}" --insecure --verify-signatures require \
    --release-base-url "file://${MIRROR}" "${base_args[@]}" --updater-url "file://${REL}/${UPDATER_NAME}" \
    --updater-checksums-url "file://${REL}/checksums.txt" "$@"
}
t_cosign_304() { t_cosign_path v3.0.4; };  expect_ok "cosign 3.0.4 from PATH is accepted" t_cosign_304
t_cosign_313() { t_cosign_path v3.1.3; };  expect_ok "cosign 3.1.3 from PATH is accepted" t_cosign_313
t_cosign_400() { t_cosign_path v4.0.0; };  expect_ok "cosign 4.0.0 from PATH is accepted" t_cosign_400
t_cosign_303() { t_cosign_path v3.0.3; };  expect_fail "cosign 3.0.3 is too old" '\[sigstore_verifier_outdated\].*3\.0\.4' t_cosign_303
t_cosign_241() { t_cosign_path v2.4.1; };  expect_fail "cosign 2.4.1 is too old" '\[sigstore_verifier_outdated\]' t_cosign_241
t_cosign_dev() { t_cosign_path devel; };   expect_fail "cosign without a usable version is refused" '\[sigstore_verifier_outdated\]' t_cosign_dev
t_cosign_304rc() { t_cosign_path v3.0.4-rc.1; }; expect_fail "a pre-release of 3.0.4 (v3.0.4-rc.1) is too old" '\[sigstore_verifier_outdated\]' t_cosign_304rc
t_cosign_305rc() { t_cosign_path v3.0.5-rc.1; }; expect_ok "a pre-release of a newer version (v3.0.5-rc.1) is accepted" t_cosign_305rc
OLD_ROOT="$(fresh)"
t_cosign_old_nothing() { local d; d="$(cosign_stub_dir v3.0.3)"; rm -f "${UPDATER_RAN}"; PATH="${d}" STUB_COSIGN_VERSION=v3.0.3 run_release_nov "${OLD_ROOT}"; }
expect_fail "an old cosign stops the installation before anything is downloaded or installed" '\[sigstore_verifier_outdated\]' t_cosign_old_nothing
if no_install "${OLD_ROOT}" && [ ! -e "${UPDATER_RAN}" ]; then ok "  ... nothing installed, the updater was not run"; else fail "something was installed or run after an outdated cosign"; fi
t_verifier_old() { STUB_COSIGN_VERSION=v3.0.3 run_release "$(fresh)"; }
expect_fail "--verifier pointing to an old cosign is refused" '\[sigstore_verifier_outdated\]' t_verifier_old
# a cosign under another name (the release asset name) gets the same version check, not a verify-bundle call
t_verifier_renamed_old() { local d="${W}/bin-renamed"; mkdir -p "${d}"; cp "${STUBS}/cosign" "${d}/cosign-linux-amd64"
  STUB_COSIGN_VERSION=v3.0.3 run_release "$(fresh)" --verifier "${d}/cosign-linux-amd64"; }
expect_fail "--verifier cosign-linux-amd64 reporting 3.0.3 is refused with sigstore_verifier_outdated" '\[sigstore_verifier_outdated\]' t_verifier_renamed_old
t_verifier_renamed_ok() { local d="${W}/bin-renamed"; mkdir -p "${d}"; cp "${STUBS}/cosign" "${d}/cosign-linux-amd64"
  STUB_COSIGN_VERSION=v3.1.3 run_release "$(fresh)" --verifier "${d}/cosign-linux-amd64"; }
expect_ok "--verifier cosign-linux-amd64 reporting 3.1.3 is called as cosign verify-blob" t_verifier_renamed_ok

t_old_cosign_installed_updater() { local r d; r="$(fresh)"; d="$(cosign_stub_dir v3.0.3)"
  mkdir -p "${r}/opt/myriarch/updater"; cp "${STUBS}/updater/myriarch-updater" "${r}/opt/myriarch/updater/myriarch-updater"
  PATH="${d}" STUB_COSIGN_VERSION=v3.0.3 run_release_nov "${r}"; }
expect_ok "an old cosign in PATH does not hide an installed myriarch-updater" t_old_cosign_installed_updater

MISSING_ROOT="$(fresh)"
t_no_verifier() { rm -f "${UPDATER_RAN}"; PATH="${NOCOSIGN}" run_release_nov "${MISSING_ROOT}"; }
expect_fail "without cosign and without an installed updater: sigstore_verifier_missing" '\[sigstore_verifier_missing\]' t_no_verifier
if no_install "${MISSING_ROOT}" && [ ! -e "${UPDATER_RAN}" ]; then ok "  ... the downloaded updater was neither installed nor used as a verifier"; else fail "the downloaded updater was used or installed without a verifier"; fi
t_no_verifier_msg() { local out; out="$(PATH="${NOCOSIGN}" run_release_nov "$(fresh)" 2>&1)" || true; printf '%s' "${out}" | grep -q -- '--updater-sha256'; }
expect_ok "the message for a missing verifier names the --updater-sha256 alternative" t_no_verifier_msg

t_installed_updater() { local r; r="$(fresh)"; mkdir -p "${r}/opt/myriarch/updater"; cp "${STUBS}/updater/myriarch-updater" "${r}/opt/myriarch/updater/myriarch-updater"
  : > "${STUB_LOG}"; PATH="${NOCOSIGN}" run_release_nov "${r}" && grep -Fq 'verify-bundle' "${STUB_LOG}" \
    && cmp -s "${REL}/${UPDATER_NAME}" "${r}/opt/myriarch/updater/myriarch-updater"; }
expect_ok "an installed myriarch-updater verifies the new bundle (upgrade) and is then replaced" t_installed_updater

t_sha_only_downloaded_verifier() { rm -f "${UPDATER_RAN}"; PATH="${NOCOSIGN}" run_install_require "$(fresh)" --updater-sha256 "${UPDATER_SHA}" && [ -e "${UPDATER_RAN}" ]; }
run_install_require() { local root="$1"; shift
  bash "${INSTALLER}" --root "${root}" --arch amd64 --templates-dir "${TEMPLATES}" --insecure --verify-signatures require \
    --release-base-url "file://${MIRROR}" "${base_args[@]}" --updater-url "file://${REL}/${UPDATER_NAME}" "$@"; }
expect_ok "hosts without a verifier: --updater-sha256 makes the hash-checked updater the verifier of collector and supervisor" t_sha_only_downloaded_verifier

t_sha_only() { run_install "$(fresh)" --updater-sha256 "${UPDATER_SHA}"; }
expect_ok "--updater-sha256 only (lab mode)" t_sha_only

echo "install-linux.sh: removed and conflicting options"
t_signing_key() { run_release "$(fresh)" --signing-key "${W}/cosign.pub"; }
expect_fail "--signing-key was removed and names the replacement" '\[option_removed\].*--updater-checksums-url' t_signing_key
t_signing_key_alone() { run_install "$(fresh)" --signing-key; }
expect_fail "--signing-key without a value fails with option_removed as well" '\[option_removed\]' t_signing_key_alone
t_sig_url() { run_release "$(fresh)" --updater-checksums-sig-url "file://${W}/elsewhere.sig"; }
expect_fail "--updater-checksums-sig-url was removed and names the replacement" '\[option_removed\].*--updater-bundle-url' t_sig_url

t_bundle_only() { run_install "$(fresh)" --updater-bundle-url "file://${REL}/checksums.txt.sigstore.json" --updater-sha256 "${UPDATER_SHA}"; }
expect_fail "--updater-bundle-url without --updater-checksums-url" '\[option_conflict\].*--updater-checksums-url' t_bundle_only

t_skip_with_checksums() { run_install "$(fresh)" --updater-checksums-url "file://${REL}/checksums.txt"; }
expect_fail "--updater-checksums-url with --verify-signatures skip" '\[option_conflict\].*--verify-signatures skip' t_skip_with_checksums

t_without_updater() { bash "${INSTALLER}" --root "$(fresh)" --arch amd64 --templates-dir "${TEMPLATES}" --insecure --verify-signatures skip \
  --release-base-url "file://${MIRROR}" --opamp-endpoint wss://x/v1/opamp --token myre_x --site-type office --site-id 1 --collector-version 0.161.0 \
  --updater-checksums-url "file://${REL}/checksums.txt"; }
expect_fail "--updater-checksums-url without --with-updater" '\[option_conflict\].*--with-updater' t_without_updater

t_version_without_updater() { bash "${INSTALLER}" --root "$(fresh)" --arch amd64 --templates-dir "${TEMPLATES}" --insecure --verify-signatures skip \
  --release-base-url "file://${MIRROR}" --opamp-endpoint wss://x/v1/opamp --token myre_x --site-type office --site-id 1 --collector-version 0.161.0 \
  --updater-version 1.0.0; }
expect_fail "--updater-version without --with-updater" '\[option_conflict\].*--with-updater' t_version_without_updater

# Mandatory without --insecure: neither hash nor checksums URL => abort (the https requirement for the updater URL comes first, hence an https URL)
t_mandatory() { bash "${INSTALLER}" --root "$(fresh)" --arch amd64 --templates-dir "${TEMPLATES}" --verify-signatures require \
  --release-base-url "https://mirror.example.com/r" --opamp-endpoint wss://x/v1/opamp --token myre_x --site-type office --site-id 1 \
  --collector-version 0.161.0 --with-updater --updater-url "https://mirror.example.com/${UPDATER_NAME}"; }
expect_fail "a hash source is mandatory without --insecure" '\[updater_hash_source_missing\]' t_mandatory

t_http_checksums() { bash "${INSTALLER}" --root "$(fresh)" --arch amd64 --templates-dir "${TEMPLATES}" \
  --release-base-url "https://mirror.example.com/r" --opamp-endpoint wss://x/v1/opamp --token myre_x --site-type office --site-id 1 \
  --collector-version 0.161.0 --with-updater --updater-url "https://mirror.example.com/${UPDATER_NAME}" \
  --updater-checksums-url "http://mirror.example.com/checksums.txt"; }
expect_fail "checksums.txt only over https (without --insecure)" '\[https_required\]' t_http_checksums
t_http_bundle() { bash "${INSTALLER}" --root "$(fresh)" --arch amd64 --templates-dir "${TEMPLATES}" \
  --release-base-url "https://mirror.example.com/r" --opamp-endpoint wss://x/v1/opamp --token myre_x --site-type office --site-id 1 \
  --collector-version 0.161.0 --with-updater --updater-url "https://mirror.example.com/${UPDATER_NAME}" \
  --updater-checksums-url "https://mirror.example.com/checksums.txt" --updater-bundle-url "http://mirror.example.com/checksums.txt.sigstore.json"; }
expect_fail "the bundle only over https (without --insecure)" '\[https_required\]' t_http_bundle


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

cat > "${STUBS}/identity-cosign" <<'STUB'
#!/bin/sh
if [ "$1" = version ]; then echo "GitVersion:    v3.1.3"; exit 0; fi
printf '%s\n' "$@" >> "${COSIGN_LOG}"
exit 0
STUB
chmod 0755 "${STUBS}/identity-cosign"
mkdir -p "${STUBS}/identity"; cp "${STUBS}/identity-cosign" "${STUBS}/identity/cosign"
t_identity_binding() {
  local log="${W}/cosign.log" r; r="$(fresh)"; : > "${log}"
  PATH="${STUBS}/identity:${PATH}" COSIGN_LOG="${log}" bash "${INSTALLER}" --root "${r}" "${plain_args[@]}" --supervisor-version 0.160.0 --verify-signatures require || return 1
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
NOCURL="${W}/bin-no-curl"; path_dir "${NOCURL}" timeout
cat > "${NOCURL}/wget" <<'STUB'
#!/bin/sh
printf '%s\n' "$@" >> "${WGET_LOG}"
exit 1
STUB
chmod 0755 "${NOCURL}/wget"
t_wget_refused() { PATH="${NOCURL}" bash "${INSTALLER}" --root "$(fresh)" --arch amd64 --templates-dir "${TEMPLATES}" --verify-signatures require \
  --release-base-url "https://mirror.example.com/r" --opamp-endpoint wss://x/v1/opamp --token myre_x --site-type office --site-id 1 --collector-version 0.161.0; }
expect_fail "wget without curl is refused without --insecure" '\[tool_missing\].*curl' t_wget_refused
t_wget_lab() { : > "${W}/wget.log"; WGET_LOG="${W}/wget.log" PATH="${NOCURL}" bash "${INSTALLER}" --root "$(fresh)" "${plain_args[@]}" --verify-signatures skip; }
expect_fail "with --insecure wget is used for labs" '\[download_failed\]' t_wget_lab
grep -Fxq -- '--secure-protocol=TLSv1_2' "${W}/wget.log" && ok "  ... with a minimum TLS version" || fail "wget was not called with --secure-protocol=TLSv1_2"

# GNU tar runs the external gzip for .tar.gz archives (bsdtar has zlib built in): without gzip the installer names the missing
# tool before it touches the host, instead of reporting the binary as missing from the archive
NOGZIP="${W}/bin-no-gzip"; WITHOUT=gzip path_dir "${NOGZIP}" curl wget
NOGZIP_ROOT="$(fresh)"
t_no_gzip() { PATH="${NOGZIP}" bash "${INSTALLER}" --root "${NOGZIP_ROOT}" "${plain_args[@]}" --verify-signatures skip; }
expect_fail "without gzip the installer stops with tool_missing and names gzip" '\[tool_missing\].*gzip' t_no_gzip
if [ -z "$(ls -A "${NOGZIP_ROOT}")" ]; then ok "  ... before anything was created"; else fail "the installer created files before it noticed that gzip is missing"; fi

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

# ---- Real cosign, offline, against the committed upstream fixtures ----------------------------------------------
# The upstream bundles in internal/updater/testdata/sigstore are genuine keyless signatures (of the .sha256 file of a
# collector release). They cannot carry the Myriarch identity, so they serve as the positive control of cosign itself
# (offline, with the trusted root) and as a genuine but foreign bundle that the installer must reject.
if have cosign; then
  echo "install-linux.sh: real cosign against the upstream fixtures (offline)"
  FIX="${ROOT}/internal/updater/testdata/sigstore"
  FIX_BLOB="otelcol-contrib_0.162.0_linux_amd64.tar.gz.sha256"
  UP_ID='^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-release\.yaml@refs/tags/v0\.162\.0$'
  ISSUER='https://token.actions.githubusercontent.com'
  real_verify() { # blob identity-regexp
    cosign verify-blob --bundle "${FIX}/${FIX_BLOB}.sigstore.json" --trusted-root "${FIX}/trusted_root.json" \
      --certificate-identity-regexp "$2" --certificate-oidc-issuer "${ISSUER}" "$1"; }
  expect_ok "positive control: cosign verifies the upstream bundle with the upstream identity" real_verify "${FIX}/${FIX_BLOB}" "${UP_ID}"
  cp "${FIX}/${FIX_BLOB}" "${W}/fixture-tampered"; printf 'x' >> "${W}/fixture-tampered"
  expect_fail "the same bundle fails for a tampered file" 'error|Error' real_verify "${W}/fixture-tampered" "${UP_ID}"
  expect_fail "the same bundle fails for the Myriarch release identity" 'expected SAN value' real_verify "${FIX}/${FIX_BLOB}" \
    "$(bash -c 'eval "$(sed -n "/^cert_identity_release()/,/^}/p" "$1")"; RELEASE_REPOSITORY=myriarch/myriarch; cert_identity_release 0.162.0' _ "${INSTALLER}")"

  # the installer with real cosign: the upstream bundle stands in for checksums.txt.sigstore.json
  REAL="${W}/real-release"; mkdir -p "${REAL}"
  cp "${FIX}/${FIX_BLOB}" "${REAL}/checksums.txt"; cp "${FIX}/${FIX_BLOB}.sigstore.json" "${REAL}/checksums.txt.sigstore.json"
  cp "${REL}/${UPDATER_NAME}" "${REAL}/"
  real_install() { # root extra-args...
    local root="$1"; shift
    bash "${INSTALLER}" --root "${root}" --arch amd64 --templates-dir "${TEMPLATES}" --insecure --verify-signatures require --trusted-root "${FIX}/trusted_root.json" \
      --release-base-url "file://${MIRROR}" "${base_args[@]}" --updater-url "file://${REL}/${UPDATER_NAME}" \
      --updater-checksums-url "file://${REAL}/checksums.txt" --verifier "$(command -v cosign)" "$@"; }
  REAL_ROOT="$(fresh)"
  t_real_foreign() { real_install "${REAL_ROOT}"; }
  expect_fail "real cosign: a genuine bundle with another identity is rejected" '\[sigstore_verification_failed\].*checksums\.txt' t_real_foreign
  if no_install "${REAL_ROOT}"; then ok "  ... and nothing was installed"; else fail "something was installed after a foreign bundle"; fi
  t_real_foreign_version() { real_install "$(fresh)" --updater-version 0.162.0 --updater-url "file://${REL}/myriarch-updater-plain-name"; }
  expect_fail "real cosign: also for the version of the fixture (the workflow and repository differ)" '\[sigstore_verification_failed\]' t_real_foreign_version
  t_real_version() { cosign version 2>&1 | grep -Eq '^GitVersion:[[:space:]]+v[0-9]+\.[0-9]+\.[0-9]+'; }
  expect_ok "the cosign of this host reports a version that the installer can read" t_real_version

  # ---- The keyless bundle of the release v0.1.0-rc.3 (genuine, committed byte for byte) --------------------------------------
  # myriarch_0.1.0-rc.3_checksums.txt and .sigstore.json are the assets checksums.txt and checksums.txt.sigstore.json of the
  # published release v0.1.0-rc.3. The LICENSE of this repository is the LICENSE asset that this checksums.txt lists (asserted
  # below), so it stands in for the updater binary: the installer takes its hash from the verified checksums.txt and installs
  # exactly that file. It is never run (the verifier is cosign, staging mode starts nothing). The stub passes the verify-blob
  # call for checksums.txt to the real cosign (STUB_REAL_COSIGN) and accepts the fake collector and supervisor of the mirror.
  # Every real-cosign call runs with an unreachable proxy and an empty TUF cache: an online lookup fails instead of passing.
  echo "install-linux.sh: real cosign against the keyless bundle of the release v0.1.0-rc.3 (offline)"
  RC_VERSION="0.1.0-rc.3"
  RC_ID='^https://github\.com/myriarch/myriarch/\.github/workflows/release\.yml@refs/tags/v0\.1\.0-rc\.3$'
  RC_SUMS="${FIX}/myriarch_${RC_VERSION}_checksums.txt"
  RC="${W}/rc-release"; mkdir -p "${RC}" "${W}/rc-tuf"
  cp "${RC_SUMS}" "${RC}/checksums.txt"; cp "${RC_SUMS}.sigstore.json" "${RC}/checksums.txt.sigstore.json"; cp "${ROOT}/LICENSE" "${RC}/LICENSE"
  offline() { HTTPS_PROXY=http://127.0.0.1:9 HTTP_PROXY=http://127.0.0.1:9 https_proxy='' http_proxy='' NO_PROXY='' no_proxy='' TUF_ROOT="${W}/rc-tuf" "$@"; }
  # the identity that the installer builds for a version, with the repository of the installer
  rc_identity() { bash -c 'eval "$(sed -n -e "/^readonly RELEASE_REPOSITORY=/p" -e "/^cert_identity_release()/,/^}/p" "$1")"; cert_identity_release "$2"' _ "${INSTALLER}" "$1"; }

  t_rc_fixture() { [ "$(sha256_of "${RC_SUMS}")" = 12d7c8ed1c017712c216dd8f5b6711a2396d70aca87261956323f825864f0ee8 ] \
    && [ "$(sha256_of "${RC_SUMS}.sigstore.json")" = 9370ec37e83f8d18bd0cf3de0eafea0b6418c90355c28cf81167ad37d3e390d9 ]; }
  expect_ok "fixture: checksums.txt and its bundle are the assets of v0.1.0-rc.3 (GitHub's SHA-256 digests)" t_rc_fixture
  t_rc_identity_built() { [ "$(rc_identity "${RC_VERSION}")" = "${RC_ID}" ]; }
  expect_ok "the installer builds the identity of 0.1.0-rc.3" t_rc_identity_built
  t_rc_license() { [ "$(sha256_of "${ROOT}/LICENSE")" = "$(awk '$2 == "LICENSE" { print $1 }' "${RC_SUMS}")" ]; }
  expect_ok "fixture: the LICENSE of the repository is the LICENSE asset of v0.1.0-rc.3" t_rc_license

  rc_cosign() { # version
    offline cosign verify-blob --bundle "${RC_SUMS}.sigstore.json" --trusted-root "${FIX}/trusted_root.json" \
      --certificate-identity-regexp "$(rc_identity "$1")" --certificate-oidc-issuer "${ISSUER}" "${RC_SUMS}"; }
  expect_ok "real cosign: the bundle verifies with the installer's identity of 0.1.0-rc.3" rc_cosign "${RC_VERSION}"
  expect_fail "real cosign: the same bundle is refused with the identity of 0.1.0-rc.2" 'expected SAN value' rc_cosign 0.1.0-rc.2
  expect_fail "real cosign: the same bundle is refused with the identity of 0.1.0" 'expected SAN value' rc_cosign 0.1.0

  rc_install() { # root version checksums-dir extra-args...
    local root="$1" ver="$2" dir="$3"; shift 3
    offline env STUB_REAL_COSIGN="$(command -v cosign)" bash "${INSTALLER}" --root "${root}" --arch amd64 --templates-dir "${TEMPLATES}" \
      --insecure --verify-signatures require --trusted-root "${FIX}/trusted_root.json" --release-base-url "file://${MIRROR}" "${base_args[@]}" \
      --updater-url "file://${dir}/LICENSE" --updater-version "${ver}" --updater-checksums-url "file://${dir}/checksums.txt" \
      --verifier "${STUBS}/cosign" "$@"; }
  t_rc_valid() { local r; r="$(fresh)"; : > "${STUB_LOG}"
    rc_install "${r}" "${RC_VERSION}" "${RC}" || return 1
    cmp -s "${ROOT}/LICENSE" "${r}/opt/myriarch/updater/myriarch-updater" || { echo "the installed file is not the one that checksums.txt lists"; return 1; }
    local call; call="$(grep '^REAL verify-blob' "${STUB_LOG}")"
    case "${call}" in *"--certificate-identity-regexp ${RC_ID} --certificate-oidc-issuer ${ISSUER} --trusted-root ${FIX}/trusted_root.json "*/checksums.txt) ;;
      *) echo "checksums.txt was not verified by the real cosign with the identity of ${RC_VERSION}: ${call}"; return 1 ;; esac; }
  expect_ok "installer: the genuine bundle authenticates checksums.txt of v0.1.0-rc.3 and the file it lists is installed" t_rc_valid

  for v in 0.1.0-rc.2 0.1.0; do
    RC_ROOT="$(fresh)"
    t_rc_other_version() { rc_install "${RC_ROOT}" "${v}" "${RC}"; }
    expect_fail "installer: the bundle of v0.1.0-rc.3 is refused for --updater-version ${v}" '\[sigstore_verification_failed\].*checksums\.txt' t_rc_other_version
    if no_install "${RC_ROOT}"; then ok "  ... and nothing was installed"; else fail "something was installed with the bundle of another version"; fi
  done

  # tampered: the line of LICENSE points to another file, which the mirror serves under that name
  EVIL="${W}/rc-tampered"; mkdir -p "${EVIL}"
  printf '#!/bin/sh\necho not the release\n' > "${EVIL}/LICENSE"
  awk -v h="$(sha256_of "${EVIL}/LICENSE")" '$2 == "LICENSE" { $1 = h } { print $1 "  " $2 }' "${RC_SUMS}" > "${EVIL}/checksums.txt"
  cp "${RC_SUMS}.sigstore.json" "${EVIL}/checksums.txt.sigstore.json"
  t_rc_tampered_copy() { [ "$(diff "${RC_SUMS}" "${EVIL}/checksums.txt" | grep -c '^[<>]')" = 2 ] && grep -q "  LICENSE\$" "${EVIL}/checksums.txt"; }
  expect_ok "the tampered copy differs from the genuine checksums.txt only in the line of LICENSE" t_rc_tampered_copy
  EVIL_ROOT="$(fresh)"
  t_rc_tampered() { rc_install "${EVIL_ROOT}" "${RC_VERSION}" "${EVIL}"; }
  expect_fail "installer: a checksums.txt with a changed line is refused by the genuine bundle" '\[sigstore_verification_failed\].*checksums\.txt' t_rc_tampered
  if no_install "${EVIL_ROOT}"; then ok "  ... and nothing was installed"; else fail "something was installed after a tampered checksums.txt"; fi

  t_rc_changed_file() { local d="${W}/rc-changed"; mkdir -p "${d}"; cp "${RC}"/* "${d}/"; printf 'x' >> "${d}/LICENSE"
    rc_install "$(fresh)" "${RC_VERSION}" "${d}"; }
  expect_fail "installer: a file that differs from its line in the verified checksums.txt is refused" '\[checksum_mismatch\]' t_rc_changed_file
else
  echo "(cosign not in PATH: the real-cosign cases are skipped; ci/install-tools.sh cosign)"
  [ "${CI:-}" != true ] || fail "the real-cosign cases are required in CI: cosign is not in PATH (ci/install-tools.sh cosign)"
fi

# ---- Windows (PowerShell) ---------------------------------------------------------------------------
if have pwsh && [ -f "${CI_DIR}/tests/installer-verify.ps1" ]; then
  echo "install-windows.ps1: signature verification (pwsh)"
  expect_ok "install-windows.ps1 (pwsh)" pwsh -NoProfile -NonInteractive -File "${CI_DIR}/tests/installer-verify.ps1" \
    -Installer "${ROOT}/deploy/scripts/install-windows.ps1" -Templates "${TEMPLATES}" -Work "${W}/win"
else
  echo "(pwsh not in PATH: install-windows.ps1 skipped)"
fi

finish_tests "installer-verify"
