<!-- Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. -->

# Cuttlefish Orchestrator Module

Helm chart: `gitops/modules/cuttlefish-orchestrator`  
Argo CD parent Application: **`mod-cuttlefish-orchestrator`** (catalog name `cuttlefish-orchestrator`, enabled from **Administration -> Modules**).

Deploys Cloud Cuttlefish Orchestrator (CCO) as an optional, toggleable Horizon SDV module behind Horizon Keycloak OIDC authentication and GKE Gateway ingress, together with additive Argo Workflows (`cco-host-image-build`, `cco-cvd-run`, `cco-cts-run`) using the `cuttlefish-workflow` container image.

`cuttlefish-orchestrator` is strictly additive and coexists alongside `workloads-android` without modifying Jenkins pipelines, KCC `ComputeInstanceTemplate` charts, or existing GCE Cuttlefish workflows.

---

## Module Metadata

| Metadata Field | Value |
| :--- | :--- |
| **STATUS** | experimental |
| **OWNER** | Google |
| **CONTACT** | [horizon-sdv@google.com](mailto:horizon-sdv@google.com) |
| **CHART VERSION** | 0.1.0 |
| **CONTAINS_COMMERCIAL** | no |

---

## Catalog

| Setting | Value |
| :--- | :--- |
| Module name | `cuttlefish-orchestrator` |
| Soft dependencies | `workloads-android`, `storage-gcs`, `workloads-common` |
| Overview service | `mod-cuttlefish-orchestrator-overview` (`portal/overview.html`) |
| Portal app link | `/cuttlefish-orchestrator/` (`Cuttlefish Orchestrator`) |

---

## Prerequisites

Before enabling `cuttlefish-orchestrator` in Horizon SDV:

- **Terraform-provisioned IAM SAs & firewalls (`terraform/modules/base`)**:
  - `gke-<prefix>cco-sa`: Workload Identity SA used by `cuttlefish-orchestrator-api` (`roles/iam.serviceAccountUser` scoped to `gke-<prefix>cco-host-vm-sa`, Spanner/GCS access, and CEL-scoped `cco_secrets_only` secret accessor).
  - `gke-<prefix>cco-host-vm-sa`: Compute Engine SA attached to managed Cuttlefish host VMs (protected by `cco-allow-api-to-hosts`, `cco-allow-iap-ssh-to-hosts`, and `cco-allow-webrtc-private-media` firewalls).
  - **Packer runtime SA & IAP SSH (`cco-host-image-build`)**: The `cco-host-image-build` workflow runs on `workflow-executor-elevated` (`gke-<prefix>argo-workflows-elevated-sa`, holding `roles/compute.instanceAdmin.v1`, `roles/iap.tunnelResourceAccessor`, and `roles/iam.serviceAccountUser`) and launches the ephemeral Packer builder VM using the GCE default Compute Engine SA (`var.sdv_gcp_compute_sa_email`), which receives IAP TCP/22 ingress from `cuttlefish-allow-tcp-22` (`terraform/modules/base/main.tf`).
- **Terraform-provisioned GCP Secret Manager secrets (`s20`, `s21`)**: `terraform apply` creates and auto-populates `<prefix>cuttlefish-orchestrator-oidc-client-secret-b64` (`s20`) and `<prefix>cuttlefish-orchestrator-oauth2-cookie-secret-b64` (`s21`) via `random_password` unless overridden via `manual_secrets` (map keys `"s20"` and `"s21"`, e.g. `manual_secrets = { s20 = "...", s21 = "..." }`) in `terraform.tfvars` (do not pre-create them manually before `terraform apply`, or Secret Manager creation fails with `409 AlreadyExists`). Verify both secrets are populated in Secret Manager before syncing so External Secrets Operator populates the OIDC client secret and `oauth2-proxy` cookie secret.
- **WebRTC client CIDRs**: Configure `cco_webrtc_client_cidrs` in `terraform.tfvars` to allow browser WebRTC media (`TCP/UDP 15550-15599`) from developer workstations or VPNs.
- **Keycloak users, groups, and `email` claim**: Users signing in to `/cuttlefish-orchestrator/` or submitting CCO workflows must belong to the Keycloak `administrators` or `developers` group and have a populated `email` attribute/claim so `oauth2-proxy` forwards `X-Auth-Request-Email` to `cuttlefish-orchestrator-api`.
- **CLI usage**: See [Using the cfctl CLI](../../../terraform/modules/sdv-container-images/images/cuttlefish-orchestrator/docs/guides/using-the-cli.md) for local and workflow `cfctl` setup and commands.

