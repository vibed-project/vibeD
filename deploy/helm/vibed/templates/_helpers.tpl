{{/*
Expand the name of the chart.
*/}}
{{- define "vibed.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "vibed.fullname" -}}
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
Common labels
*/}}
{{- define "vibed.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "vibed.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "vibed.selectorLabels" -}}
app.kubernetes.io/name: {{ include "vibed.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name
*/}}
{{- define "vibed.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "vibed.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Warm-pool image reference. An untagged repo gets the chart's appVersion
appended, so runner images track the release automatically instead of being
pinned by hand (they drifted to 0.4.4 across two releases, shipping sandbox
agents that predated the v0.5.1 fetch/extract hardening). A ref that already
carries a tag or digest is used verbatim, so deliberate pins still work.
The tag check looks only at the final path segment — a registry host may
legitimately contain a port, e.g. registry.local:5000/vibed-runner-node.
*/}}
{{- define "vibed.warmPoolImage" -}}
{{- $img := .image -}}
{{- $lastSegment := regexReplaceAll ".*/" $img "" -}}
{{- if or (contains ":" $lastSegment) (contains "@" $lastSegment) -}}
{{- $img -}}
{{- else -}}
{{- printf "%s:%s" $img .root.Chart.AppVersion -}}
{{- end -}}
{{- end -}}
