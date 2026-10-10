# shellcheck shell=sh
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Extra CA for ALL clients of a job or shell (go, crane, cosign, helm, goreleaser, node/pnpm/corepack, curl, python, git).
#
# Source it explicitly with `. contrib/enterprise-mirror/ca-env.sh` (POSIX sh, prints nothing). ci/lib.sh does not
# source it any more.
# Source of the CA: EXTRA_CA_FILE (a PEM file). Without it nothing happens. With it:
#   * SSL_CERT_FILE  = system bundle + extra CA (Go replaces the system roots when SSL_CERT_FILE is set, hence the
#                      system bundle comes first) - applies to go, crane, cosign, helm, goreleaser, kustomize, ...
#   * NODE_EXTRA_CA_CERTS = the extra CA (Node adds it to its built-in roots) - corepack/pnpm/npm
#   * CURL_CA_BUNDLE, REQUESTS_CA_BUNDLE, GIT_SSL_CAINFO = the same bundle (curl, Python/pip, git)
# Idempotent: sourcing it a second time in the same shell does nothing.

if [ -z "${EXTRA_CA_BUNDLE_READY:-}" ]; then
  _ci_ca="${EXTRA_CA_FILE:-}"
  if [ -n "${_ci_ca}" ] && [ -r "${_ci_ca}" ]; then
    _ci_sys=""
    for _f in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/cert.pem /etc/ssl/ca-bundle.pem; do
      if [ -r "${_f}" ]; then _ci_sys="${_f}"; break; fi
    done
    _ci_bundle="$(mktemp "${TMPDIR:-/tmp}/extra-ca-bundle.XXXXXX")"
    { [ -n "${_ci_sys}" ] && cat "${_ci_sys}"; cat "${_ci_ca}"; } > "${_ci_bundle}" 2>/dev/null
    chmod 0644 "${_ci_bundle}"
    SSL_CERT_FILE="${_ci_bundle}"; CURL_CA_BUNDLE="${_ci_bundle}"; REQUESTS_CA_BUNDLE="${_ci_bundle}"; GIT_SSL_CAINFO="${_ci_bundle}"
    NODE_EXTRA_CA_CERTS="${_ci_ca}"
    EXTRA_CA_BUNDLE_READY=1
    export SSL_CERT_FILE CURL_CA_BUNDLE REQUESTS_CA_BUNDLE GIT_SSL_CAINFO NODE_EXTRA_CA_CERTS EXTRA_CA_BUNDLE_READY
    unset _ci_sys _f _ci_bundle
  fi
  unset _ci_ca
fi
