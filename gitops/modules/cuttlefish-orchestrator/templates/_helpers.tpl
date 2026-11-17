{{/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/}}

{{/*
Namespace prefix normalized to empty string when unset.
*/}}
{{- define "ccoParent.prefix" -}}
{{- .Values.config.namespacePrefix | default "" -}}
{{- end -}}

{{/*
Namespace where parent overview Deployment and Service live.
Matches ModuleCatalog overviewServiceNamespace (<prefix>module-manager).
*/}}
{{- define "ccoParent.overviewNamespace" -}}
{{- if .Values.overviewNamespace -}}
{{- .Values.overviewNamespace -}}
{{- else -}}
{{- printf "%smodule-manager" (include "ccoParent.prefix" .) -}}
{{- end -}}
{{- end -}}

{{/*
Target namespace for child orchestrator workloads and oauth2-proxy.
*/}}
{{- define "ccoParent.appNamespace" -}}
{{- printf "%scuttlefish-orchestrator" (include "ccoParent.prefix" .) -}}
{{- end -}}

{{/*
Argo CD namespace hosting child Application CRs.
*/}}
{{- define "ccoParent.argocdNamespace" -}}
{{- if and .Values.argocd .Values.argocd.namespace -}}
{{- .Values.argocd.namespace -}}
{{- else -}}
{{- printf "%sargocd" (include "ccoParent.prefix" .) -}}
{{- end -}}
{{- end -}}

{{/*
Argo CD project hosting child Application CRs.
*/}}
{{- define "ccoParent.argocdProject" -}}
{{- if and .Values.argocd .Values.argocd.project -}}
{{- .Values.argocd.project -}}
{{- else -}}
{{- printf "%shorizon-sdv" (include "ccoParent.prefix" .) -}}
{{- end -}}
{{- end -}}

{{/*
Deterministic cookie name isolated per environment prefix on the shared domain.
*/}}
{{- define "ccoParent.oauth2CookieName" -}}
{{- $pfx := include "ccoParent.prefix" . -}}
{{- printf "_%scco_oauth2_proxy" $pfx -}}
{{- end -}}
