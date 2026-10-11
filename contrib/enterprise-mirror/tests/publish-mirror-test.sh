#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail" is intended in the tests (ok/fail are counters and never fail)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests contrib/enterprise-mirror/publish-mirror.sh and verify-release.sh against a throw-away mirror (put-server.py, Python 3) for
# Artifactory (Bearer) and Nexus (Basic): upload, idempotency, immutability, abort on a differing SHA-256,
# invalid signature, wrong credentials. Errors are matched by their stable tokens, not by prose.
# The keyless mode (--keyless --dir: the project's release, mirrored unchanged) runs with a cosign stub (cosign-stub.sh) and, when
# a real cosign is available, with the committed OpenTelemetry fixture, whose genuine bundle must be refused for Myriarch.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIRROR_DIR="$(cd "${HERE}/.." && pwd)"
# shellcheck source=ci/tests/lib-test.sh
. "${HERE}/../../../ci/tests/lib-test.sh"
need_cmd openssl python3 curl

W="${TEST_TMP}"
SERVER_PID=""
stop_server() { if [ -n "${SERVER_PID}" ]; then kill "${SERVER_PID}" 2>/dev/null || true; wait "${SERVER_PID}" 2>/dev/null || true; SERVER_PID=""; fi; }
trap 'stop_server; cleanup_tests' EXIT

start_server() { # type [name]
  stop_server
  rm -rf "${W}/store"; mkdir -p "${W}/store"; rm -f "${W}/port" "${W}/server.log"
  if [ "$1" = artifactory ]; then
    python3 "${HERE}/put-server.py" --dir "${W}/store" --port-file "${W}/port" --type artifactory --token s3cret-token --log "${W}/server.log" &
  else
    python3 "${HERE}/put-server.py" --dir "${W}/store" --port-file "${W}/port" --type nexus --user ci-user --password 's3cret"pw\x' --log "${W}/server.log" &
  fi
  SERVER_PID=$!
  local _; for _ in $(seq 50); do [ -s "${W}/port" ] && break; sleep 0.1; done
  PORT="$(cat "${W}/port")"
}

# ---- key + fake dist in the goreleaser layout -----------------------------------------------------------
openssl ecparam -name prime256v1 -genkey -noout -out "${W}/key.pem" 2>/dev/null
openssl ec -in "${W}/key.pem" -pubout -out "${W}/cosign.pub" 2>/dev/null
sign_blob() { openssl dgst -sha256 -sign "${W}/key.pem" -out "$1.der" "$1" && openssl base64 -A -in "$1.der" -out "$1.sig" && rm -f "$1.der"; }

VER=1.2.3
D="${W}/dist"
mkdir -p "${D}/myriarch-updater_linux_amd64_v1" "${D}/myriarch-updater_linux_arm64_v8.0" "${D}/myriarch-updater_windows_amd64_v1"
printf 'linux amd64 updater\n' > "${D}/myriarch-updater_linux_amd64_v1/myriarch-updater"
printf 'linux arm64 updater\n' > "${D}/myriarch-updater_linux_arm64_v8.0/myriarch-updater"
printf 'windows updater\n'     > "${D}/myriarch-updater_windows_amd64_v1/myriarch-updater.exe"
for a in "myriarch-updater_${VER}_linux_amd64.tar.gz" "myriarch-updater_${VER}_windows_amd64.zip" "myrctl_${VER}_linux_amd64.tar.gz" "myriarchd_${VER}_linux_amd64.tar.gz"; do
  printf 'archive %s\n' "${a}" > "${D}/${a}"
