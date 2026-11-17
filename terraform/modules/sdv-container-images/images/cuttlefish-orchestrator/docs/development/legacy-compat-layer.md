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

# Legacy compatibility layer

This document describes how CCO serves the legacy Cloud Android Orchestrator (CO)
and Host Orchestrator (HO) APIs internally. For the consumer/operator view (what
`/v1` exposes, how to enable the build-API OAuth flow), see
[../reference/legacy-api.md](../reference/legacy-api.md).

## Why it exists

Google's internal integrations (`cvdr`, the `cvd` CLI / `libhoclient`, direct API
callers) speak the legacy two-tier API and can't migrate overnight. CCO's native
API moved to `/v2`, freeing `/v1` to serve the legacy contract **byte-for-byte**
so those clients work unchanged.

This is cheap because the legacy stack and CCO have the same shape: a thin **Cloud
Orchestrator** (GCE host lifecycle + auth + reverse-proxy) in front of a per-VM
**Host Orchestrator** that does the device work. CCO already manages GCE hosts and
already speaks HO via `internal/platform/hoclient`, so the compat layer is mostly
re-exposing existing capability under the legacy URLs.

## Where it lives

```
internal/compat/legacy/
├── coapi/              upstream CO api/v1 structs, copied VERBATIM (byte-exact wire)
│   ├── instancemanager.go
│   └── signalingserver.go
├── handler.go          COHandler: interfaces (HostService/ZoneLister, the
│                       V1ZoneHintPolicy func), Register(), response/error
│                       helpers, the static infra config
├── co_hosts.go         GET /v1/zones, POST/GET/DELETE .../hosts
├── co_operations.go    POST .../operations/{op}/:wait
├── co_config.go        GET /v1/config, GET .../infra_config
├── co_proxy.go         the HO reverse-proxy catch-all
└── operations.go       operation-name encode/parse
```

## Design choices

- **It is a facade outside `internal/features/`.** Feature slices may not
  import other features, and the CO surface has to compose several (host service, zone
  catalog, TURN config). `COHandler` depends on small **local interfaces**
  (`HostService`, `ZoneLister`) that the real services satisfy structurally.
  That keeps it fake-testable and sidesteps the no-cross-feature-imports rule.
- **Plain chi handlers, not Huma.** The handlers write the verbatim `coapi`
  structs directly (and a legacy `{code, error}` envelope) so the wire shape is
  byte-exact, and the HO passthrough / WebSocket / large uploads don't fit Huma
  anyway. Native `/v2` keeps its Huma OpenAPI; `/v1` is documented by the upstream
  swagger, not re-generated (no drift).
- **Registration is nil-gated.** `router.Config.LegacyCOHandler` is wired in
  `internal/app` and registered only when non-nil, so the whole surface is
  inert unless built.

## The CO surface

Each endpoint maps onto an existing native service and returns the legacy shape.
Key behaviors that match the upstream contract exactly:

- **Host name = the CCO host ID.** Stable, available immediately at async create,
  and directly resolvable by the proxy and delete paths. The create name is
  server-assigned (`cf-…`) because the legacy `Name` field is output-only.
- **Operations.** Create/delete are asynchronous and return a legacy `Operation`
  whose name encodes the action + host ID (`create.<id>` / `delete.<id>`, a single
  URL path segment). `:wait` is a **blocking long-poll that returns the bare
  resolved resource** (the `HostInstance` for a create, `{}` for a delete), or
  `503` on timeout. It resolves by polling the host record (`hosts.Get`) on a
  ticker bounded by a wait timeout. This is exactly what `cvdr`'s `waitForOperation`
  expects (POST `:wait`, unmarshal the result, retry on 503). CCO has no operations
  store; the operation name carries everything `:wait` needs.
- **`infra_config` mints, where upstream echoes.** The wire shape is upstream's,
  and without TURN configured the answer is upstream's too: one public STUN
  server. With TURN configured it also carries an ephemeral credential, scoped
  to the host in the path. The endpoint names no device, and is read before the
  client says which one it wants. A static credential cannot work, because the
  relay module renders no shared secret, so every authentication is delegated to
  the orchestrator, which refuses a lapsed username. The lifetime is
  correspondingly long, because the client reads this once per connection and
  cannot renew.
