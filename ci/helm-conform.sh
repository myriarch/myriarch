#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Helm conformance: renders the myriarchd chart for the default values, both profiles (standard and OpenShift), the Dex
# quickstart and the production example (alone, with each ingress variant and with the OpenShift variant), validates
# every rendered manifest with kubeconform, checks the rendered myriarchd.yaml with `myriarchd config validate` and
# asserts the contract of the chart (see the assertion blocks below).
#
#   ci/helm-conform.sh [--out DIR]       default DIR: .cache/helm-render (wiped and recreated)
#
# Cases: default, openshift (openshift.enabled=true), openshift-auto (auto with --api-versions security.openshift.io/v1),
# auto-standard (auto without it: standard profile), quickstart-dex, install-guide (the values of the Helm guide), prod, prod-<variant> for each
# deploy/helm/myriarch/examples/ingress-*.yaml, prod-openshift (examples/openshift.yaml), plus option cases
# (single replica, SSH Git URL, extra ports, DNS override, enrollment tokens, OIDC, OIDC from the environment).
#
# Environment: KUBERNETES_VERSION (default 1.30.0), KUBECONFORM_CACHE (default .cache/kubeconform). The first run needs
# network access to the schema locations (Kubernetes schemas, the datreeio CRDs catalog and the OpenShift schemas); later
# runs use the cache. Resources without a schema are skipped by kubeconform; a skipped resource fails the case unless
# KUBECONFORM_ALLOW_SKIPPED=1 (then it only warns).
# MYRIARCHD_BIN: myriarchd binary for `config validate --skip-files` (default: built with `go build` into DIR).
# HELM_CONFORM_CONFIG_VALIDATE=0 skips the config validation (only for an isolated checkout whose binary does not know
# the chart keys yet; CI never sets it). Exit 1 if any render, validation or assertion failed.
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "${ROOT}"
need_cmd helm kubeconform yq

out="${ROOT}/.cache/helm-render"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --out) [ "$#" -ge 2 ] || die "--out needs a directory"; out="$2"; shift 2 ;;
    -h|--help) sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "usage: ci/helm-conform.sh [--out DIR]" ;;
  esac
done
case "${out}" in /*) ;; *) out="${ROOT}/${out}" ;; esac

chart=deploy/helm/myriarch
values=deploy/examples/production/myriarch-values.yaml
examples="${chart}/examples"
k8s_version="${KUBERNETES_VERSION:-1.30.0}"
cache="${KUBECONFORM_CACHE:-${ROOT}/.cache/kubeconform}"
crd_catalog='https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json'
# OpenShift API schemas (Route), pinned to a commit
openshift_schemas='https://raw.githubusercontent.com/melmorabity/openshift-json-schemas/62ad608b18edcd8ef4b6dd3741fc36115813fd18/v4.18-standalone-strict/{{ .ResourceKind }}{{ .KindSuffix }}.json'

rm -rf "${out}"
mkdir -p "${out}" "${cache}"   # kubeconform requires the cache directory to exist
status=0
err="$(mktemp)"; trap 'rm -f "${err}"' EXIT

# myriarchd for `config validate`
validate_config=1
myriarchd="${MYRIARCHD_BIN:-}"
if [ "${HELM_CONFORM_CONFIG_VALIDATE:-1}" = 0 ]; then
  validate_config=0
  warn "HELM_CONFORM_CONFIG_VALIDATE=0: the rendered myriarchd.yaml is NOT validated (never use this in CI)"
elif [ -z "${myriarchd}" ]; then
  need_cmd go
  myriarchd="${out}/myriarchd"
  log "building myriarchd into ${myriarchd}"
  go build -o "${myriarchd}" ./cmd/myriarchd || die "go build ./cmd/myriarchd failed"
fi

fail_case() { # case-name message
  log "$1: FAILED: $2"
  status=1
}

render() { # case-name [helm template args...] -> <out>/<case>.yaml
  local name="$1"; shift
  helm template myriarch "${chart}" --namespace myriarch "$@" >"${out}/${name}.yaml" 2>"${err}"
}

validate() { # case-name
  local name="$1" skipped
  if kubeconform -strict -ignore-missing-schemas -kubernetes-version "${k8s_version}" -cache "${cache}" \
      -schema-location default -schema-location "${crd_catalog}" -schema-location "${openshift_schemas}" \
      -summary "${out}/${name}.yaml" >"${err}" 2>&1; then
    log "${name}: kubeconform ok ($(tail -n 1 "${err}"))"
    skipped="$(tail -n 1 "${err}" | sed -n 's/.*Skipped: \([0-9][0-9]*\).*/\1/p')"
    if [ "${skipped:-0}" != 0 ]; then
      if [ "${KUBECONFORM_ALLOW_SKIPPED:-0}" = 1 ]; then
        warn "${name}: ${skipped} resource(s) have no schema and were not validated"
      else
        fail_case "${name}" "kubeconform skipped ${skipped} resource(s) without a schema (KUBECONFORM_ALLOW_SKIPPED=1 accepts this)"
      fi
    fi
  else
    cat "${err}" >&2
    fail_case "${name}" "kubeconform"
  fi
}

