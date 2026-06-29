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
  Single git clone into pipeline-workspace PVC (sharedPipelineWorkspace).
  Git artifact stages at /tmp/pipeline-repo-staging (cannot equal PVC mount path).
*/ -}}

{{- define "cts-execution.template.fetch-pipeline" -}}
{{- $root := . }}
{{- if eq (include "cts-execution.useSharedPipelineWorkspaceVolume" $root) "true" }}
    - name: fetch-pipeline
{{- include "cts-execution.pipelineWorkspacePodSpecPatch" $root }}
      inputs:
        artifacts:
          - name: pipeline-repo
            path: /tmp/pipeline-repo-staging
            git:
              repo: {{ $root.Values.spec.pipelineRepoUrl | quote }}
              revision: {{ $root.Values.spec.pipelineRepoRevision | quote }}
{{- include "cts-execution.gitArtifactCredsContent" $root | nindent 14 }}
      container:
        image: {{ include "cts-execution.builderImage" $root | quote }}
        imagePullPolicy: Always
        workingDir: /tmp
        command: [bash, -euo, pipefail, -c]
        args:
          - |
            echo "[cts-execution] copying pipeline repo to shared PVC (sharedPipelineWorkspace)."
            dest={{ include "cts-execution.pipelineMonorepoPath" $root | quote }}
            cp -a /tmp/pipeline-repo-staging/. "${dest}/"
            chown -R 1000:1000 "${dest}"
            test -d "${dest}/workloads/android/pipelines/tests/cvd_argo_gce"
        resources:
          requests:
            cpu: "500m"
            memory: "1Gi"
          limits:
            cpu: "4000m"
            memory: "4Gi"
        securityContext:
          runAsUser: 0
          runAsGroup: 0
          privileged: true
        volumeMounts:
          - name: pipeline-workspace
            mountPath: {{ include "cts-execution.pipelineMonorepoPath" $root | quote }}
{{- end }}
{{- end }}
