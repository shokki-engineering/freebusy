{{/* Copyright 2026 Srikanth K. */}}
{{/* SPDX-License-Identifier: AGPL-3.0-or-later */}}

{{- define "freebusy.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "freebusy.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "freebusy.selector" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "freebusy.secretName" -}}
{{- .Values.secrets.existingSecret | default (include "freebusy.fullname" .) -}}
{{- end -}}

{{- define "freebusy.image" -}}
{{- printf "%s:%s" .image.repository (.image.tag | default .root.Chart.AppVersion) -}}
{{- end -}}

{{- define "freebusy.hasuraURL" -}}
{{- .Values.database.hasura.url | default (printf "http://%s-engine:3000/graphql" (include "freebusy.fullname" .)) -}}
{{- end -}}

{{/* Unprivileged, read-only: the images hold a static binary and nothing else. */}}
{{- define "freebusy.securityContext" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end -}}
