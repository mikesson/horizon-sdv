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
Single git clone of the pipeline repo into pipeline-workspace PVC (sharedPipelineWorkspace).
New PVC per workflow with Delete StorageClass — no separate wipe step. Order: 5/10.
Argo rejects git artifact path when it equals a volumeMount mountPath; clone to /tmp/pipeline-repo-staging then cp onto the PVC at pipelineMonorepoPath (/horizon).
*/ -}}

{{- define "aaos-builder.template.fetch-pipeline" -}}
{{- if eq (include "aaos-builder.useSharedPipelineWorkspaceVolume" .) "true" }}
- name: fetch-pipeline
  podSpecPatch: |
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
  inputs:
    artifacts:
      - name: pipeline-repo
        # Must not match pipeline-workspace mountPath (/horizon): Argo InvalidArgument "already mounted".
        path: /tmp/pipeline-repo-staging
        git:
          repo: '{{ "{{" }}workflow.parameters.pipelineRepoUrl{{ "}}" }}'
          revision: '{{ "{{" }}workflow.parameters.pipelineRepoRevision{{ "}}" }}'
{{- include "aaos-builder.gitArtifactCredsContent" . | nindent 10 }}
  container:
    image: {{ include "aaos-builder.builderImage" . | quote }}
    workingDir: /tmp
    command: [bash, -euo, pipefail, -c]
    args:
      - |
        echo "Copying pipeline repo from Argo git staging to shared PVC (sharedPipelineWorkspace)."
        dest={{ include "aaos-builder.pipelineMonorepoPath" . | quote }}
        # Image default USER is builder (uid 1000); fresh PVC is root-owned — run this template as root (below).
        cp -a /tmp/pipeline-repo-staging/. "${dest}/"
        chown -R 1000:1000 "${dest}"
        test -d "${dest}/.git" || test -f "${dest}/README.md" || test -d "${dest}/workloads"
    resources:
{{- include "aaos-builder.workflowStepResources" (dict "step" "fetchPipeline" "root" .) | nindent 6 }}
    securityContext:
      # builder image USER is uid 1000; without root here cp/chown cannot populate root-owned PVC mount.
      runAsUser: 0
      runAsGroup: 0
      privileged: true
    volumeMounts:
      # PVC holds the tree for init/build/storage; git artifact uses a different path (see inputs.artifacts).
      - name: pipeline-workspace
        mountPath: {{ include "aaos-builder.pipelineMonorepoPath" . | quote }}
{{- end }}
{{- end }}
