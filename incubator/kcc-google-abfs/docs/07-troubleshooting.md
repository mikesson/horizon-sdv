# Troubleshooting

Common symptoms when deploying ABFS on GKE, with their cause and fix. Each entry
assumes you are working against the `abfs` namespace (`-n abfs`).

## Access and infrastructure

### `kubectl` can't reach the cluster
The control plane is reachable through an IAM-gated DNS endpoint, which works from
restricted or egress-filtered networks where the public IP is not reachable.

```bash
gcloud container clusters update CLUSTER --region REGION --enable-dns-access
gcloud container clusters get-credentials CLUSTER --region REGION --dns-endpoint
```

You need the `container.clusters.connect` permission. See
[`02-prerequisites-cluster-and-kcc.md`](./02-prerequisites-cluster-and-kcc.md).

### A Config Connector resource never becomes Ready
```bash
kubectl get configconnectorcontext -n abfs                 # controller healthy?
kubectl describe <kind> <name> -n abfs                     # events / message
```
Usual causes:
- The required API isn't enabled yet. The `serviceusage` `Service` resources in
  `infra/01-services.yaml` enable them, but `serviceusage`,
  `cloudresourcemanager`, and `iam` must be enabled manually first (bootstrap).
- The KCC Google service account lacks a role for that resource — grant it (or use
  a broad role during bring-up, then tighten).
- A referenced resource (network, project, secret) doesn't exist or the `external`
  link is wrong.

## Workloads won't start

