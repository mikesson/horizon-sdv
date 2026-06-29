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

# CVD/CTS ephemeral GCE (Path B)

Argo workflow pod orchestrates a short-lived Cuttlefish VM via **KCC `ComputeInstance` + GCS**. No pod SSH/IAP.

## How a run works

1. **Workflow pod** (`cvd_argo_gce_ephemeral.sh`) uploads job settings and a small copy of the repo to GCS, then creates a `ComputeInstance` CR. The VM boots and runs `cvd_argo_guest_startup.sh` from metadata.
2. **Guest startup** downloads those files, unpacks the repo into `/tmp/cvd-argo-ws-<vm>`, and runs **`cvd_argo_remote_entry.sh` twice**:
   - **main** — start Cuttlefish, optional MTK Connect, run CTS if applicable, optional keep-alive.
   - **teardown** — stop MTK and Cuttlefish, **gather logs** into `cvd-argo-artifacts.tgz`, upload to GCS.
3. **Workflow pod** polls `status.json` on GCS, streams guest logs (serial port 2 or Cloud Logging), downloads the artifact tarball into `/tmp/cvd-argo-artifacts`, and deletes the VM CR. It does **not** run **`storage.sh`** when Gemini is enabled (avoids a second upload after **`gemini-review`**).
4. **If Gemini is enabled**, a later pod runs **`gemini_argo_prepare_staging.sh`**, then **`gemini-review`** uploads via **`gemini_storage.sh`**; or **`sync-vm-staging-if-no-ai-review`** uploads when success review is off.

The pod never SSHs to the VM; GCS and serial/Cloud Logging are the only control plane.

## Scripts

| Script | Runs on | Role |
|--------|---------|------|
| `cvd_argo_gce_ephemeral.sh` | Workflow pod | Upload inputs, apply/wait/delete CR, poll status, download artifacts |
| `cvd_argo_guest_startup.sh` | GCE VM (metadata startup) | Load inputs from GCS, invoke remote entry twice, upload outputs |
| `cvd_argo_guest_common.sh` | GCE VM (sourced) | HOME fix, stderr trace, tee stdout/stderr to `/dev/ttyS1` (GCE port 2) |
| `cvd_argo_remote_entry.sh` | GCE VM | CVD or CTS work for one phase (`main` or `teardown`) |
| `cvd_argo_sync_vm_artifacts_to_staging.sh` | Workflow pod | Publish VM bundle to per-run staging URI (non-Gemini path) |
| `../gemini_argo_prepare_staging.sh` | Workflow pod (Gemini prepare step) | Copy guest tarball into `test-results` for AI review |

Helm: [cvd_launcher/helm](../cvd_launcher/helm/README.md) (`CVD_ARGO_MODE=cvd`), [cts_execution/helm](../cts_execution/helm/README.md) (`cts`).

## GCS layout

Under `GEMINI_TEST_RESULTS_STAGING_URI` for the workflow run:

| Prefix | Objects |
|--------|---------|
| `ephemeral-input/` | `cvd-argo-job-env.sh`, `cvd-argo-workloads.tgz`, `cvd_argo_guest_startup.sh`, `cvd_argo_guest_common.sh` |
| `ephemeral-output/` | `status.json`, `cvd-argo-artifacts.tgz`, `cvd-argo.marker` |

## `status.json`

Written by the guest on each `_write_status` call:

```json
{"phase":"running|success|failed","rc":0,"message":"..."}
```

Pod poll (`cvd_argo_poll_guest_status`) parses JSON with `python3` and exits when `phase` is `success` or `failed`, or on wall timeout (`REMOTE_RC=124`). For `failed`, `REMOTE_RC` is at least `1` even if `rc` in the file is `0`.

On main failure the guest sets **`phase=running`** (`teardown after main failure`), runs teardown, uploads `cvd-argo-artifacts.tgz`, then writes **`phase=failed`** with the main exit code. The workflow pod must not see `failed` until upload completes (otherwise it deletes the VM before `gather_artifacts` and Gemini prepare miss logs). Guest startup uses `cmd || MAIN_RC=$?` — not `if ! cmd; then MAIN_RC=$?`, because `$?` in the `then` branch is the `if` test result, not the command's.

## Logs

### Default: Serial port 2 (`CVD_ARGO_LOG_SINK=serial`, Helm `spec.guestLogSink: serial`)

