{{- define "gonka-postgres.fullname" -}}
{{- default (printf "%s-gonka-postgres" .Release.Name) .Values.fullnameOverride | trunc 50 | trimSuffix "-" -}}
{{- end -}}

{{- define "gonka-postgres.labels" -}}
app.kubernetes.io/name: gonka-postgres
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end -}}

{{- define "gonka-postgres.validate" -}}
{{- if not (.Capabilities.APIVersions.Has "postgresql.cnpg.io/v1") -}}
{{- fail "CloudNativePG >= 1.28.0 must be installed first (postgresql.cnpg.io/v1 CRDs and webhook). For offline helm template only, pass --api-versions postgresql.cnpg.io/v1." -}}
{{- end -}}
{{- if or .Values.snapshots.enabled .Values.recovery.volumeSnapshot -}}
{{- if not (.Capabilities.APIVersions.Has "snapshot.storage.k8s.io/v1") -}}
{{- fail "VolumeSnapshot backups/recovery require the snapshot.storage.k8s.io/v1 API, snapshot controller and a compatible CSI driver. For offline rendering only, pass --api-versions snapshot.storage.k8s.io/v1." -}}
{{- end -}}
{{- end -}}
{{- if and .Values.snapshots.enabled (empty .Values.snapshots.className) -}}
{{- fail "snapshots.className is required when snapshots.enabled=true" -}}
{{- end -}}
{{- end -}}
