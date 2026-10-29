#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Helm conformance of the collector chart (deploy/helm/myriarch-collector): renders it for both modes, container mode, the OpenShift
# profile and the option cases, validates every rendered manifest with kubeconform and asserts the contract of the chart: the
# unchanged upstream images pinned as in ci/versions.env, the Pod Security level "restricted" for the StatefulSet, the identity 10001
# in the standard profile and none in the OpenShift profile, the init containers, the identity volumes, the probes, and the guards that make rendering fail (ws:// without the opt-in,
# container mode on a DaemonSet, a missing trusted root, and the other rules of templates/_helpers.tpl "checks" and of the schema).
# ci/helm-conform.sh runs this script at its end with the same Kubernetes version; it can also run on its own.
#
#   ci/helm-conform-collector.sh [--out DIR]       default DIR: .cache/helm-render-collector (wiped and recreated)
#
# Environment: KUBERNETES_VERSION (default 1.30.0), KUBECONFORM_CACHE (default .cache/kubeconform). Exit 1 if any render, validation or
# assertion failed.
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "${ROOT}"
need_cmd helm kubeconform yq

out="${ROOT}/.cache/helm-render-collector"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --out) [ "$#" -ge 2 ] || die "--out needs a directory"; out="$2"; shift 2 ;;
    -h|--help) sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "usage: ci/helm-conform-collector.sh [--out DIR]" ;;
  esac
done
case "${out}" in /*) ;; *) out="${ROOT}/${out}" ;; esac

chart=deploy/helm/myriarch-collector
k8s_version="${KUBERNETES_VERSION:-1.30.0}"
cache="${KUBECONFORM_CACHE:-${ROOT}/.cache/kubeconform}"
# the pins are read from the file, not sourced: the file is data
pin() { sed -n "s/^$1=//p" ci/versions.env | head -n 1; }
otelcol_version="$(pin OTELCOL_VERSION)"
contrib_image="$(pin OTELCOL_CONTRIB_IMAGE)"
supervisor_image="$(pin OTELCOL_SUPERVISOR_IMAGE)"
# the updater image of the chart is the one of the release (appVersion), which is VERSION
app_version="$(head -n 1 VERSION)"
[ -n "${otelcol_version}" ] && [ -n "${contrib_image}" ] && [ -n "${supervisor_image}" ] || die "ci/versions.env lacks OTELCOL_VERSION, OTELCOL_CONTRIB_IMAGE or OTELCOL_SUPERVISOR_IMAGE"
contrib_digest="${contrib_image##*@}"
supervisor_digest="${supervisor_image##*@}"

rm -rf "${out}"
mkdir -p "${out}" "${cache}"
status=0
err="$(mktemp)"; trap 'rm -f "${err}"' EXIT

fail_case() { log "$1: FAILED: $2"; status=1; }

render() { # case-name [helm template args...] -> <out>/<case>.yaml
  local name="$1"; shift
  helm template myriarch "${chart}" --namespace observability "$@" >"${out}/${name}.yaml" 2>"${err}"
}

validate() { # case-name
  local name="$1" skipped
  if kubeconform -strict -ignore-missing-schemas -kubernetes-version "${k8s_version}" -cache "${cache}" -schema-location default \
      -summary "${out}/${name}.yaml" >"${err}" 2>&1; then
    log "${name}: kubeconform ok ($(tail -n 1 "${err}"))"
    skipped="$(tail -n 1 "${err}" | sed -n 's/.*Skipped: \([0-9][0-9]*\).*/\1/p')"
    [ "${skipped:-0}" = 0 ] || fail_case "${name}" "kubeconform skipped ${skipped} resource(s) without a schema"
  else
    cat "${err}" >&2
    fail_case "${name}" "kubeconform"
  fi
}

q() { yq "$2" "${out}/$1.yaml"; }
# one <case> <kind> <expression>: expression over the one document of that kind
one() { yq "select(.kind == \"$2\")" "${out}/$1.yaml" | yq "$3"; }
# wl <case> <expression>: expression over the StatefulSet or DaemonSet
wl() { yq 'select(.kind == "StatefulSet" or .kind == "DaemonSet")' "${out}/$1.yaml" | yq "$2"; }