See [Serial port 2](#serial-port-2--default-live-logs-cvd_argo_log_sinkserial) below.

### Optional: Cloud Logging (`CVD_ARGO_LOG_SINK=cloud`, Helm `spec.guestLogSink: cloud`)

Treat guest **stdout/stderr** as searchable logs instead of scraping the serial console (which includes kernel boot and systemd noise).

| Step | What |
|------|------|
| 1. Ops Agent on the VM | Baked by **`cf_host_initialise.sh`** (`ensure_google_cloud_ops_agent`) when building x86/arm64 instance templates ([Google install script](https://cloud.google.com/logging/docs/agent/ops-agent/installation) + **journald** receiver). Re-run **cf-instance-template-*** after changing this. |
| 2. Journal routing | `cvd_argo_guest_startup.sh` pipes output through `systemd-cat -t cvd-argo-guest` when available so the agent ships app lines to Cloud Logging. |
| 3. Workflow pod | **x86:** `spec.guestLogSink: serial` (default). **ARM64:** driver forces **`cloud`**. Pod calls [`gcp_logging_rest.py`](../../common/gcp/gcp_logging_rest.py) **`list-guest-app-logs`** when sink is cloud. |
| 4. Ad hoc query | From your workstation: `gcloud logging read 'resource.type="gce_instance" AND jsonPayload.SYSLOG_IDENTIFIER="cvd-argo-guest"' --limit=50` (add `labels."compute.googleapis.com/resource_name"="VM_NAME"` or `resource.labels.instance_id` to scope one VM). |

**Pod IAM:** `entries:list` needs `logging.logEntries.list` (included in `roles/logging.viewer` or `roles/logging.admin`). Default charts use **`workflow-executor-elevated`**, which has elevated logging access in Terraform.

**Guest VM IAM:** the **instance template service account** (default: `{projectNumber}-compute@developer.gserviceaccount.com`) must have **`roles/logging.logWriter`** so Ops Agent can ingest logs. The ephemeral CR only adds the **`logging.write` OAuth scope**; that scope alone does not grant `entries:list` visibility if the agent never wrote logs. Grant on the project:

```bash
gcloud projects add-iam-policy-binding PROJECT_ID \
  --member="serviceAccount:PROJECT_NUMBER-compute@developer.gserviceaccount.com" \
  --role="roles/logging.logWriter"
```

**Troubleshooting empty cloud logs:** on the VM, `systemctl status google-cloud-ops-agent` and `journalctl -t cvd-argo-guest -n 20`. In the project, any log from the instance (no marker filter):

```bash
gcloud logging read 'resource.type="gce_instance" AND labels."compute.googleapis.com/resource_name"="VM_NAME"' \
  --project=PROJECT_ID --limit=10 --freshness=1h
```

If that is empty, Ops Agent is not shipping (agent down, config error, or missing **logWriter** on the VM SA).

### Serial port 2 — default live logs (`CVD_ARGO_LOG_SINK=serial`)

GCE exposes four virtual serial ports. **Port 1** (`/dev/ttyS0`) is the primary kernel/BIOS console. **Port 2** (`/dev/ttyS1`) is where **`cvd_argo_guest_startup.sh`** tees app stdout/stderr; the workflow pod reads the **same** port via **`getSerialPortOutput`**. Guest and driver always use port **2** on every architecture (including **Ubuntu arm64**); only **`CLOUD_ZONE`** / **`CLOUD_REGION`** are overridden for `-arm64` templates.

The ephemeral driver streams **all** bytes from that port with a **`[guest]`** prefix (the prefix does not filter content). **x86 Debian** templates are usually mostly app/Cuttlefish lines on port **2**. **`cvd_argo_guest_common.sh`** tees to the log file and **`systemd-cat`**; serial is attempted in a side branch so **`tee /dev/ttyS1`** EIO cannot break journald.

**Ubuntu arm64** (`instanceTemplateName` ends with **`-arm64`**): the driver **forces `CVD_ARGO_LOG_SINK=cloud`** — serial port 2 does not carry the app console on that platform (see [Cloud Logging](#optional-cloud-logging-cvd_argo_log_sinkcloud-helm-specguestlogsink-cloud)). Helm **`guestLogSink: serial`** still applies to x86.

For kernel-only debugging on x86 set **`CVD_ARGO_SERIAL_PORT=1`**. For post-run triage use **`status.json`** + **`cvd-argo-artifacts.tgz`**. VM visibility and serial reads use **`gcp_compute_rest.py`** with the pod metadata token (no **`gcloud compute`**).

| Component | Behavior |
|-----------|----------|
| Guest | `cvd_argo_guest_startup.sh` tees stdout/stderr to **`/dev/ttyS1`** (KCC metadata **`cvd-argo-app-serial-dev`**) and `/tmp/cvd-argo-guest-startup.log` |
| Workflow pod | Polls **`getSerialPortOutput` with `port=2`** (`CVD_ARGO_SERIAL_PORT`, default `2`) via `gcp_compute_rest.py` |

Ad hoc:

```bash
gcloud compute instances get-serial-port-output INSTANCE_NAME --zone=ZONE --port=2
```

Set `CVD_ARGO_SERIAL_PORT=1` only if you need the full kernel console in Argo logs.

Use `CVD_ARGO_LOG_SINK=both` only when debugging (Cloud Logging + serial port 2).

**KCC:** metadata **`serial-port-logging-enable: "true"`** enables the serial API.

**Pod auth:** metadata **`cloud-platform`** token → `gcp_metadata_access_token.sh` → `CF_COMPUTE_REST_TOKEN`.

## Artifacts and Gemini

On **teardown**, `gather_artifacts` in `cvd_argo_remote_entry.sh` builds `/tmp/cvd-argo-out` with:

- Host and workspace `cvd-*.log`, `cuttlefish_logs*.zip`, wifi logs
- Per-instance logs under `cvd/cvd-*/logs/` (includes `kernel.log` for Gemini)
- CTS result trees when the run executed CTS

Before tarring, **`gather_artifacts`** removes symlinks from the bundle (Tradefed **`results/latest`** often points at absolute paths under **`/opt/android-cts`**; Argo artifact init rejects those and **`prepare-gemini-on-failure`** init fails with `illegal symlink target`). **`cts_store_results`** also drops **`android-cts-results/latest`** after copying suite files to the workspace root.

That directory is tarred as `cvd-argo-artifacts.tgz` and uploaded to GCS. The ephemeral-GCE pod extracts it to `/tmp/cvd-argo-artifacts/cvd-argo-out`. **`gemini_argo_prepare_staging.sh`** then merges it into `/workspace/test-results` and unzips Cuttlefish archives before `gemini-review`.

If **`HOME` was unset** on the guest (common for metadata startup), gather used to fail under `set -u`; `cvd_argo_guest_common.sh` sets `HOME` before any gather path runs.

**Gemini prepare path:** `gemini_argo_prepare_staging.sh` must write to **`/workspace/test-results`** (the `gemini-test-results` PVC). With **`sharedPipelineWorkspace: true`**, `WORKSPACE` is **`/horizon`** — do not use `${WORKSPACE}/test-results` for staging.

## Remote entry dispatch

`cvd_argo_guest_startup.sh` sets `CVD_ARGO_REMOTE_PHASE=main` then `teardown` and runs `cvd_argo_remote_entry.sh` each time.

| `CVD_ARGO_MODE` | `CVD_ARGO_REMOTE_PHASE` | Function |
|-----------------|-------------------------|----------|
| `cvd` | `main` | `run_cvd_main` |
| `cvd` | `teardown` | `run_cvd_teardown` |
| `cts` | `main` | `run_cts_main` |
| `cts` | `teardown` | `run_cts_teardown` |

## Pod env (common)

| Variable | Default | Purpose |
|----------|---------|---------|
| `CVD_ARGO_LOG_SINK` | `serial` (Helm default) | `serial` \| `cloud` \| `both` — live guest log source during poll |
| `CVD_ARGO_SERIAL_PORT` | `2` | GCE serial port for live logs (`2` → `/dev/ttyS1`; same on x86 and arm64; `1` = kernel console) |
| `CVD_ARGO_CLOUD_LOG_PAGE_SIZE` | 100 | Page size for `entries:list` |
| `CVD_ARGO_GUEST_POLL_FIRST_SEC` | 30 | Delay before first status poll |
| `CVD_ARGO_GUEST_POLL_SEC` | 45 | Poll interval |
| `CVD_ARGO_KCC_WAIT_TIMEOUT` | 20m | `kubectl wait` for ComputeInstance Ready |
| `CVD_ARGO_GCP_WAIT_TIMEOUT` | 15m | Wait for zonal VM in Compute API after KCC Ready |
| `CVD_ARGO_GUEST_WALL_TIMEOUT_SEC` | 0 (derive) | Max guest wait; 0 = computed from keep-alive/CTS/MTK |

**ARM64 templates** (`instanceTemplateName` ends with `-arm64`): the driver sets `CLOUD_ZONE` / `CLOUD_REGION` from **`ARM64_ZONE`** / **`ARM64_REGION`** and **forces `CVD_ARGO_LOG_SINK=cloud`** for live guest logs (overrides Helm **`guestLogSink: serial`**).

## Module disable (Portal / Module Manager)

Ephemeral **`ComputeInstance`** CRs in `{prefix}workflows` can block **`ConfigConnectorContext`** deletion if a workflow pod dies mid-run. **PreDelete** (cf_instance_template chart) and **Module Manager** disable both remove CRs with label **`horizon-sdv.io/ephemeral-gce=true`** and names **`cvd-*`** / **`cts-*`** before template/CCC teardown.

## Related docs

- [common/gcp/README.md](../../common/gcp/README.md) — IAM, metadata token, serial + Cloud Logging REST
- [tests/README.md](../README.md) — operator prerequisites and Gemini flow
- [cvd_launcher/helm/README.md](../cvd_launcher/helm/README.md), [cts_execution/helm/README.md](../cts_execution/helm/README.md)
