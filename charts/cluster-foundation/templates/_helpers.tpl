{{/*
Is an object protected? The entry's own `protect`, when set, else the chart's.

Called as: include "cluster-foundation.protected" (dict "root" $ "entry" $entry)
Returns "true" or "" (so `if` reads it).
*/}}
{{- define "cluster-foundation.protected" -}}
{{- $p := .root.Values.protect -}}
{{- if and (hasKey .entry "protect") (not (kindIs "invalid" .entry.protect)) -}}{{- $p = .entry.protect -}}{{- end -}}
{{- if $p -}}true{{- end -}}
{{- end -}}

{{/*
The metadata block of one object: name, optional namespace, labels and
annotations. Annotations are the computed ones (sync-wave, protection, anything
in `computed`) plus the caller's literal ones; a literal annotation or label
may not shadow a computed key.

Called as: include "cluster-foundation.metadata" (dict "root" $ "name" $n
  "namespace" "" "entry" $entry "labels" $labels "computed" $computedAnnotations
  "where" "namespaces.team-a")
*/}}
{{- define "cluster-foundation.metadata" -}}
{{- $root := .root -}}
{{- $annotations := dict -}}
{{- if not (kindIs "invalid" .entry.syncWave) -}}
{{- $_ := set $annotations "argocd.argoproj.io/sync-wave" (toString .entry.syncWave) -}}
{{- end -}}
{{- if include "cluster-foundation.protected" (dict "root" $root "entry" .entry) -}}
{{- $_ := set $annotations "argocd.argoproj.io/sync-options" "Prune=false,Delete=false" -}}
{{- if $root.Values.helmKeep -}}{{- $_ := set $annotations "helm.sh/resource-policy" "keep" -}}{{- end -}}
{{- end -}}
{{- range $k, $v := (.computed | default dict) -}}{{- $_ := set $annotations $k $v -}}{{- end -}}
{{- range $k, $v := (.entry.annotations | default dict) -}}
{{- if hasKey $annotations $k -}}
{{- fail (printf "%s.annotations sets %s, which this chart computes; use the dedicated key" $.where $k) -}}
{{- end -}}
{{- $_ := set $annotations $k $v -}}
{{- end -}}
{{- $labels := .labels | default dict -}}
name: {{ .name }}
{{- if .namespace }}
namespace: {{ .namespace }}
{{- end }}
{{- with $labels }}
labels:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with $annotations }}
annotations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}
