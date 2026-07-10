# Android Build File System (ABFS) Module

The ABFS module provides integrated support for high-performance Android Open Source Project (AOSP) builds on Google Cloud Platform, using a specialized kernel-level Android Build File System (`casfs`) and high-efficiency background uploader/synchronizer components.

---

## Module Metadata

| Metadata Field | Value |
| :--- | :--- |
| **STATUS** | experimental |
| **OWNER** | Google |
| **CONTACT** | [horizon-sdv@google.com](mailto:horizon-sdv@google.com) |
| **VERSION** | 0.1.14 |
| **CONTAINS_COMMERCIAL** | no |

---

## Features

- **Painless CASFS Kernel Compilation**: Installs and loads the customized `casfs` filesystem kernel module onto GKE worker nodes dynamically.
- **Dedicated High-Performance Node Pool Support**: Runs optimally on high-capacity GKE compute nodes (e.g., `n2-highcpu-32`) to achieve extremely low latency file reads/writes.
- **Workload Identity Enabled**: Secure, keyless IAM service account mapping to safely read/write from Spanner databases, Secret Manager, and Google Cloud Storage buckets.
- **Native GitOps Lifecycle**: Packaged as a standard Helm chart managed natively by Argo CD.

---

## Directory Structure

```
gitops/modules/abfs-module/
├── Chart.yaml                  # Parent Helm chart metadata
├── values.yaml                 # Parent values file (specifying sub-charts)
├── README.md                   # This module documentation file
├── AGENTS.md                   # Agentic deployment runbook & guidelines
├── ABFS_DEPLOYMENT_LEARNINGS.md # Technical learnings, quotas, & limitations
├── ABFS_INTEGRATION.md         # Architecture and structural integration overview
├── portal/
│   └── overview.html           # Interactive developer overview dashboard
└── abfs/                       # Sub-chart containing resources & templates
    ├── Chart.yaml              # ABFS workloads Helm chart
    ├── values.yaml             # Default workload configuration parameters
    └── templates/              # Deployment, StatefulSet, and KCC manifests
```

---

## External Platform Dependencies (To Be Consolidated via KCC)

For future releases, architectural changes aim to make this module fully self-contained. The following file modifications are currently located **outside** the `gitops/modules/abfs-module/` folder in order to support central GitOps catalog registration and the dedicated secondary cluster infrastructure:

### A. Central GitOps Catalog Registration
These files register the ABFS module with Horizon's central catalog registry and define default environment parameters:
- **`gitops/apps/module-manager/templates/module-catalog.yaml`**: Appends the ABFS module configuration block to the central manager catalog.
- **`gitops/values.yaml`**: Exposes default values, toggles, and global configuration values for the ABFS module.

### B. GCP Terraform Infrastructure
These files are modified under the root `terraform/` directories to provision the dedicated secondary GKE cluster, manage host kernel taint settings, and map Workload Identity:
- **`terraform/env/main.tf`**: Configures variables, locks down the `n2-highcpu-32` node size, and triggers the dedicated secondary cluster flag.
- **`terraform/env/providers.tf`**: Registers additional Google, Kubernetes, and Helm provider hooks for the new cluster.
- **`terraform/env/variables.tf`**: Exposes configuration variables for the secondary GKE cluster name and locations.
- **`terraform/modules/base/main.tf`**: Hooks network and routing rules into the secondary GKE control plane.
- **`terraform/modules/base/provider.tf`**: Exposes GKE client authentication keys for the secondary control plane.
- **`terraform/modules/base/variables.tf`**: Outlines environment variables for secondary cluster deployment.
- **`terraform/modules/sdv-gke-cluster/main.tf`**, **`outputs.tf`**, **`variables.tf`**: Configures standard GKE node pool taint checks and bypass overrides.
- **`terraform/modules/sdv-wi/main.tf`**: Applies bilateral Workload Identity trust permissions allowing Kubernetes service accounts inside the dedicated cluster namespace to impersonate GCP IAM credentials.

### C. Incubator (Standalone Deployment Bundle)
This root-level folder provides a decoupled, KCC-only operational package for running the ABFS engine standalone (without the core Horizon platform or Argo CD):
- **`incubator/kcc-google-abfs/`**: Contains direct KCC manifests (`infra/`), schemas (`infra/schemas/`), and automation scripts (`scripts/render.sh`, `Makefile`) to run ABFS independently.

---

## Getting Started

Detailed integration steps, architectural constraints, and deployment runbooks are documented in:
1.  **[AGENTS.md](file:///usr/local/google/home/mikeannau/.gemini/antigravity/scratch/horizon-sdv/gitops/modules/abfs-module/AGENTS.md)**: Agentic instructions to deploy, troubleshoot, and operate the module.
2.  **[ABFS_INTEGRATION.md](file:///usr/local/google/home/mikeannau/.gemini/antigravity/scratch/horizon-sdv/gitops/modules/abfs-module/ABFS_INTEGRATION.md)**: Bidirectional Workload Identity, Spanner integration, and kernel driver lifecycle overview.
3.  **[ABFS_DEPLOYMENT_LEARNINGS.md](file:///usr/local/google/home/mikeannau/.gemini/antigravity/scratch/horizon-sdv/gitops/modules/abfs-module/ABFS_DEPLOYMENT_LEARNINGS.md)**: GKE Standard cluster taints, GCE vCPU quota management, and SSD sizing.
