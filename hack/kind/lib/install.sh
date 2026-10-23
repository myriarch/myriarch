#!/usr/bin/env bash
# shellcheck shell=bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Installation building blocks of the kind probe (sourced after lib/common.sh): generated material, the infrastructure
# (PostgreSQL, Git server), the chart in two phases, the agents. No checks here; hack/kind/install.sh strings them together
# and the probe stages verify the result.
#
# Secrets (the database password, the DSN, the Git SSH keys) are generated with openssl/ssh-keygen into $PROBE_WORK/secrets
# (directory 0700, files 0600) and handed to Kubernetes through files only: never on a command line, never printed.

SECRETS_DIR="${PROBE_WORK}/secrets"
GITOPS_WORK="${PROBE_WORK}/gitops"
CHART_DIR="${ROOT}/deploy/helm/myriarch"
SERVER_IMAGE_REF=""     # <repo>@<digest>, set by load_images
COLLECTOR_IMAGE_REF=""
PUBLIC_URL="https://fleet.example.test"
export SECRETS_DIR GITOPS_WORK CHART_DIR PUBLIC_URL

# load_images: reads $PROBE_WORK/images.env (written by hack/kind/images.sh)
load_images() {
  [ -f "${PROBE_WORK}/images.env" ] || die "${PROBE_WORK}/images.env is missing (hack/kind/images.sh)"
  # shellcheck disable=SC1091
  . "${PROBE_WORK}/images.env"
  SERVER_IMAGE_REF="${SERVER_REPO}@${SERVER_DIGEST}"
  COLLECTOR_IMAGE_REF="${COLLECTOR_REPO}@${COLLECTOR_DIGEST}"
}

secret_path() { printf '%s/%s' "${SECRETS_DIR}" "$1"; }

# ---- generated material ------------------------------------------------------------------------------------------
generate_secrets() {
  (
    umask 077
    mkdir -p "${SECRETS_DIR}"
    chmod 700 "${SECRETS_DIR}"
    local f
    f="$(secret_path postgres-password)"
    if [ ! -s "${f}" ]; then random_token 32 | tr -d '\n' > "${f}"; fi
    # the DSN files are rewritten from the password every time (the password stays stable for a work directory)
    printf 'postgres://myriarch:%s@postgres.%s.svc:5432/myriarch?sslmode=disable' "$(cat "${f}")" "${NS_INFRA}" > "$(secret_path database-dsn)"
    printf 'postgres://myriarch:%s@postgres.%s.svc:5432/myriarch_dex?sslmode=disable' "$(cat "${f}")" "${NS_INFRA}" > "$(secret_path database-dsn-dex)"
    if [ ! -s "$(secret_path git-ssh-key)" ]; then
      rm -f "$(secret_path git-ssh-key)" "$(secret_path git-ssh-key).pub"
      ssh-keygen -q -t ed25519 -N '' -C 'probe-client' -f "$(secret_path git-ssh-key)"
    fi
    if [ ! -s "$(secret_path git-host-key)" ]; then
      rm -f "$(secret_path git-host-key)" "$(secret_path git-host-key).pub"
      ssh-keygen -q -t ed25519 -N '' -C 'probe-git-host' -f "$(secret_path git-host-key)"
    fi
    # known_hosts: the name the chart uses in the URL, port 2222, the public host key
    printf '[git.%s.svc]:2222 %s\n' "${NS_INFRA}" "$(awk '{print $1" "$2}' "$(secret_path git-host-key).pub")" > "$(secret_path git-known-hosts)"
  )
}

# write_values_tokens KIND_SIM_DAYS -> $PROBE_WORK/values-tokens.yaml (no secrets: names, labels and expiry dates)
write_values_tokens() {
  local days="$1" exp
  exp="$(date_plus_days "${days}")T00:00:00Z"
  cat > "${PROBE_WORK}/values-tokens.yaml" <<EOF
enrollmentTokens:
  - {name: kind-sim, labels: {source: kind-sim}, expiresAt: "${exp}"}
  - {name: kind-gw, labels: {source: kind-gw}}
EOF
  KIND_SIM_EXPIRES_AT="${exp}"
  export KIND_SIM_EXPIRES_AT
}

# ---- infrastructure ----------------------------------------------------------------------------------------------
ensure_namespace() { # name [psa-level]
  local name="$1" level="${2:-}"
  k apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: ${name}
${level:+  labels:
    pod-security.kubernetes.io/enforce: ${level}
    pod-security.kubernetes.io/enforce-version: latest
    pod-security.kubernetes.io/warn: ${level}
    pod-security.kubernetes.io/audit: ${level}}
EOF
}

