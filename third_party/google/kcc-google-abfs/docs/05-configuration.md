# Configuration

There are two places you configure a deployment:

1. **Instance values** — project/region/network specifics, supplied as
   `REPLACE_*` tokens and rendered into deployable manifests by
   [`scripts/render.sh`](../scripts/render.sh).
2. **Helm chart values** — workload sizing, image, storage, security, and feature
   toggles in [`chart/abfs/values.yaml`](../chart/abfs/values.yaml).

## 1. Instance values (`instances/<name>.env` → `render.sh`)

Every project-specific value in `infra/` and `chart/` is a `REPLACE_<NAME>` token.
Put the real values in a per-instance env file, then render a deployable copy
(the source tree stays templated):

```bash
cp instances/example.env instances/myinstance.env   # edit the values
scripts/render.sh instances/myinstance.env               # -> rendered/myinstance/
```

`render.sh` substitutes each `REPLACE_<NAME>` with the matching `NAME=` value from
your env file and writes the result under `rendered/<name>/`. Tokens you leave
unset stay in place and are reported at the end of the run, so you can fill them
in later (this is expected for the CI/CD tokens if you only deploy the data plane).

### Core data-plane tokens (required)

| Token | Meaning |
|-------|---------|
| `PROJECT_ID` | Google Cloud project that hosts ABFS. |
| `NETWORK_NAME` | Name of your existing VPC network (referenced, not created). |
| `ABFS_SUBNET_CIDR` | Primary CIDR of the subnet ABFS clients use; scopes the firewall ingress rules. |
| `SPANNER_CONFIG` | Spanner instance config, e.g. `regional-europe-west4`. |
| `BUCKET_NAME` | Globally-unique Cloud Storage bucket name for ABFS blobs. |
| `BUCKET_LOCATION` | Bucket location, e.g. `europe-west4`. |
| `HOST_PROJECT_ID` | Shared-VPC host project. For a standalone VPC, set it equal to `PROJECT_ID`. |
| `RUNTIME_SA` | Local part of the single Google SA the whole data plane runs as (the ABFS **runtime identity** and license identity; the GSA is `<SA>@<PROJECT_ID>.iam.gserviceaccount.com`). It is also the `abfs-data` node pool's service account. See [Runtime service account](#runtime-service-account-license-identity) below. |

### Runtime service account (license identity)