done
# checksums.txt like goreleaser: upload names (not the paths of the bare binaries)
{
  printf '%s  myriarch-updater_%s_linux_amd64\n'   "$(sha256_of "${D}/myriarch-updater_linux_amd64_v1/myriarch-updater")" "${VER}"
  printf '%s  myriarch-updater_%s_linux_arm64\n'   "$(sha256_of "${D}/myriarch-updater_linux_arm64_v8.0/myriarch-updater")" "${VER}"
  printf '%s  myriarch-updater_%s_windows_amd64.exe\n' "$(sha256_of "${D}/myriarch-updater_windows_amd64_v1/myriarch-updater.exe")" "${VER}"
  for a in "myriarch-updater_${VER}_linux_amd64.tar.gz" "myriarch-updater_${VER}_windows_amd64.zip" "myrctl_${VER}_linux_amd64.tar.gz" "myriarchd_${VER}_linux_amd64.tar.gz"; do
    printf '%s  %s\n' "$(sha256_of "${D}/${a}")" "${a}"
  done
} > "${D}/checksums.txt"
for f in "${D}"/myriarch-updater_*_*_*/myriarch-updater "${D}"/myriarch-updater_*_*_*/myriarch-updater.exe "${D}"/*.tar.gz "${D}"/*.zip "${D}/checksums.txt"; do
  [ -f "${f}" ] && sign_blob "${f}"
done

EXPECTED="checksums.txt checksums.txt.sig cosign.pub myriarch-updater_${VER}_linux_amd64 myriarch-updater_${VER}_linux_amd64.sig myriarch-updater_${VER}_linux_amd64.tar.gz myriarch-updater_${VER}_linux_amd64.tar.gz.sig myriarch-updater_${VER}_linux_arm64 myriarch-updater_${VER}_linux_arm64.sig myriarch-updater_${VER}_windows_amd64.exe myriarch-updater_${VER}_windows_amd64.exe.sig myriarch-updater_${VER}_windows_amd64.zip myriarch-updater_${VER}_windows_amd64.zip.sig myrctl_${VER}_linux_amd64.tar.gz myrctl_${VER}_linux_amd64.tar.gz.sig"

publish() { # extra-args... (the environment comes from the callers)
  MIRROR_ALLOW_HTTP=1 MIRROR_REPO_URL="http://127.0.0.1:${PORT}/myriarch-releases" COSIGN_PUBLIC_KEY="${W}/cosign.pub" \
    "${MIRROR_DIR}/publish-mirror.sh" --dist "${D}" --version "${VER}" "$@"
}
stored() { (cd "${W}/store/myriarch-releases/v${VER}" 2>/dev/null && printf '%s\n' * | LC_ALL=C sort | tr '\n' ' ' | sed 's/ $//'); }
# shellcheck disable=SC2086
want_list() { printf '%s\n' ${EXPECTED} | LC_ALL=C sort | tr '\n' ' ' | sed 's/ $//'; }

echo "publish-mirror.sh: Artifactory (Bearer)"
start_server artifactory
export MIRROR_TYPE=artifactory MIRROR_TOKEN=s3cret-token
expect_ok "upload to myriarch-releases/v${VER}/" publish
if [ "$(stored)" = "$(want_list)" ]; then ok "exactly the expected 15 files are in the mirror (no myriarchd archive)"; else fail "file list differs: $(stored)"; fi
if cmp -s "${D}/myriarch-updater_linux_amd64_v1/myriarch-updater" "${W}/store/myriarch-releases/v${VER}/myriarch-updater_${VER}_linux_amd64"; then ok "bare binary under the upload name"; else fail "bare binary missing or different"; fi
expect_ok "second run: everything unchanged" publish
if printf '%s' "${OUT}" | grep -q '0 uploaded, 15 unchanged'; then ok "  ... 0 uploaded, 15 unchanged"; else fail "the second run uploaded something"; fi
expect_ok "verify-release.sh against the mirror (--base-url, --assets)" env VERIFY_AUTH_HEADER="Authorization: Bearer s3cret-token" \
  "${MIRROR_DIR}/verify-release.sh" --public-key "${W}/cosign.pub" --base-url "http://127.0.0.1:${PORT}/myriarch-releases/v${VER}" --assets
expect_fail "verify-release.sh without credentials fails" "HTTP 401" \
  "${MIRROR_DIR}/verify-release.sh" --public-key "${W}/cosign.pub" --base-url "http://127.0.0.1:${PORT}/myriarch-releases/v${VER}"

# a request path that resolves outside of the store is refused: no file is created next to it, a read finds nothing
code="$(curl -s -o /dev/null -w '%{http_code}' --path-as-is -X PUT -H "Authorization: Bearer ${MIRROR_TOKEN}" --data 'outside' "http://127.0.0.1:${PORT}/../outside.txt")"
if [ "${code}" = "400" ] && [ ! -e "${W}/outside.txt" ]; then ok "a PUT outside of the store is refused (400) and creates nothing"; else fail "a PUT outside of the store answered ${code} or created a file"; fi
# a sibling directory whose name starts with the name of the store is outside of it as well (the separator is part of the check)
code="$(curl -s -o /dev/null -w '%{http_code}' --path-as-is -X PUT -H "Authorization: Bearer ${MIRROR_TOKEN}" --data 'sibling' "http://127.0.0.1:${PORT}/../store-sibling/x.txt")"
if [ "${code}" = "400" ] && [ ! -e "${W}/store-sibling" ]; then ok "a PUT into a sibling directory with the same prefix is refused (400) and creates nothing"; else fail "a PUT into a sibling directory answered ${code} or created it"; fi
printf 'secret\n' > "${W}/outside.txt"
code="$(curl -s -o /dev/null -w '%{http_code}' --path-as-is -H "Authorization: Bearer ${MIRROR_TOKEN}" "http://127.0.0.1:${PORT}/../outside.txt")"
if [ "${code}" = "404" ]; then ok "a GET outside of the store finds nothing (404)"; else fail "a GET outside of the store answered ${code}"; fi
rm -f "${W}/outside.txt"

# same version, different content (re-signed so that the signature check does not fail earlier) => abort
cp -R "${D}" "${W}/dist2"
printf 'changed updater\n' > "${W}/dist2/myriarch-updater_linux_amd64_v1/myriarch-updater"
sign_blob "${W}/dist2/myriarch-updater_linux_amd64_v1/myriarch-updater"
{ printf '%s  myriarch-updater_%s_linux_amd64\n' "$(sha256_of "${W}/dist2/myriarch-updater_linux_amd64_v1/myriarch-updater")" "${VER}"; grep -v " myriarch-updater_${VER}_linux_amd64\$" "${D}/checksums.txt"; } > "${W}/dist2/checksums.txt"
sign_blob "${W}/dist2/checksums.txt"
expect_fail "a differing SHA-256 for an existing file => abort" "mirror_content_differs" \
  env MIRROR_ALLOW_HTTP=1 MIRROR_REPO_URL="http://127.0.0.1:${PORT}/myriarch-releases" COSIGN_PUBLIC_KEY="${W}/cosign.pub" "${MIRROR_DIR}/publish-mirror.sh" --dist "${W}/dist2" --version "${VER}"
if cmp -s "${D}/myriarch-updater_linux_amd64_v1/myriarch-updater" "${W}/store/myriarch-releases/v${VER}/myriarch-updater_${VER}_linux_amd64"; then ok "  ... the existing file in the mirror stayed unchanged"; else fail "the file in the mirror was overwritten"; fi

# invalid signature => nothing is uploaded at all
start_server artifactory
cp -R "${D}" "${W}/dist3"; printf 'tamper\n' >> "${W}/dist3/myriarch-updater_linux_amd64_v1/myriarch-updater"
expect_fail "file changed after signing => abort before the upload" "checksum_mismatch|signature_invalid" \
  env MIRROR_ALLOW_HTTP=1 MIRROR_REPO_URL="http://127.0.0.1:${PORT}/myriarch-releases" COSIGN_PUBLIC_KEY="${W}/cosign.pub" "${MIRROR_DIR}/publish-mirror.sh" --dist "${W}/dist3" --version "${VER}"
if [ -z "$(stored)" ]; then ok "  ... nothing was uploaded"; else fail "uploaded despite an invalid signature"; fi

expect_fail "wrong token => clear error" "mirror_access_denied" env MIRROR_TOKEN=wrong bash -c "$(declare -f publish); PORT=${PORT} W=${W} D=${D} MIRROR_DIR=${MIRROR_DIR} VER=${VER}; publish"
expect_fail "http:// without MIRROR_ALLOW_HTTP is rejected" "mirror_https_required" env MIRROR_REPO_URL="http://127.0.0.1:${PORT}/myriarch-releases" COSIGN_PUBLIC_KEY="${W}/cosign.pub" "${MIRROR_DIR}/publish-mirror.sh" --dist "${D}" --version "${VER}"
expect_fail "unknown MIRROR_TYPE" "mirror_type_invalid" env MIRROR_TYPE=ftp MIRROR_REPO_URL="https://x.example.com/r" "${MIRROR_DIR}/publish-mirror.sh" --dist "${D}" --version "${VER}"
expect_fail "Artifactory without MIRROR_TOKEN" "MIRROR_TOKEN" env -u MIRROR_TOKEN MIRROR_REPO_URL="https://x.example.com/r" "${MIRROR_DIR}/publish-mirror.sh" --dist "${D}" --version "${VER}"

echo "publish-mirror.sh: Nexus (Basic, password with special characters)"
start_server nexus
export MIRROR_TYPE=nexus MIRROR_USER=ci-user MIRROR_PASSWORD='s3cret"pw\x'
unset MIRROR_TOKEN
expect_ok "upload with basic authentication" publish
if [ "$(stored)" = "$(want_list)" ]; then ok "exactly the expected 15 files are in the mirror"; else fail "file list differs: $(stored)"; fi
expect_ok "second run: everything unchanged" publish
expect_fail "wrong password" "mirror_access_denied" env MIRROR_PASSWORD=wrong MIRROR_ALLOW_HTTP=1 MIRROR_REPO_URL="http://127.0.0.1:${PORT}/myriarch-releases" COSIGN_PUBLIC_KEY="${W}/cosign.pub" "${MIRROR_DIR}/publish-mirror.sh" --dist "${D}" --version "${VER}"
if grep -q 's3cret' "${W}/server.log"; then fail "credentials show up in the server log"; else ok "credentials do not appear in URLs/paths"; fi

# ---- keyless mode -----------------------------------------------------------------------------------------------
echo "publish-mirror.sh --keyless: Artifactory (Bearer)"
STUB="${W}/stubbin"; mkdir -p "${STUB}"; cp "${HERE}/cosign-stub.sh" "${STUB}/cosign"; chmod +x "${STUB}/cosign"
ISSUER=https://token.actions.githubusercontent.com
KID="https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v${VER}"
# a downloaded release: the files of a GitHub release under their upload names, checksums.txt over the 39 files (here: 10), the
# bundle and the provenance file (which checksums.txt does not list)
K="${W}/krelease"; mkdir -p "${K}"
cp "${D}/myriarch-updater_linux_amd64_v1/myriarch-updater" "${K}/myriarch-updater_${VER}_linux_amd64"
cp "${D}/myriarch-updater_linux_arm64_v8.0/myriarch-updater" "${K}/myriarch-updater_${VER}_linux_arm64"
cp "${D}/myriarch-updater_windows_amd64_v1/myriarch-updater.exe" "${K}/myriarch-updater_${VER}_windows_amd64.exe"
for a in "myriarch-updater_${VER}_linux_amd64.tar.gz" "myriarch-updater_${VER}_windows_amd64.zip" "myrctl_${VER}_linux_amd64.tar.gz" "myriarchd_${VER}_linux_amd64.tar.gz"; do
  cp "${D}/${a}" "${K}/${a}"
done
printf '{"spdxVersion":"SPDX-2.3"}\n' > "${K}/myriarch-updater_${VER}_linux_amd64.tar.gz.sbom.json"
printf 'license text\n' > "${K}/LICENSE"
( cd "${K}" && for f in $(printf '%s\n' * | LC_ALL=C sort); do printf '%s  %s\n' "$(sha256_of "${f}")" "${f}"; done > "${W}/checksums.k" )
mv "${W}/checksums.k" "${K}/checksums.txt"
printf '{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"}\n' > "${K}/checksums.txt.sigstore.json"
printf '{"provenance":true}\n' > "${K}/myriarch_${VER}_provenance.intoto.jsonl"
KHOST="checksums.txt checksums.txt.sigstore.json myriarch-updater_${VER}_linux_amd64 myriarch-updater_${VER}_linux_amd64.tar.gz myriarch-updater_${VER}_linux_arm64 myriarch-updater_${VER}_windows_amd64.exe myriarch-updater_${VER}_windows_amd64.zip myrctl_${VER}_linux_amd64.tar.gz"
KALL="${KHOST} LICENSE myriarch-updater_${VER}_linux_amd64.tar.gz.sbom.json myriarchd_${VER}_linux_amd64.tar.gz myriarch_${VER}_provenance.intoto.jsonl"
want_k() { printf '%s\n' $1 | LC_ALL=C sort | tr '\n' ' ' | sed 's/ $//'; }
kpublish() { # extra-args... (environment from the callers; the stub cosign is the first cosign)
  env PATH="${STUB}:${PATH}" TOOLS_BIN="${STUB}" STUB_LOG="${W}/cosign.log" STUB_EXPECT_IDENTITY="${KID}" \
    MIRROR_ALLOW_HTTP=1 MIRROR_REPO_URL="http://127.0.0.1:${PORT}/myriarch-releases" \
    "${MIRROR_DIR}/publish-mirror.sh" --keyless --dir "${K}" --version "${VER}" "$@"
}
puts() { grep '^PUT ' "${W}/server.log" | sed 's|^PUT /myriarch-releases/v[^/]*/||'; }
start_server artifactory
export MIRROR_TYPE=artifactory MIRROR_TOKEN=s3cret-token
unset MIRROR_PASSWORD MIRROR_USER
: > "${W}/cosign.log"
expect_ok "--keyless: verifies and uploads the host files, checksums.txt and the bundle" kpublish
if [ "$(stored)" = "$(want_k "${KHOST}")" ]; then ok "exactly the 8 files of the host scope are in the mirror (no .sig, no cosign.pub, no SBOM, no myriarchd archive, no provenance)"; else fail "file list differs: $(stored)"; fi
if cmp -s "${K}/checksums.txt" "${W}/store/myriarch-releases/v${VER}/checksums.txt" && cmp -s "${K}/checksums.txt.sigstore.json" "${W}/store/myriarch-releases/v${VER}/checksums.txt.sigstore.json"; then ok "checksums.txt and its bundle are uploaded unchanged"; else fail "checksums.txt or the bundle differs from the release"; fi
if [ "$(puts | tail -n 2 | tr '\n' ' ')" = "checksums.txt checksums.txt.sigstore.json " ]; then ok "order: checksums.txt, then the bundle last (the ready marker)"; else fail "upload order: $(puts | tr '\n' ' ')"; fi
grep -qx -- "${KID}" "${W}/cosign.log" && grep -qx -- "${ISSUER}" "${W}/cosign.log" && ok "the bundle was verified against the identity of release.yml at v${VER}" || fail "cosign was not called with the identity of the tag"
expect_ok "--keyless: second run, everything unchanged" kpublish
if printf '%s' "${OUT}" | grep -q '0 uploaded, 8 unchanged'; then ok "  ... 0 uploaded, 8 unchanged"; else fail "the second run uploaded something"; fi
expect_ok "verify-release.sh --keyless against the mirror (--base-url, --assets)" env PATH="${STUB}:${PATH}" TOOLS_BIN="${STUB}" STUB_LOG="${W}/cosign.log" STUB_EXPECT_IDENTITY="${KID}" \
  VERIFY_AUTH_HEADER="Authorization: Bearer s3cret-token" \
  "${MIRROR_DIR}/verify-release.sh" --keyless --version "${VER}" --base-url "http://127.0.0.1:${PORT}/myriarch-releases/v${VER}" --assets

