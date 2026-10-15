#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail" is intended in the tests (ok/fail are counters and never fail)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Contract of the misconfiguration messages of the myriarchd Helm chart (deploy/helm/myriarch): every deliberate
# misconfiguration must make `helm template` fail with an English message that names the value, and the fully configured chart
# must render. No cluster, no network. Needs helm: ci/install-tools.sh helm
#
# Two layers refuse a value, and both are tested:
#  - the values schema (values.schema.json) refuses what a single value can get wrong: an unknown key, a wrong type, a malformed
#    string. Helm validates before it renders, and its message names the value as a JSON pointer ("at '/image/digest': ...");
#  - the templates refuse what depends on several values and what the schema cannot say, with a message that starts with the
#    value path ("execution error at (...): <value.path>: ..."). They also repeat a few checks of the schema (the digest, the
#    OpenShift mode, the names, the keys that the chart generates or does not offer), so that they hold when the validation is
#    switched off (`helm --skip-schema-validation`). The cases (4) to (21g) that the schema refuses first are therefore run with
#    the validation off, and the cases (26) and following run the same input with the validation on.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"

if ! have helm; then
  echo "helm-messages-test: helm not found in PATH or ${TOOLS_BIN}: run 'ci/install-tools.sh helm'" >&2
  exit 1
fi

