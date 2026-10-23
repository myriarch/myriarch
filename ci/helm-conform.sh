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
# (single replica, SSH Git URL, extra ports, DNS override, enrollment tokens, OIDC, OIDC from the environment), the Pod of
# "helm test" (tests-*: restricted profile, no Secret without the opt-in, the NetworkPolicy rule only with tests.enabled and without
# trustedProxies) and the PrometheusRule (prometheusrule*: validated against the CRD catalog, equal to the rules of
# deploy/examples/production/monitoring/prometheusrule.yaml).
#
# Environment: KUBERNETES_VERSION (default 1.30.0), KUBECONFORM_CACHE (default .cache/kubeconform). The first run needs
# network access to the schema locations (Kubernetes schemas, the datreeio CRDs catalog and the OpenShift schemas); later
# runs use the cache. Resources without a schema are skipped by kubeconform; a skipped resource fails the case unless
# KUBECONFORM_ALLOW_SKIPPED=1 (then it only warns).
# MYRIARCHD_BIN: myriarchd binary for `config validate --skip-files` (default: built with `go build` into DIR).
# HELM_CONFORM_CONFIG_VALIDATE=0 skips the config validation (only for an isolated checkout whose binary does not know
# the chart keys yet; CI never sets it). Exit 1 if any render, validation or assertion failed.
# At its end the script runs ci/helm-conform-collector.sh for the collector chart (deploy/helm/myriarch-collector) into DIR/collector.
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
# one <case> <kind> <expression>: expression over the one document of that kind. The document is cut out first: yq answers the
# collect operator ([...]) with an empty array, and prints a blank line for it, for every other document of the stream.
one() { yq "select(.kind == \"$2\")" "${out}/$1.yaml" | yq "$3"; }
# tpod <case> <expression>: expression over the Pod of "helm test"
tpod() { one "$1" Pod "$2"; }

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