expect() { # case description want got
  if [ "$3" != "$4" ]; then fail_case "$1" "$2: got '$4', want '$3'"; fi
}

# check_restricted <case>: the Pod Security level "restricted" (numeric 10001 identity as the overridable default), for every container
check_restricted() {
  local c="$1" n profile="${2:-standard}"
  if grep -Eq 'privileged: true|hostNetwork|hostPID|hostIPC' "${out}/${c}.yaml"; then
    fail_case "${c}" "the rendering contains a privileged container or a host namespace"
  fi
  if [ "${profile}" = standard ]; then
    expect "${c}" "pod identity: runAsUser, runAsGroup and fsGroup 10001" "10001 10001 10001" \
      "$(wl "${c}" '.spec.template.spec.securityContext | [.runAsUser, .runAsGroup, .fsGroup] | join(" ")')"
  else
    if grep -Eq 'runAsUser|runAsGroup|fsGroup' "${out}/${c}.yaml"; then fail_case "${c}" "the OpenShift profile renders a user, group or fsGroup"; fi
  fi
  expect "${c}" "no container overrides the pod identity" "0" \
    "$(wl "${c}" '.spec.template.spec | (.initContainers + .containers) | map(select(.securityContext.runAsUser != null or .securityContext.runAsGroup != null)) | length')"
  n="$(wl "${c}" '.spec.template.spec | (.initContainers + .containers) | length')"
  expect "${c}" "containers (two init containers and the main container)" "3" "${n}"
  expect "${c}" "every container: restricted context" "3" \
    "$(wl "${c}" '.spec.template.spec | (.initContainers + .containers) | map(select(.securityContext.allowPrivilegeEscalation == false and .securityContext.readOnlyRootFilesystem == true and .securityContext.runAsNonRoot == true and (.securityContext.capabilities.drop | join(",")) == "ALL" and .securityContext.capabilities.add == null and .securityContext.privileged == null)) | length')"
  expect "${c}" "pod: runAsNonRoot and the default seccomp profile" "true RuntimeDefault" \
    "$(wl "${c}" '.spec.template.spec.securityContext | [.runAsNonRoot, .seccompProfile.type] | join(" ")')"
  expect "${c}" "no API token" "false" "$(wl "${c}" '.spec.template.spec.automountServiceAccountToken')"
  expect "${c}" "no hostPath volume" "0" "$(wl "${c}" '.spec.template.spec.volumes | map(select(.hostPath)) | length')"
  expect "${c}" "no volume that is not read-only or private for the main container except the storage ones" "" \
    "$(wl "${c}" '.spec.template.spec.containers[0].volumeMounts | map(select(.readOnly != true)) | map(.name) | sort | join(",")' | grep -vx 'binaries,storage,tmp\|storage,tmp' || true)"
}

# check_images <case>: the three images of the pod, unchanged upstream images pinned by the digests of ci/versions.env
check_images() {
  local c="$1"
  expect "${c}" "init container 1: the updater, by command install-self" "ghcr.io/myriarch/myriarch-updater:${app_version} /usr/local/bin/myriarch-updater install-self /shared" \
    "$(wl "${c}" '.spec.template.spec.initContainers[0] | [.image, .command[0], .args[0], .args[1]] | join(" ")')"
  expect "${c}" "init container 2: the unchanged collector image runs the updater from the volume" \
    "docker.io/otel/opentelemetry-collector-contrib@${contrib_digest} /shared/myriarch-updater copy /otelcol-contrib /shared/" \
    "$(wl "${c}" '.spec.template.spec.initContainers[1] | [.image, .command[0], .args[0], .args[1], .args[2]] | join(" ")')"
  # The seed container writes the whole collector binary (about 400 MB) to the shared volume; 256Mi was OOM-killed in a chart test.
  expect "${c}" "init containers: the memory limit covers the collector binary that the seed container writes (1Gi)" "1Gi 1Gi" \
    "$(wl "${c}" '.spec.template.spec.initContainers | map(.resources.limits.memory) | join(" ")')"
  expect "${c}" "main container: the unchanged supervisor image" "docker.io/otel/opentelemetry-collector-opampsupervisor@${supervisor_digest}" \
    "$(wl "${c}" '.spec.template.spec.containers[0].image')"
}

