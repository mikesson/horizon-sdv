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
Helm helpers for cf-instance-template (Cuttlefish instance template workflows).
*/ -}}

{{- define "cf-instance-template.workflowNamespace" -}}
{{- coalesce .Values.namespace (printf "%s%s" (.Values.namespacePrefix | default "") "workflows") -}}
{{- end -}}

{{/* Namespace that holds the Cuttlefish publisher ServiceAccount and SSH key Secrets (KCC RoleBinding subject namespace; run-cf-template NAMESPACE). Empty => namespacePrefix + "jenkins". */}}
{{- define "cf-instance-template.publisherIdentityNamespace" -}}
{{- $ns := "" -}}
{{- if and .Values.kcc .Values.kcc.instanceTemplates .Values.kcc.instanceTemplates.publisherIdentity -}}
{{- $ns = .Values.kcc.instanceTemplates.publisherIdentity.namespace | default "" -}}
{{- end -}}
{{- if ne (trim $ns) "" -}}
{{- trim $ns -}}
{{- else -}}
{{- printf "%s%s" (.Values.namespacePrefix | default "") "jenkins" -}}
{{- end -}}
{{- end -}}

{{/* ServiceAccount name for cuttlefish-kcc-publisher RoleBinding (e.g. jenkins-sa for Jenkins agents). */}}
{{- define "cf-instance-template.publisherIdentityServiceAccountName" -}}
{{- if and .Values.kcc .Values.kcc.instanceTemplates .Values.kcc.instanceTemplates.publisherIdentity -}}
{{- .Values.kcc.instanceTemplates.publisherIdentity.serviceAccountName | default "jenkins-sa" -}}
{{- else -}}
jenkins-sa
{{- end -}}
{{- end -}}

{{- define "cf-instance-template.workflowServiceAccountName" -}}
{{- if .Values.spec.useElevatedWorkflowIam -}}
workflow-executor-elevated
{{- else -}}
{{- .Values.spec.serviceAccountName | default "workflow-executor" -}}
{{- end -}}
{{- end -}}

{{- define "cf-instance-template.scmAuthMethod" -}}
{{- $scm := .Values.scm | default dict -}}
{{- coalesce .Values.git.authMethod $scm.authMethod "" -}}
{{- end -}}

{{/*
Container env for PROJECT/REGION/ZONE: x86 uses horizon-workflow-cloud-env CLOUD_* (primary GKE);
ARM64 profile uses ARM64_* (Terraform config.arm64). Same ConfigMap as aaos-builder CLOUD_*.
*/}}
{{- define "cf-instance-template.placementCloudEnvVars" -}}
{{- $root := .root -}}
{{- $useArm64 := .useArm64 -}}
{{- if $root.Values.cloudEnvConfigMapName }}
          - name: PROJECT
            valueFrom:
              configMapKeyRef:
                name: {{ $root.Values.cloudEnvConfigMapName | quote }}
                key: CLOUD_PROJECT
          - name: REGION
            valueFrom:
              configMapKeyRef:
                name: {{ $root.Values.cloudEnvConfigMapName | quote }}
                key: {{ if $useArm64 }}ARM64_REGION{{ else }}CLOUD_REGION{{ end }}
          - name: ZONE
            valueFrom:
              configMapKeyRef:
                name: {{ $root.Values.cloudEnvConfigMapName | quote }}
                key: {{ if $useArm64 }}ARM64_ZONE{{ else }}CLOUD_ZONE{{ end }}
{{- else }}
{{- $ar := $root.Values.arm64 | default dict }}
          - name: PROJECT
            value: {{ $root.Values.spec.cloudProject | quote }}
          - name: REGION
            value: {{ if $useArm64 }}{{ $ar.region | quote }}{{ else }}{{ $root.Values.spec.cloudRegion | quote }}{{ end }}
          - name: ZONE
            value: {{ if $useArm64 }}{{ $ar.zone | quote }}{{ else }}{{ $root.Values.spec.cloudZone | quote }}{{ end }}
{{- end }}
{{- end -}}

{{- define "cf-instance-template.cloudEnvFrom" -}}
{{- if .Values.cloudEnvConfigMapName }}
envFrom:
  - configMapRef:
      name: {{ .Values.cloudEnvConfigMapName | quote }}
{{- end }}
{{- end -}}

{{- define "cf-instance-template.builderImage" -}}
{{- printf "%s-docker.pkg.dev/%s/%s:%s" .Values.spec.cloudRegion .Values.spec.cloudProject .Values.spec.dockerArtifactPathName .Values.spec.builderImageTag -}}
{{- end -}}

{{- define "cf-instance-template.gitArtifactCredsContent" -}}
{{- $auth := include "cf-instance-template.scmAuthMethod" . | trim -}}
{{- if or (eq $auth "app") (eq $auth "userpass") }}
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

{{- /*
WorkflowTemplate snippet: explicit Failed phase after run-cf-template errors (aaos-builder parity).
*/ -}}
{{/*
Sensor submit stub: Workflow parameters with empty value (required for GJSON #(name=="…").value updates).
Context: dict "root" (.Values chart root), "profile" (workflowProfiles entry).
*/}}
{{- define "cf-instance-template.sensorWebhookParameters" -}}
{{- $root := .root -}}
{{- $profile := .profile -}}
{{- range $root.Values.webhookWorkflowParameters }}
{{- if or (not .arm64Only) $profile.exposeNetworkSubnet }}
                    - name: {{ .name | quote }}
                      value: ""
{{- end }}
{{- end }}
{{- end -}}

{{/*
Sensor trigger mappings: body.<bodyKey> -> spec.arguments.parameters.#(name=="<name>").value
*/}}
{{- define "cf-instance-template.sensorWebhookParameterMappings" -}}
{{- $root := .root -}}
{{- $profile := .profile -}}
{{- range $root.Values.webhookWorkflowParameters }}
{{- if or (not .arm64Only) $profile.exposeNetworkSubnet }}
            - src:
                dependencyName: webhook-dep
                dataKey: body.{{ .bodyKey }}
              dest: spec.arguments.parameters.#(name=="{{ .name }}").value
{{- end }}
{{- end }}
{{- end -}}

{{- define "cf-instance-template.template.fail-workflow" -}}
- name: fail-workflow
  container:
    image: {{ include "cf-instance-template.builderImage" . | quote }}
{{- include "cf-instance-template.cloudEnvFrom" . | nindent 4 }}
    command: [bash, -c]
    args:
      - "echo 'Cuttlefish instance template run failed; marking workflow as Failed.'; exit 1"
{{- end }}
