{{- /*
Copyright (c) 2026 Accenture, All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

        http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Description:
Helm template helpers for aaos-builder-runtime-image (workflow namespace and service account).
*/ -}}

{{/*
Workflow namespace: explicit .Values.namespace, or namespacePrefix + "workflows" (matches GitOps namespacePrefix + workflows).
*/}}
{{- define "aaos-builder-runtime-image.workflowNamespace" -}}
{{- coalesce .Values.namespace (printf "%s%s" (.Values.namespacePrefix | default "") "workflows") -}}
{{- end -}}

{{/*
Workflow pod service account: when useElevatedWorkflowIam is true, workflow-executor-elevated (Terraform env/main.tf argo_workflows_elevated → gke-argo-workflows-elevated-sa). Otherwise spec.serviceAccountName (default workflow-executor, argo_workflows → gke-argo-workflows-sa).
*/}}
{{- define "aaos-builder-runtime-image.workflowServiceAccountName" -}}
{{- if .Values.spec.useElevatedWorkflowIam -}}
workflow-executor-elevated
{{- else -}}
{{- .Values.spec.serviceAccountName | default "workflow-executor" -}}
{{- end -}}
{{- end -}}

{{/*
SCM auth: default values and GitOps set .Values.git.authMethod; .Values.scm.authMethod is accepted for compatibility.
*/}}
{{- define "aaos-builder-runtime-image.scmAuthMethod" -}}
{{- $scm := .Values.scm | default dict -}}
{{- coalesce .Values.git.authMethod $scm.authMethod "" -}}
{{- end -}}

{{/*
Argo git artifact HTTPS credentials (same semantics as aaos-builder gitArtifactCredsContent).
*/}}
{{- define "aaos-builder-runtime-image.gitArtifactCredsContent" -}}
{{- $auth := include "aaos-builder-runtime-image.scmAuthMethod" . | trim -}}
{{- if and (or (eq $auth "app") (eq $auth "userpass")) (not (or .Values.localRepoHostPath .Values.localRepoPvcName)) }}
usernameSecret:
  name: "{{ "{{" }}workflow.uid{{ "}}" }}-pipeline-git-creds"
  key: username
passwordSecret:
  name: "{{ "{{" }}workflow.uid{{ "}}" }}-pipeline-git-creds"
  key: password
{{- else if .Values.spec.pipelineRepoSecret }}
usernameSecret:
  name: {{ .Values.spec.pipelineRepoSecret | quote }}
  key: username
passwordSecret:
  name: {{ .Values.spec.pipelineRepoSecret | quote }}
  key: password
{{- end }}
{{- end -}}
