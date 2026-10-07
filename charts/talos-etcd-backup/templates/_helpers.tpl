{{/* The checks every render runs before anything is emitted. */}}
{{- define "talos-etcd-backup.check" -}}
{{- $v := .Values -}}
{{- if not $v.image.tag }}{{ fail "image.tag is required: pin talos-backup" }}{{ end -}}
{{- if not $v.clusterName }}{{ fail "clusterName is required" }}{{ end -}}
{{- if not $v.s3.bucket }}{{ fail "s3.bucket is required" }}{{ end -}}
{{- if not $v.s3.region }}{{ fail "s3.region is required" }}{{ end -}}
{{- if $v.encryption.disabled -}}
{{- if not $v.encryption.disabledReason }}{{ fail "encryption.disabled needs encryption.disabledReason: a snapshot holds every Secret of the cluster" }}{{ end -}}
{{- if $v.encryption.ageRecipient }}{{ fail "encryption.ageRecipient is set but encryption.disabled is true" }}{{ end -}}
{{- else if not $v.encryption.ageRecipient -}}
{{- fail "encryption.ageRecipient is required (or encryption.disabled with a reason)" -}}
{{- end -}}
{{- $static := ne $v.credentials.secretName "" -}}
{{- $web := ne $v.credentials.webIdentity.roleArn "" -}}
{{- if eq $static $web }}{{ fail "credentials: set exactly one of secretName and webIdentity.roleArn" }}{{ end -}}
{{- if eq $v.serviceAccountName $v.talosServiceAccountName }}{{ fail "serviceAccountName and talosServiceAccountName must differ: the talos.dev one owns a Secret of its name" }}{{ end -}}
{{- end -}}
