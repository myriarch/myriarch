#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Offline verification of the production example (deploy/examples/production). Never writes to the repository.
#
#   deploy/examples/production/verify.sh
#
# The registry prefix of the example (registry.example.com) is a placeholder: replace it via IMAGE_REGISTRY in your
# own copy of the values and the collector overlay. This script checks digest pinning, not fixed registry hosts.
#
# Steps (every step reports its own findings; the script collects them and exits 1 if there are any):
#   1. helm lint of the chart with myriarch-values.yaml, with each ingress variant
#      (deploy/helm/myriarch/examples/ingress-*.yaml) and with the OpenShift variant (examples/openshift.yaml)
#   2. helm template (base + each variant) into $VERIFY_OUT, with a fixed test digest (image.digest); the ingress
#      variants must render an Ingress with TLS, the OpenShift variant a Route and no Ingress
#   3. myriarchd.yaml from the rendered ConfigMap: `myriarchd config validate --skip-files`; the standalone
#      deploy/examples/production/myriarchd.yaml must be equivalent to it (keys sorted)
#      and every /etc/myriarchd/secrets/<key> path must have a key in create-secrets.example.sh
#   4. kustomize build of the gateway overlay (no DaemonSet, digest-pinned image, wss:// endpoint, CA wiring,
#      optional component)
#   5. optional kubeconform over all rendered manifests (when KUBECONFORM is set)
#
# Environment:
#   MYRIARCHD_BIN          myriarchd binary with `config validate` (default: built into $VERIFY_OUT with `go build`)
#   VERIFY_SKIP_MYRIARCHD  1 = skip `config validate` with a loud warning (only while the binary lacks the command;
#                       CI never sets it)
#   HELM                helm binary (default: helm)
#   YQ                  mikefarah/yq v4 (default: yq)
#   KUSTOMIZE           kustomize binary (default: kustomize, else `kubectl kustomize`)
#   KUBECONFORM         kubeconform binary; set to enable step 5 (a resource without a schema fails the step;
#                       KUBECONFORM_ALLOW_SKIPPED=1 accepts it)
#   KUBERNETES_VERSION  schema version for kubeconform (default 1.30.0)
#   VERIFY_OUT          output directory (default: mktemp -d)
#   VERIFY_NO_PLACEHOLDERS  1 = also fail while placeholders (all-zero GUIDs, example.com hosts) remain in the values
#                       (use this as the go-live gate for a real environment)
# Exit codes: 0 all valid, 1 findings, 2 usage/tool problem.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd -P)"
CHART="$ROOT/deploy/helm/myriarch"
INGRESS_EXAMPLES="$CHART/examples"
OPENSHIFT_EXAMPLE="$INGRESS_EXAMPLES/openshift.yaml"
VALUES="$HERE/myriarch-values.yaml"
STANDALONE="$HERE/myriarchd.yaml"
OVERLAY="$HERE/collector"
COMPONENT="$OVERLAY/components/external-gateway"
SECRETS_SCRIPT="$HERE/create-secrets.example.sh"
SUPERVISOR_TEMPLATE="$ROOT/deploy/supervisor/k8s/supervisor.yaml"

HELM="${HELM:-helm}"
YQ="${YQ:-yq}"
KUBERNETES_VERSION="${KUBERNETES_VERSION:-1.30.0}"
# Fixed test digest (64 hex characters): the example must render the myriarchd image pinned by digest.
TEST_DIGEST="sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
DIGEST_RE='@sha256:[0-9a-f]{64}$'

