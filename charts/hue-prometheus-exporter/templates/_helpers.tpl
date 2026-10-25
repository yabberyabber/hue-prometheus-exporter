{{- define "hpe.name" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "hpe.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "hpe.labels" -}}
{{ include "hpe.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "hpe.secretName" -}}
{{- if .Values.applicationKey.existingSecret -}}
{{- .Values.applicationKey.existingSecret -}}
{{- else -}}
{{- include "hpe.name" . -}}
{{- end -}}
{{- end -}}

{{- define "hpe.secretKey" -}}
{{- if .Values.applicationKey.existingSecret -}}
{{- .Values.applicationKey.existingSecretKey -}}
{{- else -}}
application-key
{{- end -}}
{{- end -}}
