# ABFS Module Agent Guidelines

This document provides specialized, highly actionable guidelines, pre-requisites, and troubleshooting runbooks for AI coding agents and human operators deploying, managing, and maintaining the **Android Build File System (ABFS)** module in the Horizon SDV platform.

---

## 1. What this Module Does

The ABFS module integrates the **Android Open Source Project (AOSP)** build and synchronization engine into the Horizon platform. It consists of:
- **`casfs` Installer**: A daemonset that dynamically compiles and loads a high-efficiency kernel-level virtual filesystem driver onto GKE worker nodes.
- **ABFS Server**: A high-performance metadata service that orchestrates build trees and file caching backed by **Cloud Spanner**.
- **Gerrit Uploaders**: Background stateful uploaders that continuously synchronize the Android AOSP code trees into the environment's storage.

---

## 2. Infrastructure Requirements & Guardrails

To prevent silent failures during deployment, an agent must verify that the target environment satisfies these exact parameters:

### A. Dedicated GKE Cluster Topology
- **Pristine Kernel Pinning**: The `casfs` kernel module compiles directly against the host Linux kernel. GKE worker nodes **must** have node auto-upgrades strictly disabled (`auto_upgrade = false`) to prevent GKE from performing rolling upgrades that break driver compatibility.
- **Node Pool Taint Override**: On a completely dedicated ABFS cluster (`sdv-abfs-cluster`), do not apply custom taints like `workloadType=android-abfs` to nodes unless explicit system pod tolerations are configured. Otherwise, system-critical pods (like `kube-dns`, `metrics-server`, or `calico-node`) will remain `Pending`, resulting in complete DNS failure and network starvation inside the cluster.

### B. GCP Compute & Storage Quotas
- **vCPU Quota**: The GKE node pool `sdv-abfs-build-node-pool` is configured with `n2-highcpu-32` instances (32 vCPUs per node) to provide adequate compilation power. The target GCP project's global CPU quota `CPUS_ALL_REGIONS` must be increased to at least **64 vCPUs** (or 128 vCPUs for scale-up headroom) to allow GKE to scale up compute nodes.
- **SSD Storage Quota**: The Gerrit uploader replicas are configured with persistent volume claims of **270 GiB** each. For 2 replicas, this consumes **540 GiB** of regional SSD capacity (`premium-rwo`). Ensure the project's regional SSD quota in the chosen zone has at least **1,000 GB** of approved capacity.

---

## 3. Security and Workload Identity (Bidirectional Trust)

To enable secure, keyless authentication to Google Cloud APIs, the Google Service Account (GSA) `abfs-runtime` must have a bidirectional trust relationship configured with the Kubernetes Service Accounts (KSAs) on GKE.

### A. GSA IAM Roles (Google Cloud Console / Terraform)
The `abfs-runtime@<project-id>.iam.gserviceaccount.com` GSA must have the following project-level IAM roles applied:
- `roles/spanner.databaseUser` (creates Spanner sessions and reads schemas)
- `roles/storage.objectAdmin` (manages AAOS build cache artifacts)
- `roles/logging.logWriter` (streams uploader activity logs)
- `roles/monitoring.metricWriter` / `roles/monitoring.viewer`
- `roles/secretmanager.secretAccessor` (accesses bootstrap configuration credentials)

### B. Kubernetes Workload Identity Bindings (KSA-to-GSA)
Run the following `gcloud` commands to bind the GSA to the KSAs (`abfs-server` and `abfs-uploader`) in the `abfs` namespace:
```bash
# Trust binding for abfs-server KSA
gcloud iam service-accounts add-iam-policy-binding abfs-runtime@<PROJECT_ID>.iam.gserviceaccount.com \
    --role="roles/iam.workloadIdentityUser" \
    --member="serviceAccount:<PROJECT_ID>.svc.id.goog[abfs/abfs-server]" \
    --project=<PROJECT_ID>

# Trust binding for abfs-uploader KSA
gcloud iam service-accounts add-iam-policy-binding abfs-runtime@<PROJECT_ID>.iam.gserviceaccount.com \
    --role="roles/iam.workloadIdentityUser" \
    --member="serviceAccount:<PROJECT_ID>.svc.id.goog[abfs/abfs-uploader]" \
    --project=<PROJECT_ID>
```

### C. Kubernetes Service Account Annotations
Each KSA in the `abfs` namespace must be annotated with the GSA email:
```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: abfs-server
  namespace: abfs
  annotations:
    iam.gke.io/gcp-service-account: "abfs-runtime@<PROJECT_ID>.iam.gserviceaccount.com"
```

---

## 4. Operational Playbook & Troubleshooting

If you encounter issues during the ABFS workload rollout, execute these diagnostic and remediation steps:

### A. Core System Pod Starvation (`kube-dns` is stuck in `Pending`)
- **Symptom**: Pods remain in `Pending` with `FailedScheduling` events, and other services cannot resolve GCP API endpoints.
- **Diagnosis**: Verify if GKE nodes have custom application taints applied:
  ```bash
  kubectl get nodes -o custom-columns=NAME:.metadata.name,TAINTS:.spec.taints
  ```
- **Remediation**: Remove the taint from GKE worker nodes to allow system pods to schedule:
  ```bash
  kubectl taint nodes --all workloadType-
  ```

### B. `casfs` Kernel Module Installation Fails
- **Symptom**: `abfs-casfs-installer` DaemonSet pods crash or report errors compiling the driver.
- **Diagnosis**: Check the compilation logs:
  ```bash
  kubectl logs daemonset/abfs-casfs-installer -n abfs -c casfs-installer
  ```
- **Remediation**: Ensure the worker nodes are running GKE worker images with standard Ubuntu kernels. If GKE performed a minor version node upgrade, confirm that node auto-upgrades are fully disabled, and reboot/re-provision the node pool.

### C. Spanner API Access Denied / IAM Authentication Errors
- **Symptom**: `abfs-server` or `abfs-uploader` pods fail to connect to Cloud Spanner, reporting IAM permission errors.
- **Diagnosis**: 
  1. Confirm KSA annotations:
     ```bash
     kubectl get sa abfs-server -n abfs -o yaml
     ```
  2. Confirm Workload Identity credentials inside the container:
     ```bash
     kubectl exec -it deployment/abfs-server -n abfs -c abfs-server -- gcloud auth list
     ```
- **Remediation**: Re-apply the bidirectional IAM trust policy binding on the GSA for the specific namespace and KSA (see Section 3).