### Only the service accounts and pusher ConfigMap render (no server/uploaders)
This is **expected** while the chart is unlicensed — the data plane is gated on the
`licensed` value (default `false`). Once the licensed `abfs-data` node pool exists
(the license lives in its node metadata — see
[`02-prerequisites-cluster-and-kcc.md` §1b](./02-prerequisites-cluster-and-kcc.md#1b-create-the-dedicated-abfs-node-pool)),
start Phase B:

```bash
helm upgrade abfs ./chart/abfs -n abfs -f chart/abfs/values.yaml \
  --set licensed=true
```

### Pods are rejected at admission (Pod Security)
The ABFS pods run `privileged` + `hostPID`. The `abfs` namespace is labeled
`pod-security.kubernetes.io/enforce: privileged` for this reason. If pods are still
rejected, a cluster-wide Pod Security Admission default or an external admission
webhook (e.g. Gatekeeper/Policy Controller) is blocking privileged pods — allow
the `abfs` namespace there.

### Pods stay `Pending`
- **No schedulable nodes on the `abfs-data` pool.** ABFS pods select
  `cloud.google.com/gke-nodepool: abfs-data` and tolerate its
  `abfs.dev/dedicated=true:NoSchedule` taint. Confirm the dedicated pool exists, has
  Ready nodes, and can scale up:
  ```bash
  kubectl get nodes -l cloud.google.com/gke-nodepool=abfs-data
  ```
  If there are none, create or scale the pool (see
  [`02-prerequisites-cluster-and-kcc.md` §1b](./02-prerequisites-cluster-and-kcc.md#1b-create-the-dedicated-abfs-node-pool));
  the machine type must be large enough for the pod's requests.
- **PVC won't bind.** The `hyperdisk-balanced` StorageClass uses
  `WaitForFirstConsumer`, so the disk is created only once the pod is scheduled. If
  the pod can't schedule, the PVC stays Pending too — fix scheduling first.

### Pods stuck in `Init` on `wait-for-casfs`
ABFS pods run a `wait-for-casfs` initContainer (chart value `casfs.requireReady`,
default `true`) that blocks startup until the `casfs` kernel module is loaded on the
node. If pods sit in `Init:0/1`, casfs isn't loaded — confirm on the node:
```bash
kubectl debug node/<node> -it --image=busybox -- chroot /host sh -c 'modinfo casfs || lsmod | grep casfs'
```
The fix depends on `casfs.provider`:
- **`image` mode (default).** casfs is expected to be built into the node image. If
  it's missing, the node image doesn't ship casfs — use an image that includes it, or
  switch the `abfs-data` pool to the legacy `daemonset` mode until casfs is in COS.
- **`daemonset` mode (legacy).** The `abfs-casfs-installer` DaemonSet loads a
  Google-signed `casfs.ko` matched to the node's COS `BUILD_ID`. If casfs isn't
  loaded, the installer failed — check its logs and the signed-module source:
  ```bash
  kubectl logs -n abfs -l app.kubernetes.io/name=abfs-casfs-installer
  ```
  Confirm it found a signed module for this node's `BUILD_ID`, and that the pool
  allows loading it (see the next entry).

### `casfs` module load denied (Loadpin / unsigned module)
On Container-Optimized OS the gate on loading out-of-tree modules is the **Loadpin**
LSM, **not** Secure Boot — turning Secure Boot off has no effect on module loading.
CPU/TPU COS nodes reject all out-of-tree modules by default; the supported way to
allow them is GKE secure kernel module loading with Google-signed modules. If the
kernel rejects `casfs.ko` (e.g. `Loadpin`/`module verification failed` in the
installer logs or `dmesg`):
- The `abfs-data` pool is missing `--enable-kernel-module-signature-enforcement`
  (policy `ENFORCE_SIGNED_MODULES`). Recreate/configure the pool with it.
- Or the module isn't Google-signed — only Google-signed modules are accepted under
  `ENFORCE_SIGNED_MODULES`.

See [`02-prerequisites-cluster-and-kcc.md` §1b](./02-prerequisites-cluster-and-kcc.md#1b-create-the-dedicated-abfs-node-pool).

### Server pod is in `CrashLoopBackOff`
- **Image not reachable.** Check the pod events for `ImagePullBackOff` and confirm
  `image.repository`/`tag`/`digest` and pull permissions (the runtime SA needs
  Artifact Registry read).
- **License rejected (node-metadata model).** ABFS reads its license from the
  `abfs-data` pool's node metadata, not a Secret, and validates a GCE VM identity
  token. The most likely server log errors and their cause:
  - `no google claim found in ID token` — the pod is not on the `abfs-data`
    GCE-metadata pool, or that pool's node SA is not the licensed runtime SA. Confirm
    the pod landed on `abfs-data` and the pool runs with
    `--workload-metadata=GCE_METADATA` and `--service-account RUNTIME_SA@…`.
  - `GCE metadata abfs-license not defined` — the license is missing from node
    metadata. Set it on the pool (`--metadata-from-file abfs-license=./abfs-license.b64`).
  - `failed to decode base64 license config` — the metadata value is not base64.
    Re-encode with `base64 -w0 abfs-license.json`.

  See [`02-prerequisites-cluster-and-kcc.md` §1b](./02-prerequisites-cluster-and-kcc.md#1b-create-the-dedicated-abfs-node-pool).

### Uploaders never become Ready
Each uploader waits (via an init container) for the server to be reachable **and**
for the pusher-config reference to exist. Check the bootstrap Job first:

```bash
kubectl logs -n abfs job/abfs-pusher-config
```
If the Job failed, the uploaders will keep waiting. The Job runs the
`init / cacheman / push / put-ref` sequence against the in-cluster server — make
sure the server is Ready before debugging the Job.

## Networking

### The internal load balancer IP is stuck `<pending>`
```bash
kubectl describe svc -n abfs abfs-server
```
GKE provisions the internal L4 LB and its health-check firewall automatically.
A pending IP usually means a subnet/quota issue or a missing proxy-only subnet for
the region — check the Service events.

### NetworkPolicies aren't taking effect
NetworkPolicy is only enforced when an enforcer is present. Create the cluster with
**DataPlane V2** (`--enable-dataplane-v2`); without it the chart's NetworkPolicy
objects exist but are silently inert.

## Spanner

### How do I apply the database schema?
The deploy applies it — **the ABFS server does not self-migrate.** With `CREATE_TABLES=true`,
KCC applies the bundled schema via `SpannerDatabase.spec.ddl`; with `CREATE_TABLES=false` the
database is an empty shell, so apply it out-of-band:
`gcloud spanner databases ddl update abfs --instance=abfs --project=PROJECT_ID --ddl-file=infra/schemas/0.0.31-schema.sql`.
Symptom of a missing schema: server/clients fail with `Table not found: Objects/Projects/...`.
See [`03-spanner-schema-ownership.md`](./03-spanner-schema-ownership.md).

## CI/CD foundation

### The first Cloud Build never fires
The build is triggered by the first push to the Secure Source Manager repo. Run the
seed Job after the repo reconciles, and confirm the webhook secret and trigger IDs
are wired. The full sequence is in
[`06-cicd-foundation.md`](./06-cicd-foundation.md).

## Useful one-liners

```bash
kubectl get pods -n abfs
kubectl get spannerinstance,spannerdatabase,storagebucket,iamserviceaccount -n abfs
kubectl get svc -n abfs abfs-server -o wide        # internal LB IP for clients
kubectl get statefulset -n abfs abfs-gerrit-uploader
helm test abfs -n abfs                              # server connectivity probe
kubectl describe vpa -n abfs abfs-server           # right-sizing recommendations
```
