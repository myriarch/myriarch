#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# EXAMPLE - preparation of a production-style installation: namespaces, CA bundle, TLS secret, pull secret, the
# Secret "myriarch-secrets" and the secrets of the gateway collector.
#
# This script is NOT runnable without adaptation: it reads all values EXCLUSIVELY from files in $SECRETS_DIR (never
# from arguments or environment variables, so nothing ends up in the process list or the shell history) and aborts
# unless CONFIRM_EDITED is "yes". Check first:
#   * context/cluster:   kubectl config current-context
#   * variables below:   namespaces, registry, registry user, variants (OIDC_AUTH, MIRROR_AUTH, TLS_MODE)
#   * files in $SECRETS_DIR (table below) - create the directory with mode 0700 outside the repository
#
# Usage (example):
#   mkdir -m 0700 ~/myriarch-secrets   # put the files there (mode 0600)
#   CONFIRM_EDITED=yes SECRETS_DIR=~/myriarch-secrets deploy/examples/production/create-secrets.example.sh
#
# The script is repeatable (kubectl apply): running it again updates the secrets. After changing the Secret restart
# myriarchd (kubectl -n myriarch rollout restart deploy/myriarch): myriarchd reads most files only at startup (the mirror
# credentials are re-read on every request).
#
# Files in $SECRETS_DIR (key in the Secret = file name):
#   Required
#     database-dsn            postgres://myriarch:<password>@pg.example.com:5432/myriarch?sslmode=verify-full&sslrootcert=/etc/myriarchd/ca/ca.crt
#     git-token               read-only deploy token (read_repository) of the GitOps project (the user name is in the values)
#     github-token            GitHub fine-grained token, read access to public repositories only (release tracker)
#     slack-webhook-url       Slack incoming webhook URL (or the URL of the channel you configured)
#     ca.crt                  PEM of the CA bundle (ca.crt of your PKI, certificate chain, public) -> ConfigMap internal-ca
#     registry-token          registry token with read access -> pull secret registry-pull-secret
#   OIDC_AUTH=secret (default)   oidc-client-secret     client secret of the application registered at the identity provider
#   OIDC_AUTH=cert               oidc-client-cert, oidc-client-key   PEM certificate and key of the registered application
#   MIRROR_AUTH=token (default)  mirror-token           bearer access token of the artifact mirror
#   MIRROR_AUTH=basic            mirror-password        password or user token (the user name is in the values)
#   TLS_MODE=files (default)     tls.crt, tls.key       certificate (chain) and key of the Ingress host, issued by your CA
#   TLS_MODE=certmanager         (nothing: cert-manager issues the Secret, annotation see myriarch-values.yaml)
#   Optional
#     awx-token             token for the AWX executor (environment variable MYRIARCH_EXEC_AWX_TOKEN)
#     enrollment-token      enrollment token (myre_...) of the gateway collector, created in the myriarchd UI
#                           (only AFTER myriarchd is installed; without the file the gateway secret is skipped)
#     gateway-backend-token value for BACKEND_API_TOKEN of the gateway (a dummy is enough while the gateway
#                           only exports with the debug exporter)
#     bootstrap-password    password (12 to 72 characters) of the break-glass bootstrap admin. The script stores only its
#                           bcrypt hash, made with "myriarchd auth hash-password" (binary in PATH or MYRIARCHD_BIN),
#                           in the Secret myriarch-bootstrap (key password-hash). Use it with bootstrapAdmin.keepWithOidc
#                           in the values (see myriarch-values.yaml); keep the password itself in a vault.
#   Generated if missing: download-token-secret, gitops-webhook-secret (openssl rand; the file gitops-webhook-secret
#   is also needed as CI variable MYRIARCH_GITOPS_WEBHOOK_SECRET in the GitOps project).
set -euo pipefail
umask 077

