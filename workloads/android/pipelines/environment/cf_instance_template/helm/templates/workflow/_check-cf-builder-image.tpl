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
Check if the aaos-builder image exists in Artifact Registry (aaos-builder check-aaos-image parity).
Uses tags list instead of images describe to avoid Container Analysis PERMISSION_DENIED on many WIs.
Any check failure sets shouldBuild=true so the runtime WorkflowTemplate build runs instead of failing.
*/ -}}

{{- define "cf-instance-template.template.check-cf-builder-image" -}}
- name: check-cf-builder-image
  script:
    image: "gcr.io/google.com/cloudsdktool/cloud-sdk:slim"
    command: [bash, -euo, pipefail, -c]
{{- include "cf-instance-template.cloudEnvFrom" . | nindent 4 }}
    env:
{{- if not .Values.cloudEnvConfigMapName }}
      - name: CLOUD_PROJECT
        value: {{ .Values.spec.cloudProject | quote }}
      - name: CLOUD_REGION
        value: {{ .Values.spec.cloudRegion | quote }}
{{- end }}
      - name: IMAGE_PATH
        value: {{ .Values.spec.dockerArtifactPathName | quote }}
      - name: IMAGE_TAG
        value: {{ .Values.spec.builderImageTag | quote }}
      - name: FORCE_IMAGE_BUILD
        value: '{{ "{{" }}workflow.parameters.forceImageBuild{{ "}}" }}'
    source: |
      IMAGE="${CLOUD_REGION}-docker.pkg.dev/${CLOUD_PROJECT}/${IMAGE_PATH}:${IMAGE_TAG}"
      if [ "${FORCE_IMAGE_BUILD}" = "true" ]; then
        echo "true" > /tmp/should_build
        exit 0
      fi
      REPO_IMAGE="${CLOUD_REGION}-docker.pkg.dev/${CLOUD_PROJECT}/${IMAGE_PATH}"
      set +e
      gcloud --project="${CLOUD_PROJECT}" artifacts docker tags list "${REPO_IMAGE}" --format='value(tag)' >/tmp/tags_list 2>/tmp/image_check.out
      STATUS=$?
      set -e
      if [ "${STATUS}" -ne 0 ]; then
        echo "true" > /tmp/should_build
        echo "Image check failed (will run aaos-builder-runtime-image): ${IMAGE}" >&2
        cat /tmp/image_check.out >&2 || true
        exit 0
      fi
      if grep -qxF "${IMAGE_TAG}" /tmp/tags_list; then
        echo "false" > /tmp/should_build
      else
        echo "true" > /tmp/should_build
        echo "Tag not in registry (will run aaos-builder-runtime-image): ${IMAGE}" >&2
      fi
  outputs:
    parameters:
      - name: shouldBuild
        valueFrom:
          path: /tmp/should_build
{{- end }}
