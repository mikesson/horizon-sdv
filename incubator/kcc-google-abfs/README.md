# ABFS on GKE Standard with Config Connector (KCC)

Run the **Android Build File System (ABFS)** as Kubernetes workloads on **GKE
Standard**, with all Google Cloud resources managed declaratively by **Config
Connector (KCC)**. This is a self-contained KCC/GKE + Helm deployment of ABFS:
the cluster pre-exists, KCC manages the Google Cloud resources, and Helm deploys
the ABFS workloads.

![ABFS on GKE — architecture](diagrams/d1_architecture.svg)

```
.
├── docs/      overview, architecture, prerequisites, deployment, configuration, troubleshooting, CI/CD
├── infra/     KCC resources (Spanner, GCS, IAM runtime SA + roles, Secret Manager, firewall/DNS, APIs)
│   ├── setup/ one-time Config Connector operator CRs
│   └── cicd/  Cloud Workstations CI/CD foundation (SSM, Cloud Build, AR, Scheduler, Workstations, Private CA)
└── chart/abfs Helm chart (server, uploaders, bootstrap Job, services, NetworkPolicy)
```

## Design decisions

| Decision | Choice |
|----------|--------|
| Platform | GKE **Standard** (privileged + hostPID needed; Autopilot excluded) |
| Cluster | **Pre-exists**; KCC runs in-cluster managing only Google Cloud resources |
| CASFS | ABFS mounts the `casfs` filesystem, so the **`casfs` kernel module** must be loaded on each `abfs-data` node. Two modes via `casfs.provider`: **`image`** (default) — casfs built into the node image Google ships going forward; **`daemonset`** (legacy/interim) — `abfs-casfs-installer` loads a Google-signed `casfs.ko` until casfs ships in COS (needs the pool to allow signed modules, `ENFORCE_SIGNED_MODULES`; gated by Loadpin, not Secure Boot). See [`docs/02` §1b](./docs/02-prerequisites-cluster-and-kcc.md#1b-create-the-dedicated-abfs-node-pool) |
| Licensing & identity | Data plane runs on a dedicated **`abfs-data`** node pool whose node SA **is** the licensed runtime SA, in **GCE-metadata mode** (license in node metadata, base64 JSON) — **not** Workload Identity. WI stays cluster-wide for KCC only |
| Spanner schema | **ABFS owns it** — `SpannerDatabase.spec.ddl` empty so KCC never manages DDL |
| Network | **Pre-existing VPC** referenced, not created (standalone by default; Shared VPC optional) |
| Scope | Core ABFS data plane **+** full Workstations CI/CD foundation |

## Documentation

- [Overview](./docs/00-overview.md) — what this deploys and the key concepts (start here).
- [Architecture](./docs/01-architecture.md) — components, traffic, storage, identity.
- [Prerequisites](./docs/02-prerequisites-cluster-and-kcc.md) — required cluster properties + installing KCC.
- [Spanner schema ownership](./docs/03-spanner-schema-ownership.md) — why `ddl` is empty.
- [Deployment runbook](./docs/04-deployment-runbook.md) — step-by-step apply order + two-phase licensing.
- [Configuration](./docs/05-configuration.md) — instance values and key chart settings.
- [CI/CD foundation](./docs/06-cicd-foundation.md) — the optional Cloud Workstations pipeline.
- [Troubleshooting](./docs/07-troubleshooting.md) — symptoms, causes, and fixes.

## Quick start (summary — full steps in [`docs/04-deployment-runbook.md`](./docs/04-deployment-runbook.md))

```bash
# 0. Prereqs: existing GKE Standard cluster (COS, Workload Identity for KCC, PD CSI)
#    + a dedicated abfs-data node pool whose node SA is the licensed runtime SA in
#    GCE-metadata mode (see docs/02 §1b), with the Config Connector operator
#    installed. The casfs kernel module must be loadable on the abfs-data nodes:
#    default casfs.provider=image (casfs built into the node image); legacy
#    casfs.provider=daemonset loads a Google-signed module and needs the pool to
#    allow signed modules (ENFORCE_SIGNED_MODULES) — see docs/02 §1b.

# 1. Set instance values + render deployable manifests (source stays templated)
cp instances/example.env instances/myinstance.env   # edit PROJECT_ID, NETWORK_NAME, ...
scripts/render.sh instances/myinstance.env               # -> rendered/myinstance/
RENDER=rendered/myinstance

# 2. Bind KCC to the namespace (one-time)
kubectl apply -f $RENDER/infra/setup/configconnector.yaml
kubectl apply -f $RENDER/infra/00-namespace.yaml
kubectl apply -f $RENDER/infra/setup/configconnectorcontext.yaml

# 3. Infra (core)
kubectl apply -k $RENDER/infra/

# 4. Infra (CI/CD foundation — optional; fill its tokens in your .env first)
kubectl apply -k $RENDER/infra/cicd/

# 5. App — Phase A (gated off): print the runtime SA, submit it in the EAP form
helm install abfs $RENDER/chart/abfs -n abfs -f $RENDER/chart/abfs/values.yaml \
  --set licensed=false

# 6. License received: create the licensed abfs-data node pool with the license in
#    its node metadata (see docs/02 §1b), then start ABFS
base64 -w0 abfs-license.json > abfs-license.b64   # license goes in node metadata, not Helm
helm upgrade abfs $RENDER/chart/abfs -n abfs -f $RENDER/chart/abfs/values.yaml \
  --set licensed=true
```

## Validation

Requires **Helm 4** (the chart is apiVersion v2, which runs on Helm 4 unchanged).
The chart and manifests pass these checks:

- `helm lint ./chart/abfs` → passes (Helm v4.2.2). `values.schema.json` (JSON Schema
  draft 2020-12) validates inputs at lint/template/install and rejects bad values
  (e.g. unknown `vpa.updateMode`, non-enum `image.pullPolicy`, `uploader.count: 0`,
  malformed `image.digest`).
- `helm template` renders correctly in **both** license phases (Phase A,
  `licensed=false`: SAs + pusher ConfigMap only; Phase B, `licensed=true`: +
  Deployment, StatefulSet, Services, hook Job, NetworkPolicies, PodDisruptionBudgets,
  VPAs, helm-test Pod). Full render parses as valid YAML.
- Workloads carry the full `app.kubernetes.io/*` label set, startup/readiness/liveness
  probes, pod+container `securityContext` (privileged only where casfs requires it;
  hardened init/bootstrap containers), config checksums, and structured/digest-pinnable
  images. Selector labels are unchanged (immutable on live objects).
- Pusher-config names render as `abfs-gerrit-uploader-0..N`, matching the uploader
  StatefulSet pod hostnames.
- `kubectl kustomize infra/` and `kubectl kustomize infra/cicd/` both build; every
  manifest parses as YAML.

> These are static checks (lint, template, build). Validate against your own
> Google Cloud project and cluster before production use.

## Known caveats (see docs for detail)

- **License consumption is GCE-metadata-based, not a Secret.** A live deploy proved
  the ABFS server reads its license from the `abfs-data` node pool's `abfs-license`
  instance metadata (base64 JSON) and its entitlement check requires a GCE VM identity
  token for the licensed SA — so the data plane runs on a dedicated node pool whose
  node SA is the runtime SA, in GCE-metadata mode (Workload Identity bypassed for that
  pool). See [`docs/02` §1b](./docs/02-prerequisites-cluster-and-kcc.md#1b-create-the-dedicated-abfs-node-pool).
- **CI/CD foundation** (`infra/cicd/`) is a **functional** KCC implementation of the
  open-source `cicd-foundation` building block — per-image Cloud Build triggers +
  schedulers, Workstation configs, OOM monitoring, and an SSM-seed Job (built from the
  upstream's documented behavior + the KCC CRDs). A few values are
  server-assigned/org-specific and a real build needs the seeded source + a live
  cluster — see [`docs/06`](./docs/06-cicd-foundation.md) for the operational steps.
- **Existing VPC**: firewall + DNS resources reference the pre-existing network via
  `networkRef.external` and inherit the namespace's project; the VPC is not created.
  For a Shared VPC, add a per-resource `cnrm.cloud.google.com/project-id` host-project
  annotation and point `networkRef` at the host network.
