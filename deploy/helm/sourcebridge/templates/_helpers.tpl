{{/*
Expand the name of the chart.
*/}}
{{- define "sourcebridge.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "sourcebridge.fullname" -}}
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
{{- define "sourcebridge.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "sourcebridge.labels" -}}
helm.sh/chart: {{ include "sourcebridge.chart" . }}
{{ include "sourcebridge.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "sourcebridge.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sourcebridge.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
API component labels
*/}}
{{- define "sourcebridge.api.labels" -}}
{{ include "sourcebridge.labels" . }}
app.kubernetes.io/component: api
{{- end }}

{{/*
API selector labels
*/}}
{{- define "sourcebridge.api.selectorLabels" -}}
{{ include "sourcebridge.selectorLabels" . }}
app.kubernetes.io/component: api
{{- end }}

{{/*
Web component labels
*/}}
{{- define "sourcebridge.web.labels" -}}
{{ include "sourcebridge.labels" . }}
app.kubernetes.io/component: web
{{- end }}

{{/*
Web selector labels
*/}}
{{- define "sourcebridge.web.selectorLabels" -}}
{{ include "sourcebridge.selectorLabels" . }}
app.kubernetes.io/component: web
{{- end }}

{{/*
Worker component labels
*/}}
{{- define "sourcebridge.worker.labels" -}}
{{ include "sourcebridge.labels" . }}
app.kubernetes.io/component: worker
{{- end }}

{{/*
Worker selector labels
*/}}
{{- define "sourcebridge.worker.selectorLabels" -}}
{{ include "sourcebridge.selectorLabels" . }}
app.kubernetes.io/component: worker
{{- end }}

{{/*
SurrealDB component labels
*/}}
{{- define "sourcebridge.surrealdb.labels" -}}
{{ include "sourcebridge.labels" . }}
app.kubernetes.io/component: surrealdb
{{- end }}

{{/*
SurrealDB selector labels
*/}}
{{- define "sourcebridge.surrealdb.selectorLabels" -}}
{{ include "sourcebridge.selectorLabels" . }}
app.kubernetes.io/component: surrealdb
{{- end }}

{{/*
Redis component labels
*/}}
{{- define "sourcebridge.redis.labels" -}}
{{ include "sourcebridge.labels" . }}
app.kubernetes.io/component: redis
{{- end }}

{{/*
Redis selector labels
*/}}
{{- define "sourcebridge.redis.selectorLabels" -}}
{{ include "sourcebridge.selectorLabels" . }}
app.kubernetes.io/component: redis
{{- end }}

{{/*
SurrealDB WebSocket connection URL (used by Go API and Python worker)
*/}}
{{- define "sourcebridge.surrealdb.url" -}}
{{- if .Values.surrealdb.enabled }}
{{- printf "ws://%s-surrealdb:%d/rpc" (include "sourcebridge.fullname" .) (.Values.surrealdb.port | int) }}
{{- end }}
{{- end }}

{{/*
Redis connection URL
*/}}
{{- define "sourcebridge.redis.url" -}}
{{- if .Values.redis.enabled }}
{{- printf "redis://%s-redis:%d" (include "sourcebridge.fullname" .) (.Values.redis.port | int) }}
{{- end }}
{{- end }}

{{/*
ServiceAccount name (shared / legacy fallback — do not use for new deployments).
Per-component names are preferred: sourcebridge.api.serviceAccountName etc.
*/}}
{{- define "sourcebridge.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "sourcebridge.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
CA-230: Per-component ServiceAccount names.
When serviceAccount.create is false operators supply their own SA;
the per-component override keys (serviceAccount.apiName etc.) allow
overriding individual SAs while still having create:true for the rest.
*/}}
{{- define "sourcebridge.api.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (printf "%s-api" (include "sourcebridge.fullname" .)) .Values.serviceAccount.apiName }}
{{- else }}
{{- default "default" .Values.serviceAccount.apiName }}
{{- end }}
{{- end }}

{{- define "sourcebridge.worker.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (printf "%s-worker" (include "sourcebridge.fullname" .)) .Values.serviceAccount.workerName }}
{{- else }}
{{- default "default" .Values.serviceAccount.workerName }}
{{- end }}
{{- end }}

{{- define "sourcebridge.web.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (printf "%s-web" (include "sourcebridge.fullname" .)) .Values.serviceAccount.webName }}
{{- else }}
{{- default "default" .Values.serviceAccount.webName }}
{{- end }}
{{- end }}

{{- define "sourcebridge.surrealdb.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (printf "%s-surrealdb" (include "sourcebridge.fullname" .)) .Values.serviceAccount.surrealdbName }}
{{- else }}
{{- default "default" .Values.serviceAccount.surrealdbName }}
{{- end }}
{{- end }}
