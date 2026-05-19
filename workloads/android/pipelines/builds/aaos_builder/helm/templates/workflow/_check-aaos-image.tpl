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
Check if the aaos-builder image exists in Artifact Registry.
Order: 3/10.
Dependencies: sets shouldBuild for build-aaos-image gating.
*/ -}}

{{- define "aaos-builder.template.check-aaos-image" -}}
- name: check-aaos-image
  script:
    # Use cloud-sdk image so the check can run even if aaos-builder is missing.
    image: "gcr.io/google.com/cloudsdktool/cloud-sdk:slim"
    command: [bash, -euo, pipefail, -c]
{{- include "aaos-builder.cloudEnvFrom" . | nindent 4 }}
    env:
{{- if not .Values.cloudEnvConfigMapName }}
      - name: CLOUD_PROJECT
        value: {{ .Values.spec.cloudProject | quote }}
      - name: CLOUD_REGION
        value: {{ .Values.spec.cloudRegion | quote }}
{{- end }}
      - name: IMAGE_PATH
        value: {{ .Values.spec.imageBuild.artifactPathName | quote }}
      - name: IMAGE_TAG
        value: {{ .Values.spec.imageBuild.imageTag | quote }}
      - name: FORCE_IMAGE_BUILD
        value: '{{ "{{" }}workflow.parameters.forceImageBuild{{ "}}" }}'
    source: |
      IMAGE="${CLOUD_REGION}-docker.pkg.dev/${CLOUD_PROJECT}/${IMAGE_PATH}:${IMAGE_TAG}"
      if [ "${FORCE_IMAGE_BUILD}" = "true" ]; then
        echo "true" > /tmp/should_build
        exit 0
      fi
      set +e
      gcloud --project="${CLOUD_PROJECT}" artifacts docker images describe "${IMAGE}" >/tmp/image_check.out 2>&1
      STATUS=$?
      set -e
      if [ "${STATUS}" -eq 0 ]; then
        echo "false" > /tmp/should_build
      else
        echo "true" > /tmp/should_build
        echo "Image not found (or check failed): ${IMAGE}" >&2
        cat /tmp/image_check.out >&2 || true
      fi
  outputs:
    parameters:
      - name: shouldBuild
        valueFrom:
          path: /tmp/should_build
{{- end }}