# check_test_pod <case> [token-secret]: the Pod of "helm test" (templates/tests/test-connection.yaml). A hook that runs
# "myriarchd healthcheck" of the image of the Deployment against the Service; the restricted Pod Security profile; labels that
# neither the Service nor the Deployment selects; no Secret and no environment without the opt-in (tests.token), and with it one key
# of the named Secret.
check_test_pod() {
  local c="$1" secret="${2:-}" want_args="healthcheck --url http://myriarch.myriarch.svc:8080 --opamp-path /v1/opamp"
  expect "${c}" "one test Pod" "myriarch-test-connection" "$(tpod "${c}" '.metadata.name')"
  expect "${c}" "test Pod hook" "test before-hook-creation,hook-succeeded" \
    "$(tpod "${c}" '.metadata.annotations["helm.sh/hook"]') $(tpod "${c}" '.metadata.annotations["helm.sh/hook-delete-policy"]')"
  expect "${c}" "test Pod runs the image of the Deployment" "$(dep "${c}" '.spec.template.spec.containers[0].image')" "$(tpod "${c}" '.spec.containers[0].image')"
  expect "${c}" "test Pod command" "${want_args}${secret:+ --token-file /etc/myriarchd-test/token}" "$(tpod "${c}" '.spec.containers[0].args | join(" ")')"
  expect "${c}" "test Pod never restarts and mounts no service account token" "Never false" \
    "$(tpod "${c}" '.spec.restartPolicy') $(tpod "${c}" '.spec.automountServiceAccountToken')"
  expect "${c}" "test Pod: restricted container context" "false true true ALL RuntimeDefault" \
    "$(tpod "${c}" '.spec.containers[0].securityContext | [.allowPrivilegeEscalation, .readOnlyRootFilesystem, .runAsNonRoot, (.capabilities.drop | join(",")), .seccompProfile.type] | join(" ")')"
  expect "${c}" "test Pod: restricted pod context" "true RuntimeDefault" \
    "$(tpod "${c}" '.spec.securityContext | [.runAsNonRoot, .seccompProfile.type] | join(" ")')"
  expect "${c}" "test Pod: no added capability and not privileged" "null null" \
    "$(tpod "${c}" '.spec.containers[0].securityContext.capabilities.add') $(tpod "${c}" '.spec.containers[0].securityContext.privileged')"
  expect "${c}" "test Pod: the labels select neither the Service nor the Deployment" "myriarch-test myriarch test" \
    "$(tpod "${c}" '.metadata.labels["app.kubernetes.io/name"]') $(tpod "${c}" '.metadata.labels["app.kubernetes.io/instance"]') $(tpod "${c}" '.metadata.labels["app.kubernetes.io/component"]')"
  expect "${c}" "test Pod: requests and limits" "10m 32Mi 16Mi 64Mi 64Mi" \
    "$(tpod "${c}" '.spec.containers[0].resources | [.requests.cpu, .requests.memory, .requests["ephemeral-storage"], .limits.memory, .limits["ephemeral-storage"]] | join(" ")')"
  expect "${c}" "test Pod: no environment" "null" "$(tpod "${c}" '.spec.containers[0].env')"
  if [ -z "${secret}" ]; then
    expect "${c}" "test Pod without tests.token mounts nothing" "null null" "$(tpod "${c}" '.spec.volumes') $(tpod "${c}" '.spec.containers[0].volumeMounts')"
    if tpod "${c}" '.' | grep -Eq 'secretName|secretKeyRef|--token-file|mountPath'; then fail_case "${c}" "the test Pod without tests.token refers to a Secret or a file"; fi
  else
    expect "${c}" "test Pod mounts one key of the Secret, read-only" "${secret} token token 288 1 /etc/myriarchd-test true" \
      "$(tpod "${c}" '.spec.volumes[0].secret | [.secretName, .items[0].key, .items[0].path, .defaultMode, (.items | length)] | join(" ")') $(tpod "${c}" '.spec.containers[0].volumeMounts[0] | [.mountPath, .readOnly] | join(" ")')"
    expect "${c}" "test Pod has one volume" "1" "$(tpod "${c}" '.spec.volumes | length')"
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
  check_test_pod "${c}"
  expect "${c}" "the test Pod follows the user of the pod security context" "65532" "$(tpod ${c} '.spec.securityContext.runAsUser')"
  expect "${c}" "no PrometheusRule by default" "" "$(q ${c} 'select(.kind == "PrometheusRule") | .kind')"
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
# the Pod of "helm test": the opt-in token, the switch, and the rule of the NetworkPolicy
# ---------------------------------------------------------------------------------------------------------------------
if run_case tests-token --set tests.token.existingSecret=helm-test-token; then
  check_test_pod tests-token helm-test-token
fi
if run_case tests-token-key --set tests.token.existingSecret=helm-test-token --set tests.token.key=probe-token; then
  expect tests-token-key "the key of the Secret" "probe-token token" \
    "$(tpod tests-token-key '.spec.volumes[0].secret.items[0].key') $(tpod tests-token-key '.spec.volumes[0].secret.items[0].path')"
fi
if run_case tests-off --set tests.enabled=false; then
  expect tests-off "no test Pod" "" "$(q tests-off 'select(.kind == "Pod") | .kind')"
fi
if run_case tests-service --set service.port=9080 --set-json 'config.opamp.path="/agents/opamp"' --set fullnameOverride=fleet; then
  expect tests-service "the test Pod asks the Service on its port and the OpAMP path of the configuration" \
    "healthcheck --url http://fleet.myriarch.svc:9080 --opamp-path /agents/opamp" "$(tpod tests-service '.spec.containers[0].args | join(" ")')"
  expect tests-service "the name of the test Pod" "fleet-test-connection" "$(tpod tests-service '.metadata.name')"
fi
# scheduling follows the server: a node selector and a toleration of the pods apply to the test Pod as well
if run_case tests-scheduling --set nodeSelector.pool=edge --set-json 'tolerations=[{"key":"dedicated","operator":"Equal","value":"infra","effect":"NoSchedule"}]'; then
  expect tests-scheduling "node selector and toleration" "edge dedicated" \
    "$(tpod tests-scheduling '.spec.nodeSelector.pool') $(tpod tests-scheduling '.spec.tolerations[0].key')"
fi

# annotations for a service mesh reach the test Pod, next to the annotations of the hook
if run_case tests-annotations --set-string 'tests.podAnnotations.sidecar\.istio\.io/inject=false'; then
  expect tests-annotations "mesh annotation and hook annotations" "false test before-hook-creation,hook-succeeded" \
    "$(tpod tests-annotations '.metadata.annotations["sidecar.istio.io/inject"]') $(tpod tests-annotations '.metadata.annotations["helm.sh/hook"]') $(tpod tests-annotations '.metadata.annotations["helm.sh/hook-delete-policy"]')"
fi

# The NetworkPolicy: the test Pod needs a rule only when allowSameNamespace is false, and the rule never exists together with
# trustedProxies or clientCertHeader (any pod with the labels of the test Pod could use it). The labels of the rule are the labels of the Pod.
np_from='select(.kind == "NetworkPolicy") | .spec.ingress[0].from'
test_rule='.[] | select(has("podSelector")) | .podSelector.matchLabels | to_entries | sort_by(.key) | map(.key + "=" + .value) | join(",")'
if run_case tests-np --set networkPolicy.ingress.allowSameNamespace=false; then
  expect tests-np "the only source of port 8080 is the test Pod" "1 app.kubernetes.io/component=test,app.kubernetes.io/instance=myriarch,app.kubernetes.io/name=myriarch-test" \
    "$(q tests-np "${np_from} | length") $(q tests-np "${np_from} | ${test_rule}")"
  expect tests-np "the Pod carries these labels" "test myriarch myriarch-test" \
    "$(tpod tests-np '.metadata.labels["app.kubernetes.io/component"]') $(tpod tests-np '.metadata.labels["app.kubernetes.io/instance"]') $(tpod tests-np '.metadata.labels["app.kubernetes.io/name"]')"
  expect tests-np "the rule is for the port of the server only" "http" "$(q tests-np 'select(.kind == "NetworkPolicy") | .spec.ingress[0].ports | map(.port) | join(",")')"
  expect tests-np "no rule for the metrics port" "1" "$(q tests-np 'select(.kind == "NetworkPolicy") | .spec.ingress | length')"
fi
if run_case tests-np-off --set networkPolicy.ingress.allowSameNamespace=false --set tests.enabled=false; then
  expect tests-np-off "no rule without the test Pod: nothing reaches port 8080" "null" "$(q tests-np-off 'select(.kind == "NetworkPolicy") | .spec.ingress')"
fi
if run_case tests-np-from --set networkPolicy.ingress.allowSameNamespace=false \
    --set-json 'networkPolicy.ingress.from=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"ingress"}}}]'; then
  expect tests-np-from "the listed source first, the test Pod after it" "2 true true" \
    "$(q tests-np-from "${np_from} | length") $(q tests-np-from "${np_from}[0] | has(\"namespaceSelector\")") $(q tests-np-from "${np_from}[1] | has(\"podSelector\")")"
fi
if run_case tests-np-trusted --set networkPolicy.ingress.allowSameNamespace=false --set-json 'config.server.trustedProxies=["10.0.0.0/8"]' \
    --set-json 'networkPolicy.ingress.from=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"ingress"}}}]'; then
  expect tests-np-trusted "trustedProxies: no rule for the test Pod although tests.enabled" "1 true" \
    "$(q tests-np-trusted "${np_from} | length") $(q tests-np-trusted "${np_from}[0] | has(\"namespaceSelector\")")"
  expect tests-np-trusted "the test Pod is still rendered" "myriarch-test-connection" "$(tpod tests-np-trusted '.metadata.name')"
fi
# clientCertHeader on its own is refused by the configuration check of the server (it needs trustedProxies), so this case renders and
# validates the manifests without it
if render tests-np-cert-header --set networkPolicy.ingress.allowSameNamespace=false --set config.opamp.clientCertHeader=X-Forwarded-Client-Cert \
    --set-json 'networkPolicy.ingress.from=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"ingress"}}}]'; then
  validate tests-np-cert-header
  expect tests-np-cert-header "clientCertHeader: no rule for the test Pod" "1 true" \
    "$(q tests-np-cert-header "${np_from} | length") $(q tests-np-cert-header "${np_from}[0] | has(\"namespaceSelector\")")"
else
  cat "${err}" >&2
  fail_case tests-np-cert-header "helm template"
fi
if run_case tests-np-same-namespace --set networkPolicy.ingress.allowSameNamespace=true; then
  expect tests-np-same-namespace "every pod of the namespace is a source already: no rule of its own" "1 true" \
    "$(q tests-np-same-namespace "${np_from} | length") $(q tests-np-same-namespace "${np_from}[0] | has(\"podSelector\")")"
  expect tests-np-same-namespace "that source selects every pod" "{}" "$(q tests-np-same-namespace "${np_from}[0].podSelector" | tr -d ' ')"
fi
# with the NetworkPolicy off there is nothing to open
if run_case tests-np-disabled --set networkPolicy.enabled=false; then
  expect tests-np-disabled "no NetworkPolicy" "" "$(q tests-np-disabled 'select(.kind == "NetworkPolicy") | .kind')"
  expect tests-np-disabled "the test Pod is rendered" "myriarch-test-connection" "$(tpod tests-np-disabled '.metadata.name')"
fi

# ---------------------------------------------------------------------------------------------------------------------
# the PrometheusRule: validated against the CRD catalog, the rules of the example, selectors that follow the release
# ---------------------------------------------------------------------------------------------------------------------
example_rules=deploy/examples/production/monitoring/prometheusrule.yaml
rule_groups() { yq -o=json -I=2 'select(.kind == "PrometheusRule") | .spec.groups | sort_keys(..)' "${out}/$1.yaml"; }
if run_case prometheusrule --set prometheusRule.enabled=true --set-json 'prometheusRule.labels={"release":"kube-prometheus-stack"}'; then
  c=prometheusrule
  pr='select(.kind == "PrometheusRule") | '
  expect "${c}" "one PrometheusRule" "PrometheusRule monitoring.coreos.com/v1 myriarch" \
    "$(q ${c} "${pr}.kind") $(q ${c} "${pr}.apiVersion") $(q ${c} "${pr}.metadata.name")"
  expect "${c}" "the label of the values and the labels of the chart" "kube-prometheus-stack myriarch" \
    "$(q ${c} "${pr}.metadata.labels.release") $(q ${c} "${pr}.metadata.labels[\"app.kubernetes.io/instance\"]")"
  expect "${c}" "groups and alerts" "3 13" "$(one ${c} PrometheusRule '.spec.groups | length') $(one ${c} PrometheusRule '[.spec.groups[].rules[]] | length')"
  # the Prometheus templates of the annotations are not touched by Helm
  expect "${c}" "annotation templates" 'myriarchd rejects connections ({{ $labels.reason }})' \
    "$(q ${c} "${pr}.spec.groups[].rules[] | select(.alert == \"MyriarchConnectRejected\") | .annotations.summary")"
  # every job matcher comes with the namespace matcher
  expect "${c}" "every selector names the job and the namespace" "$(grep -o 'job="myriarch"' "${out}/${c}.yaml" | wc -l | tr -d ' ')" \
    "$(grep -o 'job="myriarch", namespace="myriarch"' "${out}/${c}.yaml" | wc -l | tr -d ' ')"
  if [ -f "${example_rules}" ]; then
    if ! diff <(rule_groups ${c}) <(yq -o=json -I=2 '.spec.groups | sort_keys(..)' "${example_rules}") >"${err}"; then
      cat "${err}" >&2
      fail_case "${c}" "the rules of the chart differ from ${example_rules} (release myriarch, namespace myriarch, two replicas): change both"
    fi
    cp "${example_rules}" "${out}/prometheusrule-example.yaml"
    validate prometheusrule-example
  fi
fi
# a release with another name in another namespace: job, namespace, deployment and replica count follow it
if helm template fleet "${chart}" --namespace production --set prometheusRule.enabled=true --set replicaCount=3 >"${out}/prometheusrule-release.yaml" 2>"${err}"; then
  c=prometheusrule-release
  expect "${c}" "job, namespace, deployment and replicas follow the release" \
    'max(myriarch_leader{job="fleet-myriarch", namespace="production"}) < 1|kube_deployment_status_replicas_available{namespace="production", deployment="fleet-myriarch"} < 3|up{job="fleet-myriarch", namespace="production"} == 0' \
    "$(one ${c} PrometheusRule '[.spec.groups[0].rules[0].expr, .spec.groups[0].rules[2].expr, .spec.groups[0].rules[3].expr] | join("|")')"
  validate "${c}"
else
  cat "${err}" >&2
  fail_case prometheusrule "helm template for another release"
fi

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
  # the test Pod leaves the user, the group and the fsGroup to the SCC as well, and stays non-root
  expect "${c}" "test Pod: no fixed user, group or fsGroup" "false false false false false" \
    "$(tpod ${c} '.spec.securityContext | has("runAsUser")') $(tpod ${c} '.spec.securityContext | has("runAsGroup")') $(tpod ${c} '.spec.securityContext | has("fsGroup")') $(tpod ${c} '.spec.containers[0].securityContext | has("runAsUser")') $(tpod ${c} '.spec.containers[0].securityContext | has("runAsGroup")')"
  expect "${c}" "test Pod: non-root and seccomp stay" "true RuntimeDefault" "$(tpod ${c} '.spec.securityContext | [.runAsNonRoot, .seccompProfile.type] | join(" ")')"
  if tpod "${c}" '.' | grep -q 'runAsUser'; then fail_case "${c}" "the test Pod contains runAsUser"; fi
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

# the collector chart (deploy/helm/myriarch-collector), same Kubernetes version and schema cache
ci/helm-conform-collector.sh --out "${out}/collector" || status=1

if [ "${status}" = 0 ]; then log "helm-conform: all cases ok (${out})"; else log "helm-conform: FAILED"; fi
exit "${status}"
