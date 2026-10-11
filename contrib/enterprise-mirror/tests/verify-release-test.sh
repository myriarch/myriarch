#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters and never fail
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test for contrib/enterprise-mirror/verify-release.sh against a throw-away artifact store that redirects downloads
# to a second listener ("storage", like object storage behind an artifact repository):
#  - verify-release.sh follows the 302 redirects and verifies checksums.txt, every file and every signature
#  - the files really come from the storage listener
#  - the Authorization header (VERIFY_AUTH_HEADER) reaches the origin but never the host a redirect points to
# Signatures are made with openssl (format contract: base64 DER-ECDSA over SHA-256, see contrib/enterprise-mirror/signing.md), so
# cosign is not needed.
# The keyless mode (--keyless: checksums.txt.sigstore.json against the identity of release.yml at the tag) is tested with a
# cosign stub (cosign-stub.sh) that logs its arguments and refuses the identity of another tag, and, when a real cosign is available (TOOLS_BIN
# or PATH), with the committed OpenTelemetry fixture: a genuine bundle of another identity must be refused.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIRROR_DIR="$(cd "${HERE}/.." && pwd)"
# shellcheck source=ci/tests/lib-test.sh
. "${HERE}/../../../ci/tests/lib-test.sh"
set -u
have openssl && have python3 && have curl || { echo "openssl/python3/curl are missing: test skipped"; exit 0; }
W="${TEST_TMP}"; VER=0.1.0
SERVER_PID=""
stop_server() { if [ -n "${SERVER_PID}" ]; then kill "${SERVER_PID}" 2>/dev/null || true; wait "${SERVER_PID}" 2>/dev/null || true; SERVER_PID=""; fi; }
trap 'stop_server; cleanup_tests' EXIT

# ---- signed release fixture ---------------------------------------------------------------------------
REL="${W}/release"; mkdir -p "${REL}"
openssl ecparam -name prime256v1 -genkey -noout -out "${W}/key.pem" 2>/dev/null
openssl ec -in "${W}/key.pem" -pubout -out "${W}/cosign.pub" 2>/dev/null
sign() { openssl dgst -sha256 -sign "${W}/key.pem" -out "$1.der" "$1" && openssl base64 -A -in "$1.der" -out "$1.sig" && rm -f "$1.der"; }
ASSETS="myriarch-updater_${VER}_linux_amd64.tar.gz myrctl_${VER}_linux_amd64.tar.gz"
for f in ${ASSETS}; do printf 'content of %s\n' "$f" > "${REL}/$f"; sign "${REL}/$f"; done
( cd "${REL}" && for f in ${ASSETS}; do printf '%s  %s\n' "$(sha256_of "$f")" "$f"; done > checksums.txt )
sign "${REL}/checksums.txt"

# ---- origin + storage ---------------------------------------------------------------------------------
cat > "${W}/server.py" <<'PY'
import http.server, os, sys, threading

root, log_path, origin_port_file, storage_port_file = sys.argv[1:5]
lock = threading.Lock()

def log(who, path, auth):
    with lock, open(log_path, "a") as f:
        f.write("%s GET %s auth=%s\n" % (who, path, auth or "-"))

class Storage(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        log("STORAGE", self.path, self.headers.get("Authorization"))
        p = os.path.join(root, os.path.basename(self.path))
        if not os.path.isfile(p):
            self.send_response(404); self.end_headers(); return
        data = open(p, "rb").read()
        self.send_response(200); self.send_header("Content-Length", str(len(data))); self.end_headers(); self.wfile.write(data)
    def log_message(self, *a): pass

storage = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Storage)