# apply_secret NAMESPACE NAME KEY=FILE...   (create or update, values only from files)
apply_secret() {
  local ns="$1" name="$2" args=() spec
  shift 2
  for spec in "$@"; do args+=("--from-file=${spec}"); done
  kn "${ns}" create secret generic "${name}" "${args[@]}" --dry-run=client -o yaml | k apply -f - >/dev/null
}

install_infra() {
  ensure_namespace "${NS_INFRA}"
  apply_secret "${NS_INFRA}" postgres-password "password=$(secret_path postgres-password)"
  POSTGRES_IMAGE="${POSTGRES_IMAGE}" render_manifest "${KIND_DIR}/manifests/postgres.yaml" POSTGRES_IMAGE | k apply -f - >/dev/null

  # Git server: keys as a Secret, scripts as a ConfigMap (the files of hack/kind/git)
  local pub
  pub="$(secret_path git-ssh-key).pub"
  apply_secret "${NS_INFRA}" git-keys "ssh_host_ed25519_key=$(secret_path git-host-key)" "authorized_keys=${pub}"
  kn "${NS_INFRA}" create configmap git-scripts --from-file="${KIND_DIR}/git" --dry-run=client -o yaml | k apply -f - >/dev/null
  render_manifest "${KIND_DIR}/manifests/git.yaml" GIT_SERVER_IMAGE GIT_SERVER_GIT_SERIES GIT_SERVER_OPENSSH_SERIES | k apply -f - >/dev/null
}

wait_infra() {
  kn "${NS_INFRA}" rollout status statefulset/postgres --timeout=300s >&2 || return 1
  kn "${NS_INFRA}" rollout status deployment/git --timeout=300s >&2 || return 1
}

# ---- GitOps fixture ----------------------------------------------------------------------------------------------
# gitops_prepare: fresh copy of the fixture in $PROBE_WORK/gitops (the probe edits the copy, never the repository)
gitops_prepare() {
  rm -rf "${GITOPS_WORK}"
  mkdir -p "${GITOPS_WORK}"
  cp -R "${KIND_DIR}/gitops/." "${GITOPS_WORK}/"
}

# gitops_tar DIR -> tar archive of regular files on stdout (python tarfile: no macOS metadata, fixed owners, sorted)
gitops_tar() {
  python3 -I -c '
import os, sys, tarfile
root = sys.argv[1]
with tarfile.open(fileobj=sys.stdout.buffer, mode="w|", format=tarfile.PAX_FORMAT) as tar:
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = sorted(d for d in dirnames if not d.startswith("."))
        for f in sorted(filenames):
            if f.startswith(".") or f.startswith("._") or f == "Thumbs.db":
                continue
            p = os.path.join(dirpath, f)
            if os.path.islink(p) or not os.path.isfile(p):
                raise SystemExit("refusing non-regular file: " + p)
            ti = tar.gettarinfo(p, arcname=os.path.relpath(p, root))
            ti.uid = ti.gid = 0
            ti.uname = ti.gname = "root"
            ti.mtime = 0
            ti.mode = 0o644
            with open(p, "rb") as fh:
                tar.addfile(ti, fh)
' "$1"
}

# gitops_publish MESSAGE -> pushes $GITOPS_WORK as the new state of main; sets GITOPS_SHA
gitops_publish() {
  local msg="$1" out tarfile
  tarfile="${PROBE_WORK}/gitops.tar"
  gitops_tar "${GITOPS_WORK}" > "${tarfile}"
  out="$(kn "${NS_INFRA}" exec -i deploy/git -- sh /scripts/publish.sh "${msg}" < "${tarfile}")" || { printf '%s\n' "${out}" >&2; return 1; }
  GITOPS_SHA="$(printf '%s\n' "${out}" | tail -n 1)"
  [[ "${GITOPS_SHA}" =~ ^[0-9a-f]{40}$ ]] || { printf '%s\n' "${out}" >&2; return 1; }
  export GITOPS_SHA
}

# ---- chart -------------------------------------------------------------------------------------------------------
chart_set_args() {
  CHART_SET=(--set "config.server.publicUrl=${PUBLIC_URL}" --set "image.repository=${SERVER_REPO}" --set "image.digest=${SERVER_DIGEST}")
}

# check_deployed_config NAMESPACE: the ConfigMap the chart rendered must pass `myriarchd config validate`
check_deployed_config() {
  local ns="$1" f
  f="${PROBE_WORK}/render/deployed-${ns}.myriarchd.yaml"
  kn "${ns}" get configmap myriarch -o jsonpath='{.data.myriarchd\.yaml}' > "${f}"
  "${ROOT}/bin/myriarchd" config validate --config "${f}" --skip-files >"${PROBE_WORK}/render/deployed-${ns}.validate.txt" 2>&1
}

