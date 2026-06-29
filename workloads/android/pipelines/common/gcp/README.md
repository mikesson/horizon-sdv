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

# GCP helpers (`workloads/android/pipelines/common/gcp`)

**Canonical location** for Horizon GCP REST helpers from GKE workflow pods (there is no `workloads/common/gcp/` tree in this repo).

Shared **metadata-token** access to Google Cloud APIs from GKE workflow pods and CF publish jobs. **No `gcloud` CLI** on the CF hot path. CVD/CTS ephemeral runs use **KCC + GCS** (not modules in this folder for VM SSH).

## Placement (`android/pipelines/common` vs `workloads/common`)

Path matches where the code was introduced and every current consumer lives under **`workloads/android/pipelines/`**:

| Consumer | Path |
|----------|------|
| CF instance template publish | `environment/cf_instance_template/` |
| CVD/CTS ephemeral GCE | `tests/cvd_argo_gce/` |

**How Android-specific is the code?**

| Module | Generic? | Notes |
|--------|----------|--------|
| `gcp_metadata_access_token.sh` | Yes | Metadata `cloud-platform` token; any GKE workload pod |
| `gcp_compute_rest.py` | Mixed | Compute + OS Login REST for **CF/Packer** and **CVD/CTS ephemeral** (`get-instance-status`, `get-serial-port-output`, orphan disks, template/image delete); env **`CF_COMPUTE_REST_TOKEN`** |
| `gcp_logging_rest.py` | Mixed | Cloud Logging **`entries:list`** for CVD/CTS ephemeral guest app logs (`list-guest-app-logs`, `decode-guest-log-lines`); same **`CF_COMPUTE_REST_TOKEN`** |
| `gcp_artifact_registry_rest.py` | Yes | Docker image tag presence via Artifact Registry REST (`dockerImages.list`); no gcloud / Container Analysis |
| `gcp_artifact_registry_check_image.sh` | Yes | Shell wrapper: metadata token + `docker-image-tag-present` (Helm `check-aaos-image` / `ensure-cf-builder-image`) |

Nothing in this directory imports Android build logic, but **renaming and path stability** are tied to the CF + Cuttlefish test pipelines today. Non-Android reuse (e.g. another workload that provisions ephemeral GCE from a template via Argo) is **possible** without a move—you would `source` / `python3` from this path in the pipeline repo mount—but we have **no non-Android consumers yet**.

**Possible future relocation** — consider promoting shared pieces to something like **`workloads/common/gcp/`** (alongside `workloads/common/storage/`, `agentic-ai/`, `mtk-connect/`) **if**:

- A second vertical needs the same metadata-token + REST pattern, **or**
- We want one documented “Horizon GKE → GCP without `gcloud`” library for platform engineers.

A move would be a deliberate refactor (not a drive-by): update `cf_create_instance_template.sh`, the `cf_compute_rest.py` shim, and workflow image `WORKSPACE` layout docs. Optional cleanup: alias `CF_COMPUTE_REST_TOKEN` → `GCP_REST_ACCESS_TOKEN`.

Until then, treat **`workloads/android/pipelines/common/gcp`** as the canonical location; do not duplicate modules under `workloads/common` without a migration plan.

## Modules

| File | Role |
|------|------|
| [`gcp_metadata_access_token.sh`](gcp_metadata_access_token.sh) | Mint `cloud-platform` OAuth token from GCE metadata server |
| [`gcp_prune_os_login_ssh_keys.sh`](gcp_prune_os_login_ssh_keys.sh) | **`gcp_prune_os_login_ssh_keys_for_caller`** — prune all OS Login keys for current identity (best-effort; Argo/Jenkins parity) |
| [`gcp_compute_rest.py`](gcp_compute_rest.py) | Compute + OS Login REST CLI (CF/Packer subcommands; transient op retry on long polls) |
| [`gcp_artifact_registry_rest.py`](gcp_artifact_registry_rest.py) | Artifact Registry REST (`docker-image-tag-present`) |
| [`gcp_artifact_registry_check_image.sh`](gcp_artifact_registry_check_image.sh) | Metadata token + tag check for workflow Helm steps |
**Compatibility:** [`cf_instance_template/cf_compute_rest.py`](../../environment/cf_instance_template/cf_compute_rest.py) is a thin shim that delegates to `gcp_compute_rest.py`.

## Environment

| Variable | Set by | Purpose |
|----------|--------|---------|
| `CF_COMPUTE_REST_TOKEN` | Shell before `python3 …` | Bearer token for all Python modules |
Obtain token in bash:

