# Backend Codebase Guide

## Architecture Overview

The Go backend uses a **vertical slice (split-stack) architecture**: code is
organized by business feature, not technical layer. Each feature owns its
handler, service, and repository.

```
cmd/                Deliverables ONLY, the binaries an operator runs
├── orchestrator/   Thin shim: main() calls internal/app.Run
└── cfctl/          CLI client
tests/              Verification infrastructure, builds with ./..., never ships
├── queueload/      Task-queue load + fault-injection harness
├── robustness/     Lifecycle invariant harness (Jepsen-style)
├── specgen/        Serves the OpenAPI spec for the schema-drift check; -dump feeds cfctl model generation
├── coverreport/    Block + error-branch coverage report (the test job's floor)
└── harness/        Shared harness libraries (faultproxy, fakegce, fakeho)
internal/
├── app/            Composition root (wiring + server lifecycle, one stage per file)
├── domain/         Pure entities and interfaces (stdlib only)
├── features/       Vertical slices (handler → service → repository + wire.go)
├── platform/       Infrastructure adapters (Spanner, GCE, GCS, TURN, ICE, ...)
├── jobs/           Background processors
├── compat/legacy/  The /v1 legacy CO/HO compatibility facade
├── middleware/     HTTP middleware (auth, CSRF, logging, ...)
├── router/         Composes features into the HTTP API (Huma + chi)
├── web/            Shared HTTP error mapping (MapError) + response helpers
└── config/         Env-only configuration (CCO_* variables)
```

### Dependency Rules (enforced by `depguard`)

