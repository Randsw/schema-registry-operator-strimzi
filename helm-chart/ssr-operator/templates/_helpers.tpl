{{/*
Expand the name of the chart.
*/}}
{{- define "ssr-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "ssr-operator.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "ssr-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "ssr-operator.labels" -}}
helm.sh/chart: {{ include "ssr-operator.chart" . }}
{{ include "ssr-operator.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "ssr-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ssr-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
control-plane: controller-manager
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "ssr-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "ssr-operator.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of an RBAC object rendered from a cluster_roles/roles entry.

Expects a dict with:
  ctx           the root context ($)
  name          the entry's .name (e.g. "manager-role")
  clusterScoped true when the object renders cluster-wide
                (ClusterRole/ClusterRoleBinding)

Cluster-scoped names embed the release namespace: ClusterRole and
ClusterRoleBinding objects live outside any namespace, so two installs of
the chart in different namespaces (the primary use case of
rbac.namespaceScoped=true) would otherwise collide on identical names and
fail with Helm ownership/already-exists errors. Namespaced Role/RoleBinding
names need no qualification: their namespace already isolates them. Role
templates and their binding templates must call this helper with the same
arguments so roleRef always points at the role rendered by the same release.
*/}}
{{- define "ssr-operator.rbacName" -}}
{{- $base := printf "%s-%s" (include "ssr-operator.fullname" .ctx) .name -}}
{{- if .clusterScoped -}}
{{- printf "%s-%s" .ctx.Release.Namespace $base -}}
{{- else -}}
{{- $base -}}
{{- end -}}
{{- end }}
