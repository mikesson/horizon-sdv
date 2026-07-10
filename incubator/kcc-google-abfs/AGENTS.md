# AGENTS.md

Guidance for coding agents working in this repository. It is vendor-neutral and applies to
any coding agent or LLM-based tool. Human contributors: start with `README.md` and `docs/`.

## What this repository is

A deployment of the **Android Build File System (ABFS)** data plane on **GKE Standard**:
Google Cloud resources are managed declaratively with **Config Connector (KCC)**, and the ABFS
workloads are deployed by a **Helm** chart. ABFS is a gRPC server (`:50051`) backed by Cloud
Spanner (metadata) and Cloud Storage (blobs), with Gerrit "uploader" workers that populate the
content store. Clients read the result through the `casfs` kernel module.

## Layout

- `chart/abfs/` — Helm chart: server Deployment, uploader StatefulSet, optional UI, the
  pusher-config bootstrap Job, services, NetworkPolicies, PodDisruptionBudgets,
  `values.yaml` + `values.schema.json`, and `templates/tests/`.
- `infra/` — KCC manifests (Spanner, GCS, IAM, Secret Manager, firewall/DNS, API enablement).
  `infra/setup/` is one-time operator configuration; `infra/cicd/` is the optional CI/CD
  foundation; `infra/schemas/` holds the Spanner DDL the deploy applies.
- `instances/` — per-environment value files. `example.env` is the template; real instance
  files (`instances/*.env`) are git-ignored.
- `scripts/render.sh` — substitutes the `REPLACE_*` tokens from an instance env file into a
  deployable copy under `rendered/<instance>/` (a build artifact; git-ignored).
- `docs/` — overview, architecture, prerequisites, schema ownership, deployment runbook,
  configuration, CI/CD foundation, troubleshooting.

## Tooling

Requires `gcloud`, `kubectl` (with built-in `kustomize`), and `helm` (v3.8+; the chart targets
Helm 4). No other build system.

## Validate changes (run before proposing any change)

```
make validate     # helm lint + helm template + kubectl kustomize (infra/ and infra/cicd/)
```

To render a specific environment:

```
cp instances/example.env instances/<name>.env    # then edit the values
make render ENV=instances/<name>.env             # writes rendered/<name>/
```

When editing the chart, keep `chart/abfs/values.schema.json` in sync with `values.yaml` and
bump `chart/abfs/Chart.yaml` `version`.

## Conventions and invariants (do not break these)

- **Parameterization.** Every environment-specific value is a `REPLACE_<NAME>` token filled
  from `instances/<name>.env`. Never hardcode a project, bucket, service account, region, or
  CIDR into the source tree — add a token and document it in `instances/example.env`.
- **Two-phase licensing.** The server, uploaders, and bootstrap Job render only when
  `licensed: true`. The ABFS license is supplied at the node level (GCE instance metadata) and
  must never be committed.
- **Node identity and machine constraints.** The data plane runs on a dedicated node pool whose node service account is the licensed runtime service account in GCE-metadata identity mode (not Workload Identity). The nodes must have the `casfs` kernel module available, and the pods run privileged with `hostPID`. This workload cannot run on a fully managed (Autopilot-style) node pool.
  - *Operational limitation:* When employing a custom node service account combined with GCE metadata service account identity mode, custom `ComputeClass` overrides or modern high-performance shapes (like `C4D` instances) may fail to provision or be ignored by GKE. This forces GKE to fall back to standard `N2` node instances.
- **Uploader sharding.** The uploader pod names must equal the pusher-config pusher names (`<namePrefix>-<ordinal>`); both are derived from `uploader.count`. Because node identity constraints may force a fallback to lower-throughput `N2` instances, the uploaders must be explicitly sharded horizontally (increasing `uploader.count`) to compensate for node performance limits and avoid single-instance CPU serialization bottlenecks.
- **CPU scaling.** The Go workloads set `GOMAXPROCS` from the pod's CPU request and run with no
  CPU limit. Preserve this so they are not throttled on large nodes.
- **License headers.** Source files carry the Apache-2.0 header; match the surrounding style.
- **Spanner schema is declarative.** `infra/20-spanner.yaml` carries the DDL, gated by the
  `CREATE_TABLES` toggle. Do not edit `spec.ddl` after the database exists — that risks data
  loss. The server does not create the schema at runtime.
- **StatefulSet immutability.** The uploader's `volumeClaimTemplates` are immutable: keep only
  stable selector labels on them (no version/chart labels), and recreate the StatefulSet to
  change disk size.

## Deploy end-to-end

Prerequisites are in `docs/02-prerequisites-cluster-and-kcc.md`; full steps in
`docs/04-deployment-runbook.md`. In summary:

1. Provide an existing GKE Standard cluster with the Config Connector operator installed, plus a
   dedicated node pool meeting the node-identity and `casfs` requirements above.
2. `cp instances/example.env instances/<name>.env`, fill it in, then `make render ENV=...`.
3. `kubectl apply -k rendered/<name>/infra` to create the Google Cloud resources.
4. `helm install abfs rendered/<name>/chart/abfs -n abfs -f .../values.yaml`. With
   `licensed: false` the data-plane workloads are gated off (bootstrap scaffolding only).
5. Obtain the ABFS license for the runtime service account, place it in the node pool's
   metadata, then `helm upgrade ... --set licensed=true` to start the data plane.

See `docs/07-troubleshooting.md` for common issues.