# q <case> <yq expression>: expression over the rendered manifests of the case (all documents)
q() { yq "$2" "${out}/$1.yaml"; }
# qc <case> <yq expression>: expression over the rendered myriarchd.yaml of the case
qc() { yq "$2" "${out}/$1.myriarchd.yaml"; }
# dep <case> <expression>: expression over the Deployment
dep() { yq "select(.kind == \"Deployment\") | $2" "${out}/$1.yaml"; }

expect() { # case description want got
  if [ "$3" != "$4" ]; then fail_case "$1" "$2: got '$4', want '$3'"; fi
}

# Environment of the config validation (NAME=value entries): the settings that a case hands to the pod as environment
# variables (extraEnv), because `config validate` applies the MYRIARCH_* overrides like the server does. Set by the case
# before run_case and cleared afterwards.
config_env=()

# check_config <case>: extract and validate myriarchd.yaml
check_config() {
  local name="$1" cfg="${out}/$1.myriarchd.yaml"
  yq 'select(.kind == "ConfigMap").data["myriarchd.yaml"]' "${out}/${name}.yaml" >"${cfg}"
  if [ "${validate_config}" = 1 ]; then
    if env ${config_env[@]+"${config_env[@]}"} "${myriarchd}" config validate --config "${cfg}" --skip-files >"${err}" 2>&1; then
      log "${name}: myriarchd config validate ok"
    else
      cat "${err}" >&2
      fail_case "${name}" "myriarchd config validate"
    fi
  fi
}

# common assertions of every case: no dev mode anywhere
check_no_dev() { # case
  local name="$1"
  if grep -Eq 'devLogin|allow-nonlocal|devtoken_insecure' "${out}/${name}.yaml"; then
    fail_case "${name}" "the rendering contains dev-mode settings (devLogin, --allow-nonlocal or the development token)"
  fi
  expect "${name}" "container args" "serve --config /etc/myriarchd/myriarchd.yaml" "$(dep "${name}" '.spec.template.spec.containers[0].args | join(" ")')"
}

run_case() { # case-name [helm template args...]
  local name="$1"; shift
  if render "${name}" "$@"; then
    log "${name}: helm template ok"
    validate "${name}"
    check_config "${name}"
    check_no_dev "${name}"
  else
    cat "${err}" >&2
    fail_case "${name}" "helm template"
    return 1
  fi
}