- **The zone token is compiled to placement vocabulary at the facade.** By
  default (`placement.v1_zone_is_hint`, read through the `V1ZoneHintPolicy`
  func wired in main; absent or unreadable degrades to hint) `createHost`
  maps the path token to a **location hint**, so the host service ranks the
  actual zone and a token CCO never cataloged still admits. In the same mode
  `listHosts` filters by **requested-or-physical** zone, so a host stays
  listed under its creation token wherever placement or a later zone
  failover lands it. With the policy off, the token is the literal pin it
  always was. Everything else on the surface resolves by host ID, and
  failover only ever moves hosts that have no Host Orchestrator yet, so no
  passthrough request can observe a move. The facade stays a dumb
  translator: it knows nothing about ranking, cooldowns, or failover.

## The HO passthrough

```
ANY /v1/zones/{zone}/hosts/{host}/*  ->  reverse-proxy to the host's HO
```

`co_proxy.go` resolves the host, then uses `net/http/httputil.ReverseProxy` to
forward everything after `.../hosts/{host}/` verbatim (chi's `*` param) to
`host.HostOrchestratorURL()`. This makes the **entire HO API (~35 endpoints)
byte-exact with zero per-endpoint code**. Colon verbs, snapshots, userartifact
uploads, displays, and the `connect_client` WebSocket all ride the passthrough
(Go's reverse proxy relays Upgrade and streamed bodies). Errors map to the legacy
envelope: host not found → 404, no host IP yet → 503, HO unreachable → 502.

chi serves all the legacy gorilla/mux path shapes (the `:wait` colon-verb, the
`{host}/*` catch-all coexisting with the specific `{host}/infra_config`); this was
verified in `internal/router/routing_compat_test.go`.

## Build-API credentials

There is no server-mediated flow: clients set the
`X-Cutf-Host-Orchestrator-BuildAPI-Creds` header themselves (cvdr's
`oauth:`/`jwt:` modes) and the proxy forwards it verbatim. See
[../reference/legacy-api.md](../reference/legacy-api.md).

## Authentication

The compat surface sits behind the same gate as everything else. Programmatic
clients authenticate exactly like `cfctl`: a **service-account self-signed JWT**
(`aud = <service-url>/*`, signed via IAM `signJwt`) sent as `Authorization:
Bearer`. Cloud Run IAP validates it and injects the assertion the app checks.
Disabling IAP alone does **not** open `/v1`. The app independently validates the
IAP assertion in `iap` mode. See [../concepts/authentication.md](../concepts/authentication.md).

## Testing

- Handler/proxy unit tests use fakes for the local interfaces; the proxy tests run
  against a real `httptest` fake HO.
- `routing_compat_test.go` proves chi can serve the exact legacy path shapes.
- The deployed surface was validated end-to-end with the real upstream `cvdr`
  binary (`cvdr host list` → `GET /v1/zones/.../hosts` → 200).

### Parity recordings, the byte-exact gate

A test enforces "byte-exact". `tools/legacyparity` boots the **real
upstream server** (`pkg/app` from `UPSTREAM/cloud-android-orchestration`,
hermetic: username-only accounts, in-memory DB, a scripted fake instance
manager) and records its exact answer (status, headers, body bytes) for each
corpus request into `internal/compat/legacy/testdata/parity/`.
`parity_test.go` replays every recording against CCO's `/v1` handler with
fakes answering the same scenario values, and diffs byte-for-byte, so it pins
serialization (field order, omitted-vs-empty fields, the trailing newline
`json.Encoder` emits) rather than values.

- Regenerate after an upstream bump or corpus change:
  `go -C tools/legacyparity run .`. The `gen` CI job runs `-check` and fails
  on stale recordings.
- The replay runs in the ordinary `test` job; `coapi/drift_test.go`
  additionally diffs the mirrored structs against upstream's `api/v1` source
  (`CCO_REQUIRE_UPSTREAM=1` in CI keeps it from silently skipping).
- **Intended divergences live in exactly two places** and nowhere else:
  `expectedParityHeaders` in `parity_test.go` (one entry, where upstream error
  responses carry a sniffed `text/plain` Content-Type because its `replyJSON`
  sets the header after `WriteHeader`, while CCO serves the correct
  `application/json`) and the `unmirrored` table in `coapi/drift_test.go`.
- One known gap the corpus does not probe: `maxResults`/`pageToken`
  pagination. CCO validates `maxResults` like upstream but always serves every
  host in one page and never emits `nextPageToken`.

## See also

- [../reference/legacy-api.md](../reference/legacy-api.md), the consumer reference
- [code-architecture.md](code-architecture.md), the native split-stack architecture
- [../concepts/authentication.md](../concepts/authentication.md), the IAP access model
