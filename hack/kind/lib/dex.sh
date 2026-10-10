#!/usr/bin/env bash
# shellcheck shell=bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# The Dex quickstart of the kind probe (sourced after lib/common.sh, install.sh and api.sh): a test CA and certificate, the
# upstream Dex chart with deploy/helm/myriarch/examples/dex/dex.yaml, a CoreDNS rewrite for dex.example.test, the myriarch
# release with examples/quickstart-dex.yaml, and the OIDC login with curl (no browser, no /etc/hosts edit: the name is
# resolved with a CoreDNS rewrite inside the cluster and curl --connect-to on the workstation).
#
# Passwords and the client secret are generated into $PROBE_WORK/secrets and handed over through files only.

DEX_HOST=dex.example.test
DEX_PORT=5554
DEX_DIR="${PROBE_WORK}/dex"
DEX_CERT_DAYS=7

dex_secret() { secret_path "$1"; }

# ---- certificates ------------------------------------------------------------------------------------------------
dex_make_certs() {
  local ossl d="${DEX_DIR}"
  ossl="$(openssl_bin)"
  mkdir -p "${d}"
  chmod 700 "${d}"
  (
    umask 077
    "${ossl}" req -x509 -newkey rsa:2048 -nodes -keyout "${d}/ca.key" -out "${d}/ca.crt" -days "${DEX_CERT_DAYS}" \
      -subj "/CN=myriarch probe test CA" \
      -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" >/dev/null 2>&1 || return 1
    "${ossl}" req -newkey rsa:2048 -nodes -keyout "${d}/tls.key" -out "${d}/tls.csr" -subj "/CN=${DEX_HOST}" >/dev/null 2>&1 || return 1
    cat > "${d}/server.ext" <<EOF
subjectAltName=DNS:${DEX_HOST}
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
EOF
    "${ossl}" x509 -req -in "${d}/tls.csr" -CA "${d}/ca.crt" -CAkey "${d}/ca.key" -CAcreateserial -out "${d}/tls.crt" \
      -days "${DEX_CERT_DAYS}" -extfile "${d}/server.ext" >/dev/null 2>&1 || return 1
  ) || return 1
  "${ossl}" verify -CAfile "${d}/ca.crt" "${d}/tls.crt" >/dev/null 2>&1
}

# ---- values ------------------------------------------------------------------------------------------------------
# dex_make_values: secrets/dex-values.yaml from examples/dex/dex.yaml with the client secret and the bcrypt hash filled in
dex_make_values() {
  (
    umask 077
    [ -s "$(dex_secret dex-client-secret)" ] || random_token 40 | tr -d '\n' > "$(dex_secret dex-client-secret)"
    [ -s "$(dex_secret dex-password)" ] || random_token 24 | tr -d '\n' > "$(dex_secret dex-password)"
    "${ROOT}/bin/myriarchd" auth hash-password < "$(dex_secret dex-password)" | tr -d '\n' > "$(dex_secret dex-password-hash)"
    yq ".config.staticClients[0].secret = load_str(\"$(dex_secret dex-client-secret)\") | .config.staticPasswords[0].hash = load_str(\"$(dex_secret dex-password-hash)\")" \
      "${CHART_DIR}/examples/dex/dex.yaml" > "$(dex_secret dex-values.yaml)"
  )
  # the comments of the example mention the placeholders: check the values themselves
  [ -s "$(dex_secret dex-values.yaml)" ] \
    && [ "$(yq '.config.staticClients[0].secret' "$(dex_secret dex-values.yaml)")" != "<client secret>" ] \
    && [ "$(yq '.config.staticPasswords[0].hash' "$(dex_secret dex-values.yaml)" | cut -c1-4)" = '$2a$' ]
}