# ------------------------------------------------------------------------------------------- ADJUST
: "${CONFIRM_EDITED:?Review and adapt the script, then set CONFIRM_EDITED=yes (see the header of the file)}"
[ "${CONFIRM_EDITED}" = "yes" ] || { echo "CONFIRM_EDITED must be 'yes'" >&2; exit 1; }
: "${SECRETS_DIR:?SECRETS_DIR (directory with the secret files) is missing}"

NS_MYRIARCHD=myriarch
NS_GATEWAY=observability
SECRET_NAME=myriarch-secrets
REGISTRY_SERVER=registry.example.com       # PLACEHOLDER: the registry of IMAGE_REGISTRY
REGISTRY_USER="registry-user"              # PLACEHOLDER: user name of the registry token (read access); not a secret
PULL_SECRET=registry-pull-secret
TLS_SECRET=fleet-tls
OIDC_AUTH="${OIDC_AUTH:-secret}"           # secret | cert
MIRROR_AUTH="${MIRROR_AUTH:-token}"        # token (bearer) | basic (user + password)
TLS_MODE="${TLS_MODE:-files}"              # files | certmanager
# -------------------------------------------------------------------------------------------------------

K="kubectl"
[ -d "$SECRETS_DIR" ] || { echo "SECRETS_DIR '$SECRETS_DIR' is not a directory" >&2; exit 1; }

need() { # need <file> ...: aborts if a file is missing or empty
  local f
  for f in "$@"; do
    [ -s "$SECRETS_DIR/$f" ] || { echo "File missing or empty: $SECRETS_DIR/$f" >&2; exit 1; }
  done
}

apply() { "$K" apply -f - >/dev/null; }

echo "Context: $("$K" config current-context)"

# --- Namespaces ------------------------------------------------------------------------------------------------
for ns in "$NS_MYRIARCHD" "$NS_GATEWAY"; do
  "$K" create namespace "$ns" --dry-run=client -o yaml | apply
done

# --- CA bundle (public part) as ConfigMap internal-ca, in both namespaces ----------------------------------------
need ca.crt
openssl x509 -in "$SECRETS_DIR/ca.crt" -noout -subject -enddate >/dev/null  # must be a PEM certificate
for ns in "$NS_MYRIARCHD" "$NS_GATEWAY"; do
  "$K" -n "$ns" create configmap internal-ca --from-file=ca.crt="$SECRETS_DIR/ca.crt" --dry-run=client -o yaml | apply
done

# --- TLS secret of the Ingress -----------------------------------------------------------------------------------
if [ "$TLS_MODE" = "files" ]; then
  need tls.crt tls.key
  "$K" -n "$NS_MYRIARCHD" create secret tls "$TLS_SECRET" --cert="$SECRETS_DIR/tls.crt" --key="$SECRETS_DIR/tls.key" \
    --dry-run=client -o yaml | apply
else
  echo "TLS_MODE=$TLS_MODE: no TLS secret created (cert-manager issues $TLS_SECRET)"
fi

# --- Pull secret for the container registry ----------------------------------------------------------------------
# Build .dockerconfigjson from the token file (the password never appears in an argument list: jq reads the file itself)
need registry-token
for ns in "$NS_MYRIARCHD" "$NS_GATEWAY"; do
  jq -n --arg server "$REGISTRY_SERVER" --arg user "$REGISTRY_USER" --rawfile pw "$SECRETS_DIR/registry-token" '
      ($pw | rtrimstr("\n")) as $p
      | {auths: {($server): {username: $user, password: $p, auth: (($user + ":" + $p) | @base64)}}}' \
    > "$SECRETS_DIR/.dockerconfigjson.tmp"
  "$K" -n "$ns" create secret generic "$PULL_SECRET" --type=kubernetes.io/dockerconfigjson \
    --from-file=.dockerconfigjson="$SECRETS_DIR/.dockerconfigjson.tmp" --dry-run=client -o yaml | apply
  rm -f "$SECRETS_DIR/.dockerconfigjson.tmp"
