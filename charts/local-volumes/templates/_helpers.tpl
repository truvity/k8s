{{/*
Is an object protected? The entry's own `protect`, when set, else the chart's.

Called as: include "local-volumes.protected" (dict "root" $ "entry" $entry)
Returns "true" or "" (so `if` reads it).
*/}}
{{- define "local-volumes.protected" -}}
{{- $p := .root.Values.protect -}}
{{- if and (hasKey .entry "protect") (not (kindIs "invalid" .entry.protect)) -}}{{- $p = .entry.protect -}}{{- end -}}
{{- if $p -}}true{{- end -}}
{{- end -}}

{{/*
The metadata block of one object: name, labels and
annotations (sync-wave and protection computed, the caller's literal ones
added; a literal one may not shadow a computed key).

Called as: include "local-volumes.metadata" (dict "root" $ "name" $n "entry" $e "labels" $l "where" "loadBalancerIPPools.lan")
*/}}
{{- define "local-volumes.metadata" -}}
{{- $root := .root -}}
{{- $annotations := dict -}}
{{- if not (kindIs "invalid" .entry.syncWave) -}}
{{- $_ := set $annotations "argocd.argoproj.io/sync-wave" (toString .entry.syncWave) -}}
{{- end -}}
{{- if include "local-volumes.protected" (dict "root" $root "entry" .entry) -}}
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
