{{/*
The profile a tenant names. A tenant naming a profile that does not exist is a
render error: a tenant with no profile would get no policy, no quota and no
labels, and look finished.

Called as: include "tenancy.profile" (dict "root" $ "name" $ns "tenant" $t) | fromYaml
*/}}
{{- define "tenancy.profile" -}}
{{- $profiles := .root.Values.profiles | default dict -}}
{{- if not (hasKey $profiles .tenant.profile) -}}
{{- fail (printf "tenants.%s names profile %q, which is not in `profiles` (have: %s)" .name .tenant.profile (join ", " (keys $profiles | sortAlpha))) -}}
{{- end -}}
{{- toYaml (index $profiles .tenant.profile) -}}
{{- end -}}

{{/*
How an object is protected: "true", "prune-only" or "false". The entry's own
`protect` wins, then the profile's `protect.<feature>`, then the chart's.

Called as: include "tenancy.mode" (dict "root" $ "profile" $p "feature" "networkPolicy" "entry" $entry)
*/}}
{{- define "tenancy.mode" -}}
{{- $m := .root.Values.protect -}}
{{- $byFeature := (.profile | default dict).protect | default dict -}}
{{- if and .feature (hasKey $byFeature .feature) -}}{{- $m = index $byFeature .feature -}}{{- end -}}
{{- $e := .entry | default dict -}}
{{- if and (hasKey $e "protect") (not (kindIs "invalid" $e.protect)) -}}{{- $m = $e.protect -}}{{- end -}}
{{- $m -}}
{{- end -}}

{{/*
The labels of one object of a tenant for one feature: the profile's, then the
tenant's merged over them. Each source contributes its map for the feature, or
its `default` map when it has none for it. Rendered as YAML (read it back with
fromYaml).

Called as: include "tenancy.labels" (dict "profile" $p "tenant" $t "feature" "nats") | fromYaml
*/}}
{{- define "tenancy.labels" -}}
{{- $feature := .feature -}}
{{- $out := dict -}}
{{- range $src := list ((.profile | default dict).labels | default dict) ((.tenant | default dict).labels | default dict) -}}
{{- $one := dict -}}
{{- if hasKey $src $feature -}}{{- $one = index $src $feature -}}
{{- else if hasKey $src "default" -}}{{- $one = index $src "default" -}}{{- end -}}
{{- range $k, $v := ($one | default dict) -}}{{- $_ := set $out $k $v -}}{{- end -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}

{{/*
The metadata block of one object: name, optional namespace, labels and the
computed annotations (sync-wave, protection, plus `annotations`).

Called as: include "tenancy.metadata" (dict "root" $ "name" $n "namespace" $ns
  "labels" $labels "wave" $wave "mode" "true" "annotations" (dict ...))
*/}}
{{- define "tenancy.metadata" -}}
{{- $annotations := dict -}}
{{- if not (kindIs "invalid" .wave) -}}
{{- $_ := set $annotations "argocd.argoproj.io/sync-wave" (toString .wave) -}}
{{- end -}}
{{- $mode := toString .mode -}}
{{- if eq $mode "true" -}}
{{- $_ := set $annotations "argocd.argoproj.io/sync-options" "Prune=false,Delete=false" -}}
{{- else if eq $mode "prune-only" -}}
{{- $_ := set $annotations "argocd.argoproj.io/sync-options" "Prune=false" -}}
{{- end -}}
{{- if and (ne $mode "false") .root.Values.helmKeep -}}{{- $_ := set $annotations "helm.sh/resource-policy" "keep" -}}{{- end -}}
{{- range $k, $v := (.annotations | default dict) -}}{{- $_ := set $annotations $k $v -}}{{- end -}}
name: {{ .name }}
{{- if .namespace }}
namespace: {{ .namespace }}
{{- end }}
{{- with .labels }}
labels:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with $annotations }}
annotations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/* RBAC subjects of a binding: a ServiceAccount carries a namespace, the others an apiGroup. */}}
{{- define "tenancy.subjects" -}}
{{- range .subjects }}
- kind: {{ .kind }}
  name: {{ .name | quote }}
  {{- if eq .kind "ServiceAccount" }}
  namespace: {{ required (printf "%s: a ServiceAccount subject needs a namespace" $.where) .namespace }}
  {{- else }}
  apiGroup: rbac.authorization.k8s.io
  {{- end }}
{{- end }}
{{- end -}}
