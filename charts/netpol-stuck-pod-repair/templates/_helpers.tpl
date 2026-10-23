{{/* TEMPORARY WORKAROUND for aws/aws-network-policy-agent#676 (fix #705):
uninstall once the node image ships an agent release with #705. */}}

{{- define "netpol-stuck-pod-repair.name" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "netpol-stuck-pod-repair.serviceAccountName" -}}
{{- .Values.serviceAccount.name | default (include "netpol-stuck-pod-repair.name" .) -}}
{{- end -}}

{{- define "netpol-stuck-pod-repair.selector" -}}
app.kubernetes.io/name: netpol-stuck-pod-repair
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "netpol-stuck-pod-repair.labels" -}}
{{ include "netpol-stuck-pod-repair.selector" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end -}}

{{/* The checks every render runs before anything is emitted. The schema
holds the shapes; these are the rules across fields. */}}
{{- define "netpol-stuck-pod-repair.check" -}}
{{- $v := .Values -}}
{{- if not $v.timeoutPatterns }}{{ fail "timeoutPatterns needs at least one entry: a candidate must show timeout evidence" }}{{ end -}}
{{- if and $v.networkPolicy.enabled (not $v.networkPolicy.apiServer.peers) }}{{ fail "networkPolicy.enabled needs networkPolicy.apiServer.peers: without egress to the API the controller sees nothing" }}{{ end -}}
{{- range $v.namespaces }}{{ if has . $v.excludeNamespaces }}{{ fail (printf "namespace %q is both in namespaces and excludeNamespaces" .) }}{{ end }}{{ end -}}
{{- end -}}
