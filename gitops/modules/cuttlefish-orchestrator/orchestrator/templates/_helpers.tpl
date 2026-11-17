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
Normalized namespacePrefix (empty string when unset).
*/}}
{{- define "cco.prefix" -}}
{{- .Values.config.namespacePrefix | default "" -}}
{{- end -}}

{{/*
Workload Identity GCP ServiceAccount email provisioned by Terraform (gke-<prefix>cco-sa).
*/}}
{{- define "cco.gcpServiceAccount" -}}
{{- printf "gke-%scco-sa@%s.iam.gserviceaccount.com" (include "cco.prefix" .) .Values.config.projectID -}}
{{- end -}}

{{/*
Compute Engine Host VM ServiceAccount email provisioned by Terraform (gke-<prefix>cco-host-vm-sa).
*/}}
{{- define "cco.hostVmServiceAccount" -}}
{{- printf "gke-%scco-host-vm-sa@%s.iam.gserviceaccount.com" (include "cco.prefix" .) .Values.config.projectID -}}
{{- end -}}

{{/*
GCS artifacts bucket provisioned by Terraform (<projectID>-<prefix>cco-artifacts).
*/}}
{{- define "cco.artifactsBucket" -}}
{{- printf "%s-%scco-artifacts" .Values.config.projectID (include "cco.prefix" .) -}}
{{- end -}}

{{/*
CCO_DEPLOYMENT value constrained to ^[a-z][a-z0-9-]{0,9}$ (max 10 chars, lowercase, starts with a letter).
Main env returns "cco"; short lowercase prefixes return "<prefix>-cco"; longer or non-alpha prefixes normalize to "<base>-<hash4>".
*/}}
{{- define "cco.deployment" -}}
{{- $pfx := include "cco.prefix" . -}}
{{- if not $pfx -}}
cco
{{- else -}}
{{- $norm := regexReplaceAll "-+" (regexReplaceAll "[^a-z0-9-]" (lower $pfx) "-") "-" | trimAll "-" -}}
{{- $candidate := printf "%s-cco" $norm -}}
{{- if and (eq $norm (trimSuffix "-" $pfx)) (le (len $candidate) 10) (regexMatch "^[a-z][a-z0-9-]{0,9}$" $candidate) -}}
{{- $candidate -}}
{{- else -}}
{{- $hash := sha256sum $pfx | trunc 4 -}}
{{- $alpha := regexReplaceAll "^[^a-z]+" $norm "" | trimAll "-" -}}
{{- $base := trunc 5 $alpha | trimSuffix "-" -}}
{{- if not $base -}}
{{- $base = "d" -}}
{{- end -}}
{{- printf "%s-%s" $base $hash -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Spanner instance ID isolated per environment within the shared GCP project.
Must satisfy Cloud Spanner constraints: ^[a-z][-a-z0-9]{4,28}[a-z0-9]$ (6-30 chars).
*/}}
{{- define "cco.spannerInstanceId" -}}
{{- $pfx := include "cco.prefix" . -}}
{{- if not $pfx -}}
cco-spanner
{{- else -}}
{{- $candidate := printf "%scco-spanner" $pfx -}}
{{- if and (le (len $candidate) 30) (regexMatch "^[a-z][-a-z0-9]{4,28}[a-z0-9]$" $candidate) -}}
{{- $candidate -}}
{{- else -}}
{{- $hash := sha256sum $pfx | trunc 4 -}}
{{- $slug := regexReplaceAll "-+" (regexReplaceAll "[^a-z0-9-]" (lower $pfx) "-") "-" | trimAll "-" | trunc 21 | trimSuffix "-" -}}
{{- if not $slug -}}
{{- $slug = "s" -}}
{{- end -}}
{{- printf "cco-%s-%s" $slug $hash -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Spanner database ID scoped inside the environment's Spanner instance.
*/}}
{{- define "cco.spannerDatabaseId" -}}
cco
{{- end -}}

{{/*
Spanner instance displayName (4-30 characters required by Cloud Spanner, balanced parentheses).
*/}}
{{- define "cco.spannerDisplayName" -}}
{{- $pfx := include "cco.prefix" . -}}
{{- $label := regexReplaceAll "-+" (trimAll "-" $pfx) "-" -}}
{{- if $label -}}
{{- printf "CCO (%s)" ($label | trunc 24 | trimSuffix "-") -}}
{{- else -}}
Cuttlefish Orchestrator
{{- end -}}
{{- end -}}

{{/*
VPC network name shared across main and sub-environments.
*/}}
{{- define "cco.network" -}}
{{- $cw := .Values.config.cloudWorkstations | default dict -}}
{{- $cw.network | default "sdv-network" -}}
{{- end -}}

{{/*
VPC subnetwork name shared across main and sub-environments.
*/}}
{{- define "cco.subnetwork" -}}
{{- $cw := .Values.config.cloudWorkstations | default dict -}}
{{- $cw.subnetwork | default "sdv-subnet" -}}
{{- end -}}

{{/*
Container image for cuttlefish-orchestrator-api.
*/}}
{{- define "cco.apiImage" -}}
{{- $imgs := .Values.config.containerImages | default dict -}}
{{- $imgs.cuttlefishOrchestratorApi | default "example/cuttlefish-orchestrator-api:latest" -}}
{{- end -}}

{{/*
Container image for cuttlefish-console.
*/}}
{{- define "cco.consoleImage" -}}
{{- $imgs := .Values.config.containerImages | default dict -}}
{{- $imgs.cuttlefishConsole | default "example/cuttlefish-console:latest" -}}
{{- end -}}
