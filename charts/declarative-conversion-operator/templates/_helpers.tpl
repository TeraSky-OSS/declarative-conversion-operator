{{/*
Base chart name.
*/}}
{{- define "declarative-conversion-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name.
*/}}
{{- define "declarative-conversion-operator.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s" (include "declarative-conversion-operator.name" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "declarative-conversion-operator.labels" -}}
app.kubernetes.io/name: {{ include "declarative-conversion-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end }}

{{/*
Manager selector labels.
*/}}
{{- define "declarative-conversion-operator.managerSelectorLabels" -}}
app.kubernetes.io/name: {{ include "declarative-conversion-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
control-plane: controller-manager
{{- end }}

{{/*
Manager ServiceAccount name.
*/}}
{{- define "declarative-conversion-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (printf "%s-manager" (include "declarative-conversion-operator.fullname" .)) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Webhook-server ServiceAccount name — shared across every ConversionWebhookServer
instance, since its RBAC (read-only, cluster-scoped) doesn't vary per instance.
*/}}
{{- define "declarative-conversion-operator.webhookServerServiceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (printf "%s-webhook-server" (include "declarative-conversion-operator.fullname" .)) .Values.serviceAccount.webhookServerName }}
{{- else }}
{{- default "default" .Values.serviceAccount.webhookServerName }}
{{- end }}
{{- end }}

{{/*
Namespace ConversionWebhookServer child resources land in when
spec.namespace is left unset — defaults to the release namespace.
*/}}
{{- define "declarative-conversion-operator.defaultServerNamespace" -}}
{{ .Release.Namespace }}
{{- end }}

{{/*
The manager's own admission-webhook Service name (referenced by both the
Certificate and the ValidatingWebhookConfiguration).
*/}}
{{- define "declarative-conversion-operator.webhookServiceName" -}}
{{ include "declarative-conversion-operator.fullname" . }}-webhook-service
{{- end }}
