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

{{/*
The custom resources a release page reports on, read-only. Granted whether or
not deploy is on: reading what a ModelRoute or LLMScaler last observed changes
nothing, and the page is how an operator finds out a model is not routed.
*/}}
{{- define "swiss.objectReadRules" -}}
- apiGroups: ["autoscaling.4pd.io", "autoscaling.modelsphere.dev"]
  resources: ["llmscalers"]
  verbs: ["get"]
- apiGroups: ["inference.x-k8s.io", "inference.modelsphere.dev"]
  resources: ["llmslorequirements"]
  verbs: ["get"]
- apiGroups: ["routing.gpucluster.io", "routing.modelsphere.dev"]
  resources: ["modelroutes"]
  verbs: ["get"]
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
LLMService access for rbac.applyWith other than helm. With allowDeploy the
release Role can write llmservices; without it, get/list/watch is enough for
the deployments page. controllerrevisions are always read-only: they hold
archived specs.
*/}}
{{- define "swiss.llmsvcRules" -}}
{{- $verbs := list "get" "list" "watch" -}}
{{- if .Values.config.allowDeploy -}}
{{- $verbs = list "get" "list" "watch" "create" "update" "patch" "delete" -}}
{{- end -}}
- apiGroups: ["serving.modelsphere.dev"]
  resources: ["llmservices"]
  verbs: {{ $verbs | toJson }}
- apiGroups: ["apps"]
  resources: ["controllerrevisions"]
  verbs: {{ list "get" "list" | toJson }}
{{- end -}}

{{/* config.applyWith and rbac.applyWith have to agree, or swissd is granted a backend it will not run, or runs one it cannot. */}}
{{- define "swiss.validateApplyWith" -}}
{{- $apply := .Values.config.applyWith | default "helm" -}}
{{- $rbacApply := .Values.rbac.applyWith | default "helm" -}}
{{- if not (or (eq $apply "helm") (eq $apply "llmsvc")) -}}
{{- fail (printf "config.applyWith %q must be helm or llmsvc" $apply) -}}
{{- end -}}
{{- if not (or (eq $rbacApply "helm") (eq $rbacApply "both") (eq $rbacApply "llmsvc")) -}}
{{- fail (printf "rbac.applyWith %q must be helm, both or llmsvc" $rbacApply) -}}
{{- end -}}
{{- if and (eq $apply "llmsvc") (eq $rbacApply "helm") -}}
{{- fail "config.applyWith is llmsvc but rbac.applyWith is helm: swissd would write LLMServices without a grant. Set rbac.applyWith to both or llmsvc." -}}
{{- end -}}
{{- if and (eq $rbacApply "llmsvc") (eq $apply "helm") -}}
{{- fail "rbac.applyWith is llmsvc but config.applyWith is helm: swissd would run helm without the deploy grant. Set config.applyWith to llmsvc, or rbac.applyWith to helm or both." -}}
{{- end -}}
{{- end -}}