- **domain/** may import the standard library only
- **features/** may import domain and platform, NOT other features
- **platform/** may import domain (repositories return domain entities), NEVER features
- **jobs/** may import domain and platform, NOT features
- **tests/** may import anything; production code (`cmd/`, `internal/`) may
  NOT import `tests/` outside `_test.go` files

Package-coupled test helpers stay colocated with the package they fake, per
the stdlib `net/http/httptest` idiom: `internal/platform/spanner/spannertest`,
`internal/domain/host/hosttest`, `internal/domain/device/devicetest`.
Standalone test infrastructure (harness binaries, fake boundary services)
lives in `tests/`.

## Domain Layer (`internal/domain/`)

| Package | Contents |
|---------|----------|
| `auth` | `Principal {Email, DisplayName}`, context helpers, `AuditEvent` |
| `device` | Device entity, lifecycle (TTL/pinning), declarative requirements, Repository + Service interfaces |
| `host` | Host entity, status state machine, provisioning models, placement intent |
| `batch` | Device-fleet batches: spec, placement plan, derived status rollup |
| `build` | Build registry entity, artifact specs/roles |
| `quota` | Single global host quota (`platform`/`global`) |
| `audit` | Operational audit entries (actor identified by email) |
| `task` | Durable task queue entities (Spanner table-as-queue) |
| `idempotency` | Request fingerprinting for Idempotency-Key support |
| `storage` | GCS bucket/object entities for the artifact browser |
| `gcecatalog`, `machinetype`, `hostconfig` | GCE catalog + host image config |

There is **no RBAC**: no roles, permissions, scopes, organizations, or
teams anywhere in the domain.

## Features Layer (`internal/features/`)

`device`, `host`, `batch`, `build`, `quota`, `storage`, `machinetype`,
`gcecatalog`, `hostconfig`, `admin`. Each follows:

- `handler.go` holds the Huma operations (validation tags, OpenAPI docs) and
  checks only that a `Principal` is present
- `service.go` holds the business logic
- `repository.go` does Spanner access via row models in
  `platform/spanner/models` (feature-owned repositories live in their
  slices; only cross-cutting repositories stay in `platform/spanner`)
- `wire.go` assembles the slice; the composition root (`internal/app`)
  calls every slice's `Wire()` exactly once

Handlers perform **no authorization**. IAP at the edge is the only gate.
`created_by` fields record the caller's email for display/audit only.

## Platform Layer (`internal/platform/`)

`spanner` (client, row models, and the cross-cutting repositories: audit,
build-API credentials, idempotency, leases, system config, task queue, zone
health, spot advice), `gce` (Compute clients + error classification), `gcs`
(Cloud Storage adapter), `hoclient` (Host Orchestrator client), `turn`
(credential minting and verification), `ice` (composes the per-leg server
lists and transport policy both API surfaces serve), `audit` (async audit
logger), `encryption` (build-API credential storage). Shared HTTP error mapping (`MapError`) and response
helpers live in `internal/web`.

## Background Jobs (`internal/jobs/`)

| Job | Purpose |
|-----|---------|
| `taskworker` | Claims and executes durable queue tasks (provisioning, cleanup) |
| `taskreclaimer` | Requeues expired leases, dead-letters exhausted tasks |
| `leaderelection` | Spanner-based lease so singleton jobs run on one replica |
| `devicecontroller` | Device health polling + CVD census + reconcile/remediate/capture suspicion |
| `hostreconciler` | GCE instance ↔ host record reconciliation, preemption recovery, orphan-VM sweep, quota reconcile |
| `reaper` | TTL-based cleanup of ephemeral hosts/devices |
| `batchsettle` | Batch settlement (atomic-fill sweep, emptied-row GC, drained batch-host reclaim) |
| `catalogsync` | GCE Spot capacity-advice probe (off by default) |

[control-plane.md](control-plane.md) documents how all of this fits together:
the durable task queue, delivery fencing, keyed reconciliation, the operation
journal, drift detection, and how Spanner's capabilities are used.

## Router and Middleware

`internal/router/router.go` mounts all features on chi + Huma, serving the
OpenAPI spec at `/openapi.json` and an interactive Scalar API reference at
`/docs` (custom route in `router/docs.go`; dark mode, models hidden, and
Huma's built-in docs route disabled). The only public paths
are the Cloud Run health probes `/health` and `/ready` (plus the build-API
OAuth endpoints when that flow is enabled). Everything else, including
`/info`, the spec, and the docs, passes through:

1. Recovery, request ID, logging, tracing
2. **CSRF**: `net/http.CrossOriginProtection`, which blocks unsafe-method
   cross-site browser requests and is transparent to bearer-token clients (the
   SPA is served same-origin, so there is no CORS layer)
3. **Auth** (`middleware/auth.go`): a single `extractIdentity` step
   - `iap` mode: validate `X-Goog-IAP-JWT-Assertion` (issuer
     `https://cloud.google.com/iap`, direct Cloud Run audience)
   - dev mode: trusted header / configured fallback email
   The verified email **is** the authorization.

Per-request read/write deadlines are applied by `middleware.RequestDeadlines`
(wired in `internal/app`); the legacy `/v1` surface, storage object
streams, and WebRTC signaling are exempt.

### Huma conventions

Every JSON endpoint under `/v2` is a Huma operation registered with
`huma.Register` and a full `huma.Operation`: `OperationID`, `Tags`,
`Summary`/`Description`, `Security` (`bearerAuth`), and `Errors` listing the
business error codes the handler can actually return (Huma appends 422/500
automatically). Non-200 defaults are declared with `DefaultStatus` (e.g.
`add-display` returns 201). Handlers return errors through
`web.MapError`, which maps domain sentinels to `huma.Error*`. All Huma
routes emit RFC 9457 problem details. `/v2/me` is a Huma operation returning
`{email, display_name}`.

The **only** chi-only routes under `/v2` are the ones Huma cannot express:
the WebRTC signaling long-poll + WebSocket, the ADB bridge at
`/v2/devices/{id}/adb`, and the storage object up/download byte streams. Each
is registered by its slice's `RegisterRaw` and mounted by the router.

The two WebSocket routes share one proxy. Both resolve a device to its host,
dial the host's operator, and relay frames until either end goes away,
differing only in the operator path and the frame size they admit. The ADB
route carries a byte stream, where frame boundaries mean nothing, and it is
what lets a client speak ADB to a device it has no route to. Both are exempt
from the per-request deadlines. A successful upgrade clears them anyway, but
the handler does real work (two lookups and a live CVD listing on the host)
before it upgrades.

## Configuration (`internal/config/`)

Configuration is **env-only**. There is no config file. `config.Load()`
layers `CCO_*` environment variables over code defaults, reads secrets from
mounted Secret Manager volumes (via `CCO_*_FILE` paths), then validates.
Deployment values arrive as environment variables. Terraform sets them in
production, and a local `.env` (see `.env.example`) supplies them for
development. They cover Spanner coordinates, project/network/subnet,
`CCO_DEVICE_INGRESS` (where WebRTC media meets the internet), `CCO_AUTH_MODE`
plus audiences, host image family/project, TURN and STUN servers, and dev
mode. Operational tuning (server timeouts, job intervals) is code defaults.
The `ingress` and `auth` *mechanisms* are Terraform-level; the app sees only
`device_ingress` and the auth mode.

## Verification

```bash
go build ./...
./bin/golangci-lint run

# Full suite, including emulator-backed repository tests:
docker run -d --name spanner-emu -p 9010:9010 gcr.io/cloud-spanner-emulator/emulator
SPANNER_EMULATOR_HOST=localhost:9010 go test ./... -count=1
```

Beyond unit and emulator tests, the repo carries fault-injection harnesses
that verify the system's invariants under crashes, pauses, clock skew, and
cloud-side drift. See the [Testing Guide](testing.md).
