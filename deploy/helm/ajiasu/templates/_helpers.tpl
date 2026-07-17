{{- define "ajiasu.name" -}}{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}{{- end -}}
{{- define "ajiasu.fullname" -}}{{- printf "%s-%s" .Release.Name (include "ajiasu.name" .) | trunc 63 | trimSuffix "-" -}}{{- end -}}
{{- define "ajiasu.labels" -}}
app.kubernetes.io/name: {{ include "ajiasu.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: ajiasu
{{- end -}}
{{- define "ajiasu.image" -}}{{ .repository }}@{{ .digest }}{{- end -}}
{{- define "ajiasu.containerSecurityContext" -}}
runAsNonRoot: true
allowPrivilegeEscalation: false
seccompProfile: { type: RuntimeDefault }
capabilities: { drop: [ALL] }
{{- end -}}
