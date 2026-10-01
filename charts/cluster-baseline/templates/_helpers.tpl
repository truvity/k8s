{{/*
Resolve the label set of one namespace.

Called as: include "cluster-baseline.resolve" (dict "root" $ "name" $name "ns" $entry)
Returns YAML: a map from mode to {level, version}, only for the modes that
render a label. Includes `override: true` when the namespace departs from the
chart defaults for any mode (and therefore needs a reason).
*/}}
{{- define "cluster-baseline.resolve" -}}
{{- $psa := .root.Values.podSecurity -}}
{{- $ns := .ns | default dict -}}
{{- $nsModes := $ns.modes | default dict -}}
{{- $out := dict -}}
{{- $override := false -}}
{{- range $mode := list "warn" "audit" "enforce" -}}
  {{- $sw := index $psa.modes $mode -}}
  {{- $nm := index $nsModes $mode | default dict -}}
  {{- $default := $sw.level | default $psa.level -}}
  {{- $level := "" -}}
  {{- $version := "" -}}
  {{- if $nm.level -}}
    {{- $level = $nm.level -}}
  {{- else if $sw.enabled -}}
    {{- $level = $ns.level | default $default -}}
  {{- end -}}
  {{- if $level -}}
    {{- $version = $nm.version | default $ns.version | default $sw.version | default $psa.version -}}
    {{- if and $sw.enabled (ne $level $default) -}}{{- $override = true -}}{{- end -}}
    {{- if not $sw.enabled -}}{{- $override = true -}}{{- end -}}
    {{- $_ := set $out $mode (dict "level" $level "version" $version) -}}
  {{- end -}}
{{- end -}}
{{- $_ := set $out "_override" $override -}}
{{- toYaml $out -}}
{{- end -}}

{{/*
Annotations of an extra object: the keep/prune pair when protected, and any
reason/owner annotations passed in as a dict.
Called as: include "cluster-baseline.annotations" (dict "protect" bool "extra" dict)
Renders nothing when there is nothing to say.
*/}}
{{- define "cluster-baseline.annotations" -}}
{{- $a := dict -}}
{{- if .protect -}}
{{- $_ := set $a "helm.sh/resource-policy" "keep" -}}
{{- $_ := set $a "argocd.argoproj.io/sync-options" "Prune=false,Delete=false" -}}
{{- end -}}
{{- range $k, $v := .extra -}}{{- $_ := set $a $k $v -}}{{- end -}}
{{- if $a -}}
annotations:
{{ toYaml $a | indent 2 }}
{{- end -}}
{{- end -}}

{{/*
A quantity as text. YAML numbers arrive as float64, which toString would write
as 1e+06 for a million; print whole numbers as integers.
*/}}
{{- define "cluster-baseline.quantity" -}}
{{- if kindIs "float64" . -}}{{- printf "%d" (int64 .) -}}{{- else -}}{{- . -}}{{- end -}}
{{- end -}}
