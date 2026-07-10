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

## Getting Started

Detailed integration steps, architectural constraints, and deployment runbooks are documented in:
1.  **[AGENTS.md](file:///usr/local/google/home/mikeannau/.gemini/antigravity/scratch/horizon-sdv/gitops/modules/abfs-module/AGENTS.md)**: Agentic instructions to deploy, troubleshoot, and operate the module.
2.  **[ABFS_INTEGRATION.md](file:///usr/local/google/home/mikeannau/.gemini/antigravity/scratch/horizon-sdv/gitops/modules/abfs-module/ABFS_INTEGRATION.md)**: Bidirectional Workload Identity, Spanner integration, and kernel driver lifecycle overview.
3.  **[ABFS_DEPLOYMENT_LEARNINGS.md](file:///usr/local/google/home/mikeannau/.gemini/antigravity/scratch/horizon-sdv/gitops/modules/abfs-module/ABFS_DEPLOYMENT_LEARNINGS.md)**: GKE Standard cluster taints, GCE vCPU quota management, and SSD sizing.
