{{- define "swiss.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "swiss.fullname" -}}
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

{{- define "swiss.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "swiss.labels" -}}
helm.sh/chart: {{ include "swiss.chart" . }}
{{ include "swiss.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "swiss.selectorLabels" -}}
app.kubernetes.io/name: {{ include "swiss.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "swiss.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "swiss.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* The profile ConfigMap this release creates, "namespace/name". */}}
{{- define "swiss.profileRef" -}}
{{- printf "%s/%s-profile" .Release.Namespace (include "swiss.fullname" .) -}}
{{- end -}}

{{/*
The profile reference swissd is configured with: whatever config.cluster.profile
names, or the one this chart creates. Resolved in one place so the ConfigMap and
the RBAC that reads it cannot disagree.
*/}}
{{- define "swiss.effectiveProfileRef" -}}
{{- if .Values.config.cluster.profile -}}
{{- .Values.config.cluster.profile -}}
{{- else -}}
{{- include "swiss.profileRef" . -}}
{{- end -}}
{{- end -}}
