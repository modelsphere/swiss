{{- define "catalog-server.name" -}}{{ default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}{{- end -}}
{{- define "catalog-server.fullname" -}}
{{- if .Values.fullnameOverride -}}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else -}}{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}{{ .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else -}}{{ printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}{{- end -}}{{- end -}}
{{- end -}}
{{- define "catalog-server.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "catalog-server.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}
{{- define "catalog-server.selectorLabels" -}}
app.kubernetes.io/name: {{ include "catalog-server.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
{{- define "catalog-server.claimName" -}}{{ .Values.persistence.existingClaim | default (printf "%s-data" (include "catalog-server.fullname" .)) }}{{- end -}}
{{/* Whether anyone may write: a tokens Secret of one's own, or entries. */}}
{{- define "catalog-server.writes" -}}{{ if or .Values.tokens.existingSecret .Values.tokens.entries }}true{{ end }}{{- end -}}
{{- define "catalog-server.tokensSecret" -}}{{ .Values.tokens.existingSecret | default (printf "%s-tokens" (include "catalog-server.fullname" .)) }}{{- end -}}
{{/* In-cluster URL, basePath included. */}}
{{- define "catalog-server.ingressUrl" -}}
{{- if .Values.ingress.enabled -}}{{ printf "%s://%s/%s/" (ternary "https" "http" (not (empty .Values.ingress.tls))) .Values.ingress.host .Values.config.basePath | replace "//" "/" | replace ":/" "://" }}{{- end -}}
{{- end -}}
{{- define "catalog-server.url" -}}
{{- printf "http://%s.%s.svc" (include "catalog-server.fullname" .) .Release.Namespace -}}{{ if ne (int .Values.service.port) 80 }}:{{ .Values.service.port }}{{ end }}{{ printf "/%s/" .Values.config.basePath | replace "//" "/" | replace "//" "/" }}
{{- end -}}
{{/* catalog-server's config file: where the chart puts things, and config. */}}
{{- define "catalog-server.config" -}}
{{- $c := .Values.config -}}
{{- $repo := dict "dir" "/data/catalog" "syncInterval" (int $c.syncInterval) -}}
{{- with $c.branch }}{{ $_ := set $repo "branch" . }}{{ end -}}
{{- $cfg := dict "port" 8000 "basePath" $c.basePath "repository" $repo "committer" $c.committer -}}
{{- if $c.pages }}{{ $_ := set $cfg "pagesDir" "/app/web" }}{{ end -}}
{{- if include "catalog-server.writes" . }}{{ $_ := set $cfg "tokensFile" (printf "/etc/catalog-server/tokens/%s" .Values.tokens.key) }}{{ end -}}
{{- toYaml (mergeOverwrite $cfg (deepCopy $c.extra)) -}}
{{- end -}}
