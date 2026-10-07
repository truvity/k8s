{{/*
Is an object protected? The entry's own `protect`, when set, else the chart's.

Called as: include "cilium-config.protected" (dict "root" $ "entry" $entry)
Returns "true" or "" (so `if` reads it).
*/}}
{{- define "cilium-config.protected" -}}
{{- $p := .root.Values.protect -}}
{{- if and (hasKey .entry "protect") (not (kindIs "invalid" .entry.protect)) -}}{{- $p = .entry.protect -}}{{- end -}}
{{- if $p -}}true{{- end -}}
{{- end -}}

{{/*
The metadata block of one cluster-scoped object: name, labels and
annotations (sync-wave and protection computed, the caller's literal ones
added; a literal one may not shadow a computed key).

Called as: include "cilium-config.metadata" (dict "root" $ "name" $n "entry" $e "labels" $l "where" "loadBalancerIPPools.lan")
*/}}
{{- define "cilium-config.metadata" -}}
{{- $root := .root -}}
{{- $annotations := dict -}}
{{- if not (kindIs "invalid" .entry.syncWave) -}}
{{- $_ := set $annotations "argocd.argoproj.io/sync-wave" (toString .entry.syncWave) -}}
{{- end -}}
{{- if include "cilium-config.protected" (dict "root" $root "entry" .entry) -}}
{{- $_ := set $annotations "argocd.argoproj.io/sync-options" "Prune=false,Delete=false" -}}
{{- if $root.Values.helmKeep -}}{{- $_ := set $annotations "helm.sh/resource-policy" "keep" -}}{{- end -}}
{{- end -}}
{{- range $k, $v := (.entry.annotations | default dict) -}}
{{- if hasKey $annotations $k -}}
{{- fail (printf "%s.annotations sets %s, which this chart computes; use the dedicated key" $.where $k) -}}
{{- end -}}
{{- $_ := set $annotations $k $v -}}
{{- end -}}
name: {{ .name }}
{{- with .labels }}
labels:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with $annotations }}
annotations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}
