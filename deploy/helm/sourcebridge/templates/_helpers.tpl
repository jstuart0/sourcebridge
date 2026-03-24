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
SurrealDB connection URL
*/}}
{{- define "sourcebridge.surrealdb.url" -}}
{{- if .Values.surrealdb.enabled }}
{{- printf "http://%s-surrealdb:%d" (include "sourcebridge.fullname" .) (.Values.surrealdb.port | int) }}
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