class Origin(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        log("ORIGIN", self.path, self.headers.get("Authorization"))
        name = os.path.basename(self.path)
        if not os.path.isfile(os.path.join(root, name)):
            self.send_response(404); self.end_headers(); return
        self.send_response(302)
        self.send_header("Location", "http://127.0.0.1:%d/blobs/%s" % (storage.server_address[1], name))
        self.send_header("Content-Length", "0"); self.end_headers()
    def log_message(self, *a): pass

origin = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Origin)
open(storage_port_file, "w").write(str(storage.server_address[1]))
open(origin_port_file, "w").write(str(origin.server_address[1]))
threading.Thread(target=storage.serve_forever, daemon=True).start()
origin.serve_forever()
PY
python3 "${W}/server.py" "${REL}" "${W}/access.log" "${W}/origin.port" "${W}/storage.port" &
SERVER_PID=$!
for _ in $(seq 1 50); do [ -s "${W}/origin.port" ] && [ -s "${W}/storage.port" ] && break; sleep 0.1; done
[ -s "${W}/origin.port" ] || { fail "test server did not start"; finish_tests verify-release-test; exit 1; }
ORIGIN="http://127.0.0.1:$(cat "${W}/origin.port")"

vr() { env -i PATH="${PATH}" HOME="${W}" VERIFY_AUTH_HEADER="Authorization: Bearer test-secret" \
  bash "${MIRROR_DIR}/verify-release.sh" --public-key "${W}/cosign.pub" --base-url "${ORIGIN}/releases/v${VER}" --assets; }

: > "${W}/access.log"
expect_ok "verify-release.sh --assets follows the 302 redirects and verifies everything" vr
grep -q '^STORAGE GET' "${W}/access.log" && ok "files came from the storage listener" || fail "the storage listener was not used"
grep -q '^ORIGIN GET .* auth=Bearer test-secret$' "${W}/access.log" && ok "the auth header reaches the origin" || fail "no auth header at the origin"
grep -q '^STORAGE GET .* auth=-$' "${W}/access.log" && ok "storage requests carry no Authorization header" || fail "storage request without the expected log entry"
grep -q '^STORAGE GET .* auth=[^-]' "${W}/access.log" && fail "the auth header was sent to the storage host of a redirect" || ok "the auth header never reaches the storage host"

