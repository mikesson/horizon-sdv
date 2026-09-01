{{/*
Main environment only — sub-environments must not deploy cluster Config Connector, KCC buckets, or the operator.
*/}}
{{- define "storage-gcs-module.mainEnv" -}}
{{- not (default false .Values.config.isSubEnvironment) -}}
{{- end -}}
