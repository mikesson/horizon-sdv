# Cloud Cuttlefish Orchestrator Architecture

## Overview

Cloud Cuttlefish Orchestrator (CCO) manages Android Cuttlefish virtual
devices on Google Cloud. A single Go service on Cloud Run orchestrates GCE
host VMs that run the devices; an Angular console streams them over WebRTC.

The platform is **single-tenant**: built-in Cloud Run IAP authenticates
every caller at the edge, and authentication is the only gate. There are no
organizations, teams, roles, or permissions. Anyone on the IAP access list
has full access to everything.

## System Components

| Component | Location | Role |
|-----------|----------|------|
| Orchestrator API | `internal/` (Go, Huma, Cloud Run) | REST API, provisioning, lifecycle |
| Web console | `web/cuttlefish-console/` (Angular) | SPA served by the same Cloud Run service |
| CLI | `cmd/cfctl/` | Programmatic access with Google ID tokens |
| Host VMs | GCE | Run the Cuttlefish devices (Host Orchestrator on :2080) |
| Database | Cloud Spanner | Hosts, devices, builds, quota, audit, catalogs |
| Config | Cloud Run env vars | `CCO_*` set by Terraform; secrets arrive as mounted Secret Manager volumes |
| TURN relay (relay mode) | `terraform/modules/relay/` | WebRTC media relay; authorizes every client through the orchestrator, so it holds no secret |

## Deployment Architecture

```
                       Internet
                          │
                          ▼
            ┌──────────────────────────┐
            │   Cloud Run + built-in   │  auth = "iap"
            │   IAP (run.app URL)      │  iap_members = access list
            └────────────┬─────────────┘
                         │ VPC egress (private ranges)
          ┌──────────────┼────────────────┐
          ▼              ▼                ▼
   ┌────────────┐ ┌────────────┐  ┌─────────────┐
   │  Spanner   │ │  GCE host  │  │ GCS builds  │
   │            │ │  VMs       │  │             │
   └────────────┘ └────────────┘  └─────────────┘
```

Three independent Terraform variables set the deployment topology (see
[Deployment Modes](../getting-started/deployment-modes.md) for the full
matrix and named scenarios):

- **`ingress`** sets CCO reachability: `public` (internet-reachable run.app
  URL) or `private` (internal ingress only). Built-in IAP works with either.
- **`auth`** sets the front door: `iap` (IAP challenges every request and
  enforces the `roles/iap.httpsResourceAccessor` list; only the IAP service
  agent holds `run.invoker`, so the URL cannot be reached around IAP).
- **`device_ingress`** sets where WebRTC device media meets the internet:
  `public` (external-IP host VMs, direct media), `private` (internal-only
  hosts, clients over VPN), or `relay` (internal-only hosts, media via TURN).

## Authentication

See [Authentication](authentication.md). IAP validates the
browser session and stamps requests with a signed
`X-Goog-IAP-JWT-Assertion`; the middleware verifies signature, issuer, and
the direct Cloud Run audience and attaches a `Principal {email,
displayName}` to the request context. CLI callers send a Google ID token as
a Bearer header. No further authorization happens anywhere in the app.

## Device Creation Flow

```
Console/CLI ──POST /v2/devices──► Orchestrator
                                      │ 1. validate host capacity (atomic)
                                      │ 2. persist device + durable task
                                      │    (one transaction)
                                      │ 3. worker pool: call Host Orchestrator
                                      ▼
                                 GCE host VM ──creates──► Cuttlefish CVD
                                      ▲
Console ◄──WebRTC (signaling proxy)───┘
```

Host capacity is reserved transactionally (`CreateWithHostIncrement`), and
the global host quota (`quotas` table, scope `platform/global`) is consumed
before a host VM is created.

## Background Jobs (`internal/jobs/`)

| Job | Purpose |
|-----|---------|
| `taskworker` | Drains the durable Spanner task queue (provisioning, CVD create/cleanup, reconcile) on every instance |
| `taskreclaimer` | Requeues expired task leases; dead-letters exhausted tasks |
| `devicecontroller` | Polls devices via the Host Orchestrator; updates health columns; feeds recovery detectors |
| `hostreconciler` | Reconciles host records with actual GCE instances; preemption recovery; orphan-VM sweep; quota reconcile |
| `reaper` | Deletes expired ephemeral hosts/devices (TTL + pinning rules) |
| `batchsettle` | Settles atomic batches, collects emptied batch rows, reclaims drained batch hosts |
| `catalogsync` | Probes GCE Spot capacity advice (off by default) |

The queue, delivery fencing, keyed reconciliation, and drift detection are
documented in [control-plane.md](../development/control-plane.md).

## Data Model

Single-tenant Spanner schema (`migrations/spanner/complete_schema.sql`):

- `hosts`, with `devices`, `snapshots` and `host_artifact_cache`
  interleaved; `batches` (batch policy rows)
- `builds`, the Android build registry (artifact specs, roles)
- `quotas`, one global row (`platform`/`global`)
- `tasks`, with `task_steps` interleaved (the durable queue and step
  journal); `operations` (handles for asynchronous device verbs);
  `leader_leases`; `idempotency_keys`
- `audit_logs`, actor identified by email (90-day retention)
- `machine_families` and `machine_type_overrides` (machine curation),
  `zone_health` and `spot_advice` (placement memory), `host_image_configs`,
  `system_config`

`created_by` columns store the creator's verified email for display and
audit only. They carry no access-control semantics.

A new database is created from `complete_schema.sql`. An existing database is
upgraded with the files in `migrations/spanner/upgrades/`, applied in version
order. `v<N>.sql` takes a database from version N-1 to N, and says which of its
statements run before the deploy of the version N binary and which after. The
binary refuses to start against a database whose `schema.version` row is older
than its own.

## Security Model

- **Edge**: IAP (or internal ingress). This is the only access-control layer.
- **App**: validates the IAP assertion; CSRF protection via
  `net/http.CrossOriginProtection` for the cookie-authenticated browser
  path; WebSocket upgrades enforce a same-host Origin check.
- **Host VMs**: reachable only over the VPC (`device_ingress = private` or
  `relay`) or a restricted client firewall (`device_ingress = public`); WebRTC
  media is DTLS-SRTP encrypted end-to-end regardless of mode.
- **Data**: Spanner and Secret Manager with least-privilege service
  accounts; the orchestrator SA holds `roles/compute.admin`,
  `roles/spanner.databaseUser`, `roles/secretmanager.secretAccessor`,
  `roles/serviceusage.serviceUsageConsumer`, and `objectAdmin` on the
  artifacts bucket.

## Technology Stack

| Layer | Technology |
|-------|------------|
| Backend | Go 1.26, Huma v2 (OpenAPI 3.1), chi |
| Frontend | Angular 22 (zoneless, signals, standalone), NgRx Signal Store, Zod v4 |
| Database | Cloud Spanner |
| Streaming | WebRTC (direct or TURN relay) |
| Infra | Terraform (google provider ≥ 7.36), Cloud Build, Artifact Registry |