run_case() { # case-name [helm template args...]
  local name="$1"; shift
  if render "${name}" "$@"; then
    log "${name}: helm template ok"
    validate "${name}"
    return 0
  fi
  cat "${err}" >&2
  fail_case "${name}" "helm template"
  return 1
}

# must_fail <case> <message part> [helm template args...]: rendering fails and the error names the part
must_fail() {
  local name="$1" part="$2"; shift 2
  if render "${name}" "$@"; then
    fail_case "${name}" "rendering succeeded, it should fail with '${part}'"
  elif grep -Fq -- "${part}" "${err}"; then
    log "${name}: rendering fails as intended ($(grep -Fo -- "${part}" "${err}" | head -n 1))"
  else
    cat "${err}" >&2
    fail_case "${name}" "rendering failed, but the error does not contain '${part}'"
  fi
}

# ---------------------------------------------------------------------------------------------------------------------
# StatefulSet (default)
# ---------------------------------------------------------------------------------------------------------------------
if run_case default; then
  c="default"
  check_restricted "${c}"
  check_images "${c}"
  expect "${c}" "kinds" "ConfigMap NetworkPolicy Service Service ServiceAccount StatefulSet" "$(yq '.kind' "${out}/${c}.yaml" | grep -v '^---$' | grep -v '^$' | sort | tr '\n' ' ' | sed 's/ $//')"
  expect "${c}" "volumeClaimTemplate storage only" "storage" "$(wl ${c} '.spec.volumeClaimTemplates | map(.metadata.name) | join(",")')"
  expect "${c}" "no second claim without container mode" "false" "$(wl ${c} '.spec.template.spec.volumes | map(.name) | contains(["binaries"])')"
  expect "${c}" "the supervisor starts the collector from the shared volume" "/shared/otelcol-contrib" \
    "$(one ${c} ConfigMap '.data["supervisor.yaml"]' | yq '.agent.executable')"
  expect "${c}" "the supervisor config has no CA without one" "null" "$(one ${c} ConfigMap '.data["supervisor.yaml"]' | yq '.server.tls')"
  expect "${c}" "wss:// by default" "wss://fleet.example.com/v1/opamp" "$(wl ${c} '.spec.template.spec.containers[0].env[] | select(.name == "MYRIARCH_OPAMP_ENDPOINT") | .value')"
  expect "${c}" "the token comes from a Secret" "myriarch-enrollment token" \
    "$(wl ${c} '.spec.template.spec.containers[0].env[] | select(.name == "MYRIARCH_ENROLLMENT_TOKEN") | .valueFrom.secretKeyRef | [.name, .key] | join(" ")')"
  expect "${c}" "site.type" "k8s-gateway" "$(wl ${c} '.spec.template.spec.containers[0].env[] | select(.name == "SITE_TYPE") | .value')"
  expect "${c}" "probes: supervisor liveness, collector readiness" "/health sup-health / health" \
    "$(wl ${c} '.spec.template.spec.containers[0] | [.livenessProbe.httpGet.path, .livenessProbe.httpGet.port, .readinessProbe.httpGet.path, .readinessProbe.httpGet.port] | join(" ")')"
  expect "${c}" "grace period" "45" "$(wl ${c} '.spec.template.spec.terminationGracePeriodSeconds')"
  # The local health file is merged in every state: the endpoint is on the pod IP and on the port of the readiness probe (one source).
  expect "${c}" "the supervisor merges the local health file first" "/etc/myriarch/health.yaml" \
    "$(one ${c} ConfigMap '.data["supervisor.yaml"]' | yq '.agent.config_files | join(" ")')"
  expect "${c}" "the local health file is mounted" "/etc/myriarch/health.yaml" \
    "$(wl ${c} '.spec.template.spec.containers[0].volumeMounts[] | select(.subPath == "health.yaml") | .mountPath')"
  expect "${c}" "health.yaml: health_check on the port of the readiness probe" "0.0.0.0:13133 health_check 13133" \
    "$(one ${c} ConfigMap '.data["health.yaml"]' | yq '.extensions.health_check.endpoint') $(one ${c} ConfigMap '.data["health.yaml"]' | yq '.service.extensions | join(",")') $(wl ${c} '.spec.template.spec.containers[0].ports[] | select(.name == "health") | .containerPort')"
  expect "${c}" "the fallback file uses the same endpoint" "0.0.0.0:13133" "$(one ${c} ConfigMap '.data["fallback.yaml"]' | yq '.extensions.health_check.endpoint')"
  expect "${c}" "fsGroup is the group of the supervisor image's user (a group, not a user)" "10001" "$(wl ${c} '.spec.template.spec.securityContext.fsGroup')"
  expect "${c}" "the shared volume is read-only for the main container" "true" \
    "$(wl ${c} '.spec.template.spec.containers[0].volumeMounts[] | select(.name == "shared") | .readOnly')"
  expect "${c}" "Service ports" "otlp-grpc,otlp-http" "$(one ${c} Service 'select(.metadata.name == "myriarch-myriarch-collector") | .spec.ports | map(.name) | join(",")')"
  expect "${c}" "the NetworkPolicy has no egress" "Ingress" "$(one ${c} NetworkPolicy '.spec.policyTypes | join(",")')"