done

# --- generated secrets (only if the file is missing) --------------------------------------------------------------
# HMAC key of the download tokens: >= 32 random bytes
[ -s "$SECRETS_DIR/download-token-secret" ] || openssl rand -base64 48 | tr -d '\n' > "$SECRETS_DIR/download-token-secret"
# Webhook secret for POST /api/v1/gitops/webhook (secret token of the Git server / CI variable MYRIARCH_GITOPS_WEBHOOK_SECRET)
[ -s "$SECRETS_DIR/gitops-webhook-secret" ] || openssl rand -hex 32 | tr -d '\n' > "$SECRETS_DIR/gitops-webhook-secret"

# --- Secret myriarch-secrets ---------------------------------------------------------------------------------------
files=(database-dsn download-token-secret git-token gitops-webhook-secret github-token slack-webhook-url)
case "$OIDC_AUTH" in
  secret) files+=(oidc-client-secret) ;;
  cert)   files+=(oidc-client-cert oidc-client-key) ;;
  *) echo "OIDC_AUTH must be 'secret' or 'cert'" >&2; exit 1 ;;
esac
case "$MIRROR_AUTH" in
  token) files+=(mirror-token) ;;
  basic) files+=(mirror-password) ;;
  *) echo "MIRROR_AUTH must be 'token' or 'basic'" >&2; exit 1 ;;
esac
[ -s "$SECRETS_DIR/awx-token" ] && files+=(awx-token)
need "${files[@]}"

args=()
for f in "${files[@]}"; do args+=(--from-file="$f=$SECRETS_DIR/$f"); done
"$K" -n "$NS_MYRIARCHD" create secret generic "$SECRET_NAME" "${args[@]}" --dry-run=client -o yaml | apply
echo "Secret $NS_MYRIARCHD/$SECRET_NAME: keys ${files[*]}"

# --- Optional: break-glass bootstrap admin (bcrypt hash of the password) -------------------------------------------------
if [ -s "$SECRETS_DIR/bootstrap-password" ]; then
  # the hash is computed locally; the password never appears in an argument list
  "${MYRIARCHD_BIN:-myriarchd}" auth hash-password --cost 12 < "$SECRETS_DIR/bootstrap-password" > "$SECRETS_DIR/.password-hash.tmp"
  "$K" -n "$NS_MYRIARCHD" create secret generic myriarch-bootstrap \
    --from-file=password-hash="$SECRETS_DIR/.password-hash.tmp" --dry-run=client -o yaml | apply
  rm -f "$SECRETS_DIR/.password-hash.tmp"
  echo "Secret $NS_MYRIARCHD/myriarch-bootstrap: key password-hash"
fi

# --- Gateway collector (namespace observability) -----------------------------------------------------------------
# Enrollment token: create it in the myriarchd UI (settings -> enrollment tokens) and store it as a file.
if [ -s "$SECRETS_DIR/enrollment-token" ]; then
  "$K" -n "$NS_GATEWAY" create secret generic myriarch-enrollment \
    --from-file=token="$SECRETS_DIR/enrollment-token" --dry-run=client -o yaml | apply
  [ -s "$SECRETS_DIR/gateway-backend-token" ] || printf 'unused-dummy-value' > "$SECRETS_DIR/gateway-backend-token"
  "$K" -n "$NS_GATEWAY" create secret generic otel-collector-secrets \
    --from-file=BACKEND_API_TOKEN="$SECRETS_DIR/gateway-backend-token" --dry-run=client -o yaml | apply
  echo "Gateway secrets created in $NS_GATEWAY (myriarch-enrollment, otel-collector-secrets)"
else
  echo "enrollment-token missing: gateway secrets skipped (create the token after installing myriarchd, then run the script again)"
fi

echo "Done. Continue with: helm upgrade --install myriarch ... (see README.md of this directory)"
