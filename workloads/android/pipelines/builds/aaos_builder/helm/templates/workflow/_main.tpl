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
Main DAG task graph for aaos-builder.
Order: 1/10.
Dependencies: orchestrates downstream templates; no inputs.
*/ -}}

{{- define "aaos-builder.template.main" -}}
{{- $remotePipeline := not (or .Values.localRepoHostPath .Values.localRepoPvcName) -}}
{{- $sharedWs := eq (include "aaos-builder.useSharedPipelineWorkspaceVolume" .) "true" -}}
{{- $auth := include "aaos-builder.scmAuthMethod" . | trim -}}
{{- $umbrellaCreds := and $remotePipeline (or (eq $auth "app") (eq $auth "userpass")) -}}
- name: main
  dag:
    tasks:
{{- if $umbrellaCreds }}
      - name: prepare-pipeline-git-creds
        templateRef:
          name: prepare-pipeline-git-creds
          template: prepare-pipeline-git-creds
          clusterScope: true
        arguments:
          parameters:
            - name: scmAuthMethod
              value: '{{ "{{" }}workflow.parameters.scmAuthMethod{{ "}}" }}'
            - name: pipelineStaticGitSecretName
              value: '{{ "{{" }}workflow.parameters.pipelineStaticGitSecretName{{ "}}" }}'
            - name: horizonSubmittedFrom
              value: '{{ "{{" }}workflow.parameters.horizonSubmittedFrom{{ "}}" }}'
{{- end }}
      - name: compute-vars
        template: compute-vars
{{- if $umbrellaCreds }}
        depends: prepare-pipeline-git-creds.Succeeded
{{- end }}
      - name: check-aaos-image
        template: check-aaos-image
      - name: build-aaos-image
        # Build the AAOS builder image only when missing or forced.
        # Uses aaos-builder-runtime-image WorkflowTemplate defaults.
        templateRef:
          name: {{ .Values.aaosBuilderImageWorkflowTemplateName | quote }}
          template: build-defaults
        depends: check-aaos-image.Succeeded
        when: >-
          '{{ "{{" }}tasks.check-aaos-image.outputs.parameters.shouldBuild{{ "}}" }}' == "true"
{{- /* fetch-pipeline + pipeline-workspace PVC only when sharedPipelineWorkspace and remote git (not local repo PVC/hostPath). Fresh disk per run via Delete StorageClass. */ -}}
{{- if $sharedWs }}
      - name: fetch-pipeline
        template: fetch-pipeline
{{- if $umbrellaCreds }}
        depends: >-
          prepare-pipeline-git-creds.Succeeded &&
          compute-vars.Succeeded &&
          check-aaos-image.Succeeded &&
          (build-aaos-image.Succeeded || build-aaos-image.Skipped)
{{- else }}
        depends: >-
          compute-vars.Succeeded &&
          check-aaos-image.Succeeded &&
          (build-aaos-image.Succeeded || build-aaos-image.Skipped)
{{- end }}
{{- end }}
      - name: clean
        template: clean
        depends: >-
{{- if $sharedWs }}
          fetch-pipeline.Succeeded &&
{{- end }}
          compute-vars.Succeeded &&
          check-aaos-image.Succeeded &&
          (build-aaos-image.Succeeded || build-aaos-image.Skipped)
        when: >-
          '{{ "{{" }}workflow.parameters.lunchTarget{{ "}}" }}' != "" &&
          '{{ "{{" }}workflow.parameters.cleanBuild{{ "}}" }}' != "NO_CLEAN"
      - name: init
        template: init
        depends: >-
{{- if $sharedWs }}
          fetch-pipeline.Succeeded &&
{{- end }}
          compute-vars.Succeeded &&
          (clean.Succeeded || clean.Skipped) &&
          check-aaos-image.Succeeded &&
          (build-aaos-image.Succeeded || build-aaos-image.Skipped)
        arguments:
          parameters:
            - name: sdkAndroidVersion
              value: '{{ "{{" }}tasks.compute-vars.outputs.parameters.sdkAndroidVersion{{ "}}" }}'
      - name: build
        template: build
        depends: init.Succeeded
        arguments:
          parameters:
            - name: sdkAndroidVersion
              value: '{{ "{{" }}tasks.compute-vars.outputs.parameters.sdkAndroidVersion{{ "}}" }}'
{{- if and (eq $auth "app") $remotePipeline }}
      - name: refresh-pipeline-git-creds-after-build
        # Re-mint installation token (~1h TTL) into the per-workflow pipeline-git-creds Secret before storage / ai-review git inits.
        templateRef:
          name: prepare-pipeline-git-creds
          template: prepare-pipeline-git-creds
          clusterScope: true
        arguments:
          parameters:
            - name: scmAuthMethod
              value: '{{ "{{" }}workflow.parameters.scmAuthMethod{{ "}}" }}'
            - name: pipelineStaticGitSecretName
              value: '{{ "{{" }}workflow.parameters.pipelineStaticGitSecretName{{ "}}" }}'
            - name: horizonSubmittedFrom
              value: '{{ "{{" }}workflow.parameters.horizonSubmittedFrom{{ "}}" }}'
        depends: >-
          build.Succeeded ||
          build.Failed ||
          build.Errored
{{- end }}
      - name: ai-review
        templateRef:
          name: {{ .Values.aiReviewWorkflowTemplateName | quote }}
          template: ai-review
        # Run only when a build task fails and Gemini AI assistant is enabled.
        # Remote git: ai-review uses its own Argo git artifact (clone to /workspace), not the WT shared pipeline-workspace PVC,
        # avoiding CWT vs WT volume mount merge issues. Local repo: mount only, no git artifact.
        # App+remote: wait for post-build token refresh before git artifact init (same Secret name).
        depends: >-
{{- if and (eq $auth "app") $remotePipeline }}
          refresh-pipeline-git-creds-after-build.Succeeded &&
{{- end }}
          (build.Failed ||
          build.Errored)
        when: >-
          '{{ "{{" }}workflow.parameters.enableGeminiAiAssistant{{ "}}" }}' == "true"
        arguments:
{{- if eq (include "aaos-builder.useAiReviewGitArtifact" .) "true" }}
          # Per-step git clone into /workspace (separate from fetch-pipeline / pipeline-workspace used by clean|init|build|storage).
          artifacts:
            - name: pipeline-repo
              path: /workspace
              git:
                repo: '{{ "{{" }}workflow.parameters.pipelineRepoUrl{{ "}}" }}'
                revision: '{{ "{{" }}workflow.parameters.pipelineRepoRevision{{ "}}" }}'
{{- include "aaos-builder.gitArtifactCredsContent" . | nindent 16 }}
{{- end }}
          parameters:
            - name: horizonSubmittedFrom
              value: '{{ "{{" }}workflow.parameters.horizonSubmittedFrom{{ "}}" }}'
            - name: sdkAndroidVersion
              value: '{{ "{{" }}tasks.compute-vars.outputs.parameters.sdkAndroidVersion{{ "}}" }}'
            - name: pipelineRepoRoot
{{- if or .Values.localRepoHostPath .Values.localRepoPvcName }}
              value: {{ .Values.localRepoMountPath | quote }}
{{- else }}
              value: "/workspace"
{{- end }}
            - name: cloudProject
              value: {{ .Values.spec.cloudProject | quote }}
            - name: cloudRegion
              value: {{ .Values.spec.cloudRegion | quote }}
            - name: geminiPromptFile
{{- if and (or .Values.localRepoHostPath .Values.localRepoPvcName) (hasPrefix "/workspace" .Values.spec.geminiPromptFile) }}
              value: {{ printf "%s%s" .Values.localRepoMountPath (trimPrefix "/workspace" .Values.spec.geminiPromptFile) | quote }}
{{- else }}
              value: {{ .Values.spec.geminiPromptFile | quote }}
{{- end }}
            - name: geminiPromptFile2
{{- if and (or .Values.localRepoHostPath .Values.localRepoPvcName) (hasPrefix "/workspace" (.Values.spec.geminiPromptFile2 | default "")) }}
              value: {{ printf "%s%s" .Values.localRepoMountPath (trimPrefix "/workspace" .Values.spec.geminiPromptFile2) | quote }}
{{- else }}
              value: {{ .Values.spec.geminiPromptFile2 | default "" | quote }}
{{- end }}
            - name: geminiPromptFile3
{{- if and (or .Values.localRepoHostPath .Values.localRepoPvcName) (hasPrefix "/workspace" (.Values.spec.geminiPromptFile3 | default "")) }}
              value: {{ printf "%s%s" .Values.localRepoMountPath (trimPrefix "/workspace" .Values.spec.geminiPromptFile3) | quote }}
{{- else }}
              value: {{ .Values.spec.geminiPromptFile3 | default "" | quote }}
{{- end }}
            - name: geminiLocationGlobal
              value: {{ .Values.spec.geminiLocationGlobal | quote }}
            - name: geminiPreviewFeatures
              value: {{ .Values.spec.geminiPreviewFeatures | quote }}
            - name: geminiCommandLine
              value: {{ include "aaos-builder.geminiCommandLineResolved" . | quote }}
            - name: geminiAiExecutionTimeoutHours
              value: {{ .Values.spec.geminiAiExecutionTimeoutHours | quote }}
            - name: geminiSkillsYaml
{{- $geminiSkillsYaml := required "spec.geminiSkillsYaml is required (filesystem path to skills.yaml for Gemini)" .Values.spec.geminiSkillsYaml }}
{{- if and (or .Values.localRepoHostPath .Values.localRepoPvcName) (hasPrefix "/workspace" $geminiSkillsYaml) }}
              value: {{ printf "%s%s" .Values.localRepoMountPath (trimPrefix "/workspace" $geminiSkillsYaml) | quote }}
{{- else }}
              value: {{ $geminiSkillsYaml | quote }}
{{- end }}
            - name: image
              value: {{ include "aaos-builder.builderImage" . | quote }}
      - name: storage
        template: storage
        # Always run after ai-review completes. runAvdSdk is true only when build succeeded (skip aaos_avd_sdk on build failure).
        depends: >-
{{- if and (eq $auth "app") $remotePipeline }}
          refresh-pipeline-git-creds-after-build.Succeeded &&
{{- end }}
          (build.Succeeded ||
          ai-review.Succeeded ||
          ai-review.Failed ||
          ai-review.Errored ||
          ai-review.Skipped)
        arguments:
          parameters:
            - name: sdkAndroidVersion
              value: '{{ "{{" }}tasks.compute-vars.outputs.parameters.sdkAndroidVersion{{ "}}" }}'
            - name: runAvdSdk
              value: {{ `{{= tasks['build'].status == 'Succeeded' ? 'true' : 'false' }}` | quote }}
            - name: buildStageFailed
              value: {{ `{{= tasks['build'].status == 'Succeeded' ? 'false' : 'true' }}` | quote }}
      - name: fail-if-build-failed
        # Run after storage when build failed so the workflow result is Failed, not Succeeded.
        template: fail-workflow
        depends: storage.Succeeded
        when: >-
          '{{ "{{" }}tasks.build.status{{ "}}" }}' == "Failed" ||
          '{{ "{{" }}tasks.build.status{{ "}}" }}' == "Error"
{{- end }}
