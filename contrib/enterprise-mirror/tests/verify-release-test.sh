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

# a tampered file behind the redirect must be detected
printf 'tampered\n' > "${REL}/myrctl_${VER}_linux_amd64.tar.gz"
expect_fail "tampered file behind the redirect is rejected" "checksum_mismatch" vr
finish_tests verify-release-test
