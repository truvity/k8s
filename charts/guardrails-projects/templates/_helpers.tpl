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

{{/*
networkPolicy.eksEgress: the egress every workload of an EKS cluster needs, as
rules appended after networkPolicy.egress (a YAML list): the API server over
the node and service ranges, HTTPS to the internet but the instance metadata
service, the EKS Pod Identity Agent, and DNS to dnsPeers, the service range
and the node ranges.
*/}}
{{- define "guardrails-projects.eksEgress" -}}
{{- $e := .Values.networkPolicy.eksEgress | default dict -}}
{{- $out := list -}}
{{- if $e.enabled -}}
{{- if not $e.nodeCIDRs }}{{ fail "networkPolicy.eksEgress.nodeCIDRs: the node range is required (the API endpoint lives in it)" }}{{ end -}}
{{- if not $e.serviceCIDR }}{{ fail "networkPolicy.eksEgress.serviceCIDR is required" }}{{ end -}}
{{- $nodes := list -}}
{{- range $c := $e.nodeCIDRs }}{{ $nodes = append $nodes (dict "ipBlock" (dict "cidr" $c)) }}{{ end -}}
{{- $service := dict "ipBlock" (dict "cidr" $e.serviceCIDR) -}}
{{- $out = list
      (dict "description" "kube-apiserver" "to" (append $nodes $service) "ports" (list (dict "port" 443 "protocol" "TCP")))
      (dict "description" "internet HTTPS" "to" (list (dict "ipBlock" (dict "cidr" "0.0.0.0/0" "except" (list "169.254.169.254/32")))) "ports" (list (dict "port" 443 "protocol" "TCP")))
      (dict "description" "EKS Pod Identity Agent" "to" (list (dict "ipBlock" (dict "cidr" "169.254.170.23/32"))) "ports" (list (dict "port" 80 "protocol" "TCP")))
      (dict "description" "cluster DNS" "to" (concat ($e.dnsPeers | default list) (list $service) $nodes) "ports" (list (dict "port" 53 "protocol" "UDP") (dict "port" 53 "protocol" "TCP"))) -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}
