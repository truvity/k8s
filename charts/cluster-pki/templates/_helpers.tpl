{{/* Annotations of every object: sync wave and protection. */}}
{{- define "cluster-pki.annotations" -}}
{{- $a := dict -}}
{{- if not (kindIs "invalid" .Values.syncWave) -}}{{- $_ := set $a "argocd.argoproj.io/sync-wave" (toString .Values.syncWave) -}}{{- end -}}
{{- if .Values.protect -}}
{{- $_ := set $a "argocd.argoproj.io/sync-options" "Prune=false,Delete=false" -}}
{{- if .Values.helmKeep -}}{{- $_ := set $a "helm.sh/resource-policy" "keep" -}}{{- end -}}
{{- end -}}
{{- with $a -}}
annotations:
  {{- toYaml . | nindent 2 }}
{{- end -}}
{{- end -}}

{{/* A CA Certificate's spec body. Called with (dict "c" $cfg "issuer" $name "kind" "ClusterIssuer"). */}}
{{- define "cluster-pki.caSpec" -}}
isCA: true
commonName: {{ .c.commonName | quote }}
{{- with .c.organization }}
subject:
  organizations:
    {{- toYaml . | nindent 4 }}
{{- end }}
secretName: {{ .c.secretName }}
duration: {{ .c.duration }}
renewBefore: {{ .c.renewBefore }}
privateKey:
  algorithm: {{ .c.privateKey.algorithm }}
  {{- if ne .c.privateKey.algorithm "Ed25519" }}
  size: {{ .c.privateKey.size }}
  {{- end }}
  # A CA renewed with a new key is a new CA: every certificate it signed
  # stops chaining. Keep the key across renewals.
  rotationPolicy: Never
{{- with .c.nameConstraints }}
nameConstraints:
  {{- toYaml . | nindent 2 }}
{{- end }}
issuerRef:
  name: {{ .issuer }}
  kind: ClusterIssuer
  group: cert-manager.io
{{- end -}}

{{- define "cluster-pki.check" -}}
{{- $v := .Values -}}
{{- if eq $v.root.mode "self-signed" -}}
{{- if not $v.root.commonName }}{{ fail "root.commonName is required in self-signed mode" }}{{ end -}}
{{- if $v.root.existingSecret }}{{ fail "root.existingSecret is set but root.mode is self-signed" }}{{ end -}}
{{- else -}}
{{- if not $v.root.existingSecret }}{{ fail "root.mode existing-secret needs root.existingSecret" }}{{ end -}}
{{- end -}}
{{- if and $v.intermediate.enabled (not $v.intermediate.commonName) }}{{ fail "intermediate.commonName is required when the intermediate is enabled" }}{{ end -}}
{{- range $k := list "root" "intermediate" -}}
{{- $c := index $v $k -}}
{{- $alg := $c.privateKey.algorithm -}}
{{- $size := int $c.privateKey.size -}}
{{- if and (eq $alg "ECDSA") (not (has $size (list 256 384 521))) }}{{ fail (printf "%s.privateKey.size %d is not an ECDSA curve size (256, 384, 521)" $k $size) }}{{ end -}}
{{- if and (eq $alg "RSA") (lt $size 3072) }}{{ fail (printf "%s.privateKey.size %d is too small for a CA (RSA 3072 or more)" $k $size) }}{{ end -}}
{{- end -}}
{{- if and $v.intermediate.enabled (eq $v.root.secretName $v.intermediate.secretName) }}{{ fail "root.secretName and intermediate.secretName must differ" }}{{ end -}}
{{- end -}}
