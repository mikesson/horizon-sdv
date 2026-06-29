<!-- Copyright (c) 2026 Accenture, All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

        http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. -->

# Android test pipelines — shared Argo / GCE assets

Scripts and Helm charts shared by **CVD Launcher** and **CTS Execution** Argo workflows on ephemeral Cuttlefish GCE VMs.

## Prerequisites

- Argo Workflows in the cluster; **`workloads-android`** Module Manager module enabled (deploys both Helm charts).
- Cuttlefish **instance template** already published (CF instance template pipeline).
- ConfigMap **`horizon-workflow-cloud-env`** in the workflows namespace (`CLOUD_PROJECT`, region, zone, domain).
- Workflow SA with **`computeinstances`** RBAC + GCS (**`workflow-executor-elevated`** when `spec.useElevatedWorkflowIam: true`).
- Optional MTK: Secret **`workflow-mtk-connect-apikey`** (created by **mtk-connect-post**; rotated by **mtk-connect-post-key**).

## Usage (operators)

| Action | Where |
|--------|--------|
| Render manifests locally | `helm template test workloads/android/pipelines/tests/cvd_launcher/helm -f .../values-local.yaml` (same for `cts_execution/helm`) |
| Submit a run | `argo submit --from workflowtemplate/cvd-launcher-on-gce -n <prefix>workflows -p instanceTemplateName=... -p cuttlefishDownloadUrl=...` |
| Webhook trigger | Sensors **`webhook-cvd-launcher-on-gce`** / **`webhook-cts-execution-on-gce`** in `<prefix>argo-events` |
| Portal catalog | WorkflowTemplates with label **`horizon-sdv.io/expose: "true"`** |

**CTS on the guest:** installs always under **`/opt/android-cts`** (symlink to **`/opt/android-cts_<ANDROID_VERSION>`**); not a workflow parameter.

## Related documentation

- [GCP helpers](../common/gcp/README.md) — metadata token, Compute REST (CF); CVD/CTS Path B uses KCC + GCS
- [Gemini scripts](../../../docs/workloads/common/agentic-ai/gemini.md) — `run_ai_review.sh`, env vars
- [CF instance template](../../../docs/workloads/android/environment/cf_instance_template.md) — Cuttlefish instance templates (KCC publish)
- Jenkins / operator docs: [cvd_launcher.md](../../../docs/workloads/android/tests/cvd_launcher.md), [cts_execution.md](../../../docs/workloads/android/tests/cts_execution.md)
- Chart READMEs: [cvd_launcher/helm/README.md](cvd_launcher/helm/README.md), [cts_execution/helm/README.md](cts_execution/helm/README.md)

## Ephemeral GCE driver (`cvd_argo_gce/`)

| Script | Purpose |
|--------|---------|
| `cvd_argo_gce_ephemeral.sh` | **Path B:** GCS + KCC `ComputeInstance`, serial console via REST, poll `status.json`, delete CR ([cvd_argo_gce/README](cvd_argo_gce/README.md)). |
| `cvd_argo_guest_startup.sh` | VM startup: download bundle, run `cvd_argo_remote_entry.sh` (main + teardown), upload artifacts. |
| `cvd_argo_remote_entry.sh` | Guest-side CVD / MTK / keep-alive and artifact gather. |
| [`../common/gcp/`](../common/gcp/) | CF Compute/OS Login REST; live guest logs via **`get-serial-port-output`** (x86) or **`gcp_logging_rest.py`** (ARM64, auto). |
| `cvd_argo_sync_vm_artifacts_to_staging.sh` | `storage.sh` upload of VM bundle to per-run GCS prefix when Gemini review does not run (or MTK skip path). |

Charts: [`cvd_launcher/helm/README.md`](cvd_launcher/helm/README.md), [`cts_execution/helm/README.md`](cts_execution/helm/README.md).

## Gemini staging and review

After ephemeral GCE finishes, the workflow has a tarball of guest logs under `/tmp/cvd-argo-artifacts`. **`gemini_argo_prepare_staging.sh`** copies that into `/workspace/test-results` (and unpacks Cuttlefish zips) so Gemini can read `kernel.log` and host logs the same way as Jenkins.

Argo uses the same **`run_ai_review.sh`** entrypoint as **aaos-builder** and Jenkins (inline **`gemini-review`** in each chart’s **`_gemini.tpl`**, not cluster **`ai-review`** **`templateRef`**).

```
run-*-ephemeral-gce → prepare-gemini-on-* → gemini-ai-review-on-* (gemini-review)
                      ↑ gemini_argo_prepare_staging.sh   ↑ run_ai_review.sh + review-post-*.sh
```

| Asset | Role |
|-------|------|
| [`gemini_argo_prepare_staging.sh`](gemini_argo_prepare_staging.sh) | Merge `/tmp/cvd-argo-artifacts` into PVC **`/workspace/test-results`**; unzip Cuttlefish logs; CTS Phase 0 file mirror; MTK-failure skip + optional GCS sync. |
| [`hooks/review-post-argowf.sh`](hooks/review-post-argowf.sh) | Shared post-hook: **`gemini_storage.sh`** upload logic. |
| [`cvd_launcher/hooks/review-post-cvd.sh`](cvd_launcher/hooks/review-post-cvd.sh) | CVD wrapper (`GEMINI_HOOK_PROFILE=cvd`). |
| [`cts_execution/hooks/review-post-cts.sh`](cts_execution/hooks/review-post-cts.sh) | CTS wrapper (`GEMINI_HOOK_PROFILE=cts`). |
| `*/helm/templates/workflow/_gemini.tpl` | **`prepare-gemini-cvd`** / **`prepare-gemini-cts`** and **`gemini-review`** container specs. |

**Environment (prepare step):** `WORKSPACE`, `TEST_RESULTS_STAGING_GCS_URI`, `BUILD_NUMBER`, `GEMINI_PREPARE_MODE` (`cvd` \| `cts`), `CLOUD_PROJECT` (MTK skip upload).

**Environment (review step):** See [`docs/workloads/common/agentic-ai/gemini.md`](../../../docs/workloads/common/agentic-ai/gemini.md). Argo sets **`GEMINI_SKIP_MOVE_ARTIFACTS=1`**, **`ANALYSIS_WORKING_DIRECTORY=/workspace/test-results`**, and hook dir/profile per chart.

## Jenkins vs Argo

Jenkins jobs under **`cvd_launcher/`** and **`cts_execution/`** use the shared **`cvdPipeline`** library: the **GCE cloud plugin** runs stages **on the VM** with a local git checkout (`jenkins-scm-creds`). Argo runs **`cvd_argo_gce_ephemeral.sh`** in a **workflow pod** (KCC + GCS; guest runs scripts locally). Large CVD/CTS inputs are always **`gcloud storage` on the VM**; the pod uploads a minimal script bundle to GCS. See [common/gcp README](../common/gcp/README.md) and chart READMEs under **`cvd_launcher/helm`** / **`cts_execution/helm`**.