# ---------------------------------------------------------------------------------------------------------------------
# default: the chart installs with its defaults
# ---------------------------------------------------------------------------------------------------------------------
if run_case default; then
  c=default
  expect "${c}" "no Ingress" "" "$(q ${c} 'select(.kind == "Ingress") | .kind')"
  expect "${c}" "no Route" "" "$(q ${c} 'select(.kind == "Route") | .kind')"
  expect "${c}" "replicas" "2" "$(dep ${c} '.spec.replicas')"
  expect "${c}" "PodDisruptionBudget" "PodDisruptionBudget" "$(q ${c} 'select(.kind == "PodDisruptionBudget") | .kind')"
  np='select(.kind == "NetworkPolicy") | '
  expect "${c}" "number of ingress rules" "1" "$(q ${c} "${np}.spec.ingress | length")"
  expect "${c}" "the only source of port 8080 is the own namespace" "true 1" \
    "$(q ${c} "${np}.spec.ingress[0].from[0] | has(\"podSelector\")") $(q ${c} "${np}.spec.ingress[0].from | length")"
  expect "${c}" "egress ports" "5432,443" "$(q ${c} "${np}[.spec.egress[1].ports[].port] | join(\",\")")"
  expect "${c}" "DNS ports" "53,53" "$(q ${c} "${np}[.spec.egress[0].ports[].port] | join(\",\")")"
  if grep -Eqi 'ingress-nginx|monitoring' "${out}/${c}.yaml"; then fail_case "${c}" "the rendering mentions ingress-nginx or monitoring"; fi
  expect "${c}" "ephemeral-storage request" "512Mi" "$(dep ${c} '.spec.template.spec.containers[0].resources.requests["ephemeral-storage"]')"
  expect "${c}" "ephemeral-storage limit" "5Gi" "$(dep ${c} '.spec.template.spec.containers[0].resources.limits["ephemeral-storage"]')"
  expect "${c}" "checksum/secret annotation" "true" "$(dep ${c} '.spec.template.metadata.annotations | has("checksum/secret")')"
  expect "${c}" "bootstrap Secret" "myriarch-bootstrap" "$(q ${c} 'select(.kind == "Secret") | .metadata.name')"
  expect "${c}" "bootstrap mount" "/etc/myriarchd/bootstrap" \
    "$(dep ${c} '.spec.template.spec.containers[0].volumeMounts[] | select(.name == "bootstrap") | .mountPath')"
  expect "${c}" "bootstrap token file" "/etc/myriarchd/bootstrap/token" "$(qc ${c} '.auth.bootstrapAdmin.tokenFile')"
  for key in '.auth.oidc.clientSecretFile' '.gitops.auth.tokenFile' '.gitops.webhookSecretFile' '.releases.tokenFile' '.auth.downloadTokenSecretFile'; do
    expect "${c}" "${key} is not rendered" "null" "$(qc ${c} "${key}")"
  done
  expect "${c}" "standard profile runAsUser" "65532" "$(dep ${c} '.spec.template.spec.securityContext.runAsUser')"
  # two renders give the same Deployment and ConfigMap (the generated Secret differs)
  if render default-again; then
    if ! diff <(q default 'select(.kind == "Deployment" or .kind == "ConfigMap")') \
              <(q default-again 'select(.kind == "Deployment" or .kind == "ConfigMap")') >/dev/null; then
      fail_case "${c}" "two renders differ in the Deployment or the ConfigMap"
    fi
  fi
  rm -f "${out}/default-again.yaml"
fi

# checksum/secret: a Secret the chart generates but does not find through lookup (it is created by this very upgrade: first
# use, or deleted to rotate it) must roll the pods on an upgrade even when the previous revision also saw no Secret
# (otherwise a second rotation leaves the old pods with the old value; enrollment tokens are reconciled at startup only).
# Offline lookup finds nothing, so helm template stands in for "Secret absent"; --is-upgrade marks the upgrade. Rendering
# without --is-upgrade stays constant (GitOps tools that render offline), and so does a setup without generated Secrets.
cs() { dep "$1" '.spec.template.metadata.annotations["checksum/secret"]'; }
if render cs-install && render cs-install-again && render cs-upgrade --is-upgrade; then
  expect default "checksum/secret is constant across renders (install)" "$(cs cs-install)" "$(cs cs-install-again)"
  if [ "$(cs cs-install)" = "$(cs cs-upgrade)" ]; then fail_case default "checksum/secret does not change for an upgrade that creates a generated Secret"; fi
