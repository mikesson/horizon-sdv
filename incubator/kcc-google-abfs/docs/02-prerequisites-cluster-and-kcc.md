# Prerequisites: the GKE cluster + Config Connector

This solution runs Config Connector (KCC) inside an existing GKE Standard
cluster and manages only Google Cloud resources; it does not create the cluster.
This document specifies the cluster properties the rest of the solution assumes,
and how to install and bind KCC. None of this is created by `infra/` or
`chart/` — it is the contract your cluster must satisfy before you deploy.

## 1. Required cluster properties (GKE Standard)

The ABFS workloads are privileged FUSE/kernel-FS containers, so the cluster must
provide:

| Requirement | Why | How |
|-------------|-----|-----|
| **GKE Standard** (not Autopilot), **regional** control plane | Autopilot rejects `privileged` + `hostPID` broadly; regional control plane for HA | `gcloud container clusters create … --region REGION` (regional) |
| **DNS-based control-plane endpoint** (DNS auth) | IAM-gated (`container.clusters.connect`) kubectl access via `*.gke.goog` — works from restricted/egress-filtered networks without IP allowlisting; pairs with private nodes + authorized networks | `--enable-dns-access` (optionally `--no-enable-ip-access` to drop the public IP endpoint). This is how kubectl reaches the control plane when the public IP is unreachable |
| **Node image = Container-Optimized OS**; the **`casfs` kernel module** loadable on the `abfs-data` pool | ABFS CASFS mounts | `cos_containerd`. casfs comes from `casfs.provider`: **`image`** (default/forward — built into the COS image Google ships going forward) or **`daemonset`** (legacy/interim — the chart's `abfs-casfs-installer` loads a signed module; the pool then needs `--enable-kernel-module-signature-enforcement`, see [§1b](#1b-create-the-dedicated-abfs-node-pool)). On COS, **Loadpin** (not Secure Boot) gates module loading |
| **Workload Identity** | KSA→GSA auth for KCC (the `cnrm-controller-manager` → `cnrm-system` impersonation depends on it) — keep it enabled cluster-wide | `--workload-pool=PROJECT_ID.svc.id.goog` |
| **A dedicated `abfs-data` node pool, running as the licensed runtime SA in GCE-metadata mode** | ABFS's license enforcement is GCE-VM-specific: the server reads the license from **node instance metadata** and runs an entitlement check that needs the `google.compute_engine` claim only a GCE VM identity carries. Running the data plane on a pool whose **node service account is the licensed runtime SA**, with Workload Identity **bypassed on that pool** (`--workload-metadata=GCE_METADATA`), makes the pod's identity token a real GCE token for the licensed SA — so both checks pass natively, with no Secret and no WI for the data plane | Create the pool per [§1b](#1b-create-the-dedicated-abfs-node-pool); the chart selects it via `nodeSelector: cloud.google.com/gke-nodepool: abfs-data` and tolerates its `abfs.dev/dedicated=true:NoSchedule` taint |
| **PD CSI driver** (default) + **Hyperdisk Balanced** | Uploader `volumeClaimTemplates` + optional server cache | Apply `infra/setup/storageclass-hyperdisk-balanced.yaml` — Titanium families (N4D/N4/C4D) are **Hyperdisk-only**, so `pd-ssd` won't bind |
| **Vertical Pod Autoscaler addon** | Right-size the workload requests | `--enable-vertical-pod-autoscaling`; the chart ships VPA objects (`vpa.updateMode`, default `Off` = recommendations) |
| **Managed Prometheus + Managed OpenTelemetry** | Metrics (GA) + OTLP traces/metrics/logs (preview) to Cloud Observability | `--enable-managed-prometheus`; `--managed-otel-scope=COLLECTION_AND_INSTRUMENTATION_COMPONENTS` (preview). Chart wires pods to the collector when `otel.enabled` |
| **DataPlane V2** | eBPF dataplane that **enforces the chart's `NetworkPolicy` objects** (without an enforcer they are silently inert) + network-policy logging + better observability | `--enable-dataplane-v2` (mutually exclusive with Calico `--enable-network-policy`) |
| **Private nodes + Cloud NAT / Private Google Access** | Keep nodes off the public internet; reach Spanner/GCS/AR privately | On your existing VPC subnet |
| **Network attachment to a pre-existing VPC** | Use an existing VPC; standalone (default) or Shared VPC | Cluster on the chosen network/subnet. For Shared VPC, host-project IAM for the GKE service agents must be in place |

> The chart's privileged pods schedule onto the dedicated `abfs-data` pool: they
> select `nodeSelector: cloud.google.com/gke-nodepool: abfs-data` and tolerate its
> `abfs.dev/dedicated=true:NoSchedule` taint (both set in `values.yaml`).

## 1a. Reference: create the cluster

The command below is a reference cluster configuration that satisfies the
requirements above. The default node pool runs **only system + KCC**; the ABFS
workloads land on the dedicated `abfs-data` pool you create in [§1b](#1b-create-the-dedicated-abfs-node-pool).
Adjust project / region / network for your environment. `--managed-otel-scope`
is preview, so the command uses `gcloud beta`.

```bash
gcloud beta container --project "PROJECT_ID" clusters create "abfs" \
  --region "REGION" \
  --release-channel "regular" --cluster-version "1.35.5-gke.1057002" \
  --network    "projects/PROJECT_ID/global/networks/NETWORK_NAME" \
  --subnetwork "projects/PROJECT_ID/regions/REGION/subnetworks/SUBNET_NAME" \
  --enable-ip-alias --enable-auto-ipam --cluster-ipv4-cidr "/17" --default-max-pods-per-node "48" \
  --enable-private-nodes --enable-master-authorized-networks --no-enable-google-cloud-access \
  --enable-dns-access --enable-k8s-tokens-via-dns --enable-k8s-certs-via-dns --enable-ip-access \
  --enable-dataplane-v2 --enable-dataplane-v2-metrics --enable-dataplane-v2-flow-observability \
  --cluster-dns=clouddns --cluster-dns-scope=cluster \
  --workload-pool "PROJECT_ID.svc.id.goog" --image-type "COS_CONTAINERD" \
  --enable-shielded-nodes --shielded-integrity-monitoring --shielded-secure-boot \
  --machine-type "n4d-standard-4" --disk-type "hyperdisk-balanced" --disk-size "100" \
  --num-nodes "1" --enable-autoscaling --min-nodes "1" --max-nodes "3" --location-policy "BALANCED" \
  --enable-vertical-pod-autoscaling \
  --enable-managed-prometheus --managed-otel-scope=COLLECTION_AND_INSTRUMENTATION_COMPONENTS \
  --logging=SYSTEM,WORKLOAD \
  --monitoring=SYSTEM,STORAGE,POD,DEPLOYMENT,STATEFULSET,DAEMONSET,HPA,JOBSET,CADVISOR,KUBELET \
  --addons HorizontalPodAutoscaling,HttpLoadBalancing,NodeLocalDNS,GcePersistentDiskCsiDriver \
  --enable-image-streaming --enable-secret-manager \
  --enable-autoupgrade --enable-autorepair --max-surge-upgrade 1 --max-unavailable-upgrade 0 \
  --no-enable-basic-auth --metadata disable-legacy-endpoints=true \
  --security-posture=standard --workload-vulnerability-scanning=disabled \
  --binauthz-evaluation-mode=DISABLED
```

Why this configuration looks the way it does:
- **Region matches the subnet**; a custom VPC also requires `--subnetwork`.
- **Default pool = system/KCC only** (`n4d-standard-4`, autoscale 1–3 per zone). ABFS pods
  do NOT run here — they land on the dedicated `abfs-data` pool created in [§1b](#1b-create-the-dedicated-abfs-node-pool),
  which carries the licensed runtime SA and the license metadata. Keep this pool small;
  size the ABFS workloads on `abfs-data` instead.
- **Secure Boot stays ON** (`--shielded-secure-boot`). On COS, Secure Boot has **no effect**
  on kernel-module loading — the **Loadpin** LSM controls that separately — so it is kept on
  for hardening on every pool. Out-of-tree casfs loading on `abfs-data` (legacy mode) is
  enabled via **secure kernel module loading**, not by disabling Secure Boot — see
  [§1b](#1b-create-the-dedicated-abfs-node-pool).
- **`hyperdisk-balanced`** boot disk — Titanium families are Hyperdisk-only. The uploader
  4 TiB **data** disks come from the `hyperdisk-balanced` StorageClass (PVCs), not the boot disk.
- **DPv2** (enforces the chart NetworkPolicies) + **DNS-based control-plane access**
  (IAM-gated) + **Managed Prometheus / OTel / VPA** to match the chart wiring.
- Omits `DCGM` from `--monitoring` (GPU-only); keeps NodeLocal DNS + Cloud DNS, image
  streaming (large ABFS image), and the Secret Manager add-on.
- **After create:** `kubectl apply -f infra/setup/storageclass-hyperdisk-balanced.yaml`
  (see §0b of the runbook), then create the `abfs-data` pool ([§1b](#1b-create-the-dedicated-abfs-node-pool)).

## 1b. Create the dedicated ABFS node pool

ABFS's license enforcement is GCE-VM-specific and **incompatible with Workload
Identity**. The server binary (1) reads its license as **base64-encoded JSON from
GCE instance metadata** under the key `abfs-license` — not from a mounted file or
Kubernetes Secret — and (2) runs an entitlement check that requires the
`google.compute_engine` claim, which is present only in a GCE VM instance identity
token and is Google-signed (so it cannot be forged). GKE Workload Identity tokens
lack that claim, so the data plane cannot run under WI.

The fix is a dedicated node pool, `abfs-data`, whose **node service account is the
licensed runtime SA** and which runs in **GCE-metadata mode**
(`--workload-metadata=GCE_METADATA`, i.e. WI bypassed for this pool only). With the
license placed in the node's `abfs-license` metadata, every pod on the pool presents
a real GCE VM identity token for the licensed SA — carrying both the expected email
**and** the `google.compute_engine` claim — so both license checks pass natively,
with no Secret mount and no Workload Identity for the data plane. The rest of the
cluster keeps WI (KCC's `cnrm-controller-manager` → `cnrm-system` impersonation
depends on it) — do **not** disable WI cluster-wide.

```bash
# base64-encode the emailed license JSON (the server base64-decodes the metadata value):
base64 -w0 abfs-license.json > abfs-license.b64

gcloud container node-pools create abfs-data \
  --cluster CLUSTER --region REGION --project PROJECT_ID \
  --service-account RUNTIME_SA@PROJECT_ID.iam.gserviceaccount.com \
  --workload-metadata=GCE_METADATA \
  --scopes=cloud-platform \
  --metadata-from-file abfs-license=./abfs-license.b64 \
  --metadata disable-legacy-endpoints=true \
  --machine-type n4d-standard-4 --disk-type hyperdisk-balanced --disk-size 100 \
  --num-nodes 1 --enable-autoscaling --min-nodes 0 --max-nodes 4 \
  --shielded-secure-boot --shielded-integrity-monitoring \
  --node-taints abfs.dev/dedicated=true:NoSchedule \
  --image-type COS_CONTAINERD
# LEGACY casfs (casfs.provider=daemonset), until casfs ships in COS — ADD:
#   --enable-kernel-module-signature-enforcement   # allow signed OOT modules (Loadpin)
#   --no-enable-autoupgrade                         # pin the COS version; pre-stage the
#                                                   # signed module for a new BUILD_ID first
```

Notes:
- **`--service-account RUNTIME_SA@…`** makes the licensed runtime SA the node
  identity, so the pod identity token is a GCE token for that SA. This SA is therefore
  **both** the workload identity **and** the node identity; grant it Artifact Registry
  read on the image repo plus the project roles in `infra/12` (Spanner, Storage,
  monitoring, logging). The caller running this command needs
  `iam.serviceAccounts.actAs` on `RUNTIME_SA`.
- **`--workload-metadata=GCE_METADATA`** bypasses Workload Identity on this pool so
  the GCE metadata server (and thus the GCE VM identity token) is exposed to pods.
- **`--metadata-from-file abfs-license=./abfs-license.b64`** is where the server reads
  the license. Rotating the license means updating this metadata and recreating/rolling
  the pool's nodes.
- **casfs on this pool.** ABFS needs the `casfs` kernel module loaded on these nodes
  (chart `casfs.provider`):
  - **`image` (default / forward path):** casfs is built into the COS image Google ships
    going forward — nothing extra to do here, and the pool can auto-upgrade normally.
  - **`daemonset` (legacy / interim):** until casfs is in COS, the chart's
    `abfs-casfs-installer` DaemonSet loads a **Google-signed** `casfs.ko` per node. COS's
    **Loadpin** LSM blocks out-of-tree modules on CPU nodes by default, so add
    **`--enable-kernel-module-signature-enforcement`** (policy `ENFORCE_SIGNED_MODULES`;
    requires cgroup v2 + GKE ≥ 1.34.1-gke.2364000) to permit loading *signed* modules, and
    **pin the pool's COS version** (`--no-enable-autoupgrade`) so you can pre-stage the
    signed module for a new BUILD_ID before upgrading. Secure Boot is irrelevant here —
    Loadpin, not Secure Boot, is the gate on COS. Verify with `modinfo casfs` on a node.
- **`--node-taints abfs.dev/dedicated=true:NoSchedule`** keeps general workloads off the
  pool; the chart's pods tolerate it and select the pool by name.
- **Sizing:** the values above are a baseline. Production should raise `--machine-type`
  and `--max-nodes` to fit the ABFS server (high-memory) plus the uploaders.

## 2. Install Config Connector in the cluster

Config Connector supports two in-cluster modes; this solution uses **namespaced
mode** (one KCC identity per namespace), the recommended multi-tenant posture.

1. **Install the operator** (once per cluster):
   ```bash
   gcloud container clusters get-credentials ABFS_CLUSTER --region REGION --project PROJECT_ID
   # Install the Config Connector operator (see KCC install docs for the bundle URL/version).
   ```
2. **Create the KCC Google service account** and grant it the roles needed to
   manage everything in `infra/` (broadly: `roles/owner` or a tighter custom set
   covering Spanner, Storage, IAM admin, Secret Manager, Compute, DNS, Cloud
   Build, Secure Source Manager, Cloud Scheduler, Workstations, Private CA,
   Service Usage):
   ```bash
   gcloud iam service-accounts create cnrm-system --project PROJECT_ID
   gcloud projects add-iam-policy-binding PROJECT_ID \
     --member="serviceAccount:cnrm-system@PROJECT_ID.iam.gserviceaccount.com" \
     --role="roles/owner"
   gcloud iam service-accounts add-iam-policy-binding \
     cnrm-system@PROJECT_ID.iam.gserviceaccount.com \
     --member="serviceAccount:PROJECT_ID.svc.id.goog[cnrm-system/cnrm-controller-manager-abfs]" \
     --role="roles/iam.workloadIdentityUser"
   ```
3. **Apply the operator CRs** in [`../infra/setup/`](../infra/setup/):
   - `configconnector.yaml` — cluster-wide `ConfigConnector` (mode: namespaced).
   - `configconnectorcontext.yaml` — binds the `abfs` namespace to the KCC GSA.
4. **Annotate the target namespace with the project id** (done by
   `infra/00-namespace.yaml`):
   ```yaml
   metadata:
     annotations:
       cnrm.cloud.google.com/project-id: "PROJECT_ID"
   ```

> If you would rather use **Config Controller** (Google-managed KCC) as a
> separate management cluster, the `infra/` manifests apply unchanged — only the
> install/binding steps above differ. This guide documents the in-cluster path.

## 3. APIs

KCC needs the relevant Google Cloud APIs enabled on the project. `infra/01-services.yaml`
enables them declaratively via `serviceusage` `Service` resources, **but** the
Service Usage and Config Connector APIs themselves, plus the KCC GSA, must exist
before KCC can reconcile anything (bootstrap). Enable at minimum
`serviceusage.googleapis.com`, `cloudresourcemanager.googleapis.com`, and
`iam.googleapis.com` manually first.

## 4. Verify before proceeding

```bash
kubectl get crds | grep cnrm.cloud.google.com                                 # KCC CRDs present
kubectl get configconnectorcontext -n abfs                                     # healthy
kubectl get nodes -l cloud.google.com/gke-nodepool=abfs-data                   # abfs-data nodes Ready
kubectl describe node <abfs-data-node> | grep -i taint                         # abfs.dev/dedicated=true:NoSchedule present
modinfo casfs                                                                  # (on an abfs-data node) module available
gcloud compute instances describe <abfs-data-node> --zone ZONE \
  --format='value(metadata.items.filter("key:abfs-license").extract("value"))' # license metadata present
kubectl get storageclass                                                       # hyperdisk-balanced present
```

For day-2 configuration knobs see [Configuration](./05-configuration.md); if a
prerequisite check fails, see [Troubleshooting](./07-troubleshooting.md).
