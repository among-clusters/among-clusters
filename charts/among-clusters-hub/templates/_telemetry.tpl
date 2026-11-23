{{- define "among-clusters-hub.sentryEnv" -}}
{{- if .Values.sentry.enabled }}
{{- if and .Values.sentry.dsn .Values.sentry.dsnSecretRef.name }}
{{- fail "set sentry.dsn or sentry.dsnSecretRef.name, not both" }}
{{- end }}
- name: SENTRY_DSN
{{- if .Values.sentry.dsn }}
  value: {{ .Values.sentry.dsn | quote }}
{{- else }}
  valueFrom:
    secretKeyRef:
      name: {{ required "sentry.dsnSecretRef.name is required when Sentry is enabled" .Values.sentry.dsnSecretRef.name | quote }}
      key: {{ required "sentry.dsnSecretRef.key is required when Sentry is enabled" .Values.sentry.dsnSecretRef.key | quote }}
{{- end }}
- name: SENTRY_RELEASE
  value: {{ required "sentry.release must identify the exact image source" .Values.sentry.release | quote }}
- name: SENTRY_ENVIRONMENT
  value: {{ .Values.sentry.environment | quote }}
{{- end }}
{{- end }}
