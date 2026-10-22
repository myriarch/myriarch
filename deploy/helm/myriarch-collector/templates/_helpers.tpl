{{- /*
Copyright The Myriarch Authors
SPDX-License-Identifier: Apache-2.0
*/ -}}
{{/* Names and labels */}}
{{- define "myriarch-collector.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "myriarch-collector.fullname" -}}
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

{{- define "myriarch-collector.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "myriarch-collector.selectorLabels" -}}
app.kubernetes.io/name: {{ include "myriarch-collector.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "myriarch-collector.labels" -}}
helm.sh/chart: {{ include "myriarch-collector.chart" . }}
{{ include "myriarch-collector.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: myriarch
{{- end -}}

{{- define "myriarch-collector.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "myriarch-collector.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Repository of an image with its registry host; call with (dict "root" . "image" .Values.images.<name> "key" "images.<name>"). A repository
whose first path segment is a registry host (it contains a "." or a ":", or it is "localhost": the rule of the container runtimes) is
used unchanged. Otherwise the registry is global.imageRegistry, else the registry of the image; without either, the repository stays
as it is and the container runtime applies its default registry.
*/}}
{{- define "myriarch-collector.imageRepository" -}}
{{- $repo := .image.repository | default "" | toString -}}
{{- if not $repo -}}
{{- fail (printf "%s.repository: must not be empty" .key) -}}
{{- end -}}
{{- $first := first (splitList "/" $repo) -}}
{{- if and (contains "/" $repo) (or (contains "." $first) (contains ":" $first) (eq $first "localhost")) -}}
{{- $repo -}}
{{- else -}}
{{- $registry := (.root.Values.global | default dict).imageRegistry | default .image.registry | default "" | toString -}}
{{- if $registry -}}
{{- printf "%s/%s" (trimSuffix "/" $registry) $repo -}}
{{- else -}}
{{- $repo -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Image reference: with a digest => repository@digest (the tag is ignored), otherwise repository:tag (default: appVersion) */}}
{{- define "myriarch-collector.image" -}}
{{- $repository := include "myriarch-collector.imageRepository" . -}}
{{- if .image.digest -}}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" .image.digest) -}}
{{- fail (printf "%s.digest: must have the form sha256:<64 hex characters>, got %q" .key .image.digest) -}}
{{- end -}}
{{- printf "%s@%s" $repository .image.digest -}}
{{- else -}}
{{- printf "%s:%s" $repository (default .root.Chart.AppVersion .image.tag) -}}
{{- end -}}
{{- end -}}

{{/*
OpenShift profile: renders "true" while the profile is active and nothing otherwise.
openshift.enabled is true, false or auto; auto looks for the API security.openshift.io/v1 (under "helm template"
only with --api-versions security.openshift.io/v1).
*/}}
{{- define "myriarch-collector.openshift" -}}
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
{{- define "myriarch-collector.podSecurityContext" -}}
{{- if include "myriarch-collector.openshift" . -}}
{{- toYaml (omit .Values.podSecurityContext "runAsUser" "runAsGroup" "fsGroup" "fsGroupChangePolicy" "supplementalGroups") -}}
{{- else -}}
{{- toYaml .Values.podSecurityContext -}}
{{- end -}}
{{- end -}}

{{/* "true" in container mode */}}
{{- define "myriarch-collector.containerMode" -}}
{{- if .Values.containerMode.enabled -}}true{{- end -}}
{{- end -}}

{{/* "true" for the DaemonSet */}}
{{- define "myriarch-collector.daemonset" -}}
{{- if eq .Values.mode "daemonset" -}}true{{- end -}}
{{- end -}}

{{- define "myriarch-collector.siteType" -}}
{{- default (ternary "k8s-node" "k8s-gateway" (eq .Values.mode "daemonset")) .Values.site.type -}}
{{- end -}}

{{/* Directory on the node for the identity of the DaemonSet (persistence.hostPath) */}}
{{- define "myriarch-collector.hostPath" -}}
{{- default (printf "/var/lib/myriarch/%s" (include "myriarch-collector.fullname" .)) .Values.persistence.hostPath.path -}}
{{- end -}}

{{/* Path of the collector binary that the supervisor starts */}}
{{- define "myriarch-collector.collectorExecutable" -}}
{{- if include "myriarch-collector.containerMode" . -}}
/var/lib/myriarch/collector/current/otelcol-contrib
{{- else -}}
/shared/otelcol-contrib
{{- end -}}
{{- end -}}

{{- define "myriarch-collector.terminationGracePeriodSeconds" -}}
{{- if include "myriarch-collector.containerMode" . -}}
{{- max (int .Values.terminationGracePeriodSeconds) 360 -}}
{{- else -}}
{{- int .Values.terminationGracePeriodSeconds -}}
{{- end -}}
{{- end -}}

{{/*
Rules that relate two values; the schema checks the shape of each value on its own. Included by the workload templates, renders nothing.
*/}}
{{- define "myriarch-collector.checks" -}}
{{- $endpoint := .Values.server.endpoint -}}
{{- if hasPrefix "wss://" $endpoint -}}
{{- else if hasPrefix "ws://" $endpoint -}}
{{- if not .Values.server.allowInsecureTransport -}}
{{- fail "server.endpoint: ws:// sends the enrollment token in clear text. Use a wss:// endpoint, or set server.allowInsecureTransport=true to accept an unencrypted connection on purpose" -}}
{{- end -}}
{{- else -}}
{{- fail (printf "server.endpoint: must start with wss:// (or ws:// together with server.allowInsecureTransport=true), got %q" $endpoint) -}}
{{- end -}}
{{- if and .Values.server.tls.existingSecret .Values.server.tls.existingConfigMap -}}
{{- fail "server.tls: set existingSecret or existingConfigMap, not both" -}}
{{- end -}}
{{- if and .Values.nodeLogs.enabled (not (include "myriarch-collector.daemonset" .)) -}}
{{- fail "nodeLogs.enabled: the node logs are read by the DaemonSet only (mode=daemonset)" -}}
{{- end -}}
{{- if and .Values.persistence.hostPath.enabled (not (include "myriarch-collector.daemonset" .)) -}}
{{- fail "persistence.hostPath.enabled: only for mode=daemonset; the StatefulSet keeps its identity on a PVC" -}}
{{- end -}}
{{- if include "myriarch-collector.containerMode" . -}}
{{- if include "myriarch-collector.daemonset" . -}}
{{- fail "containerMode.enabled: container mode needs a volume per replica and is available for mode=statefulset only" -}}
{{- end -}}
{{- if and .Values.containerMode.update.requireSignature (not .Values.containerMode.trustedRoot.existingConfigMap) -}}
{{- fail "containerMode.trustedRoot.existingConfigMap: required while containerMode.update.requireSignature is true (the updater verifies the signature of a collector with the Sigstore trusted root, offline). Create the ConfigMap from `myriarch-updater fetch-trusted-root`, or switch the verification off on purpose with containerMode.update.requireSignature=false" -}}
{{- end -}}
{{- end -}}
{{- end -}}
