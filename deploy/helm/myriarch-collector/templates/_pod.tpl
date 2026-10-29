{{- /*
Copyright The Myriarch Authors
SPDX-License-Identifier: Apache-2.0
*/ -}}
{{/*
The pod template of the StatefulSet and the DaemonSet (the part below "template:"). Both modes run the same containers; only the
volume for the storage directory of the supervisor differs and is added by the workload.
*/}}
{{- define "myriarch-collector.podTemplate" -}}
{{- $container := include "myriarch-collector.containerMode" . -}}
{{- $tls := .Values.server.tls -}}
{{- $securityContext := .Values.securityContext -}}
{{- $updaterImage := include "myriarch-collector.image" (dict "root" . "image" .Values.images.updater "key" "images.updater") -}}
{{- $collectorImage := include "myriarch-collector.image" (dict "root" . "image" .Values.images.collector "key" "images.collector") -}}
{{- $supervisorImage := include "myriarch-collector.image" (dict "root" . "image" .Values.images.supervisor "key" "images.supervisor") -}}
metadata:
  labels:
    {{- include "myriarch-collector.labels" . | nindent 4 }}
    {{- with .Values.podLabels }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
  annotations:
    checksum/config: {{ include (print $.Template.BasePath "/configmap.yaml") . | sha256sum }}
    {{- with .Values.podAnnotations }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
spec:
  serviceAccountName: {{ include "myriarch-collector.serviceAccountName" . }}
  automountServiceAccountToken: {{ .Values.serviceAccount.automountToken }}
  terminationGracePeriodSeconds: {{ include "myriarch-collector.terminationGracePeriodSeconds" . }}
  securityContext:
    {{- include "myriarch-collector.podSecurityContext" . | nindent 4 }}
  {{- with .Values.priorityClassName }}
  priorityClassName: {{ . }}
  {{- end }}
  {{- with .Values.imagePullSecrets }}
  imagePullSecrets:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  initContainers:
    # 1. The updater copies its own static binary to the shared volume.
    - name: install-updater
      image: {{ $updaterImage | quote }}
      imagePullPolicy: {{ .Values.images.updater.pullPolicy }}
      command: ["/usr/local/bin/myriarch-updater"]
      args: ["install-self", "/shared"]
      resources:
        {{- toYaml .Values.initResources | nindent 8 }}
      securityContext:
        {{- toYaml $securityContext | nindent 8 }}
      volumeMounts:
        - {name: shared, mountPath: /shared}
    # 2. The collector image has no shell and no cp: the updater from the shared volume copies the collector binary.
    - name: seed-collector
      image: {{ $collectorImage | quote }}
      imagePullPolicy: {{ .Values.images.collector.pullPolicy }}
      command: ["/shared/myriarch-updater"]
      args: ["copy", "/otelcol-contrib", "/shared/"]
      resources:
        {{- toYaml .Values.initResources | nindent 8 }}
      securityContext:
        {{- toYaml $securityContext | nindent 8 }}
      volumeMounts:
        - {name: shared, mountPath: /shared}
  containers:
    - name: collector
      image: {{ $supervisorImage | quote }}
      imagePullPolicy: {{ .Values.images.supervisor.pullPolicy }}
      {{- if $container }}
      command: ["/shared/myriarch-updater"]
      args: ["container", "--config=/etc/myriarch/updater.yaml"]
      {{- else }}
      command: ["/usr/local/bin/opampsupervisor"]
      args: ["--config=/etc/opampsupervisor/supervisor.yaml"]
      {{- end }}
      env:
        - name: MYRIARCH_OPAMP_ENDPOINT
          value: {{ .Values.server.endpoint | quote }}
        - name: MYRIARCH_ENROLLMENT_TOKEN
          valueFrom:
            secretKeyRef:
              name: {{ .Values.enrollment.existingSecret }}
              key: {{ .Values.enrollment.key }}
        - {name: SITE_TYPE, value: {{ include "myriarch-collector.siteType" . | quote }}}
        - {name: SITE_ID, value: {{ .Values.site.id | quote }}}
        - {name: SITE_NAME, value: {{ .Values.site.name | quote }}}
        - {name: SITE_COUNTRY, value: {{ .Values.site.country | quote }}}
        - {name: SITE_REGION, value: {{ .Values.site.region | quote }}}
        - {name: SITE_TIMEZONE, value: {{ .Values.site.timezone | quote }}}
        - {name: ENVIRONMENT, value: {{ .Values.site.environment | quote }}}
        - {name: K8S_CLUSTER_NAME, value: {{ .Values.site.clusterName | quote }}}
        - name: K8S_POD_NAME
          valueFrom: {fieldRef: {fieldPath: metadata.name}}
        - name: K8S_NAMESPACE_NAME
          valueFrom: {fieldRef: {fieldPath: metadata.namespace}}
        - name: K8S_NODE_NAME
          valueFrom: {fieldRef: {fieldPath: spec.nodeName}}
        {{- with .Values.extraEnv }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      {{- with .Values.extraEnvFrom }}
      envFrom:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      ports:
        - {name: health, containerPort: {{ include "myriarch-collector.healthPort" . }}, protocol: TCP}
        - {name: sup-health, containerPort: 13134, protocol: TCP}
        {{- if $container }}
        - {name: updater-health, containerPort: 13135, protocol: TCP}
        {{- end }}
        {{- range .Values.ports }}
        - name: {{ .name }}
          containerPort: {{ .port }}
          protocol: {{ .protocol | default "TCP" }}
        {{- end }}
      {{- if $container }}
      # The updater answers /healthz while its main loop runs and /readyz while the supervisor runs and no swap is in progress.
      startupProbe:
        httpGet: {path: /healthz, port: updater-health}
        periodSeconds: 5
        failureThreshold: 60
      livenessProbe:
        httpGet: {path: /healthz, port: updater-health}
        periodSeconds: 15
        failureThreshold: 4
      readinessProbe:
        httpGet: {path: /readyz, port: updater-health}
        periodSeconds: 10
        failureThreshold: 3
      {{- else }}
      # Liveness checks the supervisor (it restarts a crashed collector); readiness checks the collector (health_check extension of
      # the local health configuration, which is merged before the configuration from the server, or of the fallback configuration).
      livenessProbe:
        httpGet: {path: /health, port: sup-health}
        initialDelaySeconds: 20
        periodSeconds: 15
        failureThreshold: 4
      readinessProbe:
        httpGet: {path: /, port: health}
        periodSeconds: 10
        failureThreshold: 3
      {{- end }}
      resources:
        {{- toYaml .Values.resources | nindent 8 }}
      securityContext:
        {{- toYaml $securityContext | nindent 8 }}
      volumeMounts:
        - {name: config, mountPath: /etc/opampsupervisor/supervisor.yaml, subPath: supervisor.yaml, readOnly: true}
        - {name: config, mountPath: /etc/myriarch/fallback.yaml, subPath: fallback.yaml, readOnly: true}
        {{- if .Values.healthCheck.enabled }}
        - {name: config, mountPath: /etc/myriarch/health.yaml, subPath: health.yaml, readOnly: true}
        {{- end }}
        {{- if $container }}
        - {name: config, mountPath: /etc/myriarch/updater.yaml, subPath: updater.yaml, readOnly: true}
        - {name: binaries, mountPath: /var/lib/myriarch}
        {{- if .Values.containerMode.trustedRoot.existingConfigMap }}
        - {name: trust, mountPath: /etc/myriarch/trust, readOnly: true}
        {{- end }}
        {{- end }}
        - {name: shared, mountPath: /shared, readOnly: true}
        - {name: storage, mountPath: /var/lib/otelcol/supervisor}
        - {name: tmp, mountPath: /tmp}
        {{- if or $tls.existingSecret $tls.existingConfigMap }}
        - {name: ca, mountPath: /etc/myriarch/ca, readOnly: true}
        {{- end }}
        {{- if .Values.nodeLogs.enabled }}
        - {name: node-logs, mountPath: {{ .Values.nodeLogs.mountPath }}, readOnly: true}
        {{- end }}
        {{- with .Values.extraVolumeMounts }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
  volumes:
    - name: config
      configMap:
        name: {{ include "myriarch-collector.fullname" . }}
    - name: shared
      emptyDir:
        sizeLimit: {{ .Values.sharedSizeLimit }}
    - name: tmp
      emptyDir:
        sizeLimit: {{ .Values.tmpSizeLimit }}
    {{- if $tls.existingSecret }}
    - name: ca
      secret:
        secretName: {{ $tls.existingSecret }}
        items:
          - {key: {{ $tls.key }}, path: {{ $tls.key }}}
    {{- else if $tls.existingConfigMap }}
    - name: ca
      configMap:
        name: {{ $tls.existingConfigMap }}
        items:
          - {key: {{ $tls.key }}, path: {{ $tls.key }}}
    {{- end }}
    {{- if and $container .Values.containerMode.trustedRoot.existingConfigMap }}
    - name: trust
      configMap:
        name: {{ .Values.containerMode.trustedRoot.existingConfigMap }}
        items:
          - {key: {{ .Values.containerMode.trustedRoot.key }}, path: {{ .Values.containerMode.trustedRoot.key }}}
    {{- end }}
    {{- if .Values.nodeLogs.enabled }}
    - name: node-logs
      hostPath:
        path: {{ .Values.nodeLogs.hostPath }}
    {{- end }}
    {{- if include "myriarch-collector.daemonset" . }}
    - name: storage
      {{- if .Values.persistence.hostPath.enabled }}
      hostPath:
        path: {{ include "myriarch-collector.hostPath" . }}
        type: {{ .Values.persistence.hostPath.type }}
      {{- else }}
      emptyDir: {}
      {{- end }}
    {{- end }}
    {{- with .Values.extraVolumes }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
  {{- with .Values.nodeSelector }}
  nodeSelector:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with .Values.tolerations }}
  tolerations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with .Values.affinity }}
  affinity:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- if .Values.topologySpreadConstraints }}
  topologySpreadConstraints:
    {{- range .Values.topologySpreadConstraints }}
    - {{- toYaml (merge (deepCopy .) (dict "labelSelector" (dict "matchLabels" (include "myriarch-collector.selectorLabels" $ | fromYaml)))) | nindent 6 }}
    {{- end }}
  {{- end }}
{{- end -}}
