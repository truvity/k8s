{{/*
The protection annotations of a data-bearing object (`protect` true).
Called as: include "guardrails-projects.protect" (dict "root" $ "protect" bool)
*/}}
{{- define "guardrails-projects.protect" -}}
{{- if .protect -}}
argocd.argoproj.io/sync-options: Prune=false,Delete=false
{{- if .root.Values.helmKeep }}
helm.sh/resource-policy: keep
{{- end }}
{{- end -}}
{{- end -}}
