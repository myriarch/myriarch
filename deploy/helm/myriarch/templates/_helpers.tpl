{{- /*
Copyright The Myriarch Authors
SPDX-License-Identifier: Apache-2.0
*/ -}}
{{/* Names and labels */}}
{{- define "myriarch.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "myriarch.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "myriarch.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "myriarch.selectorLabels" -}}
app.kubernetes.io/name: {{ include "myriarch.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "myriarch.labels" -}}
helm.sh/chart: {{ include "myriarch.chart" . }}
{{ include "myriarch.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "myriarch.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "myriarch.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Repository of the image with its registry host. A repository whose first path segment is a registry host (it contains a "." or
a ":", or it is "localhost": the rule of the container runtimes) is used unchanged: that is the form of the values of v0.1.0.
Otherwise the registry is global.imageRegistry, else image.registry; without either, the repository stays as it is and the
container runtime applies its default registry.
*/}}
{{- define "myriarch.imageRepository" -}}
{{- $repo := .Values.image.repository | default "" | toString -}}
{{- if not $repo -}}
{{- fail "image.repository: must not be empty" -}}
{{- end -}}
{{- $first := first (splitList "/" $repo) -}}
{{- if and (contains "/" $repo) (or (contains "." $first) (contains ":" $first) (eq $first "localhost")) -}}
{{- $repo -}}
{{- else -}}
{{- $registry := (.Values.global | default dict).imageRegistry | default .Values.image.registry | default "" | toString -}}
{{- if $registry -}}
{{- printf "%s/%s" (trimSuffix "/" $registry) $repo -}}
{{- else -}}
{{- $repo -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Image reference: with image.digest set => repository@digest (the tag is ignored), otherwise repository:tag (default: appVersion) */}}
{{- define "myriarch.image" -}}
{{- $repository := include "myriarch.imageRepository" . -}}
{{- if .Values.image.digest -}}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" .Values.image.digest) -}}
{{- fail (printf "image.digest: must have the form sha256:<64 hex characters>, got %q" .Values.image.digest) -}}
{{- end -}}
{{- printf "%s@%s" $repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" $repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}
{{- end -}}

{{/*
OpenShift profile: renders "true" while the profile is active and nothing otherwise.
openshift.enabled is true, false or auto; auto looks for the API security.openshift.io/v1 (under "helm template"
only with --api-versions security.openshift.io/v1).
*/}}
{{- define "myriarch.openshift" -}}
{{- $mode := toString .Values.openshift.enabled | lower -}}
{{- if eq $mode "true" -}}
true
{{- else if eq $mode "false" -}}
{{- else if eq $mode "auto" -}}
{{- if .Capabilities.APIVersions.Has "security.openshift.io/v1" -}}true{{- end -}}
{{- else -}}
{{- fail (printf "openshift.enabled: must be true, false or auto, got %q" (toString .Values.openshift.enabled)) -}}
{{- end -}}
{{- end -}}

{{/* Pod security context: the OpenShift profile leaves user, group and fsGroup to the SCC. */}}
{{- define "myriarch.podSecurityContext" -}}
{{- if include "myriarch.openshift" . -}}
{{- toYaml (omit .Values.podSecurityContext "runAsUser" "runAsGroup" "fsGroup") -}}
{{- else -}}
{{- toYaml .Values.podSecurityContext -}}
{{- end -}}
{{- end -}}

{{/* Route host: openshift.route.host or the host name of config.server.publicUrl (without port). */}}
{{- define "myriarch.routeHost" -}}
{{- $host := .Values.openshift.route.host | default "" -}}
{{- if not $host -}}
{{- $host = (urlParse (.Values.config.server.publicUrl | default "")).hostname | default "" -}}
{{- end -}}
{{- if not $host -}}
{{- fail "openshift.route.host: no host name can be derived; set openshift.route.host or an https config.server.publicUrl" -}}
{{- end -}}
{{- $host -}}
{{- end -}}

{{/*
Single sign-on as the chart sees it: renders "true" while config.auth.oidc.enabled is set, or while oidcFromEnvironment
declares that OIDC is switched on by environment variables (MYRIARCH_AUTH_OIDC_ENABLED in extraEnv, a ConfigMap or a
Secret), which the chart cannot read. The server decides at runtime; this only keeps the chart consistent with it.
*/}}
{{- define "myriarch.oidc.enabled" -}}
{{- $oidc := (.Values.config.auth | default dict).oidc | default dict -}}
{{- if or $oidc.enabled .Values.oidcFromEnvironment -}}true{{- end -}}
{{- end -}}

{{/*
Bootstrap admin: active (renders "true") while bootstrapAdmin.enabled and (OIDC is off or keepWithOidc is set).
This is the same rule as AuthConfig.BootstrapActive in the server.
*/}}
{{- define "myriarch.bootstrap.active" -}}
{{- if and .Values.bootstrapAdmin.enabled (or (not (include "myriarch.oidc.enabled" .)) .Values.bootstrapAdmin.keepWithOidc) -}}true{{- end -}}
{{- end -}}

{{- define "myriarch.bootstrap.secretName" -}}
{{- default (printf "%s-bootstrap" (include "myriarch.fullname" .)) .Values.bootstrapAdmin.existingSecret -}}
{{- end -}}

{{/* Key of the one-time token in the bootstrap Secret ("" = no token); a generated Secret always has one. */}}
{{- define "myriarch.bootstrap.tokenKey" -}}
{{- if .Values.bootstrapAdmin.existingSecret -}}
{{- .Values.bootstrapAdmin.tokenKey | default "" -}}
{{- else -}}
{{- .Values.bootstrapAdmin.tokenKey | default "token" -}}
{{- end -}}
{{- end -}}

{{- define "myriarch.bootstrap.validate" -}}
{{- $b := .Values.bootstrapAdmin -}}
{{- if and (not $b.enabled) (not (include "myriarch.oidc.enabled" .)) -}}
{{- fail "bootstrapAdmin.enabled: no login path is configured; enable config.auth.oidc or bootstrapAdmin, or set oidcFromEnvironment=true when MYRIARCH_AUTH_OIDC_ENABLED=true comes from the environment (extraEnv); myriarchd refuses to start without a login path in serve mode" -}}
{{- end -}}
{{- if $b.enabled -}}
{{- if not (regexMatch "^[A-Za-z0-9._@-]{1,64}$" (toString $b.username)) -}}
{{- fail (printf "bootstrapAdmin.username: must be 1 to 64 characters: letters, digits, \".\", \"_\", \"@\" or \"-\"; got %q" (toString $b.username)) -}}
{{- end -}}
{{- if and (not $b.existingSecret) $b.passwordHashKey -}}
{{- fail "bootstrapAdmin.passwordHashKey: needs bootstrapAdmin.existingSecret; the generated Secret holds only a one-time token" -}}
{{- end -}}
{{- if and $b.existingSecret (not $b.tokenKey) (not $b.passwordHashKey) -}}
{{- fail "bootstrapAdmin.tokenKey: tokenKey or passwordHashKey is required with bootstrapAdmin.existingSecret; name the key of the token or of the bcrypt password hash" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Declarative enrollment tokens: names are DNS labels and unique. */}}
{{- define "myriarch.enrollment.validate" -}}
{{- $seen := dict -}}
{{- range $i, $e := .Values.enrollmentTokens -}}
{{- $n := toString ($e.name | default "") -}}
{{- if not (regexMatch "^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$" $n) -}}
{{- fail (printf "enrollmentTokens[%d].name: must be a DNS label (lower case letters, digits and \"-\", 1 to 63 characters, unique); got %q" $i $n) -}}
{{- end -}}
{{- if hasKey $seen $n -}}
{{- fail (printf "enrollmentTokens[%d].name: duplicate name %q" $i $n) -}}
{{- end -}}
{{- $_ := set $seen $n true -}}
{{- end -}}
{{- end -}}

{{/* Secret of an enrollment token entry: dict "root" <root context> "entry" <entry>. */}}
{{- define "myriarch.enrollment.secretName" -}}
{{- default (printf "%s-enrollment-%s" (include "myriarch.fullname" .root) .entry.name) .entry.existingSecret -}}
{{- end -}}

{{/*
Rendered myriarchd.yaml: a transformed copy of .Values.config.
 * guards: keys that the chart generates or does not offer must not be set in config;
 * pruning: *File fields of disabled features and empty *File fields are dropped;
 * injection: auth.bootstrapAdmin and opamp.enrollmentTokens from the top-level values.
*/}}
{{- define "myriarch.config" -}}
{{- $cfg := deepCopy .Values.config -}}
{{- $auth := get $cfg "auth" | default dict -}}
{{- $opamp := get $cfg "opamp" | default dict -}}
{{- if hasKey $auth "bootstrapAdmin" -}}
{{- fail "config.auth.bootstrapAdmin: must not be set; set the top-level bootstrapAdmin values instead" -}}
{{- end -}}
{{- if hasKey $opamp "enrollmentTokens" -}}
{{- fail "config.opamp.enrollmentTokens: must not be set; set the top-level enrollmentTokens values instead" -}}
{{- end -}}
{{- if hasKey $auth "devLogin" -}}
{{- fail "config.auth.devLogin: not available in this chart; the chart always starts myriarchd in serve mode" -}}
{{- end -}}
{{- if hasKey $cfg "dev" -}}
{{- fail "config.dev: not available in this chart; the chart always starts myriarchd in serve mode" -}}
{{- end -}}
{{- /* inline secrets would be stored in the ConfigMap: they belong in the Secret and the *File settings */ -}}
{{- if (get (get $cfg "database" | default dict) "dsn") -}}
{{- fail "config.database.dsn: must not be set (the ConfigMap would contain the password); put the DSN into the Secret (key database-dsn, config.database.dsnFile)" -}}
{{- end -}}
{{- if (get (get $auth "oidc" | default dict) "clientSecret") -}}
{{- fail "config.auth.oidc.clientSecret: must not be set (the ConfigMap would contain the secret); put it into the Secret (key oidc-client-secret, config.auth.oidc.clientSecretFile)" -}}
{{- end -}}
{{- if (get (get $cfg "gitops" | default dict) "webhookSecret") -}}
{{- fail "config.gitops.webhookSecret: must not be set (the ConfigMap would contain the secret); put it into the Secret (key gitops-webhook-secret, config.gitops.webhookSecretFile)" -}}
{{- end -}}
{{- range $i, $ch := (get (get $cfg "notifications" | default dict) "channels" | default list) -}}
{{- if (get $ch "url") -}}
{{- fail (printf "config.notifications.channels[%d].url: must not be set (the ConfigMap would contain the webhook URL); put it into the Secret and set urlFile" $i) -}}
{{- end -}}
{{- end -}}
{{- include "myriarch.bootstrap.validate" . -}}
{{- include "myriarch.enrollment.validate" . -}}
{{- /* pruning */ -}}
{{- $oidc := get $auth "oidc" | default dict -}}
{{- if not (include "myriarch.oidc.enabled" .) -}}
{{- range $k := list "clientSecretFile" "clientCertificateFile" "clientKeyFile" -}}
{{- $_ := unset $oidc $k -}}
{{- end -}}
{{- end -}}
{{- if not (get $auth "downloadTokenSecretFile") -}}
{{- $_ := unset $auth "downloadTokenSecretFile" -}}
{{- end -}}
{{- $gitops := get $cfg "gitops" | default dict -}}
{{- $gauth := get $gitops "auth" | default dict -}}
{{- range $k := list "tokenFile" "sshKeyFile" "knownHostsFile" "caFile" -}}
{{- if or (not $gitops.enabled) (not (get $gauth $k)) -}}
{{- $_ := unset $gauth $k -}}
{{- end -}}
{{- end -}}
{{- if or (not $gitops.enabled) (not $gitops.webhookSecretFile) -}}
{{- $_ := unset $gitops "webhookSecretFile" -}}
{{- end -}}
{{- $releases := get $cfg "releases" | default dict -}}
{{- if or (not $releases.enabled) (not $releases.tokenFile) -}}
{{- $_ := unset $releases "tokenFile" -}}
{{- end -}}
{{- /* injection */ -}}
{{- /*
  An enabled bootstrap admin is rendered also while OIDC makes it inactive (no keepWithOidc): the server then
  deactivates the account with the reason oidc_enabled. The credential files are read only while the account is active,
  and the Secret is not mounted in that case.
*/ -}}
{{- if .Values.bootstrapAdmin.enabled -}}
{{- $b := .Values.bootstrapAdmin -}}
{{- $bootstrap := dict "enabled" true "username" $b.username "keepWithOidc" (default false $b.keepWithOidc) -}}
{{- if include "myriarch.bootstrap.tokenKey" . -}}
{{- $_ := set $bootstrap "tokenFile" "/etc/myriarchd/bootstrap/token" -}}
{{- end -}}
{{- if $b.passwordHashKey -}}
{{- $_ := set $bootstrap "passwordHashFile" "/etc/myriarchd/bootstrap/password-hash" -}}
{{- end -}}
{{- $_ := set $auth "bootstrapAdmin" $bootstrap -}}
{{- $_ := set $cfg "auth" $auth -}}
{{- end -}}
{{- if .Values.enrollmentTokens -}}
{{- $tokens := list -}}
{{- range .Values.enrollmentTokens -}}
{{- $e := dict "name" .name "tokenFile" (printf "/etc/myriarchd/enrollment/%s" .name) -}}
{{- if .labels -}}
{{- $_ := set $e "labels" .labels -}}
{{- end -}}
{{- if .expiresAt -}}
{{- $_ := set $e "expiresAt" (toString .expiresAt) -}}
{{- end -}}
{{- $tokens = append $tokens $e -}}
{{- end -}}
{{- $_ := set $opamp "enrollmentTokens" $tokens -}}
{{- $_ := set $cfg "opamp" $opamp -}}
{{- end -}}
{{- toYaml $cfg -}}
{{- end -}}

{{/*
SHA-256 over the identity (uid and resourceVersion) of every Secret the pod mounts, as seen through lookup
(existingSecret, bootstrap Secret, enrollment Secrets). The version changes whenever the content changes, and the
annotation reveals nothing about the content (an unsalted digest of the data would be an offline verifier for weak
values, readable by anyone who may read the Deployment). Without cluster access (helm template) lookup returns
nothing and the value is constant.
*/}}
{{- define "myriarch.secretChecksum" -}}
{{- $names := list .Values.existingSecret -}}
{{- $generated := list -}}
{{- if include "myriarch.bootstrap.active" . -}}
{{- $bootstrap := include "myriarch.bootstrap.secretName" . -}}
{{- $names = append $names $bootstrap -}}
{{- if not .Values.bootstrapAdmin.existingSecret -}}{{- $generated = append $generated $bootstrap -}}{{- end -}}
{{- end -}}
{{- range .Values.enrollmentTokens -}}
{{- $token := include "myriarch.enrollment.secretName" (dict "root" $ "entry" .) -}}
{{- $names = append $names $token -}}
{{- if not .existingSecret -}}{{- $generated = append $generated $token -}}{{- end -}}
{{- end -}}
{{- $data := dict -}}
{{- range $n := uniq $names -}}
{{- $s := lookup "v1" "Secret" $.Release.Namespace $n -}}
{{- if $s -}}
{{- $_ := set $data $n (dict "uid" $s.metadata.uid "resourceVersion" $s.metadata.resourceVersion) -}}
{{- else if and $.Release.IsUpgrade (has $n $generated) -}}
{{- /* The chart creates this Secret in this very upgrade (first use, or deleted to rotate it): the pods that run now
       cannot have the new value, and an enrollment token is only reconciled at startup. The revision makes the pod
       template differ from the previous one even if that one also saw no Secret (a second rotation). */ -}}
{{- $_ := set $data $n (dict "createdInRevision" $.Release.Revision) -}}
{{- end -}}
{{- end -}}
{{- toJson $data | sha256sum -}}
{{- end -}}

{{/* TCP port of Git over SSH (22 or the port of an ssh:// URL); empty unless gitops is enabled with an SSH URL. */}}
{{- define "myriarch.gitSshPort" -}}
{{- $g := .Values.config.gitops | default dict -}}
{{- if and $g.enabled $g.repoUrl -}}
{{- $u := toString $g.repoUrl -}}
{{- if hasPrefix "ssh://" $u -}}
{{- $port := regexFind ":[0-9]+$" (toString (urlParse $u).host) | trimPrefix ":" -}}
{{- default "22" $port -}}
{{- else if regexMatch "^[^/@:]+@[^/:]+:" $u -}}
22
{{- end -}}
{{- end -}}
{{- end -}}

{{/* With an enabled NetworkPolicy, ingress or HTTPRoute needs a named source for port 8080. */}}
{{- define "myriarch.checkIngressSources" -}}
{{- if and .Values.networkPolicy.enabled (or .Values.ingress.enabled .Values.httpRoute.enabled) (not .Values.networkPolicy.ingress.from) -}}
{{- fail "networkPolicy.ingress.from: must name the ingress controller or Gateway (namespace and pod selector) when networkPolicy.enabled and ingress.enabled or httpRoute.enabled are true; otherwise the NetworkPolicy blocks the traffic from it" -}}
{{- end -}}
{{- end -}}

{{/*
Labels that select the Pod of "helm test". The name differs from the selector labels of the server on purpose: the Service and
the Deployment select by app.kubernetes.io/name and app.kubernetes.io/instance, and the test Pod must be neither an endpoint of the
Service nor a pod the Deployment counts. The NetworkPolicy selects the test Pod by these labels.
*/}}
{{- define "myriarch.testSelectorLabels" -}}
app.kubernetes.io/name: {{ printf "%s-test" (include "myriarch.name" . | trunc 58 | trimSuffix "-") }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: test
{{- end -}}

{{- define "myriarch.testLabels" -}}
helm.sh/chart: {{ include "myriarch.chart" . }}
{{ include "myriarch.testSelectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Whether the Pod of "helm test" is rendered (tests.enabled): renders "true", or nothing. A missing tests map counts as off.
"helm upgrade --reuse-values" replaces the defaults of the chart with the values of the release that is upgraded, so a release of
v0.1.0 has no tests map there, and the chart has to render for it as it did before: without the Pod and without a source for it in
the NetworkPolicy. Ask this helper, and do not read the switch from .Values directly; the same holds for the map prometheusRule,
which templates/prometheusrule.yaml reads with a default.
*/}}
{{- define "myriarch.tests.enabled" -}}
{{- if (.Values.tests | default dict).enabled -}}true{{- end -}}
{{- end -}}

{{/*
The Secret of the test token (tests.token.existingSecret): renders the name, or nothing without the opt-in. The Pod of "helm test"
mounts one key of it and nothing else, and the Secrets of the server itself are refused: the test would hand the database DSN or the
credentials of the bootstrap admin to a Pod that has no use for them. The command refuses a file that holds anything but an enrollment
token as well, so a mistake here is not sent to a server either.
*/}}
{{- define "myriarch.tests.tokenSecret" -}}
{{- $t := (.Values.tests | default dict).token | default dict -}}
{{- $name := toString ($t.existingSecret | default "") -}}
{{- if $name -}}
{{- if not (toString ($t.key | default "")) -}}
{{- fail "tests.token.key: must not be empty when tests.token.existingSecret is set; name the key of the token in the Secret" -}}
{{- end -}}
{{- if or (eq $name (toString .Values.existingSecret)) (eq $name (include "myriarch.bootstrap.secretName" .)) -}}
{{- fail (printf "tests.token.existingSecret: %q is a Secret of the server itself (existingSecret or the bootstrap Secret); name a Secret of its own that holds an enrollment token for the test only" $name) -}}
{{- end -}}
{{- $name -}}
{{- end -}}
{{- end -}}
