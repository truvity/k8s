{{/*
Is a policy protected? The entry's own `protect`, when set, else the chart's.

Called as: include "cluster-network-policies.protected" (dict "root" $ "entry" $entry)
Returns "true" or "" (so `if` reads it).
*/}}
{{- define "cluster-network-policies.protected" -}}
{{- $p := .root.Values.protect -}}
{{- if and (hasKey .entry "protect") (not (kindIs "invalid" .entry.protect)) -}}{{- $p = .entry.protect -}}{{- end -}}
{{- if $p -}}true{{- end -}}
{{- end -}}

{{/*
The enabled presets as policy entries (a YAML list), rendered after
`policies` and held to the same rules.

metricsServer: metrics-server scrapes every kubelet on 10250 and calls the API
server on 443, both over the node and service ranges, and resolves names. Only
egress is selected: the API server's calls into it stay unrestricted.
*/}}
{{- define "cluster-network-policies.presets" -}}
{{- $out := list -}}
{{- $ms := (.Values.presets | default dict).metricsServer | default dict -}}
{{- if $ms.enabled -}}
{{- if not $ms.nodeCIDRs }}{{ fail "presets.metricsServer.nodeCIDRs: the node range is required (the kubelets and the API endpoint live in it)" }}{{ end -}}
{{- if not $ms.serviceCIDR }}{{ fail "presets.metricsServer.serviceCIDR is required" }}{{ end -}}
{{- $nodes := list -}}
{{- range $c := $ms.nodeCIDRs }}{{ $nodes = append $nodes (dict "ipBlock" (dict "cidr" $c)) }}{{ end -}}
{{- $service := dict "ipBlock" (dict "cidr" $ms.serviceCIDR) -}}
{{- $p := dict
      "name" $ms.name
      "namespace" $ms.namespace
      "reason" $ms.reason
      "podSelector" $ms.podSelector
      "policyTypes" (list "Egress")
      "egress" (list
        (dict "to" (append $nodes $service) "ports" (list (dict "port" 10250 "protocol" "TCP") (dict "port" 443 "protocol" "TCP")))
        (dict "to" (concat ($ms.dnsPeers | default list) (list $service) $nodes) "ports" (list (dict "port" 53 "protocol" "UDP") (dict "port" 53 "protocol" "TCP")))) -}}
{{- if not (kindIs "invalid" $ms.syncWave) }}{{ $_ := set $p "syncWave" $ms.syncWave }}{{ end -}}
{{- if hasKey $ms "protect" }}{{ if not (kindIs "invalid" $ms.protect) }}{{ $_ := set $p "protect" $ms.protect }}{{ end }}{{ end -}}
{{- $out = append $out $p -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}
