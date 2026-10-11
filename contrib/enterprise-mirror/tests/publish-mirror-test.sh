#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail" is intended in the tests (ok/fail are counters and never fail)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests contrib/enterprise-mirror/publish-mirror.sh and verify-release.sh against a throw-away mirror (put-server.py, Python 3) for
# Artifactory (Bearer) and Nexus (Basic): upload, idempotency, immutability, abort on a differing SHA-256,
# invalid signature, wrong credentials. Errors are matched by their stable tokens, not by prose.
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

finish_tests "publish-mirror-test"
