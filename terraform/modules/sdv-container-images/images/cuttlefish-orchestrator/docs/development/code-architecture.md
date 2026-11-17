<!-- Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. -->

# Code Architecture

This document describes the split-stack (vertical slice) architecture used
across the Go backend and Angular frontend, and how a request flows through
the system. For per-layer detail see [backend.md](backend.md) and
[frontend.md](frontend.md); for authentication see
[../concepts/authentication.md](../concepts/authentication.md).

## Overview

Both stacks organize code **by business feature**, not technical layer.
Each feature is a self-contained slice that owns its transport, logic, and
data access. The platform is single-tenant: authentication (Cloud Run IAP)
is the only gate, and there is no authorization model in either stack.

```
 Backend slice                      Frontend slice
 ─────────────                      ──────────────
 features/device/                   devices/
 ├── handler.go    (HTTP/Huma)      ├── components/   (UI)
 ├── service.go    (logic)          ├── store/        (Signal Store)
 └── repository.go (Spanner)        ├── services/     (API + Zod)
     (wire.go → internal/app)       └── models/       (types + schemas)
```

## Part 1: Backend

- **Domain** (`internal/domain/`): pure entities + interfaces, stdlib only.
  `auth.Principal` is `{Email, DisplayName}`.
- **Features** (`internal/features/`): device, host, batch, build, quota,
  storage, machinetype, gcecatalog, hostconfig, relay, admin. Handlers check
  only that a principal exists; no permission checks anywhere.
- **Platform** (`internal/platform/`): Spanner client + row models, GCE
  clients, GCS adapter, Host Orchestrator client, TURN, ICE, audit. Shared
  HTTP error mapping (`web.MapError`) lives in `internal/web`.
- **Jobs** (`internal/jobs/`): taskworker, taskreclaimer, leaderelection,
  devicecontroller, hostreconciler, reaper, batchsettle, catalogsync. See
  [control-plane.md](control-plane.md).
- **Router** (`internal/router/`): chi + Huma; OpenAPI 3.1 at
  `/openapi.json`.
- **Middleware** (`internal/middleware/`): recovery → request ID → logging
  → tracing → CSRF (`net/http.CrossOriginProtection`; the SPA is served
  same-origin, so there is no CORS layer) → auth (`extractIdentity`).
- **Composition root** (`internal/app`): calls every slice's `Wire()`,
  builds the jobs and the server; `cmd/orchestrator/main.go` is a thin shim.

Import direction is enforced by `depguard`: features import {domain,
platform, web}, jobs import {domain, platform}, `web` imports only domain,
platform never imports features, and no feature imports another feature.
Only the router and `internal/app` import the feature packages.

## Part 2: Authentication

Identity is established once, at the middleware, and attached to the
request context:

| Mode | Mechanism |
|------|-----------|
| `iap` | Validate `X-Goog-IAP-JWT-Assertion` (ES256, issuer `https://cloud.google.com/iap`, audience `/projects/N/locations/R/services/S`) |
| dev | Trusted header / configured fallback email (local only) |

The verified email is the entire authorization. Handlers retrieve it with
`auth.PrincipalFromContext(ctx)` and use it only for `created_by`
audit fields.

## Part 3: Frontend

- Zoneless change detection, standalone components, OnPush everywhere.
- NgRx **Signal Store** per feature; no classic NgRx.
- API services validate responses with **Zod v4** and transform snake_case
  to camelCase.
- Auth state is `{ user: {email, displayName}, isAuthenticated, isLoading }`
  loaded from `/v2/me` in a `provideAppInitializer`; no role gating in routes,
  nav, or templates.

## Part 4: Request Flow

```
Browser ── IAP (sign-in + access list) ──► Cloud Run
                                              │ middleware: CSRF → extractIdentity
                                              ▼
                                       feature handler (Huma validation)
                                              ▼
                                       service (business logic)
                                              ▼
                                       repository ──► Spanner
```

CLI requests are identical except the edge check is the bearer ID token
validated by IAP.

Device streaming bypasses Huma: chi routes proxy WebSocket signaling to the
host's Host Orchestrator, and WebRTC media flows directly to the host or via
TURN, depending on `device_ingress`.

## Part 5: Cross-Cutting Concerns

- **Validation**: Huma JSON-schema tags (backend), Zod (frontend);
  `npm run check:schema-drift` + `tests/specgen` keep them aligned, and
  `npm run check:fixture-shapes` holds the e2e mock to the same schemas.
- **Errors**: RFC 9457 problem details via `huma.Error*`;
  `web.MapError` maps domain errors to status codes.
- **Audit**: async audit logger writes `audit_logs` entries with the
  actor's email.
- **Observability**: OpenTelemetry tracing, structured slog logging with
  request IDs.

## Benefits

- A feature's full behavior is readable in one directory on each stack.
- Slices can be deleted wholesale (as the tenancy removal demonstrated).
- The dependency rules keep domain logic free of infrastructure concerns.