# ---- Dex chart ---------------------------------------------------------------------------------------------------
dex_pull_chart() { # sets DEX_TGZ
  local dir="${PROBE_WORK}/dex-chart" got
  mkdir -p "${dir}"
  DEX_TGZ="${dir}/dex-${DEX_CHART_VERSION}.tgz"
  if [ ! -s "${DEX_TGZ}" ]; then
    helm pull dex --repo https://charts.dexidp.io --version "${DEX_CHART_VERSION}" --destination "${dir}" >/dev/null 2>&1 || return 1
  fi
  got="$(sha256_file "${DEX_TGZ}")"
  [ "${got}" = "${DEX_CHART_SHA256}" ]
}

dex_install() {
  local repo digest
  repo="${DEX_IMAGE%%:*}"
  digest="${DEX_IMAGE##*@}"
  # ghcr.io/dexidp/dex:v2.46.0@sha256:... -> repository ghcr.io/dexidp/dex, digest sha256:...
  ensure_namespace "${NS_DEXIDP}"
  kn "${NS_DEXIDP}" create secret tls dex-tls --cert="${DEX_DIR}/tls.crt" --key="${DEX_DIR}/tls.key" --dry-run=client -o yaml | k apply -f - >/dev/null
  h install dex "${DEX_TGZ}" -n "${NS_DEXIDP}" -f "$(dex_secret dex-values.yaml)" \
    --set "image.repository=${repo}" --set "image.digest=${digest}" > "${PROBE_WORK}/logs/dex-install.log" 2>&1
}

# ---- CoreDNS -----------------------------------------------------------------------------------------------------
# coredns_add_rewrite: dex.example.test -> the Service of Dex, before the kubernetes plugin (idempotent)
coredns_add_rewrite() {
  local corefile new
  corefile="$(k -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}')"
  if printf '%s' "${corefile}" | grep -q "rewrite name ${DEX_HOST} "; then return 0; fi
  new="$(printf '%s\n' "${corefile}" | awk -v host="${DEX_HOST}" -v svc="dex.${NS_DEXIDP}.svc.cluster.local" '
    /^[[:space:]]*kubernetes[[:space:]]/ && !done { printf "    rewrite name %s %s\n", host, svc; done = 1 }
    { print }')"
  printf '%s' "${new}" | grep -q "rewrite name ${DEX_HOST} " || return 1
  k -n kube-system get configmap coredns -o json | jq --arg c "${new}" '.data.Corefile = $c' | k replace -f - >/dev/null || return 1
  k -n kube-system rollout restart deployment/coredns >/dev/null && k -n kube-system rollout status deployment/coredns --timeout=120s >/dev/null
}

coredns_remove_rewrite() {
  [ -f "${KUBECONFIG}" ] || return 0
  local corefile new
  corefile="$(k -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}' 2>/dev/null)" || return 0
  printf '%s' "${corefile}" | grep -q "rewrite name ${DEX_HOST} " || return 0
  new="$(printf '%s\n' "${corefile}" | grep -v "rewrite name ${DEX_HOST} ")"
  k -n kube-system get configmap coredns -o json | jq --arg c "${new}" '.data.Corefile = $c' | k replace -f - >/dev/null || return 1
  k -n kube-system rollout restart deployment/coredns >/dev/null
  k -n kube-system rollout status deployment/coredns --timeout=120s >/dev/null
}

# ---- myriarch with Dex ---------------------------------------------------------------------------------------------
dex_myriarch_install() {
  ensure_namespace "${NS_DEX}" restricted
  apply_secret "${NS_DEX}" myriarch-secrets "database-dsn=$(secret_path database-dsn-dex)" "oidc-client-secret=$(dex_secret dex-client-secret)"
  kn "${NS_DEX}" create configmap dex-ca --from-file="ca.crt=${DEX_DIR}/ca.crt" --dry-run=client -o yaml | k apply -f - >/dev/null
  h install myriarch "${CHART_DIR}" -n "${NS_DEX}" -f "${CHART_DIR}/examples/quickstart-dex.yaml" -f "${KIND_DIR}/values/dex-overlay.yaml" \
    --set "image.repository=${SERVER_REPO}" --set "image.digest=${SERVER_DIGEST}" > "${PROBE_WORK}/logs/dex-myriarch-install.log" 2>&1
}

