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

{{/*
networkPolicy.tailnetIngress: the ingress rules of one row's tailnets (a YAML
list of rules with a description), appended after networkPolicy.ingress. One
rule per tailnet the row lists, in name order: the router pods of the
namespaces whose `routerLabel` names that tailnet, plus the tailnet's own
`peers`, on its `ports` (or every port, with `allPorts`). Then, with `shared`
on and for a row that lists any tailnet, the compatibility rule: `shared.peers`
on every port, which no tailnet owns.
Called as: include "guardrails-projects.tailnetIngress" (dict "root" $ "row" $p)
*/}}
{{- define "guardrails-projects.tailnetIngress" -}}
{{- $t := .root.Values.networkPolicy.tailnetIngress | default dict -}}
{{- $row := .row -}}
{{- $out := list -}}
{{- if and $row.tailnets (not $t.enabled) -}}
{{- fail (printf "projects %s: lists tailnets, but networkPolicy.tailnetIngress is not enabled" $row.name) -}}
{{- end -}}
{{- if $t.enabled -}}
{{- if not $t.routerLabel }}{{ fail "networkPolicy.tailnetIngress.routerLabel: the namespace label naming a router's tailnet is required" }}{{ end -}}
{{- range $name := sortAlpha ($row.tailnets | default list) -}}
{{- if not (hasKey ($t.tailnets | default dict) $name) }}{{ fail (printf "projects %s: tailnet %s is not under networkPolicy.tailnetIngress.tailnets" $row.name $name) }}{{ end -}}
{{- $tn := index $t.tailnets $name | default dict -}}
{{- if eq (not $tn.allPorts) (not $tn.ports) -}}
{{- fail (printf "networkPolicy.tailnetIngress.tailnets.%s: declare its ports, or set allPorts: true for every port (exactly one of the two)" $name) -}}
{{- end -}}
{{- $router := dict "namespaceSelector" (dict "matchLabels" (dict $t.routerLabel $name)) -}}
{{- with $t.routerPodSelector }}{{ $_ := set $router "podSelector" . }}{{ end -}}
{{- $rule := dict "description" (printf "tailnet %s" $name) "from" (concat (list $router) ($tn.peers | default list)) -}}
{{- with $tn.ports }}{{ $_ := set $rule "ports" . }}{{ end -}}
{{- $out = append $out $rule -}}
{{- end -}}
{{- $s := $t.shared | default dict -}}
{{- if and $s.enabled $row.tailnets -}}
{{- if not $s.reason }}{{ fail "networkPolicy.tailnetIngress.shared: a reason is required (the peers cross tailnets)" }}{{ end -}}
{{- if not $s.peers }}{{ fail "networkPolicy.tailnetIngress.shared.peers: at least one peer is required when enabled" }}{{ end -}}
{{- $out = append $out (dict "description" (printf "tailnets, shared (compatibility): %s" $s.reason) "from" $s.peers) -}}
{{- end -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}