fi

# the shape of the upstream pins: tag and digest in values.yaml equal ci/versions.env
expect values "tag of the collector image" "${otelcol_version}" "$(yq '.images.collector.tag' "${chart}/values.yaml")"
expect values "tag of the supervisor image" "${otelcol_version}" "$(yq '.images.supervisor.tag' "${chart}/values.yaml")"
expect values "digest of the collector image" "${contrib_digest}" "$(yq '.images.collector.digest' "${chart}/values.yaml")"
expect values "digest of the supervisor image" "${supervisor_digest}" "$(yq '.images.supervisor.digest' "${chart}/values.yaml")"

# ---------------------------------------------------------------------------------------------------------------------
# DaemonSet
# ---------------------------------------------------------------------------------------------------------------------
if run_case daemonset --set mode=daemonset; then
  c="daemonset"
  check_restricted "${c}"
  check_images "${c}"
  expect "${c}" "kinds" "ConfigMap DaemonSet NetworkPolicy ServiceAccount" "$(yq '.kind' "${out}/${c}.yaml" | grep -v '^---$' | grep -v '^$' | sort | tr '\n' ' ' | sed 's/ $//')"
  expect "${c}" "the identity is an emptyDir without hostPath" "true false" "$(wl ${c} '.spec.template.spec.volumes[] | select(.name == "storage") | [has("emptyDir"), has("hostPath")] | join(" ")')"
  expect "${c}" "site.type" "k8s-node" "$(wl ${c} '.spec.template.spec.containers[0].env[] | select(.name == "SITE_TYPE") | .value')"
  expect "${c}" "rolling update" "10%" "$(wl ${c} '.spec.updateStrategy.rollingUpdate.maxUnavailable')"
fi
if run_case daemonset-hostpath --set mode=daemonset --set persistence.hostPath.enabled=true --set nodeLogs.enabled=true \
    --set-json 'tolerations=[{"operator":"Exists"}]'; then
  c="daemonset-hostpath"
  expect "${c}" "the only hostPath volumes: the identity and the node logs" "node-logs:/var/log/pods storage:/var/lib/myriarch/myriarch-myriarch-collector" \
    "$(wl ${c} '.spec.template.spec.volumes | map(select(.hostPath)) | sort_by(.name) | map(.name + ":" + .hostPath.path) | join(" ")')"
  expect "${c}" "the node logs are mounted read-only" "true" \
    "$(wl ${c} '.spec.template.spec.containers[0].volumeMounts[] | select(.name == "node-logs") | .readOnly')"
  expect "${c}" "toleration" "Exists" "$(wl ${c} '.spec.template.spec.tolerations[0].operator')"
  if grep -Eq 'privileged: true' "${out}/${c}.yaml"; then fail_case "${c}" "a privileged container"; fi
