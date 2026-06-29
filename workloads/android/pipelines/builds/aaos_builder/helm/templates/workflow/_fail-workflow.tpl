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
Fail the workflow so the end result is Failed when the build step failed.
Used after storage so gemini-review and storage still run, but the workflow
phase reflects the build failure.
*/ -}}

{{- define "aaos-builder.template.fail-workflow" -}}
- name: fail-workflow
  container:
    image: {{ include "aaos-builder.builderImage" . | quote }}
{{- include "aaos-builder.cloudEnvFrom" . | nindent 4 }}
    command: [bash, -c]
    args:
      - "echo 'Build failed; marking workflow as Failed.'; exit 1"
{{- end }}