---

## Directory Structure

```text
gitops/modules/cuttlefish-orchestrator/
├── Chart.yaml                                  # Parent module chart
├── values.yaml                                 # Parent values and image references
├── README.md                                   # This file
├── portal/
│   └── overview.html                           # Developer Portal overview page
├── templates/
│   ├── _helpers.tpl                            # Namespace and child-app naming helpers
│   ├── application-argo-workflows.yaml         # Child App <prefix>mod-<module>-argo-workflows
│   ├── application-cuttlefish-orchestrator.yaml # Child App <prefix>mod-<module>-app
│   ├── application-oauth2-proxy.yaml           # Child App <prefix>mod-<module>-oauth2-proxy
│   └── module-overview-http.yaml               # Overview Nginx Deployment/Service
├── orchestrator/                               # Child chart: Spanner, CCO API, Console, Gateway, NetworkPolicies
│   ├── Chart.yaml
│   ├── values.yaml
│   └── templates/
└── argo-workflows/                             # Child chart: CCO WorkflowTemplates, Sensors, NetworkPolicy
    ├── Chart.yaml
    ├── values.yaml
    └── templates/
        ├── networkpolicy.yaml                  # Egress allow-workflow-pods-egress-to-cco-api
        ├── sensors/                            # EventBridge webhooks for all 3 templates
        └── workflowtemplates/                  # cco-host-image-build, cco-cvd-run, cco-cts-run
```

---

## What It Deploys

| Child Application | Chart / Source | Target Namespace | Purpose |
| :--- | :--- | :--- | :--- |
| `<prefix>mod-<module>-app` | `orchestrator/` | `<prefix>cuttlefish-orchestrator` | Cluster-mode KCC `SpannerInstance` (`cco-spanner` / `<prefix>cco-spanner` or `cco-<slug>-<hash4>`, 100 PU, regional) + `SpannerDatabase` (`cco`, schema v87), ESO `SecretStore` + `ExternalSecret` (`s20`/`s21`), `Sync` seed Job (`cco-spanner-seed`), `cuttlefish-orchestrator-api` (`:8080`, `CCO_AUTH_MODE=proxy`, `CCO_DEVICE_INGRESS=private`), `cuttlefish-console` (`:8080`), GKE Gateway `HTTPRoute`s (`/cuttlefish-orchestrator` -> `/cuttlefish-orchestrator/`), `HealthCheckPolicy` (`/ping`), `GCPBackendPolicy` (`timeoutSec: 86400`), `cco-cleanup` suspended `CronJob`, and NetworkPolicies. |
| `<prefix>mod-<module>-oauth2-proxy` | `oauth2-proxy` (`8.3.0`) | `<prefix>cuttlefish-orchestrator` | OIDC protection for `/cuttlefish-orchestrator/` against Horizon Keycloak (`administrators` and `developers` groups). |
| `<prefix>mod-<module>-argo-workflows` | `argo-workflows/` | `<prefix>workflows` & `<prefix>argo-events` | `NetworkPolicy/allow-workflow-pods-egress-to-cco-api`, `WorkflowTemplate`s `cco-host-image-build`, `cco-cvd-run`, and `cco-cts-run` (`ctsWorkspaceStorageSize: 40Gi`), plus Argo Events `Sensor`s `webhook-cco-host-image-build`, `webhook-cco-cvd-run`, and `webhook-cco-cts-run`. |

---

## Argo Workflows and `cuttlefish-workflow` Image