fi

if run_case no-health-file --set healthCheck.enabled=false; then
  expect no-health-file "no local health file" "null false" \
    "$(one no-health-file ConfigMap '.data["supervisor.yaml"]' | yq '.agent.config_files') $(one no-health-file ConfigMap '.data | has("health.yaml")')"
  expect no-health-file "the health file is not mounted" "0" "$(wl no-health-file '[.spec.template.spec.containers[0].volumeMounts[] | select(.subPath == "health.yaml")] | length')"
fi

# ---------------------------------------------------------------------------------------------------------------------
# container mode
# ---------------------------------------------------------------------------------------------------------------------
if run_case container --set containerMode.enabled=true --set containerMode.trustedRoot.existingConfigMap=myriarch-trusted-root; then
  c="container"
  check_restricted "${c}"
  check_images "${c}"
  expect "${c}" "two volumeClaimTemplates" "storage,binaries" "$(wl ${c} '.spec.volumeClaimTemplates | map(.metadata.name) | join(",")')"
  expect "${c}" "the updater is PID 1" "/shared/myriarch-updater container --config=/etc/myriarch/updater.yaml" \
    "$(wl ${c} '.spec.template.spec.containers[0] | .command[0] + " " + (.args | join(" "))')"
  expect "${c}" "probes on :13135" "updater-health updater-health updater-health" \
    "$(wl ${c} '.spec.template.spec.containers[0] | [.startupProbe.httpGet.port, .livenessProbe.httpGet.port, .readinessProbe.httpGet.port] | join(" ")')"
  expect "${c}" "the port 13135 is declared" "13135" "$(wl ${c} '.spec.template.spec.containers[0].ports[] | select(.name == "updater-health") | .containerPort')"
  expect "${c}" "grace period of at least 360 s" "360" "$(wl ${c} '.spec.template.spec.terminationGracePeriodSeconds')"
  expect "${c}" "the supervisor starts the collector that the updater activates" "/var/lib/myriarch/collector/current/otelcol-contrib" \
    "$(one ${c} ConfigMap '.data["supervisor.yaml"]' | yq '.agent.executable')"
  expect "${c}" "updater.yaml: seed, symlink activation and signature" "/shared/otelcol-contrib symlink true /etc/myriarch/trust/trusted_root.json" \
    "$(one ${c} ConfigMap '.data["updater.yaml"]' | yq '[.container.seedBinary, .collector.activation, .update.requireSignature, .update.trustedRootFile] | join(" ")')"
  expect "${c}" "updater.yaml: no insecure transport" "false" "$(one ${c} ConfigMap '.data["updater.yaml"]' | yq '.server.allowInsecureTransport')"
  expect "${c}" "the NetworkPolicy allows the health port of the updater" "true" "$(one ${c} NetworkPolicy '.spec.ingress | map(.ports[].port) | contains(["updater-health"])')"
fi
if run_case container-more --set containerMode.enabled=true --set containerMode.update.requireSignature=false --set terminationGracePeriodSeconds=900; then
  expect container-more "a larger grace period stays" "900" "$(wl container-more '.spec.template.spec.terminationGracePeriodSeconds')"
  expect container-more "no trust volume without a trusted root" "false" "$(wl container-more '.spec.template.spec.volumes | map(.name) | contains(["trust"])')"
fi
must_fail container-no-root "containerMode.trustedRoot.existingConfigMap: required" --set containerMode.enabled=true
must_fail container-daemonset "available for mode=statefulset only" --set mode=daemonset --set containerMode.enabled=true --set containerMode.update.requireSignature=false

