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
Helm helpers for cvd-launcher (CVD Launcher Argo workflows).
*/ -}}

{{- define "cvd-launcher.workflowNamespace" -}}
{{- coalesce .Values.namespace (printf "%s%s" (.Values.namespacePrefix | default "") "workflows") -}}
{{- end -}}

{{- define "cvd-launcher.workflowServiceAccountName" -}}
{{- if .Values.spec.useElevatedWorkflowIam -}}
workflow-executor-elevated
{{- else -}}
{{- .Values.spec.serviceAccountName | default "workflow-executor" -}}
{{- end -}}
{{- end -}}

{{- define "cvd-launcher.scmAuthMethod" -}}
{{- $scm := .Values.scm | default dict -}}
{{- coalesce .Values.git.authMethod $scm.authMethod "" -}}
{{- end -}}

{{- define "cvd-launcher.cloudEnvFrom" -}}
{{- if .Values.cloudEnvConfigMapName }}
envFrom:
  - configMapRef:
      name: {{ .Values.cloudEnvConfigMapName | quote }}
{{- end }}
{{- end -}}

{{- define "cvd-launcher.builderImage" -}}
{{- printf "%s-docker.pkg.dev/%s/%s:%s" .Values.spec.cloudRegion .Values.spec.cloudProject .Values.spec.dockerArtifactPathName .Values.spec.builderImageTag -}}
{{- end -}}

{{/*
Gemini CLI for gemini-review: when spec.geminiModel is non-empty, same as aaos-builder —
gemini --model <name> --yolo --output-format json; otherwise spec.geminiCommandLine.
*/}}
{{- define "cvd-launcher.geminiCommandLineResolved" -}}
{{- if .Values.spec.geminiModel -}}
gemini --model {{ .Values.spec.geminiModel }} --yolo --output-format json
{{- else -}}
{{- .Values.spec.geminiCommandLine | default "gemini --yolo --output-format json" -}}
{{- end -}}
{{- end -}}

{{- define "cvd-launcher.podAffinitySameWorkflow" -}}
affinity:
  podAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      - labelSelector:
          matchExpressions:
            - key: workflows.argoproj.io/workflow
              operator: In
              values:
                - {{ "{{workflow.name}}" }}
        topologyKey: kubernetes.io/hostname
{{- end -}}

{{- define "cvd-launcher.useSharedPipelineWorkspaceVolume" -}}
{{- if .Values.sharedPipelineWorkspace }}true{{- end -}}
{{- end -}}

{{- define "cvd-launcher.usePerPodGitArtifact" -}}
{{- if not .Values.sharedPipelineWorkspace }}true{{- end -}}
{{- end -}}

{{- define "cvd-launcher.useGeminiReviewGitArtifact" -}}
true
{{- end -}}

{{- define "cvd-launcher.pipelineMonorepoPath" -}}
{{- if eq (include "cvd-launcher.useSharedPipelineWorkspaceVolume" .) "true" -}}/horizon{{- else -}}/workspace{{- end -}}
{{- end -}}

{{- define "cvd-launcher.pipelineWorkspaceVolumeMounts" -}}
{{- if eq (include "cvd-launcher.useSharedPipelineWorkspaceVolume" .) "true" }}
          - name: pipeline-workspace
            mountPath: {{ include "cvd-launcher.pipelineMonorepoPath" . | quote }}
{{- end }}
{{- end -}}

{{- define "cvd-launcher.pipelineWorkspacePodSpecPatch" -}}
{{- if eq (include "cvd-launcher.useSharedPipelineWorkspaceVolume" .) "true" }}
      podSpecPatch: |
{{ include "cvd-launcher.podAffinitySameWorkflow" . | nindent 8 }}
{{- end }}
{{- end -}}

{{- define "cvd-launcher.pipelineRepoGitArtifactItem" -}}
{{- $root := . }}
- name: pipeline-repo
  path: /workspace
  git:
    repo: {{ $root.Values.spec.pipelineRepoUrl | quote }}
    revision: {{ $root.Values.spec.pipelineRepoRevision | quote }}
{{- include "cvd-launcher.gitArtifactCredsContent" $root | nindent 4 }}
{{- end -}}

{{- define "cvd-launcher.pipelineRepoGitArtifactInputs" -}}
{{- $root := . }}
{{- if eq (include "cvd-launcher.usePerPodGitArtifact" $root) "true" }}
        artifacts:
{{ include "cvd-launcher.pipelineRepoGitArtifactItem" $root | nindent 10 }}
{{- end }}
{{- end -}}

{{- define "cvd-launcher.gitArtifactCredsContent" -}}
{{- $auth := include "cvd-launcher.scmAuthMethod" . | trim -}}
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