```bash
# shellcheck source=gcp_metadata_access_token.sh
source workloads/android/pipelines/common/gcp/gcp_metadata_access_token.sh
export CF_COMPUTE_REST_TOKEN="$(gcp_metadata_access_token)"
```

## Consumers

| Consumer | Uses |
|----------|------|
| `cf_create_instance_template.sh` | `gcp_compute_rest.py`; **`gcp_prune_os_login_ssh_keys.sh`** before Packer; orphan Packer disks via REST |
| `cf_instance_template` / `aaos_builder` Helm | `gcp_artifact_registry_check_image.sh` (builder image tag gate) |
| `cvd_argo_gce_ephemeral.sh` | **Path B:** KCC + GCS; VM wait via **`get-instance-status`**; live guest logs via **`get-serial-port-output`** or **`gcp_logging_rest.py list-guest-app-logs`** (`CVD_ARGO_LOG_SINK`) |

## CVD/CTS ephemeral GCE (Path B — KCC + GCS)

**Decision (2026-05):** Argo does **not** SSH/SCP to the test VM. The workflow pod:

1. Uploads `cvd-argo-job-env.sh`, `cvd-argo-workloads.tgz`, and `cvd_argo_guest_startup.sh` to `gs://…/ephemeral-input/`.
2. Applies a namespaced **`ComputeInstance`** CR (`kubectl`; Config Connector reconciles to GCE).
3. Guest **startup script** downloads inputs, runs `cvd_argo_remote_entry.sh` (main + teardown), uploads artifacts and **`ephemeral-output/status.json`** (instance metadata **`serial-port-logging-enable: true`**).
4. Pod polls GCS status and guest logs (`CVD_ARGO_LOG_SINK`): **`getSerialPortOutput`** (serial, default) or **`entries:list`** via `gcp_logging_rest.py` (Cloud Logging, Ops Agent + journal on the VM); deletes the CR on exit.

Requires **`computeinstances`** RBAC on `workflow-executor` / `workflow-executor-elevated` in `{prefix}workflows` (see `gitops/templates/argo-workflows-init.yaml`). VM needs **`devstorage.read_write`** (extra OAuth scopes on the ephemeral `ComputeInstance` spec).

## OS Login key prune (CF Packer only)

Jenkins **GCE cloud** agents do **not** use OS Login import for Cuttlefish VM SSH. **CF** Packer still prunes before **`importSshPublicKey`** on the builder identity.

Call **`gcp_prune_os_login_ssh_keys_for_caller`** once per run **before** the first `importSshPublicKey` (CF: before `packer init` only — **not** CVD/CTS):

```bash
# shellcheck source=gcp_prune_os_login_ssh_keys.sh
source workloads/android/pipelines/common/gcp/gcp_prune_os_login_ssh_keys.sh
export GCP_COMPUTE_REST_PY="$(pwd)/workloads/android/pipelines/common/gcp/gcp_compute_rest.py"
gcp_prune_os_login_ssh_keys_for_caller
```

Best-effort (warnings only). Implementation: **`gcp_compute_rest.py prune-os-login-ssh-keys`**.


## IAM (typical)

- **CF / Packer:** Compute REST + OS Login prune (see `cf_create_instance_template.sh`)
- **CVD/CTS pod:** `kubectl` on `computeinstances` + GCS read/write on staging bucket + **`get-instance-status`** / **`get-serial-port-output`** and/or **`logging.logEntries.list`** (metadata token → `gcp_compute_rest.py` / `gcp_logging_rest.py`; elevated SA recommended for Cloud Logging)
- **CVD/CTS VM:** `devstorage.read_write` on ephemeral instance (scopes on KCC `ComputeInstance`)

## Known limitations

- Guest logs: **x86** — serial port **2** (`getSerialPortOutput?port=2`); **ARM64** — Cloud Logging only (driver override). See [cvd_argo_gce/README.md](../../tests/cvd_argo_gce/README.md).
- **CF Packer** uses OS Login import + prune; **CVD/CTS** use KCC + GCS (no pod SSH).

## Related docs

- [CVD/CTS ephemeral GCE (Path B)](../../tests/cvd_argo_gce/README.md)
- [CF instance template](../../../../docs/workloads/android/environment/cf_instance_template.md)
- [CVD Launcher tests README](../../tests/README.md)
- [CVD Launcher Helm README](../../tests/cvd_launcher/helm/README.md)
- [CTS Execution Helm README](../../tests/cts_execution/helm/README.md)