# ---------------------------------------------------------------------------------------------------------------------
# connection to the server
# ---------------------------------------------------------------------------------------------------------------------
must_fail ws-without-optin "ws:// sends the enrollment token in clear text" --set server.endpoint=ws://myriarch.myriarch.svc:8080/v1/opamp
must_fail http-scheme "does not match pattern" --set server.endpoint=https://fleet.example.com/v1/opamp
if run_case ws-optin --set server.endpoint=ws://myriarch.myriarch.svc:8080/v1/opamp --set server.allowInsecureTransport=true; then
  expect ws-optin "the endpoint" "ws://myriarch.myriarch.svc:8080/v1/opamp" "$(wl ws-optin '.spec.template.spec.containers[0].env[] | select(.name == "MYRIARCH_OPAMP_ENDPOINT") | .value')"
fi
if run_case ca-secret --set server.tls.existingSecret=fleet-ca --set server.tls.key=ca.crt; then
  c="ca-secret"
  expect "${c}" "ca_file in the supervisor config" "/etc/myriarch/ca/ca.crt" "$(one ${c} ConfigMap '.data["supervisor.yaml"]' | yq '.server.tls.ca_file')"
  expect "${c}" "the CA volume is a read-only Secret mount" "fleet-ca true" \
    "$(wl ${c} '.spec.template.spec | [(.volumes[] | select(.name == "ca") | .secret.secretName), (.containers[0].volumeMounts[] | select(.name == "ca") | .readOnly)] | join(" ")')"
fi
if run_case ca-configmap --set containerMode.enabled=true --set containerMode.update.requireSignature=false --set server.tls.existingConfigMap=fleet-ca; then
  c="ca-configmap"
  expect "${c}" "the CA volume is a ConfigMap" "fleet-ca" "$(wl ${c} '.spec.template.spec.volumes[] | select(.name == "ca") | .configMap.name')"
  expect "${c}" "updater.yaml: caFile" "/etc/myriarch/ca/ca.pem" "$(one ${c} ConfigMap '.data["updater.yaml"]' | yq '.server.caFile')"
fi
must_fail ca-both "set existingSecret or existingConfigMap, not both" --set server.tls.existingSecret=a --set server.tls.existingConfigMap=b

# ---------------------------------------------------------------------------------------------------------------------
# options
# ---------------------------------------------------------------------------------------------------------------------
must_fail node-logs-statefulset "the DaemonSet only" --set nodeLogs.enabled=true
must_fail hostpath-statefulset "only for mode=daemonset" --set persistence.hostPath.enabled=true
if run_case options --set replicaCount=3 --set site.id=k8s-de-prod --set site.name="Regional gateway DE" --set site.type=custom \
    --set persistence.size=5Gi --set persistence.storageClass=fast --set service.enabled=false --set networkPolicy.enabled=false \
    --set-json 'extraEnv=[{"name":"BACKEND_API_TOKEN","valueFrom":{"secretKeyRef":{"name":"backend","key":"token"}}}]' \
    --set-json 'topologySpreadConstraints=[{"maxSkew":1,"topologyKey":"kubernetes.io/hostname","whenUnsatisfiable":"ScheduleAnyway"}]'; then
  c="options"
  expect "${c}" "replicas" "3" "$(wl ${c} '.spec.replicas')"
  expect "${c}" "claim" "5Gi fast" "$(wl ${c} '.spec.volumeClaimTemplates[0].spec | [.resources.requests.storage, .storageClassName] | join(" ")')"
  expect "${c}" "no Service for the ports and no NetworkPolicy" "1 0" "$(q ${c} 'select(.kind == "Service") | .metadata.name' | grep -c headless) $(q ${c} 'select(.kind == "NetworkPolicy") | .kind' | grep -c NetworkPolicy || true)"
  expect "${c}" "site attributes" "k8s-de-prod Regional gateway DE custom" \
    "$(wl ${c} '.spec.template.spec.containers[0].env | map(select(.name == "SITE_ID" or .name == "SITE_NAME" or .name == "SITE_TYPE")) | sort_by(.name) | map(.value) | .[0] + " " + .[1] + " " + .[2]' | sed 's/^\(.*\) \(.*\) \(.*\)$/\1 \2 \3/')"
  expect "${c}" "the spread constraint gets the labels of the release" "myriarch-collector" \
    "$(wl ${c} '.spec.template.spec.topologySpreadConstraints[0].labelSelector.matchLabels["app.kubernetes.io/name"]')"
