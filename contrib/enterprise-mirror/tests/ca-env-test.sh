#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016  # "A && ok || fail": ok/fail are counters; single quotes are intentional (code for bash -c)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test for contrib/enterprise-mirror/ca-env.sh: the extra CA (EXTRA_CA_FILE) must reach Node (NODE_EXTRA_CA_CERTS) and Python/OpenSSL
# (SSL_CERT_FILE); without a CA file nothing may change. Runs against a local HTTPS server with its own CA.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIRROR_DIR="$(cd "${HERE}/.." && pwd)"
# shellcheck source=ci/tests/lib-test.sh
. "${HERE}/../../../ci/tests/lib-test.sh"
set -u
have openssl || { echo "openssl is missing: test skipped"; exit 0; }
d="${TEST_TMP}/ca"; mkdir -p "${d}"
(
  cd "${d}"
  openssl req -x509 -newkey rsa:2048 -nodes -keyout ca.key -out ca.crt -days 2 -subj "/CN=ci-test-ca" -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" -addext "subjectKeyIdentifier=hash" >/dev/null 2>&1
  openssl req -newkey rsa:2048 -nodes -keyout srv.key -out srv.csr -subj "/CN=localhost" >/dev/null 2>&1
  printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\nauthorityKeyIdentifier=keyid\n' > ext.cnf
  openssl x509 -req -in srv.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out srv.crt -days 2 -extfile ext.cnf >/dev/null 2>&1
) || { fail "CA creation"; finish_tests ca-env-test; exit 1; }

# Without a CA file: no change
out="$(env -u EXTRA_CA_FILE -u EXTRA_CA_BUNDLE_READY -u SSL_CERT_FILE sh -c '. "$1"; printf "%s|%s" "${SSL_CERT_FILE:-}" "${NODE_EXTRA_CA_CERTS:-}"' _ "${MIRROR_DIR}/ca-env.sh")"
[ "${out}" = "|" ] && ok "without a CA file the environment stays unchanged" || fail "changed without a CA file: ${out}"

# With a CA file: the bundle contains the CA and (if present) the system bundle
out="$(EXTRA_CA_FILE="${d}/ca.crt" env -u EXTRA_CA_BUNDLE_READY sh -c '. "$1"; printf "%s|%s|%s|%s" "${SSL_CERT_FILE}" "${NODE_EXTRA_CA_CERTS}" "${CURL_CA_BUNDLE}" "${GIT_SSL_CAINFO}"' _ "${MIRROR_DIR}/ca-env.sh")"
IFS='|' read -r b n c g <<< "${out}"
[ -r "${b}" ] && grep -q -- "-----BEGIN CERTIFICATE-----" "${b}" && cmp -s <(tail -n "$(wc -l < "${d}/ca.crt")" "${b}") "${d}/ca.crt" \
  && ok "the SSL_CERT_FILE bundle ends with the internal CA" || fail "bundle ${b} does not contain the CA"
[ "${n}" = "${d}/ca.crt" ] && ok "NODE_EXTRA_CA_CERTS points to the internal CA" || fail "NODE_EXTRA_CA_CERTS=${n}"
[ "${c}" = "${b}" ] && [ "${g}" = "${b}" ] && ok "curl and git use the same bundle" || fail "CURL_CA_BUNDLE/GIT_SSL_CAINFO differ"
for sys in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/cert.pem; do
  if [ -r "${sys}" ]; then
    [ "$(wc -l < "${b}")" -gt "$(wc -l < "${d}/ca.crt")" ] && ok "system certificates stay in the bundle (${sys})" || fail "the system bundle is missing from the bundle"
    break
  fi
done

# Functional test against an HTTPS server with the internal CA
if have python3; then
  cat > "${TEST_TMP}/srv.py" <<PY
import http.server, ssl, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.end_headers(); self.wfile.write(b"ok")
    def log_message(self, *a): pass
s = http.server.HTTPServer(("127.0.0.1", 0), H)
c = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER); c.load_cert_chain("${d}/srv.crt", "${d}/srv.key")
s.socket = c.wrap_socket(s.socket, server_side=True)
open("${TEST_TMP}/port", "w").write(str(s.server_address[1]))
s.serve_forever()
PY
  python3 "${TEST_TMP}/srv.py" & srv=$!; disown "${srv}" 2>/dev/null
  for _ in $(seq 1 50); do [ -s "${TEST_TMP}/port" ] && break; sleep 0.1; done
  port="$(cat "${TEST_TMP}/port" 2>/dev/null)"
  trap 'kill ${srv} 2>/dev/null; cleanup_tests' EXIT
  if [ -n "${port}" ]; then
    py='import urllib.request,sys; print(urllib.request.urlopen(sys.argv[1]).read().decode())'
    if env -u SSL_CERT_FILE python3 -c "${py}" "https://localhost:${port}/" >/dev/null 2>&1; then
      warn "Python trusts the test CA without configuration (weak test)"
    fi
    # Some Python builds ignore SSL_CERT_FILE (for example the macOS Command Line Tools Python, which links LibreSSL), so
    # ca-env.sh cannot reach them. Probe that first: with SSL_CERT_FILE honored, the default context holds the test CA.
    probe='import ssl,sys; c=ssl.create_default_context(); sys.exit(0 if any("ci-test-ca" in str(x.get("subject")) for x in c.get_ca_certs()) else 1)'
    if SSL_CERT_FILE="${d}/ca.crt" python3 -c "${probe}" 2>/dev/null; then
      out="$(EXTRA_CA_FILE="${d}/ca.crt" env -u EXTRA_CA_BUNDLE_READY sh -c '. "$1"; python3 -c "$2" "$3"' _ "${MIRROR_DIR}/ca-env.sh" "${py}" "https://localhost:${port}/" 2>&1)"
      [ "${out}" = ok ] && ok "Python/OpenSSL connects with the internal CA" || fail "Python: ${out}"
    else
      warn "python3 ($(python3 -c 'import ssl; print(ssl.OPENSSL_VERSION)' 2>/dev/null)) ignores SSL_CERT_FILE: Python case skipped"
    fi
    if have node; then
      js='require("https").get(process.argv[1],r=>{let b="";r.on("data",x=>b+=x);r.on("end",()=>console.log(b))}).on("error",e=>{console.log("ERR "+e.code);process.exit(1)})'
      if env -u NODE_EXTRA_CA_CERTS node -e "${js}" "https://localhost:${port}/" >/dev/null 2>&1; then
        warn "Node trusts the test CA without configuration (weak test)"
      fi
      out="$(EXTRA_CA_FILE="${d}/ca.crt" env -u EXTRA_CA_BUNDLE_READY sh -c '. "$1"; node -e "$2" "$3"' _ "${MIRROR_DIR}/ca-env.sh" "${js}" "https://localhost:${port}/" 2>&1)"
      [ "${out}" = ok ] && ok "Node connects with the internal CA" || fail "Node: ${out}"
    fi
  else
    fail "the test HTTPS server did not start"
  fi
fi
finish_tests ca-env-test
