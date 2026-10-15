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

# expect_image <name> <image> <helm args...>: the chart renders and the pod runs exactly this image
expect_image() {
  local name="$1" want="$2" got; shift 2
  if ! got="$(render "$@" 2>&1)"; then fail "${name} (does not render)"; printf '%s\n' "${got}" | sed 's/^/        /' >&2; return; fi
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

# what the schema accepts
expect_ok "(62) null switches off an entry of a map" render --set-json 'podAnnotations={"legacy":null}'
expect_ok "(63) null removes a value that the chart defines" render --set config.server.metricsListenAddr=null
expect_ok "(64) a parent chart may pass a null registry" render --set-json 'global={"imageRegistry":null,"other":{"a":1}}'
expect_ok "(65) quantities as numbers and strings" render --set resources.requests.cpu=1 --set resources.requests.memory=536870912 --set resources.limits.memory=1Gi
expect_ok "(66) a declared enrollment token with a Secret" render --set-json 'enrollmentTokens=[{"name":"gateway","existingSecret":"gateway-token","key":"token","labels":{"site":"hq"},"expiresAt":"2030-01-01T00:00:00Z"}]'

# --- the image: registry, repository, digest and global.imageRegistry --------------------------------------------------------
expect_image "(70) the default image" "ghcr.io/myriarch/myriarch:0.1.0"
expect_image "(71) registry and repository" "registry.example.com:5000/myriarch/server:0.1.0" --set image.registry=registry.example.com:5000 --set image.repository=myriarch/server
expect_image "(72) a repository with a host is used as it is (the values of v0.1.0)" "registry.example.com/myriarch/myriarch:0.1.0" --set image.repository=registry.example.com/myriarch/myriarch
expect_image "(73) the host of the repository wins over image.registry" "registry.example.com/myriarch/myriarch:0.1.0" --set image.registry=other.example.com --set image.repository=registry.example.com/myriarch/myriarch
expect_image "(74) a repository with a host and a port" "localhost:5001/myriarch/myriarch:0.1.0" --set image.repository=localhost:5001/myriarch/myriarch
expect_image "(75) a repository on localhost" "localhost/myriarch:0.1.0" --set image.repository=localhost/myriarch
expect_image "(76) the digest replaces the tag" "ghcr.io/myriarch/myriarch@${DIGEST}" --set "image.digest=${DIGEST}" --set image.tag=9.9.9
expect_image "(77) the digest with a repository that has a host" "registry.example.com/myriarch/myriarch@${DIGEST}" --set image.repository=registry.example.com/myriarch/myriarch --set "image.digest=${DIGEST}"
expect_image "(78) an explicit tag" "ghcr.io/myriarch/myriarch:0.2.0-rc.1" --set image.tag=0.2.0-rc.1
expect_image "(79) global.imageRegistry replaces image.registry" "mirror.example.com/myriarch/myriarch:0.1.0" --set global.imageRegistry=mirror.example.com --set image.registry=ghcr.io
expect_image "(80) global.imageRegistry does not touch a repository with a host" "registry.example.com/x/y:0.1.0" --set global.imageRegistry=mirror.example.com --set image.repository=registry.example.com/x/y
expect_image "(81) an empty registry leaves the repository as it is" "myriarch/myriarch:0.1.0" --set image.registry=
expect_image "(82) a repository of one segment" "ghcr.io/myriarch:0.1.0" --set image.repository=myriarch
expect_path "(83) an empty repository" image.repository --set image.repository=null
finish_tests "helm-messages-test"