# Phase A of the install: only the required values: the Secret with database-dsn and config.server.publicUrl.
chart_install_minimal() {
  ensure_namespace "${NS_APP}" restricted
  kn "${NS_APP}" create secret generic myriarch-secrets --from-file="database-dsn=$(secret_path database-dsn)" --save-config >/dev/null
  chart_set_args
  h install myriarch "${CHART_DIR}" -n "${NS_APP}" "${CHART_SET[@]}" > "${PROBE_WORK}/notes-install.txt"
}

# Phase B: the full configuration (GitOps over SSH, declared enrollment tokens). Never --reuse-values.
chart_upgrade_full() {
  apply_secret "${NS_APP}" myriarch-secrets "database-dsn=$(secret_path database-dsn)" \
    "git-ssh-key=$(secret_path git-ssh-key)" "git-known-hosts=$(secret_path git-known-hosts)"
  chart_set_args
  h upgrade myriarch "${CHART_DIR}" -n "${NS_APP}" "${CHART_SET[@]}" \
    -f "${KIND_DIR}/values/probe.yaml" -f "${PROBE_WORK}/values-tokens.yaml" > "${PROBE_WORK}/notes-upgrade.txt"
}

# ---- agents ------------------------------------------------------------------------------------------------------
# collector_configmaps: the three files of the collector pod, generated from the shipped templates so that their
# defects show up here
collector_configmaps() {
  local dir="${PROBE_WORK}/collector"
  mkdir -p "${dir}"
  yq '.agent.executable = "/var/lib/myriarch/collector/current/otelcol-contrib"' "${ROOT}/contrib/legacy-kubernetes-collector/supervisor/supervisor.yaml" > "${dir}/supervisor.yaml"
  cp "${ROOT}/contrib/legacy-kubernetes-collector/supervisor/fallback.yaml" "${dir}/fallback.yaml"
  yq ".update.failedRetryBackoff = \"${UPDATER_RETRY_BACKOFF}s\" | .update.healthTimeout = \"45s\" | .update.healthInterval = \"2s\" | .update.healthSuccesses = 3 | .container.stopTimeout = \"20s\"" \
    "${ROOT}/contrib/legacy-kubernetes-collector/updater/updater.yaml" > "${dir}/updater.yaml"
  kn "${NS_APP}" create configmap kind-gw-config --from-file="${dir}" --dry-run=client -o yaml | k apply -f - >/dev/null
  kn "${NS_APP}" create configmap myriarch-trust --from-file="trusted_root.json=${PROBE_WORK}/trust/trusted_root.json" --dry-run=client -o yaml | k apply -f - >/dev/null
}

deploy_agents() {
  SERVER_IMAGE="${SERVER_IMAGE_REF}" render_manifest "${KIND_DIR}/manifests/sim.yaml" SERVER_IMAGE | k apply -f - >/dev/null
  collector_configmaps
  COLLECTOR_IMAGE="${COLLECTOR_IMAGE_REF}" CLUSTER_NAME="${KIND_CLUSTER_NAME}" render_manifest "${KIND_DIR}/manifests/collector.yaml" COLLECTOR_IMAGE CLUSTER_NAME | k apply -f - >/dev/null
}

# bootstrap_token_from_notes FILE: the command that the chart NOTES print to read the generated bootstrap token, run for
# the probe without ever executing rendered text as shell. The printed line must have exactly the expected shape
# (kubectl [-n NS] get secret NAME -o jsonpath='{.data.KEY}' | base64 -d); namespace, name and key are taken from it and
# the fixed command is run through the guarded kubectl wrapper. The token goes to FILE (mode 0600), never to the log.
# Returns 1 and prints the reason when the line does not match.
bootstrap_token_from_notes() {
  local file="$1" line ns name key
  local re="^kubectl (-n|--namespace) ([a-z0-9][a-z0-9.-]*) get secret ([a-z0-9][a-z0-9.-]*) -o jsonpath='\\{\\.data\\.([A-Za-z0-9_.-]+)\\}' \\| base64 (-d|--decode)$"
  line="$(h get notes myriarch -n "${NS_APP}" 2>/dev/null | grep 'get secret myriarch-bootstrap' | head -n 1 | sed 's/^ *//')"
  if [[ ! "${line}" =~ ${re} ]]; then
    warn "the NOTES command has an unexpected shape: ${line}"
    return 1
  fi
  ns="${BASH_REMATCH[2]}"; name="${BASH_REMATCH[3]}"; key="${BASH_REMATCH[4]}"
  [ "${ns}" = "${NS_APP}" ] || { warn "the NOTES command names namespace '${ns}', expected '${NS_APP}'"; return 1; }
  ( umask 077; kn "${ns}" get secret "${name}" -o "jsonpath={.data.${key}}" 2>/dev/null | base64 -d > "${file}" )
}