else
  cat "${err}" >&2
  fail_case default "helm template for the checksum cases"
fi
cs_oidc=(--set config.auth.oidc.enabled=true --set config.auth.oidc.clientId=myriarch --set-json 'config.auth.oidc.roleMapping.admin=["admins"]')
if render cs-oidc "${cs_oidc[@]}" && render cs-oidc-upgrade "${cs_oidc[@]}" --is-upgrade; then
  expect default "checksum/secret without generated Secrets is the same for an upgrade" "$(cs cs-oidc)" "$(cs cs-oidc-upgrade)"
else
  cat "${err}" >&2
  fail_case default "helm template for the checksum cases (OIDC)"
fi
rm -f "${out}"/cs-*.yaml

# option cases on top of the defaults
if run_case replicas-1 --set replicaCount=1; then
  expect replicas-1 "no PodDisruptionBudget with one replica" "" "$(q replicas-1 'select(.kind == "PodDisruptionBudget") | .kind')"
fi

np_ports() { q "$1" 'select(.kind == "NetworkPolicy") | [.spec.egress[1].ports[].port] | join(",")'; }
if run_case git-ssh --set config.gitops.enabled=true --set config.gitops.repoUrl=git@git.example.com:team/fleet.git --set config.gitops.auth.sshKeyFile=/etc/myriarchd/secrets/git-ssh-key --set config.gitops.auth.knownHostsFile=/etc/myriarchd/secrets/git-known-hosts; then
  expect git-ssh "SSH port of a scp-like URL" "5432,443,22" "$(np_ports git-ssh)"
fi
if run_case git-ssh-port --set config.gitops.enabled=true --set config.gitops.repoUrl=ssh://git@git.example.com:2222/team/fleet.git --set config.gitops.auth.sshKeyFile=/etc/myriarchd/secrets/git-ssh-key --set config.gitops.auth.knownHostsFile=/etc/myriarchd/secrets/git-known-hosts; then
  expect git-ssh-port "SSH port of an ssh:// URL" "5432,443,2222" "$(np_ports git-ssh-port)"
fi
# the values of the kind probe (hack/kind): must keep rendering and validating, and the SSH port of the in-cluster Git server
# is added to the egress rule automatically
if run_case kind-probe -f hack/kind/values/probe.yaml --set config.server.publicUrl=https://fleet.example.test \
    -f <(printf 'enrollmentTokens:\n  - {name: kind-sim, labels: {source: kind-sim}, expiresAt: "2030-01-01T00:00:00Z"}\n  - {name: kind-gw, labels: {source: kind-gw}}\n'); then
  expect kind-probe "SSH port of the in-cluster Git server" "5432,443,2222" "$(np_ports kind-probe)"
  expect kind-probe "declared enrollment tokens" "kind-sim,kind-gw" "$(qc kind-probe '[.opamp.enrollmentTokens[].name] | join(",")')"
fi
if run_case git-https --set config.gitops.enabled=true --set config.gitops.repoUrl=https://git.example.com/team/fleet.git; then
  expect git-https "no SSH port for https" "5432,443" "$(np_ports git-https)"
fi
if run_case extra-ports --set-json 'networkPolicy.egress.extraPorts=[5556,443]'; then
  expect extra-ports "extraPorts merged and deduplicated" "5432,443,5556" "$(np_ports extra-ports)"
fi
if run_case dns-override --set-json 'networkPolicy.egress.dns.to=[{"namespaceSelector":{"matchLabels":{"dns":"custom"}}}]' \
    --set-json 'networkPolicy.egress.dns.ports=[5353]'; then
  expect dns-override "DNS destination" "custom" \
    "$(q dns-override 'select(.kind == "NetworkPolicy") | .spec.egress[0].to[0].namespaceSelector.matchLabels.dns')"
  expect dns-override "DNS port" "5353,5353" "$(q dns-override 'select(.kind == "NetworkPolicy") | [.spec.egress[0].ports[].port] | join(",")')"