The `argo-workflows/` child chart exposes three additive `WorkflowTemplate`s (`horizon-sdv.io/expose: "true"`) using the `cuttlefish-workflow` container image (`cfctl`, `gcloud`, `packer` `1.15.0` with `googlecompute` `v1.2.4`, `adb`, Java 17 JRE, `openssh-client`, `python3`, `jq`):

1. **`cco-host-image-build`** (runs on `workflow-executor-elevated` SA):
   - Pre-prunes stale OS Login SSH keys, then runs Packer over IAP (`omit_external_ip = true`, `use_internal_ip = true`, `use_iap = true`) against Debian 13 (`debian-13` with `enable-vmx` for `x86_64`, or `debian-13-arm64` for `arm64` `c4a-*`).
   - Installs `cuttlefish-base`, `cuttlefish-user`, and `cuttlefish-orchestration` (`>= 1.58.0`) with **zero** `/etc/cuttlefish/custom_configs`, and verifies `:1080`, `:2080`, and KVM kernel support.
   - `onExit` deletes any orphan `packer-*` VM and attached/unattached disks; optionally updates CCO host-image family mappings via `cfctl admin host-image update` (`cco-host` or `cco-host-arm64`).
2. **`cco-cvd-run`** (runs on `workflow-executor` SA):
   - Registers build artifacts (`POST /v2/builds`), launches 1-8 Cuttlefish instances (`POST /v2/devices`, using `mode: "advanced"` `env_config.instances` when `numInstances > 1`), polls until `running`, connects `cfctl device connect`, verifies `sys.boot_completed=1`, triggers `POST /v2/devices/{id}/bugreport` and uploads logs to GCS, and tears down on exit unless `keepDevice=true`.
3. **`cco-cts-run`** (runs on `workflow-executor` SA):
   - Launches 1-8 CCO virtual devices from the target build, attaches them over `cfctl device connect`, runs sharded `cts-tradefed` (`invocation_summary.txt`, `test_result.xml`, `test_result_failures_suite.html`), archives CTS results + CCO bugreport to GCS, fails the workflow if `FAILED > 0` in `invocation_summary.txt`, and tears down the device in `onExit`.

---

## Coexistence with `workloads-android`

- `cuttlefish-orchestrator` declares `workloads-android`, `storage-gcs`, and `workloads-common` as `softDependencies` only, so it can be enabled independently or side-by-side.
- Existing `workloads-android` templates (`cf_instance_template`, `cvd-launcher-on-gce`, `cts-execution-on-gce`, and Jenkins pipelines) remain completely unmodified.
- CCO manages GCE host VMs directly through its Go control plane rather than KCC `ComputeInstance` / `ComputeInstanceTemplate` CRs, and bakes clean upstream Cuttlefish host images (`cco-host` / `cco-host-arm64`) without `custom_configs`.

---

## Security Notes

- **`enableNetworkPolicies: true` is required in production.** `cuttlefish-orchestrator-api` runs in `CCO_AUTH_MODE=proxy` with `CCO_TRUSTED_PROXIES` restricted strictly to cluster Pod CIDRs (`podSourceCIDRs`, e.g. `10.10.0.0/16`, never node subnets or broad RFC1918 ranges). `NetworkPolicy/allow-cuttlefish-orchestrator-api` restricts `:8080` ingress strictly to `cuttlefish-console` and workflow pods in `<prefix>workflows` carrying `horizon-sdv.io/cuttlefish-api-client: "true"`, while `probeSourceCIDRs` target only dedicated kubelet probe port `:8081` (`CCO_PROBE_PORT=8081` on API, `CCO_CONSOLE_PROBE_PORT=8081` on Console).
- **In-cluster Workflow authentication**: `cco-host-image-build` (when `updateCcoMapping=true`), `cco-cvd-run`, and `cco-cts-run` talk to `http://cuttlefish-orchestrator-api.<ccoNamespace>.svc.cluster.local:8080` using `CFCTL_AUTH_NONE=true` and explicit `CFCTL_PROXY_EMAIL` / `X-Auth-Request-Email` headers (fail-closed if empty), allowed on egress by `NetworkPolicy/allow-workflow-pods-egress-to-cco-api`.
- **Shell parameter safety**: WorkflowTemplates pass all user/workflow parameters strictly through container `env:` variables rather than direct `{{workflow.parameters.*}}` expansion inside shell scripts.
- **No KCC/CNRM resources in `<prefix>workflows`**: Spanner resources are confined to `<prefix>cuttlefish-orchestrator`.

