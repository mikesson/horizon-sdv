# Horizon-SDV: ABFS Deployment Learnings & Troubleshooting Knowledge Base

This document lists and captures all critical technical insights, root-cause analyses, and troubleshooting workflows gained during the end-to-end deployment and tuning of the Android Build File System (ABFS) on GKE.

---

## 1. GKE Node Pool Taints & DNS Blackout

> [!WARNING]
> **Technical Issue:**
> Standard GKE system-critical pods (such as `kube-dns`, `calico-node`, `metrics-server`, and GKE storage CSI driver controllers) are scheduled dynamically to support the cluster. However, these core system daemonsets **do not carry custom tolerations** for application-specific taints.
>
> In our initial configuration, the single node pool `sdv-abfs-build-node-pool` was provisioned with the taint `workloadType=android-abfs`. Because this was the only node pool in the dedicated cluster, system-critical pods were stuck in `Pending` indefinitely. This resulted in a **total DNS blackout inside the cluster**, blocking `abfs-server` from resolving GCP endpoints and causing licensing entitlement validation to fail.

### Root-Cause Analysis
- GKE Autopilot avoids this by managing system taints natively, but on GKE Standard, custom taints applied to the sole node pool of a cluster will starve system daemonsets.
- Since `kube-dns` was unschedulable, the `abfs-server` pods could not perform public HTTP lookups to verify the licensing entitlement keys, leaving the server in an unready state.

### Resolution & Actionable Guidance
1. **Disable Taints on Dedicated Clusters**: Modified the Terraform infrastructure logic to make node-pool taints conditional. If the cluster is entirely dedicated to ABFS (`var.enable_dedicated_abfs_cluster = true`), we omit the `workloadType` taint, as workload isolation is unnecessary on a dedicated cluster.
2. **Manual Remediation**: Cleared the live node taints using:
   ```bash
   kubectl taint nodes <node-name> workloadType:NoSchedule-
   ```
3. **Key Learning**: Never apply exclusive custom taints to node pools on GKE Standard unless you have a separate untainted system node pool, or have manually patched system DaemonSets (like `kube-dns`) with corresponding tolerations.

---

## 2. GCP CPU & SSD Storage Quota Exhaustion (QIRs)

> [!IMPORTANT]
> **Technical Issue:**
> High-performance developer platform modules require extensive cloud compute and storage resources. Standard GCP project initialization quotas are highly restrictive and will silently block GKE cluster autoscaling.

### The Quota Bottlenecks
- **CPU Quota**: The project's global CPU quota (`CPUS_ALL_REGIONS`) was capped at **32.0**. Since the main platform cluster (`sdv-cluster`) was already consuming 8 CPUs, GKE was unable to provision the required `n2-highcpu-32` (32 vCPUs) build node, leaving the cluster autoscaler blocked.
- **Storage Quota**: The initial request for 2,000 GB of Regional SSD (`premium-rwo`) storage in `europe-west3` was denied due to standard regional threshold limits.

### Resolution & Resource Sizing Validation
1. **CPU Quota Increase**: Guided the user to submit a Quota Increase Request (QIR) for **64 CPUs**, which was successfully approved. This provides comfortable headroom to run the `n2-highcpu-32` compute node alongside core system services.
2. **SSD Storage Quota Tuning**: Requested and secured a **1,000 GB Regional SSD quota** in `europe-west3`.
3. **Rigorous Capacity Planning**:
   - Calculated the storage profile: Gerrit uploader StatefulSet replicas are configured at **270 GiB** each.
   - With 2 active replicas, the total storage consumed is **540 GiB** (\(2 \times 270\text{ GiB}\)), fitting safely within the approved 1,000 GB regional SSD quota limit.
   - Disabled the server-side SSD cache (`server.cache.enabled = false`) to eliminate any extra storage footprint on the server.
   
```
[Total Approved Quota: 1000 GiB]
├── Allocated: 540 GiB (Uploader 0 + Uploader 1)  [==================== 54%]
└── Free Margin: 460 GiB                         [================== 46%]
```

---

## 3. Spanner IAM Policy & Workload Identity Alignment

