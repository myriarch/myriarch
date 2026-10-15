#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail" is intended in the tests (ok/fail are counters and never fail)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Contract of the misconfiguration messages of the myriarchd Helm chart (deploy/helm/myriarch/templates): every deliberate
# misconfiguration must make `helm template` fail with an English message that starts with the value path
# ("execution error at (...): <value.path>: ..."), and the fully configured chart must render. No cluster, no network.
# Needs helm: ci/install-tools.sh helm
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

# expect_image <name> <image> <helm args...>: the chart renders and the pod runs exactly this image
expect_image() {
  local name="$1" want="$2" got; shift 2
  if ! got="$(render "$@" 2>&1)"; then fail "${name} (does not render)"; printf '%s\n' "${got}" | sed 's/^/        /' >&2; return; fi
  got="$(printf '%s\n' "${got}" | sed -n 's/^ *image: "\(.*\)"$/\1/p' | sort -u)"
  if [ "${got}" = "${want}" ]; then ok "${name}"; else fail "${name} (image ${got:-none}, want ${want})"; fi
}

# expect_path <name> <value.path> <helm args...>: rendering fails, the message contains ": <value.path>: " and is not German
expect_path() {
  local name="$1" path="$2"; shift 2
  expect_fail "${name}" ": ${path//./\\.}: " render "$@"
  if printf '%s' "${OUT}" | grep -Eq -- "${german}"; then fail "${name} (message is not English)"; printf '%s\n' "${OUT}" | sed 's/^/        /' >&2; fi
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
expect_path "(4) malformed image digest" image.digest --set image.digest=bogus
expect_path "(5) HTTPRoute without parentRefs" httpRoute.parentRefs "${TP[@]}" "${FROM[@]}" "${RT[@]}"
expect_path "(6) HTTPRoute without hostnames" httpRoute.hostnames "${TP[@]}" "${FROM[@]}" "${RT[@]}" "${REFS[@]}"
expect_path "(7) HTTPRoute without confirmed TLS termination" httpRoute.tlsTerminatedAtGateway "${TP[@]}" "${FROM[@]}" "${RT[@]}" "${REFS[@]}" "${HOSTS[@]}"
expect_path "(8) HTTPRoute without trustedProxies" config.server.trustedProxies "${FROM[@]}" "${RT[@]}" "${REFS[@]}" "${HOSTS[@]}" --set httpRoute.tlsTerminatedAtGateway=true
expect_path "(9) openshift.enabled is not true, false or auto" openshift.enabled --set openshift.enabled=maybe
expect_path "(10) trustedProxies together with same-namespace access" networkPolicy.ingress.allowSameNamespace --set-json 'config.server.trustedProxies=["10.0.0.0/8"]'
expect_path "(11) bootstrap admin set in config" config.auth.bootstrapAdmin --set config.auth.bootstrapAdmin.enabled=true
expect_path "(12) enrollment tokens set in config" config.opamp.enrollmentTokens --set-json 'config.opamp.enrollmentTokens=[]'
expect_path "(13) dev login in config" config.auth.devLogin --set config.auth.devLogin.enabled=true
expect_path "(14) dev mode in config" config.dev --set config.dev.seedEnrollmentToken=x
expect_path "(15) existingSecret without token key and password hash key" bootstrapAdmin.tokenKey --set bootstrapAdmin.existingSecret=my-bootstrap --set bootstrapAdmin.tokenKey=
expect_path "(16) password hash key without existingSecret" bootstrapAdmin.passwordHashKey --set bootstrapAdmin.passwordHashKey=password-hash
expect_path "(17) invalid bootstrap user name" bootstrapAdmin.username --set 'bootstrapAdmin.username=not valid!'
expect_path "(18) enrollment token name is not a DNS label" 'enrollmentTokens\[0\].name' --set-json 'enrollmentTokens=[{"name":"Not_A_Label"}]'
expect_path "(19) duplicate enrollment token name" 'enrollmentTokens\[1\].name' --set-json 'enrollmentTokens=[{"name":"a"},{"name":"a"}]'
expect_path "(20) Route host cannot be derived" openshift.route.host --set openshift.enabled=true --set config.server.publicUrl=
expect_path "(21) no login path" bootstrapAdmin.enabled --set bootstrapAdmin.enabled=false
# OIDC in the environment is invisible to the chart: without the declaration there still is no login path, and the message names the value
OIDCENV=(--set-json 'extraEnv=[{"name":"MYRIARCH_AUTH_OIDC_ENABLED","value":"true"}]')
expect_path "(21b) OIDC only in extraEnv, not declared" bootstrapAdmin.enabled --set bootstrapAdmin.enabled=false "${OIDCENV[@]}"
expect_fail "(21c) the no-login-path message names oidcFromEnvironment" 'oidcFromEnvironment' render --set bootstrapAdmin.enabled=false

# inline secrets would be stored in the ConfigMap
expect_path "(21d) inline database DSN" config.database.dsn --set config.database.dsn=postgres://u:p@db/x
expect_path "(21e) inline OIDC client secret" config.auth.oidc.clientSecret --set config.auth.oidc.clientSecret=s3cret
expect_path "(21f) inline GitOps webhook secret" config.gitops.webhookSecret --set config.gitops.webhookSecret=s3cret
expect_path "(21g) inline notification URL" 'config.notifications.channels\[0\].url' --set-json 'config.notifications.channels=[{"name":"a","type":"slack","url":"https://hooks.example.com/x"}]'

expect_ok "(22) the defaults render" render
expect_ok "(23) the OpenShift profile renders" render --set openshift.enabled=true
expect_ok "(24) the Dex quickstart renders" render -f "${chart}/examples/quickstart-dex.yaml"
expect_ok "(25) the configured Ingress renders" render "${TP[@]}" "${ING[@]}" "${FROM[@]}" --set-json 'ingress.tls=[{"secretName":"fleet-tls","hosts":["fleet.example.com"]}]'
expect_ok "(25b) OIDC declared through the environment renders without the bootstrap admin" render --set bootstrapAdmin.enabled=false --set oidcFromEnvironment=true "${OIDCENV[@]}"

DIGEST="sha256:$(printf '0123456789abcdef%.0s' 1 2 3 4)"

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