FAILS=()
fail() { FAILS+=("$1"); printf 'FAIL: %s\n' "$1" >&2; }
note() { printf '%s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 2; }

# --- tools --------------------------------------------------------------------------------------------------------
command -v "$HELM" >/dev/null 2>&1 || die "helm not found (set HELM)"
command -v "$YQ" >/dev/null 2>&1 || die "yq not found (set YQ; needs mikefarah/yq v4)"
"$YQ" --version 2>&1 | grep -qi 'mikefarah' || die "$YQ is not mikefarah/yq v4 (the Python 'yq' wrapper is not supported)"
if [ -z "${KUSTOMIZE:-}" ]; then
  if command -v kustomize >/dev/null 2>&1; then KUSTOMIZE=kustomize
  elif command -v kubectl >/dev/null 2>&1; then KUSTOMIZE="kubectl kustomize"
  else die "neither kustomize nor kubectl found (set KUSTOMIZE)"; fi
fi
for f in "$VALUES" "$STANDALONE" "$SECRETS_SCRIPT" "$SUPERVISOR_TEMPLATE" "$OPENSHIFT_EXAMPLE"; do
  [ -f "$f" ] || die "missing file: $f"
done

if [ -z "${VERIFY_OUT:-}" ]; then VERIFY_OUT="$(mktemp -d)"; fi
mkdir -p "$VERIFY_OUT" || die "cannot create VERIFY_OUT=$VERIFY_OUT"
VERIFY_OUT="$(cd "$VERIFY_OUT" && pwd -P)"   # physical path: kustomize resolves symlinks (macOS: /var -> /private/var)
case "$VERIFY_OUT" in "$ROOT"|"$ROOT"/*) die "VERIFY_OUT must be outside the repository ($ROOT)";; esac
note "output directory: $VERIFY_OUT"

VARIANTS=()
for v in "$INGRESS_EXAMPLES"/ingress-*.yaml; do [ -f "$v" ] && VARIANTS+=("$v"); done
[ "${#VARIANTS[@]}" -gt 0 ] || fail "no ingress variants found in $INGRESS_EXAMPLES"

# sorted, comment-free JSON of a YAML document (for comparisons)
normalize() { "$YQ" -o=json -I=2 'sort_keys(..)' "$1"; }

# --- 1. helm lint -------------------------------------------------------------------------------------------------
step "1. helm lint"
lint() { # lint <label> <extra helm args...>
  local label="$1"; shift
  if out="$("$HELM" lint "$CHART" "$@" 2>&1)"; then note "ok: helm lint $label"
  else fail "helm lint $label"; printf '%s\n' "$out" >&2; fi
}
lint "(chart defaults)"
lint "myriarch-values.yaml" -f "$VALUES" --set image.digest="$TEST_DIGEST"
for v in "${VARIANTS[@]}" "$OPENSHIFT_EXAMPLE"; do lint "myriarch-values.yaml + $(basename "$v")" -f "$VALUES" -f "$v" --set image.digest="$TEST_DIGEST"; done

# --- 2. helm template ---------------------------------------------------------------------------------------------
step "2. helm template"
render() { # render <outfile> <extra helm args...>
  local outfile="$1"; shift
  if "$HELM" template myriarch "$CHART" -n myriarch --set image.digest="$TEST_DIGEST" "$@" > "$outfile" 2> "$outfile.err"; then
    note "ok: rendered $(basename "$outfile")"; rm -f "$outfile.err"; return 0
  fi
  fail "helm template $(basename "$outfile")"; cat "$outfile.err" >&2; return 1
}
RENDERED="$VERIFY_OUT/rendered.yaml"
render "$RENDERED" -f "$VALUES"
RENDERED_FILES=()
[ -s "$RENDERED" ] && RENDERED_FILES+=("$RENDERED")
for v in "${VARIANTS[@]}"; do
  name="$(basename "$v" .yaml)"
  if render "$VERIFY_OUT/rendered-$name.yaml" -f "$VALUES" -f "$v"; then
    RENDERED_FILES+=("$VERIFY_OUT/rendered-$name.yaml")
    # every variant must keep the TLS Ingress with the WebSocket timeouts for its controller (annotation set differs)
    n="$("$YQ" 'select(.kind == "Ingress") | .spec.tls | length' "$VERIFY_OUT/rendered-$name.yaml" 2>/dev/null)"
    [ "${n:-0}" -ge 1 ] || fail "ingress variant $name renders an Ingress without spec.tls"
  fi
done

# OpenShift variant: a Route with edge TLS instead of an Ingress; no fixed user, group or fsGroup
OS_RENDERED="$VERIFY_OUT/rendered-openshift.yaml"
if render "$OS_RENDERED" -f "$VALUES" -f "$OPENSHIFT_EXAMPLE"; then
  RENDERED_FILES+=("$OS_RENDERED")
  [ "$("$YQ" 'select(.kind == "Route") | .spec.tls.termination' "$OS_RENDERED")" = "edge" ] || fail "openshift variant renders no Route with edge TLS"
  [ -z "$("$YQ" 'select(.kind == "Ingress") | .metadata.name' "$OS_RENDERED")" ] || fail "openshift variant still renders an Ingress"
  psc="$("$YQ" 'select(.kind == "Deployment") | .spec.template.spec.securityContext | (has("runAsUser") or has("runAsGroup") or has("fsGroup"))' "$OS_RENDERED")"
  [ "$psc" = "false" ] || fail "openshift variant keeps runAsUser, runAsGroup or fsGroup in the pod security context"
fi

# invariants of the main rendering
if [ -s "$RENDERED" ]; then
  reps="$("$YQ" 'select(.kind == "Deployment") | .spec.replicas' "$RENDERED")"
  [ "$reps" = "2" ] || fail "Deployment replicas is '$reps', expected 2 (HA)"
  "$YQ" -e 'select(.kind == "NetworkPolicy")' "$RENDERED" >/dev/null 2>&1 || fail "no NetworkPolicy rendered (networkPolicy.enabled must stay true)"
  "$YQ" -e 'select(.kind == "PodDisruptionBudget")' "$RENDERED" >/dev/null 2>&1 || fail "no PodDisruptionBudget rendered"
  img="$("$YQ" 'select(.kind == "Deployment") | .spec.template.spec.containers[0].image' "$RENDERED")"
  # digest pinning instead of fixed registry hosts: <registry>/<path>/myriarchd@sha256:<64 hex>
  if [[ "$img" =~ ^.+/myriarch$DIGEST_RE ]] && [[ "$img" == *"$TEST_DIGEST" ]]; then note "ok: myriarchd image is pinned by digest ($img)"
  else fail "myriarchd image '$img' is not of the form <registry>/myriarch@sha256:<64 hex> with the digest passed by --set image.digest"; fi
  [ "$("$YQ" 'select(.kind == "Deployment") | .spec.template.spec.imagePullSecrets[0].name' "$RENDERED")" = "registry-pull-secret" ] || fail "myriarchd Deployment has no imagePullSecret registry-pull-secret"
  # the optional secretEnv must not block the pod
  opt="$("$YQ" 'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "MYRIARCH_EXEC_AWX_TOKEN") | .valueFrom.secretKeyRef.optional' "$RENDERED")"
  [ "$opt" = "true" ] || fail "MYRIARCH_EXEC_AWX_TOKEN is not an optional secretKeyRef"
fi

# --- 3. myriarchd configuration -----------------------------------------------------------------------------------------
step "3. myriarchd configuration"
RENDERED_CFG="$VERIFY_OUT/myriarchd.rendered.yaml"
if [ -s "$RENDERED" ]; then
  printf '%s\n' "$("$YQ" 'select(.kind == "ConfigMap" and .metadata.name == "myriarch") | .data["myriarchd.yaml"]' "$RENDERED")" > "$RENDERED_CFG"
  if [ "$(cat "$RENDERED_CFG")" = "" ] || [ "$(cat "$RENDERED_CFG")" = "null" ]; then
    fail "could not extract myriarchd.yaml from the rendered ConfigMap"
  else
    note "ok: extracted $RENDERED_CFG"
    # (a) standalone file == rendered ConfigMap
    if diff -u <(normalize "$RENDERED_CFG") <(normalize "$STANDALONE") > "$VERIFY_OUT/config.diff"; then
      note "ok: myriarchd.yaml is equivalent to the rendered ConfigMap"
    else
      fail "deploy/examples/production/myriarchd.yaml differs from the config: block of myriarch-values.yaml (+ chart defaults); diff (rendered vs standalone):"
      cat "$VERIFY_OUT/config.diff" >&2
    fi
    # (b) every secret file referenced by the configuration is a key of the documented Secret
    while IFS= read -r key; do
      [ -n "$key" ] || continue
      if grep -q -- "$key" "$SECRETS_SCRIPT"; then note "ok: secret key '$key' is covered by create-secrets.example.sh"
      else fail "config references /etc/myriarchd/secrets/$key but create-secrets.example.sh does not mention '$key'"; fi
    done < <(grep -o '/etc/myriarchd/secrets/[A-Za-z0-9._-]*' "$RENDERED_CFG" | sed 's#.*/##' | sort -u)
  fi

  # (c) myriarchd's own validation (production rules, no files read, no network)
  if [ "${VERIFY_SKIP_MYRIARCHD:-}" = "1" ]; then
    printf '\nWARNING: VERIFY_SKIP_MYRIARCHD=1 - "myriarchd config validate" was NOT run. The configuration was only checked\n' >&2
    printf '         for equivalence, not against the myriarchd validation rules. Never use this in CI.\n\n' >&2
  else
    if [ -z "${MYRIARCHD_BIN:-}" ]; then
      command -v go >/dev/null 2>&1 || die "MYRIARCHD_BIN not set and go not found"
      MYRIARCHD_BIN="$VERIFY_OUT/myriarchd"
      note "building myriarchd into $MYRIARCHD_BIN"
      if ! (cd "$ROOT" && go build -o "$MYRIARCHD_BIN" ./cmd/myriarchd) 2> "$VERIFY_OUT/build.err"; then
        fail "go build ./cmd/myriarchd failed"; cat "$VERIFY_OUT/build.err" >&2; MYRIARCHD_BIN=""
      fi
    fi
    if [ -n "${MYRIARCHD_BIN:-}" ]; then
      if ! "$MYRIARCHD_BIN" config validate --help >/dev/null 2>&1; then
        fail "$MYRIARCHD_BIN is too old (needs a myriarchd build that has the config validate command; VERIFY_SKIP_MYRIARCHD=1 skips this step while it is missing)"
      else
        for cfg in "$RENDERED_CFG" "$STANDALONE"; do
          if out="$("$MYRIARCHD_BIN" config validate --config "$cfg" --skip-files 2>&1)"; then
            note "ok: myriarchd config validate $(basename "$cfg") -> $(printf '%s' "$out" | head -1)"
            printf '%s\n' "$out" | grep '^WARNING' >&2 || true
          else
            fail "myriarchd config validate $cfg"; printf '%s\n' "$out" >&2
          fi
        done
      fi
    fi
  fi

  # (d) placeholders (information; failure only with VERIFY_NO_PLACEHOLDERS=1)
  ph="$(grep -c -E '00000000-0000-0000-0000-0000000000|[A-Za-z0-9.-]+\.example\.com' "$RENDERED_CFG" || true)"
  if [ "${ph:-0}" -gt 0 ]; then
    if [ "${VERIFY_NO_PLACEHOLDERS:-}" = "1" ]; then fail "$ph placeholder line(s) (example.com hosts, all-zero GUIDs) remain in the rendered configuration"
    else note "info: $ph placeholder line(s) remain in the configuration (replace them before a real installation; VERIFY_NO_PLACEHOLDERS=1 turns this into a failure)"; fi
  fi
fi

# --- 4. gateway collector overlay --------------------------------------------------------------------------------------
step "4. gateway collector overlay (kustomize)"
COLLECTOR="$VERIFY_OUT/collector.yaml"
# shellcheck disable=SC2086 # KUSTOMIZE may be "kubectl kustomize"
if $KUSTOMIZE build "$OVERLAY" > "$COLLECTOR" 2> "$COLLECTOR.err"; then
  note "ok: kustomize build collector overlay"
  [ -z "$("$YQ" 'select(.kind == "DaemonSet") | .metadata.name' "$COLLECTOR")" ] || fail "overlay still renders a DaemonSet"
  [ -z "$("$YQ" 'select(.kind == "NetworkPolicy" and .metadata.name == "otel-agent") | .metadata.name' "$COLLECTOR")" ] || fail "overlay still renders the NetworkPolicy otel-agent"
  [ "$("$YQ" 'select(.kind == "StatefulSet" and .metadata.name == "otel-gateway") | .metadata.name' "$COLLECTOR")" = "otel-gateway" ] || fail "overlay renders no StatefulSet otel-gateway"
  img="$("$YQ" 'select(.kind == "StatefulSet") | .spec.template.spec.containers[0].image' "$COLLECTOR")"
  # digest pinning instead of fixed registry hosts: <registry>/<path>@sha256:<64 hex>
  if [[ "$img" =~ ^.+/.+$DIGEST_RE ]]; then note "ok: gateway image is pinned by digest ($img)"
  else fail "gateway image '$img' is not pinned by digest (<image>@sha256:<64 hex>); set digest: in collector/kustomization.yaml"; fi
  [ "$("$YQ" 'select(.kind == "StatefulSet") | .spec.template.spec.imagePullSecrets[0].name' "$COLLECTOR")" = "registry-pull-secret" ] || fail "gateway has no imagePullSecret registry-pull-secret"
  ep="$("$YQ" 'select(.kind == "ConfigMap" and .metadata.name == "myriarch-site") | .data.MYRIARCH_OPAMP_ENDPOINT' "$COLLECTOR")"
  case "$ep" in wss://*) note "ok: OpAMP endpoint $ep";; *) fail "MYRIARCH_OPAMP_ENDPOINT '$ep' must start with wss://";; esac
  for kv in K8S_CLUSTER_NAME=example-cluster ENVIRONMENT=production SITE_COUNTRY=ZZ SITE_TIMEZONE=UTC SITE_REGION=example-region; do
    got="$("$YQ" 'select(.kind == "ConfigMap" and .metadata.name == "myriarch-site") | .data.'"${kv%%=*}" "$COLLECTOR")"
    [ "$got" = "${kv#*=}" ] || fail "myriarch-site ${kv%%=*} is '$got', expected '${kv#*=}' (base values leaked?)"
  done
  sid="$("$YQ" 'select(.kind == "StatefulSet") | .spec.template.spec.containers[0].env[] | select(.name == "SITE_ID") | .value' "$COLLECTOR")"
  [ "$sid" = "example-cluster" ] || fail "SITE_ID is '$sid', expected example-cluster"
  # CA wiring: ca_file in the rendered supervisor.yaml, mounted from ConfigMap internal-ca
  # (command substitution drops the trailing blank line yq adds after a block scalar)
  printf '%s\n' "$("$YQ" 'select(.kind == "ConfigMap" and .metadata.name == "otel-supervisor-config") | .data["supervisor.yaml"]' "$COLLECTOR")" > "$VERIFY_OUT/supervisor.rendered.yaml"
  cafile="$("$YQ" '.server.tls.ca_file' "$VERIFY_OUT/supervisor.rendered.yaml")"
  if [ -z "$cafile" ] || [ "$cafile" = "null" ]; then fail "supervisor.yaml has no server.tls.ca_file"
  else
    note "ok: supervisor server.tls.ca_file $cafile"
    "$YQ" -e 'select(.kind == "StatefulSet") | .spec.template.spec.containers[0].volumeMounts[] | select(.mountPath == "'"$(dirname "$cafile")"'" and .name == "internal-ca")' "$COLLECTOR" >/dev/null 2>&1 \
      || fail "ca_file $cafile is not covered by a volumeMount of the internal-ca ConfigMap"
  fi
  # supervisor.yaml must equal the template deploy/supervisor/k8s/supervisor.yaml with exactly server.tls.ca_file set
  # (compared structurally: comments and key order are ignored)
  "$YQ" '.server.tls.ca_file = "/etc/myriarch/ca/ca.crt"' "$SUPERVISOR_TEMPLATE" > "$VERIFY_OUT/supervisor.expected.yaml"
  if diff -u <(normalize "$VERIFY_OUT/supervisor.expected.yaml") <(normalize "$VERIFY_OUT/supervisor.rendered.yaml") > "$VERIFY_OUT/supervisor.diff"; then
    note "ok: overlay supervisor.yaml = template deploy/supervisor/k8s/supervisor.yaml + ca_file"
  else
    fail "overlay supervisor.yaml deviates from deploy/supervisor/k8s/supervisor.yaml (apart from server.tls.ca_file); update collector/supervisor-configmap-patch.yaml:"
    cat "$VERIFY_OUT/supervisor.diff" >&2
  fi
  if [ "$("$YQ" 'select(.kind == "ConfigMap" and .metadata.name == "otel-supervisor-config") | .data["fallback.yaml"] | length > 0' "$COLLECTOR")" != "true" ]; then
    fail "fallback.yaml is missing from otel-supervisor-config"
  fi
else
  fail "kustomize build $OVERLAY"; cat "$COLLECTOR.err" >&2
fi

# optional component (external OTLP gateway), built through a wrapper kustomization in $VERIFY_OUT
EXT="$VERIFY_OUT/external-gateway"
mkdir -p "$EXT"
rel() { python3 -c 'import os,sys; print(os.path.relpath(sys.argv[1], sys.argv[2]))' "$1" "$2"; }
if command -v python3 >/dev/null 2>&1; then
  cat > "$EXT/kustomization.yaml" <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - $(rel "$OVERLAY" "$EXT")
components:
  - $(rel "$COMPONENT" "$EXT")
EOF
  # shellcheck disable=SC2086
  if $KUSTOMIZE build "$EXT" > "$VERIFY_OUT/collector-external.yaml" 2> "$VERIFY_OUT/collector-external.err"; then
    t="$("$YQ" 'select(.kind == "Service" and .metadata.name == "otel-gateway-external") | .spec.type' "$VERIFY_OUT/collector-external.yaml")"
    [ "$t" = "LoadBalancer" ] || fail "component external-gateway: Service otel-gateway-external is '$t', expected LoadBalancer"
    [ "$("$YQ" 'select(.kind == "NetworkPolicy" and .metadata.name == "otel-gateway-external") | .metadata.name' "$VERIFY_OUT/collector-external.yaml")" = "otel-gateway-external" ] || fail "component external-gateway: NetworkPolicy missing"
    note "ok: optional component external-gateway builds"
  else
    fail "kustomize build with component external-gateway"; cat "$VERIFY_OUT/collector-external.err" >&2
  fi
else
  note "skip: python3 not found, optional component not built"
fi

# --- 5. kubeconform --------------------------------------------------------------------------------------------------------
step "5. kubeconform"
if [ -n "${KUBECONFORM:-}" ]; then
  command -v "$KUBECONFORM" >/dev/null 2>&1 || [ -x "$KUBECONFORM" ] || die "KUBECONFORM=$KUBECONFORM is not executable"
  # same schema sources as ci/helm-conform.sh: CRD catalog and the OpenShift API (Route), pinned to a commit
  CRD_CATALOG='https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json'
  OPENSHIFT_SCHEMAS='https://raw.githubusercontent.com/melmorabity/openshift-json-schemas/62ad608b18edcd8ef4b6dd3741fc36115813fd18/v4.18-standalone-strict/{{ .ResourceKind }}{{ .KindSuffix }}.json'
  conform_files=("${RENDERED_FILES[@]}" "$COLLECTOR" "$VERIFY_OUT/collector-external.yaml" "$HERE/monitoring/prometheusrule.yaml")
  for f in "${conform_files[@]}"; do
    [ -s "$f" ] || continue
    if out="$("$KUBECONFORM" -strict -ignore-missing-schemas -kubernetes-version "$KUBERNETES_VERSION" \
        -schema-location default -schema-location "$CRD_CATALOG" -schema-location "$OPENSHIFT_SCHEMAS" \
        -summary "$f" 2>&1)"; then
      note "ok: kubeconform $(basename "$f"): $(printf '%s' "$out" | tail -1)"
      skipped="$(printf '%s' "$out" | tail -1 | sed -n 's/.*Skipped: \([0-9][0-9]*\).*/\1/p')"
      if [ "${skipped:-0}" != 0 ]; then
        # a resource without a schema was not validated (for example the Route of the OpenShift profile)
        if [ "${KUBECONFORM_ALLOW_SKIPPED:-0}" = 1 ]; then
          note "warn: kubeconform $(basename "$f"): $skipped resource(s) have no schema and were not validated"
        else
          fail "kubeconform $(basename "$f"): $skipped resource(s) without a schema were skipped (KUBECONFORM_ALLOW_SKIPPED=1 accepts this)"
        fi
      fi
    else
      fail "kubeconform $(basename "$f")"; printf '%s\n' "$out" >&2
    fi
  done
else
  note "skip: KUBECONFORM not set"
fi

# --- result -----------------------------------------------------------------------------------------------------------------
printf '\n'
if [ "${#FAILS[@]}" -eq 0 ]; then
  note "RESULT: all checks passed (output in $VERIFY_OUT)"
  exit 0
fi
printf 'RESULT: %d finding(s):\n' "${#FAILS[@]}" >&2
for f in "${FAILS[@]}"; do printf '  - %s\n' "$f" >&2; done
exit 1