fi

if run_case enrollment --set-json 'enrollmentTokens=[{"name":"gateway","labels":{"site":"hq"}}]'; then
  c=enrollment
  expect "${c}" "projected item" "gateway" \
    "$(dep ${c} '.spec.template.spec.volumes[] | select(.name == "enrollment-tokens") | .projected.sources[0].secret.items[0].path')"
  expect "${c}" "mount" "/etc/myriarchd/enrollment" \
    "$(dep ${c} '.spec.template.spec.containers[0].volumeMounts[] | select(.name == "enrollment-tokens") | .mountPath')"
  tok="$(q ${c} 'select(.kind == "Secret" and .metadata.name == "myriarch-enrollment-gateway") | .data.token' | base64 -d 2>/dev/null || true)"
  case "${tok}" in myre_*) ;; *) fail_case "${c}" "the generated enrollment token does not start with myre_" ;; esac
  expect "${c}" "config tokenFile" "/etc/myriarchd/enrollment/gateway" "$(qc ${c} '.opamp.enrollmentTokens[0].tokenFile')"
  expect "${c}" "config labels" "hq" "$(qc ${c} '.opamp.enrollmentTokens[0].labels.site')"
fi

if run_case oidc --set config.auth.oidc.enabled=true --set config.auth.oidc.clientId=myriarch \
    --set-json 'config.auth.oidc.roleMapping.admin=["admins"]'; then
  c=oidc
  expect "${c}" "no bootstrap Secret" "" "$(q ${c} 'select(.kind == "Secret") | .metadata.name')"
  # enabled but inactive next to OIDC: rendered (the server audits the deactivation as oidc_enabled), not mounted
  expect "${c}" "inactive bootstrap admin is rendered" "true false" \
    "$(qc ${c} '.auth.bootstrapAdmin.enabled') $(qc ${c} '.auth.bootstrapAdmin.keepWithOidc')"
  expect "${c}" "inactive bootstrap admin is not mounted" "" \
    "$(dep ${c} '.spec.template.spec.volumes[] | select(.name == "bootstrap") | .name')"
  expect "${c}" "clientSecretFile stays" "/etc/myriarchd/secrets/oidc-client-secret" "$(qc ${c} '.auth.oidc.clientSecretFile')"
fi

# OIDC switched on by environment variables only: the chart sees nothing but the declaration oidcFromEnvironment. The
# variables reach the pod through extraEnv and the config validation through config_env.
oidc_env=(MYRIARCH_AUTH_OIDC_ENABLED=true MYRIARCH_AUTH_OIDC_CLIENTID=myriarch MYRIARCH_AUTH_OIDC_ROLEMAPPING_ADMIN=admins)
oidc_env_json='[{"name":"MYRIARCH_AUTH_OIDC_ENABLED","value":"true"},{"name":"MYRIARCH_AUTH_OIDC_CLIENTID","value":"myriarch"},{"name":"MYRIARCH_AUTH_OIDC_ROLEMAPPING_ADMIN","value":"admins"}]'
config_env=("${oidc_env[@]}")
if run_case oidc-env --set bootstrapAdmin.enabled=false --set oidcFromEnvironment=true --set-json "extraEnv=${oidc_env_json}"; then
  c=oidc-env
  expect "${c}" "no Secret" "" "$(q ${c} 'select(.kind == "Secret") | .metadata.name')"
  expect "${c}" "no bootstrap configuration" "null" "$(qc ${c} '.auth.bootstrapAdmin')"
  expect "${c}" "the file leaves OIDC to the environment" "false" "$(qc ${c} '.auth.oidc.enabled')"
  expect "${c}" "clientSecretFile stays" "/etc/myriarchd/secrets/oidc-client-secret" "$(qc ${c} '.auth.oidc.clientSecretFile')"
  expect "${c}" "the variable reaches the container" "true" \
    "$(dep ${c} '.spec.template.spec.containers[0].env[] | select(.name == "MYRIARCH_AUTH_OIDC_ENABLED") | .value')"