# ---- keyless mode ---------------------------------------------------------------------------------------
echo "verify-release.sh --keyless"
STUB="${W}/stubbin"; mkdir -p "${STUB}"
cp "${HERE}/cosign-stub.sh" "${STUB}/cosign"; chmod +x "${STUB}/cosign"
ID_OF() { printf 'https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v%s' "$1"; }
ISSUER=https://token.actions.githubusercontent.com
printf '{"stub":"bundle"}\n' > "${REL}/checksums.txt.sigstore.json"
KREL="${W}/krelease"; mkdir -p "${KREL}"
cp "${REL}/checksums.txt" "${REL}/checksums.txt.sigstore.json" "${KREL}/"
for f in ${ASSETS}; do cp "${REL}/$f" "${KREL}/$f"; done   # no .sig files: the keyless mode needs none
kv() { # args... (stub cosign first in TOOLS_BIN and PATH)
  env -i PATH="${STUB}:${PATH}" HOME="${W}" TOOLS_BIN="${STUB}" STUB_LOG="${W}/cosign.log" \
    STUB_EXPECT_IDENTITY="${STUB_EXPECT_IDENTITY:-$(ID_OF "${VER}")}" STUB_COSIGN_VERSION="${STUB_COSIGN_VERSION:-v3.1.3}" \
    bash "${MIRROR_DIR}/verify-release.sh" "$@"
}
: > "${W}/cosign.log"
expect_ok "--keyless --dir --assets verifies the bundle and the hashes (no .sig files needed)" kv --keyless --version "${VER}" --dir "${KREL}" --assets
want="$(printf '%s\n' verify-blob --bundle "<in>/checksums.txt.sigstore.json" --certificate-identity "$(ID_OF "${VER}")" --certificate-oidc-issuer "${ISSUER}" "<in>/checksums.txt" ----)"
got="$(sed -E 's|^/.*/in/|<in>/|' "${W}/cosign.log")"
if [ "${got}" = "${want}" ]; then ok "cosign verify-blob got exactly the bundle, the identity of the tag, the issuer and checksums.txt"; else fail "unexpected cosign arguments:"; printf '%s\n' "${got}" | sed 's/^/        /' >&2; fi
grep -q -- '--insecure' "${W}/cosign.log" && fail "an --insecure flag reached cosign" || ok "no --insecure flag reaches cosign"
: > "${W}/cosign.log"
cp "${KREL}/checksums.txt.sigstore.json" "${W}/trusted_root.json"
expect_ok "--trusted-root is passed through to cosign" kv --keyless --version "${VER}" --dir "${KREL}" --trusted-root "${W}/trusted_root.json"
grep -qx -- '--trusted-root' "${W}/cosign.log" && grep -qx -- "${W}/trusted_root.json" "${W}/cosign.log" && ok "  ... --trusted-root <file> is in the cosign call" || fail "--trusted-root did not reach cosign"
: > "${W}/cosign.log"
STUB_EXPECT_IDENTITY="$(ID_OF 0.2.0-rc.1)" expect_ok "a release candidate verifies against the identity of its own tag" kv --keyless --version v0.2.0-rc.1 --dir "${KREL}"
grep -qx -- "$(ID_OF 0.2.0-rc.1)" "${W}/cosign.log" && ok "  ... the identity is bound to v0.2.0-rc.1 (a leading v is accepted)" || fail "wrong identity for the release candidate"
expect_fail "a bundle of another tag is refused (identity of the version does not match)" "signature_invalid" kv --keyless --version 0.1.1 --dir "${KREL}"
expect_fail "--keyless needs --version" "needs --version" kv --keyless --dir "${KREL}"
expect_fail "an invalid version is refused" "invalid --version" kv --keyless --version "0.1.0;x" --dir "${KREL}"
expect_fail "--keyless and --public-key exclude each other" "exclude each other" kv --keyless --public-key "${W}/cosign.pub" --version "${VER}" --dir "${KREL}"
expect_fail "neither --keyless nor --public-key" "exactly one of --public-key" kv --version "${VER}" --dir "${KREL}"
expect_fail "--keyless does not work on --dist" "not on --dist" kv --keyless --version "${VER}" --dist "${W}"
expect_fail "--trusted-root belongs to --keyless" "belongs to --keyless" kv --public-key "${W}/cosign.pub" --trusted-root "${W}/trusted_root.json" --dir "${REL}"
mkdir -p "${W}/knobundle"; cp "${KREL}/checksums.txt" "${W}/knobundle/"
expect_fail "a missing bundle is reported" "checksums.txt.sigstore.json not found" kv --keyless --version "${VER}" --dir "${W}/knobundle"
STUB_COSIGN_VERSION=v3.0.3 expect_fail "an old cosign is refused" "cosign_outdated" kv --keyless --version "${VER}" --dir "${KREL}"
STUB_COSIGN_VERSION=v3.0.4-rc.1 expect_fail "a pre-release of the minimum cosign is refused" "cosign_outdated" kv --keyless --version "${VER}" --dir "${KREL}"
STUB_COSIGN_VERSION=v3.0.4 expect_ok "cosign 3.0.4 is accepted" kv --keyless --version "${VER}" --dir "${KREL}"
STUB_COSIGN_VERSION=v4.0.0 expect_ok "a newer major version is accepted" kv --keyless --version "${VER}" --dir "${KREL}"
expect_fail "cosign missing" "cosign_missing" env -i PATH="/usr/bin:/bin" HOME="${W}" TOOLS_BIN="${W}/empty-bin" \
  bash "${MIRROR_DIR}/verify-release.sh" --keyless --version "${VER}" --dir "${KREL}"
