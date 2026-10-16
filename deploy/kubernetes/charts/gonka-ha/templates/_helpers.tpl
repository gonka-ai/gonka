{{- define "gonka.fullname" -}}
{{- default (printf "%s-gonka-ha" .Release.Name) .Values.fullnameOverride | trunc 40 | trimSuffix "-" -}}
{{- end -}}

{{- define "gonka.labels" -}}
app.kubernetes.io/name: gonka-ha
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end -}}

{{- define "gonka.selector" -}}
app.kubernetes.io/name: gonka-ha
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "gonka.oracleURL" -}}
http://{{ include "gonka.fullname" . }}-oracle.{{ .Release.Namespace }}.svc.{{ .Values.clusterDomain }}:9100/versions
{{- end -}}

{{- define "gonka.scheduling" -}}
affinity:
  podAntiAffinity:
    {{- if .root.Values.scheduling.requireDistinctNodes }}
    requiredDuringSchedulingIgnoredDuringExecution:
      - topologyKey: kubernetes.io/hostname
        labelSelector:
          matchLabels:
            {{- include "gonka.selector" . | nindent 12 }}
    {{- else }}
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          topologyKey: kubernetes.io/hostname
          labelSelector:
            matchLabels:
              {{- include "gonka.selector" . | nindent 14 }}
    {{- end }}
{{- with .config.nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .config.tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .root.Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}