fi
# the bootstrap admin (default on) is inactive next to OIDC from the environment: rendered, not mounted, no Secret
if run_case oidc-env-bootstrap --set oidcFromEnvironment=true --set-json "extraEnv=${oidc_env_json}"; then
  c=oidc-env-bootstrap
  expect "${c}" "no bootstrap Secret" "" "$(q ${c} 'select(.kind == "Secret") | .metadata.name')"
  expect "${c}" "inactive bootstrap admin is rendered" "true false" \
    "$(qc ${c} '.auth.bootstrapAdmin.enabled') $(qc ${c} '.auth.bootstrapAdmin.keepWithOidc')"
  expect "${c}" "inactive bootstrap admin is not mounted" "" \
    "$(dep ${c} '.spec.template.spec.volumes[] | select(.name == "bootstrap") | .name')"
fi
config_env=()

# ---------------------------------------------------------------------------------------------------------------------
# profiles
# ---------------------------------------------------------------------------------------------------------------------
check_openshift_profile() { # case
  local c="$1"
  expect "${c}" "Route with edge TLS" "edge" "$(q ${c} 'select(.kind == "Route") | .spec.tls.termination')"
  expect "${c}" "Route timeout" "3600s" "$(q ${c} 'select(.kind == "Route") | .metadata.annotations["haproxy.router.openshift.io/timeout"]')"
  expect "${c}" "Route tunnel timeout" "3600s" "$(q ${c} 'select(.kind == "Route") | .metadata.annotations["haproxy.router.openshift.io/timeout-tunnel"]')"
  expect "${c}" "OpenShift DNS port" "5353" "$(q ${c} 'select(.kind == "NetworkPolicy") | .spec.egress[0].ports[0].port')"
  expect "${c}" "OpenShift DNS namespace" "openshift-dns" \
    "$(q ${c} 'select(.kind == "NetworkPolicy") | .spec.egress[0].to[0].namespaceSelector.matchLabels["kubernetes.io/metadata.name"]')"
  expect "${c}" "router namespace selector" "true" \
    "$(q ${c} 'select(.kind == "NetworkPolicy") | .spec.ingress[0].from[] | select(has("namespaceSelector")) | .namespaceSelector.matchLabels | has("policy-group.network.openshift.io/ingress")')"
  expect "${c}" "no fixed user, group or fsGroup" "false false false" \
    "$(dep ${c} '.spec.template.spec.securityContext | has("runAsUser")') $(dep ${c} '.spec.template.spec.securityContext | has("runAsGroup")') $(dep ${c} '.spec.template.spec.securityContext | has("fsGroup")')"
  expect "${c}" "runAsNonRoot stays" "true" "$(dep ${c} '.spec.template.spec.securityContext.runAsNonRoot')"
  if dep "${c}" '.' | grep -q 'runAsUser'; then fail_case "${c}" "the Deployment contains runAsUser"; fi
}

if run_case openshift --set openshift.enabled=true; then check_openshift_profile openshift; fi
if run_case openshift-auto --set openshift.enabled=auto --api-versions security.openshift.io/v1; then check_openshift_profile openshift-auto; fi
if run_case auto-standard --set openshift.enabled=auto; then
  expect auto-standard "auto without the OpenShift API renders the standard profile (no Route)" "" "$(q auto-standard 'select(.kind == "Route") | .kind')"
  expect auto-standard "standard profile runAsUser" "65532" "$(dep auto-standard '.spec.template.spec.securityContext.runAsUser')"
fi