# the identity of the project is a constant of the script and equals the repository of internal/brand
repo_brand="$(sed -n 's|^[[:space:]]*Repository[[:space:]]*=[[:space:]]*"https://github.com/\([^"]*\)".*|\1|p' "${MIRROR_DIR}/../../internal/brand/brand.go")"
repo_script="$(sed -n 's|^readonly RELEASE_REPOSITORY="\(.*\)"$|\1|p' "${MIRROR_DIR}/verify-release.sh")"
if [ -n "${repo_brand}" ] && [ "${repo_brand}" = "${repo_script}" ]; then ok "RELEASE_REPOSITORY equals the repository of internal/brand (${repo_brand})"; else fail "RELEASE_REPOSITORY (${repo_script}) differs from internal/brand (${repo_brand})"; fi
# a file that was changed after the release was signed
cp -R "${KREL}" "${W}/ktamper"; printf 'tampered\n' > "${W}/ktamper/myrctl_${VER}_linux_amd64.tar.gz"
expect_fail "--keyless --assets: a tampered file is refused" "checksum_mismatch" kv --keyless --version "${VER}" --dir "${W}/ktamper" --assets
# through the origin that redirects to the storage: the files and the bundle come from the mirror, the auth header stays at the origin
: > "${W}/access.log"
expect_ok "--keyless --base-url follows the redirects and verifies everything" env VERIFY_AUTH_HEADER="Authorization: Bearer test-secret" \
  PATH="${STUB}:${PATH}" TOOLS_BIN="${STUB}" STUB_LOG="${W}/cosign.log" STUB_EXPECT_IDENTITY="$(ID_OF "${VER}")" \
  bash "${MIRROR_DIR}/verify-release.sh" --keyless --version "${VER}" --base-url "${ORIGIN}/releases/v${VER}" --assets
grep -q '^STORAGE GET .*checksums.txt.sigstore.json' "${W}/access.log" && ok "  ... the bundle came from the storage listener" || fail "the bundle was not fetched through the redirect"
grep -q '^STORAGE GET .* auth=[^-]' "${W}/access.log" && fail "the auth header was sent to the storage host of a redirect" || ok "  ... the auth header never reaches the storage host"

# real cosign (skipped when none is installed): a GENUINE bundle of another identity (OpenTelemetry, committed fixture of the
# updater tests) must be refused for the identity of Myriarch, and the positive control shows that the fixture verifies
REAL_COSIGN=""
for c in "${TOOLS_BIN}/cosign" "$(command -v cosign 2>/dev/null || true)"; do [ -n "${c}" ] && [ -x "${c}" ] && { REAL_COSIGN="${c}"; break; }; done
FIX="${MIRROR_DIR}/../../internal/updater/testdata/sigstore"
if [ -z "${REAL_COSIGN}" ]; then
  echo "  skip  real cosign not found (make tools TOOLS=cosign): the cases with the OpenTelemetry fixture are skipped"
  [ "${CI:-}" != true ] || fail "the real-cosign cases are required in CI"
else
  RC="${W}/realcosign"; mkdir -p "${RC}/bin" "${RC}/home" "${RC}/rel"
  ln -s "${REAL_COSIGN}" "${RC}/bin/cosign"
  cp "${FIX}/otelcol-contrib_0.162.0_linux_amd64.tar.gz.sha256" "${RC}/rel/checksums.txt"
  cp "${FIX}/otelcol-contrib_0.162.0_linux_amd64.tar.gz.sha256.sigstore.json" "${RC}/rel/checksums.txt.sigstore.json"
  expect_ok "real cosign: the fixture verifies with its own identity (positive control)" env HOME="${RC}/home" TUF_ROOT="${RC}/home/tuf" \
    "${REAL_COSIGN}" verify-blob --bundle "${RC}/rel/checksums.txt.sigstore.json" \
    --certificate-identity "https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/base-release.yaml@refs/tags/v0.162.0" \
    --certificate-oidc-issuer "${ISSUER}" --trusted-root "${FIX}/trusted_root.json" "${RC}/rel/checksums.txt"
  rv() { env -i PATH="${RC}/bin:/usr/bin:/bin" HOME="${RC}/home" TUF_ROOT="${RC}/home/tuf" TOOLS_BIN="${RC}/bin" \
    bash "${MIRROR_DIR}/verify-release.sh" --keyless --version "${VER}" --dir "${RC}/rel" --trusted-root "${FIX}/trusted_root.json"; }
  expect_fail "real cosign: a genuine bundle of another identity is refused" "signature_invalid.*expected SAN value" rv
  printf '0000\n' >> "${RC}/rel/checksums.txt"
  expect_fail "real cosign: a tampered checksums.txt is refused" "signature_invalid" rv
fi

# a tampered file behind the redirect must be detected
printf 'tampered\n' > "${REL}/myrctl_${VER}_linux_amd64.tar.gz"
expect_fail "tampered file behind the redirect is rejected" "checksum_mismatch" vr
finish_tests verify-release-test