ABFS validates the license against the Google service account the data plane runs
as — that SA must appear in the license's `allowed_service_accounts`. On GKE the
whole data plane runs as a **single runtime SA**, which is also the `abfs-data` node
pool's service account: ABFS's license check needs a GCE VM identity token for the
licensed SA (it requires the `google.compute_engine` claim that Workload Identity
tokens lack), so the pool runs in GCE-metadata mode and the SA doubles as the node
identity. The license itself is delivered through that pool's node metadata, not a
Secret — see [`02-prerequisites-cluster-and-kcc.md` §1b](./02-prerequisites-cluster-and-kcc.md#1b-create-the-dedicated-abfs-node-pool).
Choose the SA *before* requesting the license. Two modes, mirroring the Terraform
create-or-reference pattern:

| Mode | Set in `instances/<name>.env` | What happens |
|------|-------------------------------|--------------|
| **Create fresh SA** (default) | `RUNTIME_SA` = a new name (e.g. `abfs-sa`), `CREATE_RUNTIME_SAS=true` | `infra/10` creates the SA; submit it in the license EAP form; `infra/12` binds the runtime project roles onto it. Use it as the `abfs-data` node pool SA. |
| **Bring your own** (pre-existing, already-licensed SA) | `RUNTIME_SA` = your SA (e.g. `abfs-sa`), `CREATE_RUNTIME_SAS=false` | `render.sh` drops `infra/10` (KCC does not create/acquire your SA); `infra/12` binds the roles onto it by external reference. |

`CREATE_RUNTIME_SAS` is a control variable read by `render.sh` (not a `REPLACE_*`
token); it defaults to `true`. `infra/12` binds all runtime roles
(`spanner.databaseUser`, `storage.objectAdmin`, `monitoring.metricWriter`,
`monitoring.viewer`, `stackdriver.resourceMetadata.writer`, `logging.logWriter`) to
this one SA; it also needs Artifact Registry read on the image repo, because it is
both the runtime identity **and** the node identity for the `abfs-data` pool.

### Spanner schema toggle (`CREATE_TABLES`)

The ABFS server does **not** create the Spanner schema at runtime; the deploy
applies it (see [`03-spanner-schema-ownership.md`](./03-spanner-schema-ownership.md)).
`CREATE_TABLES` is a control variable read by `render.sh` (not a `REPLACE_*` token);
it defaults to `false` and mirrors Terraform's `abfs_spanner_database_create_tables`.

| Mode | Set in `instances/<name>.env` | What happens |
|------|-------------------------------|--------------|
| **Apply schema with KCC** | `CREATE_TABLES=true` (the worked example sets this) | `render.sh` keeps the `spec.ddl` block in `infra/20-spanner.yaml` (mirrored from `infra/schemas/0.0.31-schema.sql`), so KCC creates the database **and** applies the schema. |
| **Empty shell** (default) | `CREATE_TABLES=false` | `render.sh` blanks the ddl (`ddl: []`); KCC creates an empty database and you apply the schema out-of-band (`gcloud spanner databases ddl update … --ddl-file=infra/schemas/0.0.31-schema.sql`). |

Do **not** edit `spec.ddl` after the first apply (data-loss risk) — the KCC
equivalent of `ignore_changes = [ddl]`.

### CI/CD foundation tokens (only if you deploy `infra/cicd/`)

| Token | Meaning |
|-------|---------|
| `AR_REGION`, `SSM_REGION`, `CWS_REGION`, `CLOUD_BUILD_REGION`, `SCHEDULER_REGION` | Regions for Artifact Registry, Secure Source Manager, Workstations, Cloud Build, and Cloud Scheduler. |
| `CWS_SUBNET_NAME`, `CWS_MACHINE_TYPE` | Cloud Workstations host subnet and machine type. |
| `SSM_CLONE_URL` | Upstream git URL seeded into the new Secure Source Manager repo. |

A few CI/CD tokens can only be supplied **after** the relevant resource
reconciles or are organization-specific — fill them once available:
`SSM_REPO_URL`, `*_TRIGGER_ID`, `CWS_CREATOR`, `CWS_USER`, `ALERT_EMAIL`, and (for
a private Secure Source Manager instance) `CA_ORGANIZATION` / `CA_COMMON_NAME`.
See [`06-cicd-foundation.md`](./06-cicd-foundation.md) for the order and source of
each.

## 2. Helm chart values

`chart/abfs/values.yaml` is fully commented (every value carries a
`# -- <name> -- <description>` line) and is validated by `values.schema.json` at
lint/template/install time. Override with `-f my-values.yaml` or `--set key=value`.
The settings you are most likely to change:

### Image
| Value | Default | Notes |
|-------|---------|-------|
| `image.repository` | the ABFS image repo | Container image, no tag/digest. |
| `image.tag` | chart `appVersion` | Tag; empty falls back to `appVersion`. |
| `image.digest` | `""` | Pin by immutable digest (`sha256:…`) for production; overrides `tag`. |
| `image.pullPolicy` | `IfNotPresent` | `IfNotPresent` \| `Always` \| `Never`. |

### License (two-phase)
| Value | Default | Notes |
|-------|---------|-------|
| `licensed` | `false` | Gate. While `false`, only the bootstrap (ServiceAccounts + pusher ConfigMap) renders. Set `--set licensed=true` once the licensed `abfs-data` node pool exists, to start the server + uploaders + bootstrap Job. The license itself is **not** a chart value — it is delivered via node metadata on the `abfs-data` pool ([docs/02 §1b](./02-prerequisites-cluster-and-kcc.md#1b-create-the-dedicated-abfs-node-pool)). |
| `runtimeServiceAccountName` | `REPLACE_RUNTIME_SA` token | Local part of the runtime/license SA; **informational only** — the workload's actual identity comes from the `abfs-data` node pool's service account, not from the chart. |

### Data backends (must match `infra/`)
| Value | Default | Notes |
|-------|---------|-------|
| `projectId` | token | Project; matches the namespace project-id annotation. |
| `spanner.instance` / `spanner.database` | `abfs` / `abfs` | Spanner references. |
| `bucket` | token | GCS blob bucket. |

### Service accounts
| Value | Default | Notes |
|-------|---------|-------|
| `serviceAccounts.server` / `.uploader` / `.ui` | `abfs-server` / `abfs-uploader` / `abfs-ui` | KSA (pod) names. The data-plane identity is **not** Workload Identity — it comes from the `abfs-data` node pool's [runtime SA](#runtime-service-account-license-identity), so these KSAs carry no GSA annotation. |
| `createServiceAccounts` | `true` | Render the KSAs from this chart (set `false` if managed elsewhere). |

### ABFS client transport
The ABFS **server** serves PLAINTEXT gRPC and, by default, no client auth, but ABFS
**clients** default to TLS — so the in-cluster clients (the uploader and the
pusher-config bootstrap Job) must be told otherwise or the gRPC handshake fails
(`first record does not look like a TLS handshake`). The chart wires these into the
client command flags.

| Value | Default | Notes |
|-------|---------|-------|
| `client.tls` | `false` | When `false`, clients pass `--disable-tls=true`. Set `true` only if you front the server with TLS. |
| `client.authType` | `none` | ABFS client auth type → `--auth-type`: `none` \| `gcpuser` \| `gcpsa`. |

### Server
| Value | Default | Notes |
|-------|---------|-------|
| `server.replicas` | `1` | The server is a singleton; keep `1`. |
| `server.resources` | `8` CPU / `32Gi` | A memory limit equal to the request bounds node pressure; size to your node. |
| `server.service.internalLoadBalancer` | `true` | Internal L4 LB for out-of-cluster clients. |
| `server.cache.enabled` | `false` | Optional persistent cache PVC (`server.cache.size`, retained on uninstall by default). |

### Uploaders
| Value | Default | Notes |
|-------|---------|-------|
| `uploader.count` | `3` | Number of uploader pods. **Must** equal the number of pusher entries — they are generated from this count. |
| `uploader.namePrefix` | `abfs-gerrit-uploader` | StatefulSet + pod hostname prefix; must equal the pusher-config names. |
| `uploader.dataDisk.size` | `4096Gi` | Per-pod data PVC size (Hyperdisk Balanced). |

### Scheduling, security, and platform features
| Value | Default | Notes |
|-------|---------|-------|
| `nodeSelector` / `tolerations` | the `abfs-data` pool | Default to `cloud.google.com/gke-nodepool: abfs-data` and a toleration for the `abfs.dev/dedicated=true:NoSchedule` taint, so pods land on the dedicated licensed pool. |
| `hostPID` / `securityContext.privileged` | `true` / `true` | Required for the casfs mount; do not disable. |
| `otel.enabled` | `true` | Inject OTLP exporter env (no-op until the image emits OTLP). |
| `vpa.enabled` / `vpa.updateMode` | `true` / `Off` | Vertical Pod Autoscaler; `Off` = recommendations only. |
| `networkPolicy.enabled` | `true` | In-cluster NetworkPolicies (enforced only with DataPlane V2). |
| `ui.enabled` | `false` | Optional ABFS UI front-end. |
| `tests.enabled` | `true` | Render the `helm test` connectivity probe. |

> **Writable `$HOME`.** The server, uploader, and UI pods each mount an `emptyDir`
> at `/home/abfs-server` (the container's `$HOME`). The image does not create that
> directory, and `git-cookie-authdaemon` plus the ABFS client config/cache need to
> write there — so this volume is always mounted (not a toggle).

For the complete, authoritative list, read
[`chart/abfs/values.yaml`](../chart/abfs/values.yaml) — every value is documented
inline.

## Supplying the license

The license is sensitive; keep it out of version control. It is **not** a Helm
value and is never mounted as a Secret. Instead it is delivered as base64-encoded
JSON in the `abfs-license` node metadata of the dedicated `abfs-data` pool, whose
service account is the licensed runtime SA. Create that pool with the license
metadata (see [`02-prerequisites-cluster-and-kcc.md` §1b](./02-prerequisites-cluster-and-kcc.md#1b-create-the-dedicated-abfs-node-pool)),
then start the data plane:

```bash
base64 -w0 abfs-license.json > abfs-license.b64   # the value placed in node metadata
helm upgrade abfs ./chart/abfs -n abfs -f chart/abfs/values.yaml \
  --set licensed=true
```

Rotating the license means updating the node-pool `abfs-license` metadata and
recreating/rolling the pool's nodes — no chart change needed.