# ---------------------------------------------------------------------------------------------------------------------
# quickstart with Dex
# ---------------------------------------------------------------------------------------------------------------------
if run_case quickstart-dex -f "${examples}/quickstart-dex.yaml"; then
  c=quickstart-dex
  expect "${c}" "OIDC enabled" "true" "$(qc ${c} '.auth.oidc.enabled')"
  expect "${c}" "no bootstrap configuration" "null" "$(qc ${c} '.auth.bootstrapAdmin')"
  expect "${c}" "Dex issuer equals the OIDC issuerUrl" "$(qc ${c} '.auth.oidc.issuerUrl')" \
    "$(yq '.config.issuer' "${examples}/dex/dex.yaml")"
  expect "${c}" "Dex port in the issuer" "5554" "$(qc ${c} '.auth.oidc.issuerUrl' | sed -E 's|^https://[^:/]+:([0-9]+)/.*|\1|')"
  expect "${c}" "Dex port in the egress rule" "5432,443,5554" "$(np_ports ${c})"
fi

# ---------------------------------------------------------------------------------------------------------------------
# values of the guide docs/operations/install-helm.md: minimal installation, GitOps over HTTPS, HTTPRoute, declared token
# ---------------------------------------------------------------------------------------------------------------------
if run_case install-guide -f "${examples}/install-guide.yaml"; then
  c=install-guide
  expect "${c}" "HTTPRoute" "HTTPRoute" "$(q ${c} 'select(.kind == "HTTPRoute") | .kind')"
  expect "${c}" "HTTPRoute host" "fleet.example.com" "$(q ${c} 'select(.kind == "HTTPRoute") | .spec.hostnames[0]')"
  expect "${c}" "no Ingress" "" "$(q ${c} 'select(.kind == "Ingress") | .kind')"
  expect "${c}" "GitOps is enabled" "true" "$(qc ${c} '.gitops.enabled')"
  expect "${c}" "GitOps token file" "/etc/myriarchd/secrets/git-token" "$(qc ${c} '.gitops.auth.tokenFile')"
  expect "${c}" "declared enrollment token Secret" "myriarch-enrollment-linux-hosts" \
    "$(q ${c} 'select(.kind == "Secret" and .metadata.name == "myriarch-enrollment-linux-hosts") | .metadata.name')"
  expect "${c}" "enrollment token label" "production" "$(qc ${c} '.opamp.enrollmentTokens[0].labels.environment')"
  np='select(.kind == "NetworkPolicy") | '
  expect "${c}" "allowSameNamespace=false: the only source of port 8080 is the Gateway namespace" "false 1 gateway-system" \
    "$(q ${c} "${np}.spec.ingress[0].from[0] | has(\"podSelector\")") $(q ${c} "${np}.spec.ingress[0].from | length") $(q ${c} "${np}.spec.ingress[0].from[0].namespaceSelector.matchLabels[\"kubernetes.io/metadata.name\"]")"
fi

# ---------------------------------------------------------------------------------------------------------------------
# production example
# ---------------------------------------------------------------------------------------------------------------------
if [ -f "${values}" ]; then
  run_case prod -f "${values}" || true
  for f in "${examples}"/ingress-*.yaml; do
    [ -f "${f}" ] || continue
    v="$(basename "${f}" .yaml)"
    v="${v#ingress-}"
    if run_case "prod-${v}" -f "${values}" -f "${f}"; then
      expect "prod-${v}" "Ingress with TLS" "1" "$(q "prod-${v}" 'select(.kind == "Ingress") | .spec.tls | length')"
    fi
  done
  if [ -f "${examples}/openshift.yaml" ]; then
    if run_case prod-openshift -f "${values}" -f "${examples}/openshift.yaml"; then
      check_openshift_profile prod-openshift
      expect prod-openshift "no Ingress" "" "$(q prod-openshift 'select(.kind == "Ingress") | .kind')"
    fi
  fi
else
  warn "${values} is missing: skipping the production example cases"
fi

if [ "${status}" = 0 ]; then log "helm-conform: all cases ok (${out})"; else log "helm-conform: FAILED"; fi
exit "${status}"