---

## Module Disable and Teardown Flow

Disabling `cuttlefish-orchestrator` in the Developer Portal triggers Module Manager (`cuttlefish_orchestrator_teardown.go`):

1. Pauses reconciliation on the parent `mod-<module>` Application and core child `<prefix>mod-<module>-app` Application by setting `argocd.argoproj.io/skip-reconcile: "true"` (and terminating any active sync operation).
2. Scales `cuttlefish-orchestrator-api` to `0` replicas and waits until all active (non-terminal) API pods exit so no new host VMs are provisioned during drain.
3. Creates a one-shot `Job` from the suspended `CronJob/cco-cleanup` (`cco-cleanup-<base36-ts>-<seq>`) and waits for it to reap all GCE VMs labeled `managed-by=cloud-cuttlefish-orchestrator` and `cco-deployment=<deployment>` for the project and environment prefix.
4. Disables automated sync and clears `argocd.argoproj.io/skip-reconcile` on `<prefix>mod-<module>-app`, then clears `argocd.argoproj.io/skip-reconcile` on parent `mod-<module>` using `context.Background()` (`resumeCuttlefishOrchestratorCleanupTimeout`) immediately before parent Application deletion, allowing Argo CD's `resources-finalizer` to cascade-delete all three child Applications (`<prefix>mod-<module>-app`, `<prefix>mod-<module>-oauth2-proxy`, `<prefix>mod-<module>-argo-workflows`).

---

## Configuration Values

| Value Key | Default / Source | Purpose |
| :--- | :--- | :--- |
| `config.enableNetworkPolicies` | `true` | Enables parent/child NetworkPolicies in `<prefix>cuttlefish-orchestrator` and `<prefix>workflows`. |
| `config.containerImages.cuttlefishOrchestratorApi` | Injected by Terraform via `MODULE_CONFIG` | API server image (`cuttlefish-orchestrator-api:1.0.2`). |
| `config.containerImages.cuttlefishConsole` | Injected by Terraform via `MODULE_CONFIG` | Angular console Nginx image (`cuttlefish-console:1.0.2`). |
| `config.containerImages.cuttlefishWorkflow` | Injected by Terraform via `MODULE_CONFIG` | Argo workflow runner image (`cuttlefish-workflow:1.0.2`). |
| `config.arm64.zone` / `config.arm64.subnetwork` | Injected by Terraform via `MODULE_CONFIG` | Optional ARM64 zone/subnetwork for `c4a-*` `cco-host-image-build` and `c4a` hosts. |
| `api.hostImageProject` | `""` (falls back to `config.projectID`) | Project hosting `cco-host` / `cco-host-arm64` GCE image families. |
| `spanner.processingUnits` | `100` | KCC `SpannerInstance` processing units (`100` for cost-efficient dev/regional deploy). |
| `spanner.deletionPolicy` | `delete` | Spanner cleanup policy on module disable (`delete` allows clean teardown). |

---

## Local Verification

```bash
helm lint gitops/modules/cuttlefish-orchestrator \
  -f .agents/plugins/horizon-dev/skills/horizon-create-module/scripts/lint-values.yaml
helm lint gitops/modules/cuttlefish-orchestrator/orchestrator
helm lint gitops/modules/cuttlefish-orchestrator/argo-workflows

helm template cco gitops/modules/cuttlefish-orchestrator \
  --set repo.url=https://github.com/example/horizon-sdv.git \
  --set repo.revision=main
helm template cco-wf gitops/modules/cuttlefish-orchestrator/argo-workflows
```
