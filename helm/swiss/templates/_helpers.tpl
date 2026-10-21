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

{{/* The Secret holding the login, whether this chart made it or the operator did. */}}
{{- define "swiss.authSecretName" -}}
{{- if .Values.auth.existingSecret -}}
{{- .Values.auth.existingSecret -}}
{{- else -}}
{{- printf "%s-auth" (include "swiss.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/* Exactly what the sglang and vllm charts render, plus helm's release storage. */}}
{{- define "swiss.deployRules" -}}
{{- $all := list "get" "list" "watch" "create" "update" "patch" "delete" }}
- apiGroups: [""]
  resources: ["secrets", "configmaps", "services", "serviceaccounts"]
  verbs: {{ $all | toJson }}
- apiGroups: ["apps"]
  resources: ["deployments"]
  verbs: {{ $all | toJson }}
- apiGroups: ["policy"]
  resources: ["poddisruptionbudgets"]
  verbs: {{ $all | toJson }}
- apiGroups: ["rbac.authorization.k8s.io"]
  resources: ["roles", "rolebindings"]
  verbs: {{ $all | toJson }}
{{/*
Kubernetes refuses to let a subject create a Role granting permissions it does
not itself hold, so swissd must hold everything the charts hand out. These two
are the cart subchart's HA lease election -- swissd never uses them, it only
passes them on.
*/}}
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  verbs: ["get", "list", "watch", "create", "update", "patch"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list", "watch", "patch"]
- apiGroups: ["monitoring.coreos.com"]
  resources: ["servicemonitors"]
  verbs: {{ $all | toJson }}
- apiGroups: ["leaderworkerset.x-k8s.io"]
  resources: ["leaderworkersets"]
  verbs: {{ $all | toJson }}
- apiGroups: ["disaggregatedset.x-k8s.io"]
  resources: ["disaggregatedsets"]
  verbs: {{ $all | toJson }}
- apiGroups: ["autoscaling.4pd.io", "autoscaling.modelsphere.dev"]
  resources: ["llmscalers"]
  verbs: {{ $all | toJson }}
- apiGroups: ["inference.x-k8s.io", "inference.modelsphere.dev"]
  resources: ["llmslorequirements"]
  verbs: {{ $all | toJson }}
- apiGroups: ["routing.gpucluster.io", "routing.modelsphere.dev"]
  resources: ["modelroutes"]
  verbs: {{ $all | toJson }}
{{- end -}}

{{/*
LLMService access, granted whichever install method a release uses: the method
is picked per install, so the grant cannot depend on it. controllerrevisions are
read-only: they hold archived specs.
*/}}
{{- define "swiss.llmsvcRules" -}}
- apiGroups: ["serving.modelsphere.dev"]
  resources: ["llmservices"]
  verbs: ["get","list","watch","create","update","patch","delete"]
- apiGroups: ["apps"]
  resources: ["controllerrevisions"]
  verbs: {{ list "get" "list" | toJson }}
{{- end -}}

{{/*
catalog-server, vendored: model-catalog's catalog-server/chart, its templates
copied into templates/catalog-server/ as they are, each wrapped to render, when
catalogServer.enabled, with this context in place of the swiss chart's: its
values are catalogServer, its chart catalog-server's. One line differs: the
config checksum includes the config, not the ConfigMap's template path.
*/}}
{{- define "swiss.catalogServer" -}}
Values: {{- toYaml .Values.catalogServer | nindent 2 }}
Release: {{- toYaml .Release | nindent 2 }}
Chart: { Name: catalog-server, Version: 0.1.0, AppVersion: "0.1.0" }
{{- end -}}