fi
must_fail schema-unknown-key "additional properties 'bogus' not allowed" --set bogus=1
must_fail schema-bad-digest "digest" --set images.updater.digest=sha256:abc
must_fail schema-bad-mode "mode" --set mode=deployment
must_fail schema-no-secret-name "/enrollment/existingSecret" --set enrollment.existingSecret=
if run_case digest-and-mirror --set images.updater.digest=sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef \
    --set global.imageRegistry=mirror.example.com:5000; then
  c="digest-and-mirror"
  expect "${c}" "the updater by digest, all three images from the mirror" \
    "mirror.example.com:5000/myriarch/myriarch-updater@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef mirror.example.com:5000/otel/opentelemetry-collector-contrib@${contrib_digest} mirror.example.com:5000/otel/opentelemetry-collector-opampsupervisor@${supervisor_digest}" \
    "$(wl ${c} '.spec.template.spec | [.initContainers[0].image, .initContainers[1].image, .containers[0].image] | join(" ")')"
fi

# the examples of the chart render and validate
for f in "${chart}"/examples/*.yaml; do
  run_case "example-$(basename "${f}" .yaml)" -f "${f}" || true
done

# ---------------------------------------------------------------------------------------------------------------------
# OpenShift profile: no fixed IDs at all
# ---------------------------------------------------------------------------------------------------------------------
check_openshift() { # case
  local c="$1"
  check_restricted "${c}" openshift
  expect "${c}" "no fsGroup" "null" "$(wl "${c}" '.spec.template.spec.securityContext.fsGroup')"
  expect "${c}" "no fsGroupChangePolicy" "null" "$(wl "${c}" '.spec.template.spec.securityContext.fsGroupChangePolicy')"
  if grep -Eq 'fsGroup|runAsUser|runAsGroup' "${out}/${c}.yaml"; then fail_case "${c}" "the OpenShift profile renders a user, group or fsGroup"; fi
}
if run_case openshift --set openshift.enabled=true; then check_openshift openshift; fi
if run_case openshift-daemonset --set openshift.enabled=true --set mode=daemonset; then check_openshift openshift-daemonset; fi
if run_case openshift-container --set openshift.enabled=true --set containerMode.enabled=true --set containerMode.trustedRoot.existingConfigMap=myriarch-trusted-root; then
  check_openshift openshift-container
fi
if run_case openshift-auto --set openshift.enabled=auto --api-versions security.openshift.io/v1; then check_openshift openshift-auto; fi
if run_case auto-standard --set openshift.enabled=auto; then
  expect auto-standard "auto without the API renders the standard profile" "10001" "$(wl auto-standard '.spec.template.spec.securityContext.fsGroup')"
fi

# the identity is an overridable default: null leaves the fields out, a value replaces them
if run_case identity-null --set podSecurityContext.runAsUser=null --set podSecurityContext.runAsGroup=null --set podSecurityContext.fsGroup=null; then
  if grep -Eq 'runAsUser|runAsGroup|fsGroup:' "${out}/identity-null.yaml"; then fail_case identity-null "null did not leave the identity fields out"; fi
fi
if run_case identity-override --set podSecurityContext.runAsUser=20000 --set podSecurityContext.runAsGroup=20001; then
  expect identity-override "overridden identity" "20000 20001 10001" \
    "$(wl identity-override '.spec.template.spec.securityContext | [.runAsUser, .runAsGroup, .fsGroup] | join(" ")')"
fi

if [ "${status}" = 0 ]; then log "helm-conform-collector: all cases ok (${out})"; else log "helm-conform-collector: FAILED"; fi
exit "${status}"
