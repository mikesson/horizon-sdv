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

# Legacy API compatibility (`/v1`)

> For how this layer is built internally, see
> [../development/legacy-compat-layer.md](../development/legacy-compat-layer.md).

CCO's native API lives at **`/v2`**. The **`/v1`** namespace is a byte-for-byte
compatibility layer for the legacy Cloud Android Orchestrator (CO) and Android
Cuttlefish Host Orchestrator (HO) APIs, so existing integrations (`cvdr`, the
`cvd` CLI / `libhoclient`, and direct API callers) keep working unchanged.

Clients authenticate like any other CCO caller: a service account on the IAP
access list presenting a self-signed JWT. For the end-to-end `cvdr` setup
(service account, token minting, `cvdr.toml`), see
[Quick Start §7](../getting-started/quick-start.md#7-cli-access-a-service-account-for-cfctl-and-cvdr).

## What is served at `/v1`

### Cloud Orchestrator surface

Re-implemented over CCO's native services, returning the verbatim upstream wire
shapes:

| Method | Path |
| --- | --- |
| GET | `/v1/zones` |
| POST / GET | `/v1/zones/{zone}/hosts` |
| DELETE | `/v1/zones/{zone}/hosts/{host}` |
| POST | `/v1/zones/{zone}/operations/{operation}/:wait` |
| GET | `/v1/config` |
| GET | `/v1/zones/{zone}/hosts/{host}/infra_config` |

Notes:

- The legacy host **name** is the CCO host ID returned by create/list.
- Host create honors `machine_type`, `min_cpu_platform`, `boot_disk_size_gb`
  and a **single** accelerator config; a request with more than one
  accelerator config is rejected with `400` (upstream maps the full list, but
  CCO models one accelerator shape end to end, and an explicit refusal is
  better than the silent truncation this mapping used to do). List responses
  echo the platform and the effective boot-disk size.
- A **docker-only** create (`host_instance.docker`, upstream's on-premises
  shape, which is what OmniLab ATS sends) provisions a GCE host from the
  operator's `docker-host` system-config pair (machine type plus lease hours;
  compiled defaults `n1-standard-8` and 12 h). `image_name` is ignored,
  because the deployment has exactly one host image. The host is created
  exclusive with `max_devices` 1, and the orphan-CVD census skips it: its CVDs
  arrive through the proxy below with no CCO device rows, and the lease set at
  create is its whole lifetime, which nothing can extend. A body with both
  sub-messages is a gcp create; with neither, `400`.
- Host create/delete are asynchronous and return a legacy `Operation`. Poll it
  with `:wait`, which **blocks until the resource settles** and then returns the
  resource directly (the `HostInstance` for a create, `{}` for a delete), or
  `503` on timeout (re-issue `:wait`).
- `infra_config` answers the wire shape the upstream CO does: the public STUN
  servers, plus, where the deployment relays media, one TURN entry whose
  credential CCO **mints per request**, scoped to the host in the path and to
  the caller. Upstream serves a static credential from its config file; CCO
  cannot, because its relays hold no secret to verify one against. The
  credential is long-lived (12h) because the legacy client reads this endpoint
  once per connection and has no way to renew.
- That TURN entry carries the URIs for **both legs** in one list, the client's
  and the device's. The legacy contract admits only one list, which the client
  hands to its own peer connection and to the device alike, so each end has to
  find its own reachable URI among the other's.

### Zone tokens are placement hints

By default (`placement.v1_zone_is_hint`, settable via
`PUT /v2/admin/system-config/v1-zone-hint`), the `{zone}` in a `/v1` host
create is read as a **placement hint** and not a literal pin: the server ranks
the actual zone across its enabled catalog (capacity headroom, recent
failures), and a token CCO has never cataloged, say a Google-default zone
hardcoded in an existing `cvdr` config, still **admits** and gets dynamic
placement. Consequences for legacy clients, all invisible to them:

- The token is persisted as the host's requested zone, and
  `GET /v1/zones/{zone}/hosts` lists by **requested-or-physical** zone, so a
  host stays visible under the token it was created with wherever placement
  (or a later automatic zone failover) physically lands it, so
  create → list → delete flows never lose a host.