# ---- the login ---------------------------------------------------------------------------------------------------
# curl_dex ARGS...: a request to Dex through the port-forward, verified against the test CA
curl_dex() {
  curl -s -m 30 --cacert "${DEX_DIR}/ca.crt" --connect-to "${DEX_HOST}:${DEX_PORT}:127.0.0.1:${PROBE_PORT_DEX}" "$@"
}

# header_value FILE NAME -> value of the last header with that name (CR removed)
header_value() { awk -v n="$(printf '%s' "$2" | tr '[:upper:]' '[:lower:]')" 'BEGIN{FS=": "} { k=tolower($1); if (k==n) { v=$0; sub(/^[^:]*: /,"",v); gsub(/\r/,"",v); last=v } } END{print last}' "$1"; }

# dex_login JAR: the OIDC authorization code flow of a browser, with curl. Prints the final HTTP status of the callback.
# JAR is the cookie jar of the myriarch session; the Dex cookies live in a separate jar.
dex_login() {
  local jar="$1" dexjar="${DEX_DIR}/cookies-dex" hdr="${DEX_DIR}/headers.txt" body="${DEX_DIR}/body.html"
  local loc action origin
  rm -f "${dexjar}" "${hdr}" "${body}"
  # 1. myriarch starts the flow
  curl -s -m 20 -o /dev/null -D "${hdr}" -c "${jar}" "${API_BASE}/api/v1/auth/login?redirect=/" || return 1
  loc="$(header_value "${hdr}" location)"
  case "${loc}" in "https://${DEX_HOST}:${DEX_PORT}/dex/auth"*) ;; *) echo "start:unexpected-location"; return 1 ;; esac
  # 2. Dex: authorization endpoint -> connector chooser or login form (redirects stay on Dex)
  local hops=0 url="${loc}"
  while [ "${hops}" -lt 6 ]; do
    hops=$((hops + 1))
    curl_dex -o "${body}" -D "${hdr}" -b "${dexjar}" -c "${dexjar}" "${url}" || return 1
    loc="$(header_value "${hdr}" location)"
    [ -n "${loc}" ] || break
    case "${loc}" in
      /*) url="https://${DEX_HOST}:${DEX_PORT}${loc}" ;;
      *) url="${loc}" ;;
    esac
  done
  # 3. the login form: post the demo user's credentials
  action="$(sed -n 's/.*<form[^>]* action="\([^"]*\)".*/\1/p' "${body}" | head -n 1 | sed 's/&amp;/\&/g')"
  [ -n "${action}" ] || { echo "form:not-found"; return 1; }
  case "${action}" in /*) action="https://${DEX_HOST}:${DEX_PORT}${action}" ;; esac
  curl_dex -o "${body}" -D "${hdr}" -b "${dexjar}" -c "${dexjar}" -X POST \
    --data-urlencode "login=admin@example.test" --data-urlencode "password@$(dex_secret dex-password)" "${action}" || return 1
  # 4. follow Dex until the redirect goes to the myriarch callback
  hops=0
  while [ "${hops}" -lt 8 ]; do
    hops=$((hops + 1))
    loc="$(header_value "${hdr}" location)"
    [ -n "${loc}" ] || { echo "redirect:none"; return 1; }
    case "${loc}" in
      https://myriarch.example.test/*) break ;;
      /*) loc="https://${DEX_HOST}:${DEX_PORT}${loc}" ;;
    esac
    curl_dex -o "${body}" -D "${hdr}" -b "${dexjar}" -c "${dexjar}" "${loc}" || return 1
  done
  case "${loc}" in https://myriarch.example.test/*) ;; *) echo "redirect:no-callback"; return 1 ;; esac
  # 5. the callback on the port-forward (the public URL is a placeholder host)
  origin="${API_BASE}"
  curl -s -m 30 -o /dev/null -w '%{http_code}' -b "${jar}" -c "${jar}" "${origin}${loc#https://myriarch.example.test}"
}
