{{- define "argo-center.name" -}}argo-center{{- end -}}

{{- define "argo-center.labels" -}}
app.kubernetes.io/name: argo-center
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "argo-center.selectorLabels" -}}
app.kubernetes.io/name: argo-center
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* Environment variable holding a cluster's ArgoCD token. */}}
{{- define "argo-center.tokenEnv" -}}
{{- printf "ARGOCD_TOKEN_%s" (. | upper | replace "-" "_") -}}
{{- end -}}

{{- define "argo-center.secretName" -}}
{{- .Values.argo_center.existingSecret | default "argo-center" -}}
{{- end -}}