> [!CAUTION]
> **Technical Issue:**
> The Config Connector (KCC) operator running in the core cluster was blocked from updating the project's IAM policy, resulting in `UpdateFailed` errors for `iampolicymember` resources. This left the Google Service Account (GSA) `abfs-runtime` without the necessary permissions, and the Kubernetes Service Accounts (KSAs) on the dedicated cluster had no Workload Identity bindings or annotations.

### Root-Cause Analysis
- GKE Workload Identity requires precise bidirectional bindings:
  1. The KSA must be annotated with the GSA email.
  2. The GSA must carry an IAM policy binding granting `roles/iam.workloadIdentityUser` to the KSA's identity string: `serviceAccount:<project-id>.svc.id.goog[<namespace>/<ksa-name>]`.
- If KCC lacks broad project-level IAM permission or is located on a separate control-plane cluster, these bindings will fail to reconcile automatically, leaving pods unable to authenticate with Spanner or GCS.

### Resolution & Automation Script
Developed and executed an automated terminal script to bypass KCC's IAM limitation and bind the identities securely:
1. **Grant Project-Level Roles to GSA**:
   - `roles/spanner.databaseUser` (creates sessions/accesses data)
   - `roles/storage.objectAdmin` (manages AOSP storage)
   - `roles/secretmanager.secretAccessor` (accesses pusher-config git credential secrets)
   - `roles/logging.logWriter` & `roles/monitoring.metricWriter`
2. **Bind GSA to cluster KSAs via Workload Identity**:
   ```bash
   gcloud iam service-accounts add-iam-policy-binding "abfs-runtime@horizon-sdv-deploy-3.iam.gserviceaccount.com" \
     --role="roles/iam.workloadIdentityUser" \
     --member="serviceAccount:horizon-sdv-deploy-3.svc.id.goog[abfs/abfs-server]"
   ```
3. **Annotate KSAs**: Annotated the KSAs `abfs-server` and `abfs-uploader` with the GSA email in the `abfs` namespace.

---

## 4. CASFS Kernel Module Compilation & Host OS Pinning

### Technical Insight
The `abfs-casfs-installer` DaemonSet compiles and loads the `casfs` virtual filesystem kernel module directly into the GKE node's host operating system kernel.
- **Kernel Header Tight-Coupling**: Because compilation is tightly coupled with host Linux kernel headers, GKE nodes running standard Container-Optimized OS (COS) must have their versions strictly pinned.
- **GKE Management Policy**: Under standard platform configurations, enabling `auto_upgrade` will trigger automatic cluster upgrades, updating the underlying kernel. If the CASFS compilation script does not contain immediate support for the new kernel version, GKE node scaling will succeed but the CASFS installer will crash loop, breaking the file system mounts on Gerrit uploaders.
- **Best Practice**: For clusters employing custom compiled kernel modules, **always disable node-pool auto-upgrade** (`auto_upgrade = false`) and manage cluster updates through scheduled, validated maintenance windows.

---

## 5. Quick-Reference Troubleshooting Checklist

| Symptom | Probable Cause | Diagnostic Command | Remediation Action |
|:---|:---|:---|:---|
| **`kube-dns` pods in `Pending`** | Custom node taints applied to the sole node pool. | `kubectl get pods -n kube-system -o wide` | Untaint the GKE node pool or make the taint conditional in Terraform. |
| **GKE Nodes fail to scale up** | Global GCE CPU or Regional SSD quota exceeded. | `gcloud compute project-info describe --format="yaml(quotas)"` | Submit a Quota Increase Request (QIR) via the GCP Console. |
| **ABFS Server fails licensing check** | Cluster DNS failure or metadata server blocked. | `kubectl logs deployment/abfs-server -n abfs` | Verify `kube-dns` is running and the node pool is in GCE-metadata mode. |
| **`UpdateFailed` on `iampolicymember`** | Config Connector lacks IAM project ownership. | `kubectl describe iampolicymember -n abfs` | Apply bindings manually using the `apply_iam_bindings.sh` script. |
| **`abfs-gerrit-uploader` crash looping** | `casfs` kernel module not loaded on the host node. | `kubectl logs daemonset/abfs-casfs-installer -n abfs` | Verify host kernel version and ensure node pool allows signed/unsigned custom modules. |
