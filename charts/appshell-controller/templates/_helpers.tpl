{{/* The release's full name, at most 63 characters, as Kubernetes names allow. */}}
{{- define "appshell-controller.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "appshell-controller.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "appshell-controller.labels" -}}
{{ include "appshell-controller.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{/* The service account trusted-deployment records name, with the release's namespace. */}}
{{- define "appshell-controller.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "appshell-controller.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- required "serviceAccount.name is required when serviceAccount.create is false" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* By digest when one is given, so what runs can't change under a moved tag. */}}
{{- define "appshell-controller.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}
{{- end -}}

{{/*
The registry URL, refused unless it's https or allowInsecure says otherwise. The values schema
requires a URL; this is the rule the schema can't express.
*/}}
{{- define "appshell-controller.registryURL" -}}
{{- $url := required "registry.url is required: the appshell registry to apply to" .Values.registry.url -}}
{{- if and (not (hasPrefix "https://" $url)) (not .Values.registry.allowInsecure) -}}
{{- fail (printf "registry.url %q is not https; the controller won't send its token in the clear (registry.allowInsecure for local development)" $url) -}}
{{- end -}}
{{- /* Every trailing slash, as the controller strips them, so the audience matches its URL. */ -}}
{{- regexReplaceAll "/+$" $url "" -}}
{{- end -}}

{{/* The registry's deploy audience: the URL with /deploy, unless its operator chose another. */}}
{{- define "appshell-controller.audience" -}}
{{- default (printf "%s/deploy" (include "appshell-controller.registryURL" .)) .Values.registry.audience -}}
{{- end -}}

{{- define "appshell-controller.managerRules" }}
# Its own resources and nothing else: read them, write their status, add and remove its finalizer
# (a patch). It never creates or deletes one. Events, to say what it did. As generated from the
# controller's markers into config/rbac/role.yaml.
rules:
  - apiGroups: ["registry.appshell.org"]
    resources: ["appshellapplications"]
    verbs: ["get", "list", "watch", "patch"]
  - apiGroups: ["registry.appshell.org"]
    resources: ["appshellapplications/status"]
    verbs: ["get", "update", "patch"]
  - apiGroups: ["registry.appshell.org"]
    resources: ["appshellapplications/finalizers"]
    verbs: ["update"]
  - apiGroups: ["events.k8s.io"]
    resources: ["events"]
    verbs: ["create", "patch"]
{{- end }}