# a file with the same name and other content is never overwritten (the release files are immutable)
mkdir -p "${W}/k2"; cp "${K}"/* "${W}/k2/"
printf 'changed updater\n' > "${W}/k2/myriarch-updater_${VER}_linux_amd64"
( cd "${W}/k2" && for f in $(printf '%s\n' * | LC_ALL=C sort); do case "${f}" in checksums.txt*|myriarch_*_provenance*) continue ;; esac; printf '%s  %s\n' "$(sha256_of "${f}")" "${f}"; done > checksums.k && mv checksums.k checksums.txt )
expect_fail "--keyless: a differing SHA-256 for an existing file => abort" "mirror_content_differs" \
  env PATH="${STUB}:${PATH}" TOOLS_BIN="${STUB}" STUB_LOG="${W}/cosign.log" STUB_EXPECT_IDENTITY="${KID}" MIRROR_ALLOW_HTTP=1 MIRROR_REPO_URL="http://127.0.0.1:${PORT}/myriarch-releases" \
  "${MIRROR_DIR}/publish-mirror.sh" --keyless --dir "${W}/k2" --version "${VER}"
if cmp -s "${K}/myriarch-updater_${VER}_linux_amd64" "${W}/store/myriarch-releases/v${VER}/myriarch-updater_${VER}_linux_amd64"; then ok "  ... the existing file in the mirror stayed unchanged"; else fail "the file in the mirror was overwritten"; fi

# a file changed after signing, a bundle for another tag, dry run, scope: nothing is uploaded
start_server artifactory
mkdir -p "${W}/k3"; cp "${K}"/* "${W}/k3/"; printf 'tamper\n' >> "${W}/k3/myrctl_${VER}_linux_amd64.tar.gz"
expect_fail "--keyless: a file changed after signing => abort before the upload" "checksum_mismatch" \
  env PATH="${STUB}:${PATH}" TOOLS_BIN="${STUB}" STUB_LOG="${W}/cosign.log" STUB_EXPECT_IDENTITY="${KID}" MIRROR_ALLOW_HTTP=1 MIRROR_REPO_URL="http://127.0.0.1:${PORT}/myriarch-releases" \
  "${MIRROR_DIR}/publish-mirror.sh" --keyless --dir "${W}/k3" --version "${VER}"
expect_fail "--keyless: a bundle that does not belong to this tag => abort before the upload" "signature_invalid" \
  env PATH="${STUB}:${PATH}" TOOLS_BIN="${STUB}" STUB_LOG="${W}/cosign.log" STUB_EXPECT_IDENTITY="https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v9.9.9" MIRROR_ALLOW_HTTP=1 MIRROR_REPO_URL="http://127.0.0.1:${PORT}/myriarch-releases" \
  "${MIRROR_DIR}/publish-mirror.sh" --keyless --dir "${K}" --version "${VER}"
expect_ok "--keyless --dry-run" kpublish --dry-run
if [ -z "$(stored)" ]; then ok "  ... nothing was uploaded (failures and dry run)"; else fail "uploaded despite a failure or --dry-run: $(stored)"; fi
export SCOPE=all
expect_ok "--keyless with SCOPE=all uploads every file that checksums.txt lists and the provenance file" kpublish
unset SCOPE
if [ "$(stored)" = "$(want_k "${KALL}")" ]; then ok "  ... 12 files, bundle and checksums.txt last"; else fail "file list differs: $(stored)"; fi
if [ "$(puts | tail -n 2 | tr '\n' ' ')" = "checksums.txt checksums.txt.sigstore.json " ]; then ok "  ... the bundle is the last upload also with SCOPE=all"; else fail "upload order: $(puts | tr '\n' ' ')"; fi
: > "${W}/cosign.log"
start_server artifactory
cp "${K}/checksums.txt.sigstore.json" "${W}/trusted_root.json"
expect_ok "--keyless --trusted-root is passed to cosign" kpublish --trusted-root "${W}/trusted_root.json"
grep -qx -- '--trusted-root' "${W}/cosign.log" && grep -qx -- "${W}/trusted_root.json" "${W}/cosign.log" && ok "  ... --trusted-root <file> is in the cosign call" || fail "--trusted-root did not reach cosign"

# options
expect_fail "--keyless takes no --dist" "not from --dist" kpublish --dist "${D}"
expect_fail "--dir belongs to --keyless" "belongs to --keyless" env MIRROR_REPO_URL="https://x.example.com/r" "${MIRROR_DIR}/publish-mirror.sh" --dist "${D}" --dir "${K}" --version "${VER}"
expect_fail "--keyless needs --dir" "needs --dir" env "${MIRROR_DIR}/publish-mirror.sh" --keyless --version "${VER}"
expect_fail "--keyless needs --version" "needs --version" env "${MIRROR_DIR}/publish-mirror.sh" --keyless --dir "${K}"
expect_fail "--keyless: an invalid version" "invalid --version" env "${MIRROR_DIR}/publish-mirror.sh" --keyless --dir "${K}" --version "1.2"
expect_fail "--keyless: an invalid SCOPE" "scope_invalid" env SCOPE=everything "${MIRROR_DIR}/publish-mirror.sh" --keyless --dir "${K}" --version "${VER}"
expect_fail "--keyless: a mirror over http:// is rejected" "mirror_https_required" env MIRROR_REPO_URL="http://127.0.0.1:${PORT}/myriarch-releases" "${MIRROR_DIR}/publish-mirror.sh" --keyless --dir "${K}" --version "${VER}"

# real cosign (skipped when none is installed): a GENUINE bundle of another identity is refused and nothing is uploaded
REAL_COSIGN=""
for c in "${TOOLS_BIN:-}/cosign" "$(command -v cosign 2>/dev/null || true)"; do [ -n "${c}" ] && [ -x "${c}" ] && [ "${c}" != "${STUB}/cosign" ] && { REAL_COSIGN="${c}"; break; }; done
FIX="${MIRROR_DIR}/../../internal/updater/testdata/sigstore"
if [ -z "${REAL_COSIGN}" ]; then
  echo "  skip  real cosign not found (make tools TOOLS=cosign): the case with the OpenTelemetry fixture is skipped"
  [ "${CI:-}" != true ] || fail "the real-cosign cases are required in CI"
else
  start_server artifactory
  RC="${W}/realcosign"; mkdir -p "${RC}/bin" "${RC}/home" "${RC}/rel"
  ln -s "${REAL_COSIGN}" "${RC}/bin/cosign"
  cp "${FIX}/otelcol-contrib_0.162.0_linux_amd64.tar.gz.sha256" "${RC}/rel/checksums.txt"
  cp "${FIX}/otelcol-contrib_0.162.0_linux_amd64.tar.gz.sha256.sigstore.json" "${RC}/rel/checksums.txt.sigstore.json"
  expect_fail "real cosign: a genuine bundle of another identity is refused" "signature_invalid.*expected SAN value" \
    env PATH="${RC}/bin:/usr/bin:/bin" HOME="${RC}/home" TUF_ROOT="${RC}/home/tuf" TOOLS_BIN="${RC}/bin" MIRROR_ALLOW_HTTP=1 MIRROR_REPO_URL="http://127.0.0.1:${PORT}/myriarch-releases" \
    "${MIRROR_DIR}/publish-mirror.sh" --keyless --dir "${RC}/rel" --version "${VER}" --trusted-root "${FIX}/trusted_root.json"
  if [ -z "$(stored)" ]; then ok "  ... nothing was uploaded"; else fail "uploaded despite a bundle of another identity"; fi
fi

finish_tests "publish-mirror-test"