- Everything else resolves by host ID (get, delete, `:wait`, the HO
  passthrough), so a zone move during provisioning is unobservable beyond
  ordinary provisioning latency absorbed by `:wait`.
- Setting the policy to `false` restores the literal behavior: the token
  pins the host to that exact zone and it is never moved.

`GET /v1/zones` continues to list the enabled catalog zones.

The contract mirrors the upstream `cloud-android-orchestration` `api/v1` types.

### Host Orchestrator passthrough

```
ANY /v1/zones/{zone}/hosts/{host}/{path...}
```

is reverse-proxied **verbatim** to the on-VM Host Orchestrator. This exposes the
entire HO API byte-for-byte, without re-implementing any of it: `cvds`,
`operations`, `userartifacts`, displays, snapshots, screen recordings, the
`connect_client` WebSocket, and so on.

The HO contract is **documented by the upstream swagger**, which is the canonical
source (CCO serves it unchanged; it is intentionally not re-generated here to
avoid drift):

- `android-cuttlefish/frontend/src/host_orchestrator/docs/swagger.yaml`

One caveat for snapshots: the native `/v2` snapshot verbs keep a server-side
inventory (listing, lost-marking when a host's disk is recreated, cleanup on
device delete). A snapshot created through this `/v1` passthrough (`cvdr
snapshot`, or a raw `POST .../cvds/{group}/{instance}/snapshots`) lands on
the host but is **invisible to that inventory**: `/v2` listings, the console
card, and the automatic cleanup do not know it exists, exactly as upstream
never tracks it. It remains usable through `/v1` (`cvdr start
--snapshot_id=...`) until the host's disk goes away.

#### Bugreports through `/v1` need a recent client

A client built against **libhoclient older than 2025-10-22** cannot download a
bugreport through `/v1` under IAP, and **fails silently** rather than erroring.
Its final `GET /cvdbugreports/{uuid}` was built as a raw request instead of going
through the helper that attaches `Authorization`, so IAP answers the
unauthenticated call with a redirect to a sign-in page. The client follows it,
gets a 200, and writes the HTML into the destination file, producing a
"bugreport" of roughly 900 KB that is a web page. Nothing in the passthrough can prevent this:
IAP rejects the call before it reaches CCO, and exempting the path would let
anyone download bugreports.

The capture itself is fine. Fetching the same artifact with a bearer token
returns the real archive:

```bash
curl -H "Authorization: Bearer $TOKEN" \
  ".../v1/zones/{zone}/hosts/{host}/cvdbugreports/{uuid}" -o bugreport.zip
```

Remedies, in order of preference:

- Use `cfctl device bugreport <device>`, or the console. That path captures
  server-side and stores the archive in the artifact bucket, so it is unaffected.
- Rebuild the client against libhoclient ≥ 2025-10-22, which fixed the missing
  credential.

A rebuilt client still writes the response body **before** checking the
status, so a token that expires mid-capture reproduces the same silent result.
An `--include_adb_bugreport` capture takes minutes and legacy tokens are
short-lived. **Always confirm a downloaded artifact opens as an archive**
instead of trusting its size; `unzip -l bugreport.zip` is enough.

## API documentation

- **Native `/v2`**: auto-generated OpenAPI at `/openapi.json`, browsable at
  `/docs`.
- **Legacy `/v1`**: the upstream contract above. It is not re-documented as a
  second OpenAPI spec.

## Build-API credentials

The legacy CO's server-mediated OAuth flow (`/auth`, `/oauth2callback`,
`/deauth` + header injection on the proxy) is **not implemented**. Clients that
need Android Build API credentials on the host supply them client-side:
cvdr's `--build_api_creds_source=oauth:<file>` and `jwt:<file>` modes set the
`X-Cutf-Host-Orchestrator-BuildAPI-Creds` header themselves, and the `/v1`
proxy forwards it to the Host Orchestrator verbatim. The server-mediated
`injected` mode is the one cvdr option CCO does not serve.
