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
User-supplied labels with chart identity / manager selector keys stripped
so they cannot override app.kubernetes.io/name|instance|managed-by|version,
helm.sh/chart, or control-plane.
*/}}
{{- define "declarative-conversion-operator.userLabels" -}}
{{- $skip := list "app.kubernetes.io/name" "app.kubernetes.io/instance" "app.kubernetes.io/managed-by" "app.kubernetes.io/version" "helm.sh/chart" "control-plane" -}}
{{- range $k, $v := . }}
{{- if not (has $k $skip) }}
{{ $k }}: {{ $v | quote }}
{{- end }}
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
{{- include "declarative-conversion-operator.userLabels" .Values.commonLabels }}
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
Manager container image. Prefer digest pinning when image.manager.digest is set.
*/}}
{{- define "declarative-conversion-operator.managerImage" -}}
{{- $repo := printf "%s/%s" .Values.image.registry .Values.image.manager.repository -}}
{{- if .Values.image.manager.digest -}}
{{- printf "%s@%s" $repo .Values.image.manager.digest -}}
{{- else -}}
{{- printf "%s:%s" $repo (.Values.image.manager.tag | default .Chart.AppVersion) -}}
{{- end -}}
{{- end }}

{{/*
Webhook-server container image (also passed to the manager as --default-webhook-server-image).
Prefer digest pinning when image.webhookServer.digest is set.
*/}}
{{- define "declarative-conversion-operator.webhookServerImage" -}}
{{- $repo := printf "%s/%s" .Values.image.registry .Values.image.webhookServer.repository -}}
{{- if .Values.image.webhookServer.digest -}}
{{- printf "%s@%s" $repo .Values.image.webhookServer.digest -}}
{{- else -}}
{{- printf "%s:%s" $repo (.Values.image.webhookServer.tag | default .Chart.AppVersion) -}}
{{- end -}}
{{- end }}

{{/*
The manager's own admission-webhook Service name (referenced by both the
Certificate and the ValidatingWebhookConfiguration).
*/}}
{{- define "declarative-conversion-operator.webhookServiceName" -}}
{{ include "declarative-conversion-operator.fullname" . }}-webhook-service
{{- end }}