chart="${ROOT}/deploy/helm/myriarch"
# The default tag of the image is the appVersion of the chart, and scripts/set-version.sh moves it with every release candidate. The
# cases of the image read it from Chart.yaml instead of repeating it, so that a version bump does not break them.
APP="$(sed -n 's/^appVersion: "\([^"[:space:]]*\)"[[:space:]]*$/\1/p' "${chart}/Chart.yaml")"
if [ -z "${APP}" ]; then echo "helm-messages-test: no appVersion found in ${chart}/Chart.yaml" >&2; exit 1; fi
# German umlauts or German function words in a message (word match, case-insensitive)
german='[äöüÄÖÜß]|(^|[^[:alpha:]])(und|oder|nicht|ist|ein|eine|der|das|muss|leer|obwohl|wenn|bei|ohne|sonst|bewusst)([^[:alpha:]]|$)'   # check-language: ignore (German words on purpose)

render() { helm template t "${chart}" "$@"; }
render_guard() { helm template t "${chart}" --skip-schema-validation "$@"; }   # the checks of the templates only

# expect_path <name> <value.path> <helm args...>: rendering fails, the message contains ": <value.path>: " and is not German
expect_path() {
  local name="$1" path="$2"; shift 2
  expect_fail "${name}" ": ${path//./\\.}: " render "$@"
  if printf '%s' "${OUT}" | grep -Eq -- "${german}"; then fail "${name} (message is not English)"; printf '%s\n' "${OUT}" | sed 's/^/        /' >&2; fi
}

# expect_guard <name> <value.path> <helm args...>: like expect_path, with the schema validation off (see the header)
expect_guard() {
  local name="$1" path="$2"; shift 2
  expect_fail "${name}" ": ${path//./\\.}: " render_guard "$@"
  if printf '%s' "${OUT}" | grep -Eq -- "${german}"; then fail "${name} (message is not English)"; printf '%s\n' "${OUT}" | sed 's/^/        /' >&2; fi
}

# expect_schema <name> <json pointer> <helm args...>: the schema validation fails at that place, with an English message
expect_schema() {
  local name="$1" pointer="$2"; shift 2
  expect_fail "${name}" "at '${pointer}'" render "$@"
  if ! printf '%s' "${OUT}" | grep -q "values don't meet the specifications of the schema"; then fail "${name} (not a message of the schema validation)"; printf '%s\n' "${OUT}" | sed 's/^/        /' >&2; fi
  if printf '%s' "${OUT}" | grep -Eq -- "${german}"; then fail "${name} (message is not English)"; printf '%s\n' "${OUT}" | sed 's/^/        /' >&2; fi
}

# expect_renders <name> <regex> <helm args...>: the chart renders, and a line of the output matches the regular expression
expect_renders() {
  local name="$1" want="$2" got; shift 2
  if ! got="$(render "$@" 2>&1)"; then fail "${name} (does not render)"; printf '%s\n' "${got}" | sed 's/^/        /' >&2; return; fi
  if printf '%s\n' "${got}" | grep -Eq -- "${want}"; then ok "${name}"; else fail "${name} (no line matches /${want}/)"; fi
}

# expect_image <name> <image> <helm args...>: the chart renders and the pod runs exactly this image (RENDERER names the function
# that renders: the chart itself by default, or the parent chart of the case (67))
expect_image() {
  local name="$1" want="$2" got; shift 2
  if ! got="$("${RENDERER:-render}" "$@" 2>&1)"; then fail "${name} (does not render)"; printf '%s\n' "${got}" | sed 's/^/        /' >&2; return; fi
  got="$(printf '%s\n' "${got}" | sed -n 's/^ *image: "\(.*\)"$/\1/p' | sort -u)"
  if [ "${got}" = "${want}" ]; then ok "${name}"; else fail "${name} (image ${got:-none}, want ${want})"; fi
}

TP=(--set-json 'config.server.trustedProxies=["10.0.0.0/8"]' --set networkPolicy.ingress.allowSameNamespace=false)   # trusted proxies (need allowSameNamespace=false)
ING=(--set ingress.enabled=true)                                                    # Ingress (off by default)
FROM=(--set-json 'networkPolicy.ingress.from=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"ingress"}}}]')
RT=(--set httpRoute.enabled=true)                                                   # HTTPRoute
REFS=(--set-json 'httpRoute.parentRefs=[{"name":"gw"}]')
HOSTS=(--set-json 'httpRoute.hostnames=["fleet.example.com"]')

expect_path "(1) Ingress without trustedProxies" config.server.trustedProxies "${ING[@]}" "${FROM[@]}"
expect_path "(2) ingress without TLS secret for an https publicUrl" ingress.tls "${TP[@]}" "${ING[@]}" "${FROM[@]}"
expect_path "(3) networkPolicy without an ingress controller source" networkPolicy.ingress.from "${TP[@]}" "${ING[@]}" --set ingress.allowWithoutTls=true
expect_guard "(4) malformed image digest" image.digest --set image.digest=bogus
expect_path "(5) HTTPRoute without parentRefs" httpRoute.parentRefs "${TP[@]}" "${FROM[@]}" "${RT[@]}"
expect_path "(6) HTTPRoute without hostnames" httpRoute.hostnames "${TP[@]}" "${FROM[@]}" "${RT[@]}" "${REFS[@]}"
expect_path "(7) HTTPRoute without confirmed TLS termination" httpRoute.tlsTerminatedAtGateway "${TP[@]}" "${FROM[@]}" "${RT[@]}" "${REFS[@]}" "${HOSTS[@]}"
expect_path "(8) HTTPRoute without trustedProxies" config.server.trustedProxies "${FROM[@]}" "${RT[@]}" "${REFS[@]}" "${HOSTS[@]}" --set httpRoute.tlsTerminatedAtGateway=true
expect_guard "(9) openshift.enabled is not true, false or auto" openshift.enabled --set openshift.enabled=maybe
expect_path "(10) trustedProxies together with same-namespace access" networkPolicy.ingress.allowSameNamespace --set-json 'config.server.trustedProxies=["10.0.0.0/8"]'
expect_guard "(11) bootstrap admin set in config" config.auth.bootstrapAdmin --set config.auth.bootstrapAdmin.enabled=true
expect_guard "(12) enrollment tokens set in config" config.opamp.enrollmentTokens --set-json 'config.opamp.enrollmentTokens=[]'
expect_guard "(13) dev login in config" config.auth.devLogin --set config.auth.devLogin.enabled=true
expect_guard "(14) dev mode in config" config.dev --set config.dev.seedEnrollmentToken=x
expect_path "(15) existingSecret without token key and password hash key" bootstrapAdmin.tokenKey --set bootstrapAdmin.existingSecret=my-bootstrap --set bootstrapAdmin.tokenKey=
expect_path "(16) password hash key without existingSecret" bootstrapAdmin.passwordHashKey --set bootstrapAdmin.passwordHashKey=password-hash
expect_guard "(17) invalid bootstrap user name" bootstrapAdmin.username --set 'bootstrapAdmin.username=not valid!'
expect_guard "(18) enrollment token name is not a DNS label" 'enrollmentTokens\[0\].name' --set-json 'enrollmentTokens=[{"name":"Not_A_Label"}]'
expect_path "(19) duplicate enrollment token name" 'enrollmentTokens\[1\].name' --set-json 'enrollmentTokens=[{"name":"a"},{"name":"a"}]'
expect_path "(20) Route host cannot be derived" openshift.route.host --set openshift.enabled=true --set config.server.publicUrl=
expect_path "(21) no login path" bootstrapAdmin.enabled --set bootstrapAdmin.enabled=false
# OIDC in the environment is invisible to the chart: without the declaration there still is no login path, and the message names the value
OIDCENV=(--set-json 'extraEnv=[{"name":"MYRIARCH_AUTH_OIDC_ENABLED","value":"true"}]')
expect_path "(21b) OIDC only in extraEnv, not declared" bootstrapAdmin.enabled --set bootstrapAdmin.enabled=false "${OIDCENV[@]}"
expect_fail "(21c) the no-login-path message names oidcFromEnvironment" 'oidcFromEnvironment' render --set bootstrapAdmin.enabled=false

# inline secrets would be stored in the ConfigMap
expect_guard "(21d) inline database DSN" config.database.dsn --set config.database.dsn=postgres://u:p@db/x
expect_guard "(21e) inline OIDC client secret" config.auth.oidc.clientSecret --set config.auth.oidc.clientSecret=s3cret
expect_guard "(21f) inline GitOps webhook secret" config.gitops.webhookSecret --set config.gitops.webhookSecret=s3cret
expect_guard "(21g) inline notification URL" 'config.notifications.channels\[0\].url' --set-json 'config.notifications.channels=[{"name":"a","type":"slack","url":"https://hooks.example.com/x"}]'

expect_ok "(22) the defaults render" render
expect_ok "(23) the OpenShift profile renders" render --set openshift.enabled=true
expect_ok "(24) the Dex quickstart renders" render -f "${chart}/examples/quickstart-dex.yaml"
expect_ok "(25) the configured Ingress renders" render "${TP[@]}" "${ING[@]}" "${FROM[@]}" --set-json 'ingress.tls=[{"secretName":"fleet-tls","hosts":["fleet.example.com"]}]'
expect_ok "(25b) OIDC declared through the environment renders without the bootstrap admin" render --set bootstrapAdmin.enabled=false --set oidcFromEnvironment=true "${OIDCENV[@]}"

# --- the values schema (values.schema.json): the same input as the guard cases above, and what only the schema can see ---------------
TOKEN="myre_$(printf 'a%.0s' $(seq 1 40))"   # the form of an enrollment token, never a real one
DIGEST="sha256:$(printf '0123456789abcdef%.0s' 1 2 3 4)"

expect_schema "(26) unknown top-level key" '' --set replicas=3
expect_schema "(27) unknown nested key" /image --set image.tagg=0.2.0
expect_schema "(28) misspelled key of the configuration" /config/server --set config.server.listenAdr=:8080
expect_schema "(29) unknown section of the configuration" /config --set config.serverr.listenAddr=:8080
expect_schema "(30) unknown key in a list entry" /ingress/hosts/0 --set-json 'ingress.hosts=[{"host":"fleet.example.com","hostt":"x"}]'
expect_schema "(31) unknown key of a probe" /probes/liveness --set probes.liveness.initialDelaySeconds=5
expect_schema "(32) replicaCount is not a number" /replicaCount --set replicaCount=two
expect_schema "(33) a number setting of the configuration is a word" /config/opamp/maxConnectionsPerReplica --set config.opamp.maxConnectionsPerReplica=many
expect_schema "(34) a list setting of the configuration is a string" /config/server/trustedProxies --set config.server.trustedProxies=10.0.0.0/8
expect_schema "(35) a duration is a number" /config/server/shutdownTimeout --set config.server.shutdownTimeout=30
expect_schema "(36) a duration has no Go unit" /config/retention/audit --set config.retention.audit=30d
expect_schema "(37) malformed image digest" /image/digest --set image.digest=bogus
expect_schema "(38) openshift.enabled is not true, false or auto" /openshift/enabled --set openshift.enabled=maybe
expect_schema "(39) service type" /service/type --set service.type=ExternalName
expect_schema "(40) port out of range" /service/port --set service.port=70000
expect_schema "(41) image registry with a scheme" /image/registry --set image.registry=https://ghcr.io
expect_schema "(42) image repository with a tag" /image/repository --set image.repository=ghcr.io/myriarch/myriarch:0.2.0
expect_schema "(43) image repository in upper case" /image/repository --set image.repository=Myriarch/Myriarch
expect_schema "(43b) first segment of the image repository in upper case and no registry host" /image/repository --set image.repository=Myriarch/myriarch
expect_schema "(44) global image registry with a scheme" /global/imageRegistry --set global.imageRegistry=https://mirror.example.com
expect_schema "(45) bootstrap user name" /bootstrapAdmin/username --set 'bootstrapAdmin.username=not valid!'
expect_schema "(46) enrollment token name is not a DNS label" /enrollmentTokens/0/name --set-json 'enrollmentTokens=[{"name":"Not_A_Label"}]'

# secrets are not values: the schema has no key for them, and a reference to a Secret never has the form of a token
expect_schema "(47) enrollment token as a value" /enrollmentTokens/0 --set-json "enrollmentTokens=[{\"name\":\"a\",\"token\":\"${TOKEN}\"}]"
expect_schema "(48) enrollment token as the key of a Secret" /enrollmentTokens/0/key --set-json "enrollmentTokens=[{\"name\":\"a\",\"key\":\"${TOKEN}\"}]"
expect_schema "(49) enrollment token as the name of a Secret" /enrollmentTokens/0/existingSecret --set-json "enrollmentTokens=[{\"name\":\"a\",\"existingSecret\":\"${TOKEN}\"}]"
expect_schema "(50) enrollment token as the token key of the bootstrap admin" /bootstrapAdmin/tokenKey --set bootstrapAdmin.existingSecret=my-bootstrap --set "bootstrapAdmin.tokenKey=${TOKEN}"
expect_schema "(51) enrollment tokens in the configuration" /config/opamp --set-json 'config.opamp.enrollmentTokens=[]'
expect_schema "(52) bootstrap admin in the configuration" /config/auth --set config.auth.bootstrapAdmin.enabled=true
expect_schema "(53) development login" /config/auth --set config.auth.devLogin.enabled=true
expect_schema "(54) development mode" /config --set config.dev.seedEnrollmentToken=x
expect_schema "(55) inline database DSN" /config/database --set config.database.dsn=postgres://u:p@db/x
expect_schema "(56) inline OIDC client secret" /config/auth/oidc --set config.auth.oidc.clientSecret=s3cret
expect_schema "(57) inline GitOps webhook secret" /config/gitops --set config.gitops.webhookSecret=s3cret
expect_schema "(58) inline notification URL" /config/notifications/channels/0 --set-json 'config.notifications.channels=[{"name":"a","type":"slack","url":"https://hooks.example.com/x"}]'

# helm lint reads the same schema; a values file is checked like --set
expect_fail "(59) helm lint refuses an unknown key" "at '/image'" helm lint "${chart}" --set image.tagg=0.2.0
printf 'image:\n  tagg: 0.2.0\n' > "${TEST_TMP}/typo.yaml"
expect_fail "(60) a values file with an unknown key" "at '/image'" render -f "${TEST_TMP}/typo.yaml"
expect_fail "(61) the validation can be switched off, the guards stay (an unknown key is then ignored)" 'image.digest: must have' render_guard --set image.tagg=0.2.0 --set image.digest=bogus

# what the schema accepts, and the null that it does not
expect_ok "(62) null switches off an entry of a map" render --set-json 'podAnnotations={"legacy":null}'
# Helm keeps a null for a key that values.yaml does not define, the chart renders it into myriarchd.yaml, and the server refuses it at its start
expect_schema "(62b) null is no value for an entry of a map of the configuration" /config/executors/secretEnvHosts/MYRIARCH_EXEC_A --set-json 'config.executors.secretEnvHosts={"MYRIARCH_EXEC_A":null}'
expect_schema "(62c) null is no value for an executor secret" /config/executors/secrets/awx --set-json 'config.executors.secrets={"awx":null}'
expect_ok "(63) null removes a value that the chart defines" render --set config.server.metricsListenAddr=null
expect_ok "(64) a parent chart may pass a null registry" render --set-json 'global={"imageRegistry":null,"other":{"a":1}}'
expect_ok "(65) quantities as numbers and strings" render --set resources.requests.cpu=1 --set resources.requests.memory=536870912 --set resources.limits.memory=1Gi
expect_ok "(66) a declared enrollment token with a Secret" render --set-json 'enrollmentTokens=[{"name":"gateway","existingSecret":"gateway-token","key":"token","labels":{"site":"hq"},"expiresAt":"2030-01-01T00:00:00Z"}]'

# A parent chart (an umbrella chart) includes this chart with the usual dependency condition. Helm validates the values that the parent
# passes to the subchart, and they contain the key of the condition: `myriarch.enabled` arrives as `enabled`. Copying the chart into
# charts/ of the parent is all Helm needs to load it as a subchart; there is no repository and no network.
umbrella="${TEST_TMP}/umbrella"
mkdir -p "${umbrella}/charts"
cp -R "${chart}" "${umbrella}/charts/myriarch"
printf 'apiVersion: v2\nname: umbrella\nversion: 0.0.1\ndependencies:\n  - name: myriarch\n    version: ">=0.0.0-0"\n    repository: ""\n    condition: myriarch.enabled\n' > "${umbrella}/Chart.yaml"
printf 'global:\n  imageRegistry: mirror.example.com\nmyriarch:\n  enabled: true\n  config:\n    server:\n      publicUrl: https://fleet.example.com\n' > "${umbrella}/values.yaml"
render_umbrella() { helm template u "${umbrella}" "$@"; }
RENDERER=render_umbrella expect_image "(67) a parent chart switches the chart on with a dependency condition and passes global.imageRegistry" "mirror.example.com/myriarch/myriarch:${APP}"
if got="$(render_umbrella --set myriarch.enabled=false 2>&1)" && ! printf '%s\n' "${got}" | grep -q '^kind:'; then ok "(68) the dependency condition switches the chart off"; else fail "(68) the dependency condition switches the chart off"; printf '%s\n' "${got}" | sed 's/^/        /' >&2; fi
expect_schema "(69) the key of the dependency condition is a boolean" /enabled --set enabled=maybe

# --- the image: registry, repository, digest and global.imageRegistry --------------------------------------------------------
expect_image "(70) the default image" "ghcr.io/myriarch/myriarch:${APP}"
expect_image "(71) registry and repository" "registry.example.com:5000/myriarch/server:${APP}" --set image.registry=registry.example.com:5000 --set image.repository=myriarch/server
expect_image "(72) a repository with a host is used as it is (the values of v0.1.0)" "registry.example.com/myriarch/myriarch:${APP}" --set image.repository=registry.example.com/myriarch/myriarch
expect_image "(73) the host of the repository wins over image.registry" "registry.example.com/myriarch/myriarch:${APP}" --set image.registry=other.example.com --set image.repository=registry.example.com/myriarch/myriarch
expect_image "(74) a repository with a host and a port" "localhost:5001/myriarch/myriarch:${APP}" --set image.repository=localhost:5001/myriarch/myriarch
expect_image "(75) a repository on localhost" "localhost/myriarch:${APP}" --set image.repository=localhost/myriarch
expect_image "(76) the digest replaces the tag" "ghcr.io/myriarch/myriarch@${DIGEST}" --set "image.digest=${DIGEST}" --set image.tag=9.9.9
expect_image "(77) the digest with a repository that has a host" "registry.example.com/myriarch/myriarch@${DIGEST}" --set image.repository=registry.example.com/myriarch/myriarch --set "image.digest=${DIGEST}"
expect_image "(78) an explicit tag" "ghcr.io/myriarch/myriarch:9.8.7-rc.1" --set image.tag=9.8.7-rc.1
expect_image "(79) global.imageRegistry replaces image.registry" "mirror.example.com/myriarch/myriarch:${APP}" --set global.imageRegistry=mirror.example.com --set image.registry=ghcr.io
expect_image "(80) global.imageRegistry does not touch a repository with a host" "registry.example.com/x/y:${APP}" --set global.imageRegistry=mirror.example.com --set image.repository=registry.example.com/x/y
expect_image "(81) an empty registry leaves the repository as it is" "myriarch/myriarch:${APP}" --set image.registry=
expect_image "(82) a repository of one segment" "ghcr.io/myriarch:${APP}" --set image.repository=myriarch
expect_path "(83) an empty repository" image.repository --set image.repository=null

# --- TLS at the entry points (docs/security/model.md): the switches are booleans, and the Route redirects plain HTTP ------------------
# A template counts the string "false" as true, so without the schema --set-string ingress.allowWithoutTls=false skips the TLS check
expect_schema "(84) the TLS switch of the Ingress is a boolean" /ingress/allowWithoutTls "${TP[@]}" "${ING[@]}" "${FROM[@]}" --set-string ingress.allowWithoutTls=false
expect_schema "(85) the TLS switch of the Gateway is a boolean" /httpRoute/tlsTerminatedAtGateway "${TP[@]}" "${FROM[@]}" "${RT[@]}" "${REFS[@]}" "${HOSTS[@]}" --set-string httpRoute.tlsTerminatedAtGateway=false
expect_schema "(86) the policy for plain HTTP at the router is one of three" /openshift/route/tls/insecureEdgeTerminationPolicy --set openshift.enabled=true --set openshift.route.tls.insecureEdgeTerminationPolicy=Never
expect_renders "(87) the Route terminates TLS at the router" '^ +termination: edge$' --set openshift.enabled=true
expect_renders "(88) the Route redirects plain HTTP by default" '^ +insecureEdgeTerminationPolicy: Redirect$' --set openshift.enabled=true
expect_renders "(89) plain HTTP at the router is an explicit opt-in" '^ +insecureEdgeTerminationPolicy: Allow$' --set openshift.enabled=true --set openshift.route.tls.insecureEdgeTerminationPolicy=Allow

# --- the Pod of "helm test" and the PrometheusRule (tests.*, prometheusRule.*) ------------------------------------------------------
# The schema refuses what one value can get wrong; the templates repeat the refusal of the Secrets of the server itself (a Pod that
# runs a health check must not get the database DSN or the credentials of the bootstrap admin), which also holds without the schema.
expect_schema "(90) the switch of the test Pod is a boolean" /tests/enabled --set-string tests.enabled=false
expect_schema "(91) unknown key of the test Pod" /tests --set tests.tokenSecret=helm-test-token
expect_schema "(92) unknown key of the test token" /tests/token --set tests.token.secret=helm-test-token
expect_schema "(93) the Secret of the test token has the form of an enrollment token" /tests/token/existingSecret --set "tests.token.existingSecret=${TOKEN}"
expect_schema "(94) the key of the test token has the form of an enrollment token" /tests/token/key --set tests.token.existingSecret=helm-test-token --set "tests.token.key=${TOKEN}"
expect_schema "(95) the key of the test token is empty" /tests/token/key --set tests.token.existingSecret=helm-test-token --set tests.token.key=
expect_schema "(96) a quantity of the test Pod" /tests/resources/requests/cpu --set tests.resources.requests.cpu=lots
expect_schema "(97) the test Pod has no image of its own" /tests --set tests.image.tag=0.2.0
expect_schema "(98) the switch of the PrometheusRule is a boolean" /prometheusRule/enabled --set-string prometheusRule.enabled=false
expect_schema "(99) unknown key of the PrometheusRule" /prometheusRule --set prometheusRule.groups=none
expect_schema "(100) a label of the PrometheusRule is a number" /prometheusRule/labels/release --set-json 'prometheusRule.labels={"release":1}'
expect_guard "(101) the key of the test token is empty" tests.token.key --set tests.token.existingSecret=helm-test-token --set tests.token.key=
expect_path "(102) the test token in the Secret of the server" tests.token.existingSecret --set tests.token.existingSecret=myriarch-secrets
expect_path "(103) the test token in the generated bootstrap Secret" tests.token.existingSecret --set tests.token.existingSecret=t-myriarch-bootstrap
expect_path "(104) the test token in the bootstrap Secret of the values" tests.token.existingSecret --set bootstrapAdmin.existingSecret=my-bootstrap --set tests.token.existingSecret=my-bootstrap
expect_path "(105) the test token in a changed Secret of the server" tests.token.existingSecret --set existingSecret=fleet-secrets --set tests.token.existingSecret=fleet-secrets
expect_schema "(105b) an annotation of the test Pod is a number" /tests/podAnnotations/sidecar.istio.io~1inject --set-json 'tests.podAnnotations={"sidecar.istio.io/inject":1}'
expect_ok "(106) the test token in a Secret of its own" render --set tests.token.existingSecret=helm-test-token
expect_ok "(107) the test Pod and the PrometheusRule are switched off" render --set tests.enabled=false --set prometheusRule.enabled=false
expect_ok "(108) the PrometheusRule renders" render --set prometheusRule.enabled=true --set-json 'prometheusRule.labels={"release":"kube-prometheus-stack"}'
# the Pod of "helm test" is a hook; a render without it has no Pod, and a render with it has exactly one
test_pods() { render "$@" 2>/dev/null | grep -c '^kind: Pod$'; }
if [ "$(test_pods)" = 1 ] && [ "$(test_pods --set tests.enabled=false)" = 0 ] && [ "$(test_pods --skip-tests)" = 0 ]; then ok "(109) one test Pod by default, none with tests.enabled=false or --skip-tests"; else fail "(109) one test Pod by default, none with tests.enabled=false or --skip-tests"; fi
# no Secret reaches the Pod without the opt-in: the rendered Pod names no Secret, and with the opt-in only the named one
pod_text() { render "$@" 2>/dev/null | awk '/^kind: Pod$/ {p=1} p && /^---$/ {p=0} p'; }
if ! pod_text | grep -Eq 'secretName|secretKeyRef|--token-file'; then ok "(110) the test Pod refers to no Secret by default"; else fail "(110) the test Pod refers to no Secret by default"; fi
if [ "$(pod_text --set tests.token.existingSecret=helm-test-token | grep -c 'secretName:')" = 1 ] && pod_text --set tests.token.existingSecret=helm-test-token | grep -q 'secretName: "helm-test-token"'; then ok "(111) with the opt-in the test Pod mounts the named Secret and no other"; else fail "(111) with the opt-in the test Pod mounts the named Secret and no other"; fi

# --- helm upgrade --reuse-values from a release of v0.1.0 ----------------------------------------------------------------------------
# "helm upgrade --reuse-values" renders the chart with the values of the release that is upgraded in place of the defaults of the new
# chart (Helm replaces the values of the chart with those of the old release). A map that values.yaml gained after v0.1.0 (tests,
# prometheusRule) is then missing, and a template that reads a key of it without a default stops with "nil pointer evaluating
# interface {}.enabled". The copy of the chart below has the values.yaml of v0.1.0 as its defaults (the file of the tag, unchanged:
# ci/tests/testdata/helm-values-v0.1.0.yaml) and the templates and the schema of this chart. A later map without a guard fails here too.
reuse="${TEST_TMP}/reuse-values"
mkdir -p "${reuse}"
cp -R "${chart}" "${reuse}/myriarch"
cp "${ROOT}/ci/tests/testdata/helm-values-v0.1.0.yaml" "${reuse}/myriarch/values.yaml"
render_reuse() { helm template t "${reuse}/myriarch" "$@"; }
reuse_text() { render_reuse "$@" 2>/dev/null; }
expect_ok "(112) the chart renders with the defaults of v0.1.0, as in helm upgrade --reuse-values" render_reuse
# what the old values lack keeps the behaviour of v0.1.0: no test Pod, no PrometheusRule, no source for the test Pod in the NetworkPolicy
reuse_np() { render_reuse --show-only templates/networkpolicy.yaml "$@" 2>/dev/null; }
reuse_all="$(reuse_text)"
if printf '%s\n' "${reuse_all}" | grep -q '^kind: Deployment$' && ! printf '%s\n' "${reuse_all}" | grep -Eq '^kind: (Pod|PrometheusRule)$'; then ok "(113) no test Pod and no PrometheusRule without the maps in the values"; else fail "(113) no test Pod and no PrometheusRule without the maps in the values"; fi
if reuse_np --set networkPolicy.ingress.allowSameNamespace=false | grep -q 'kind: NetworkPolicy' && ! reuse_np --set networkPolicy.ingress.allowSameNamespace=false | grep -q 'component: test'; then ok "(114) no source for the test Pod in the NetworkPolicy without the map tests"; else fail "(114) no source for the test Pod in the NetworkPolicy without the map tests"; fi
# switching both on by --set still renders: the maps are partial then (only the switch), and the Pod has no requests and limits
reuse_on=(--set tests.enabled=true --set prometheusRule.enabled=true)
expect_ok "(115) the switches alone, on top of the values of v0.1.0, render" render_reuse "${reuse_on[@]}"
if [ "$(reuse_text "${reuse_on[@]}" | grep -c '^kind: Pod$')" = 1 ] && [ "$(reuse_text "${reuse_on[@]}" | grep -c '^kind: PrometheusRule$')" = 1 ]; then ok "(116) one test Pod and one PrometheusRule with the switches on"; else fail "(116) one test Pod and one PrometheusRule with the switches on"; fi
if reuse_np "${reuse_on[@]}" --set networkPolicy.ingress.allowSameNamespace=false | grep -q 'app.kubernetes.io/component: test'; then ok "(117) the source for the test Pod follows the switch"; else fail "(117) the source for the test Pod follows the switch"; fi
# tests.resources is missing from such values, and the documents say so: the Pod has no requests and limits then
reuse_pod() { render_reuse "$@" 2>/dev/null | awk '/^kind: Pod$/ {p=1} p && /^---$/ {p=0} p'; }
if reuse_pod "${reuse_on[@]}" | grep -q '^kind: Pod$' && ! reuse_pod "${reuse_on[@]}" | grep -q 'resources:'; then ok "(117b) the test Pod has no requests and limits when tests.resources is missing"; else fail "(117b) the test Pod has no requests and limits when tests.resources is missing"; fi
finish_tests "helm-messages-test"
