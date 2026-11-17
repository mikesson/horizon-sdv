# Frontend Codebase Guide

## Architecture Overview

The Angular 22 console (`web/cuttlefish-console/`) is a zoneless,
signals-first, standalone-component SPA served by the orchestrator's Cloud
Run service. Cloud Run IAP signs the user in before the SPA ever loads, so
the frontend has **no login flow, no roles, and no permission gating**.
Every authenticated user sees the full UI.

```
src/app/
├── app.component.*   Shell: toolbar, nav rail, router outlet
├── app.config.ts     Providers (zoneless CD, router, interceptors)
├── app.routes.ts     Routes (all behind authGuard)
├── auth/             Guard, store, service, model
├── devices/          Device list/detail/create (wizard + advanced), player
├── hosts/            Host list/detail/create
├── builds/           Build registry
├── machine-types/    Machine type catalog (host-create + admin Catalog)
├── quota/            Global quota service + gauge
├── admin/            Capacity, Operations, Catalog, Activity pages
├── dashboard/        Landing dashboard
├── storage/          GCS artifact browser (a dialog inside build-create,
│                     not a nav destination)
└── shared/           Components, services, utils, models
```

Nav rail sections: **Main** {Dashboard} · **Resources** {Devices, Builds} ·
**Infrastructure** {Hosts} · **Administration** {Capacity, Operations,
Catalog, Activity} · **Developer** {API Documentation}. The IA is
device-first: devices and builds are the product surface, hosts are the
capacity fleet beneath them.

## Authentication Layer (`auth/`)

- **Model** (`models/user.model.ts`): `User { email, displayName }` is the
  whole auth model. No roles, permissions, organizations, or tenants.
- **Schema** (`models/user.schema.ts`): Zod validation of `/v2/me`.
- **Service** (`services/auth.service.ts`): `loadCurrentUser()`; `logout()`
  redirects to `/?gcp-iap-mode=CLEAR_LOGIN_COOKIE` (IAP sign-out).
- **Store** (`store/auth.signal-store.ts`): `user`, `isAuthenticated`,
  `isLoading` + `setUser`/`loadUser`/`logout` methods. No selectors for
  roles or orgs.
- **Guard** (`guards/auth.guard.ts`): waits for the initial `/v2/me` load;
  unauthenticated state routes to `/unauthorized` (IAP normally prevents
  this from ever happening).
- **Bootstrap**: a `provideAppInitializer` in `app.config.ts` loads `/v2/me`
  before the router starts so guards see settled state.

## State Management

Feature stores use **NgRx Signal Store** (`@ngrx/signals` +
`signals/entities`). Classic NgRx (store/effects/devtools) is not used.

```ts
export const DeviceStore = signalStore(
  {providedIn: 'root'},
  withEntities<Device>(),
  withState(initialState),
  withComputed(...),
  withMethods((store, api = inject(DeviceApiService)) => ({...})),
);
```

## API Layer

API services validate every response with **Zod v4** schemas
(`*.schema.ts`) and transform snake_case → camelCase. Register every schema
in `scripts/schema-registry.ts`. An unregistered schema is silently outside
both gates below. Keep schemas in sync with the backend:

```bash
go run ./tests/specgen &          # serves /openapi.json on :8888
npm run check:schema-drift        # Zod fields against the spec
npm run check:fixture-shapes      # e2e fixtures against the Zod schemas
```

The fixture gate also runs as a leg of `npm run lint`, so a schema change
that breaks the browser suite's mock fails there in seconds instead of in
the opt-in e2e job.

## Component Patterns

- Standalone components, `ChangeDetectionStrategy.OnPush`
- `input()` / `output()` signal APIs
- `@if` / `@for` (with `track`) control flow
- No method calls in templates; use signals/computed

## WebRTC Streaming (`devices/services/streaming/`)

`DeviceStreamService` is a registry of per-device `DeviceStreamSession`
objects. Each session owns one connection end to end: the signaling
transport (WebSocket-first via `/v2/devices/{id}/signaling/ws`, HTTP
long-poll fallback), the `RTCPeerConnection` with the fixed upstream data
channel set, renegotiation, and reconnection backoff. UI-facing state
(`state`, `streams`, `metrics`, `error`) is exposed as read-only signals; the
WebRTC machinery itself never lives in signals.

`GET /v2/devices/{id}/ice-config` answers with **one list per end** of the
connection. `client_servers` configures the local `RTCPeerConnection`;
`device_servers` is sent to the device in `request-offer` and is never used
locally. They name the same relays at different addresses, the browser's
external and the host's internal, so sending the wrong one produces a
connection that never establishes. `ice_transport_policy` comes from the
deployment (`relay` where device media never meets the internet at the host)
and binds this end only; under it the candidate pool drops to zero, since
each pooled session would allocate on every TURN server for candidates it
will never use.

There is no credential refresh. An expiry is baked into the TURN username, so
renewing means a new allocation, and re-gathering means rebuilding the
connection, which the session never does in place. It reconnects instead.
Expiry therefore arrives as an ICE failure and is handled as one.

A deployment with no relay is not an error. The endpoint answers with STUN
alone. A 5xx here is a real failure and fails the session,
because the backend serving it is the one serving signaling. Inbound
signaling messages are Zod-validated at the transport boundary.

The Cuttlefish wire protocol (device-as-offerer, asymmetric ICE formats,
`?start=N` poll counter) is pinned by contract tests in
`streaming-protocol.spec.ts` (the real stack over faked HTTP, WebSocket,
and RTCPeerConnection boundaries) plus a stubbed-WebRTC Playwright spec
(`e2e/device-player.spec.ts`). Run both after touching anything in
`streaming/`; a live device cannot be assumed for verification.

## ADB over WebRTC (`devices/services/adb/`)

`AdbService` is a registry of per-device `AdbSession` objects with **claim
counting**: every consumer (device player page, logcat, tool panels)
acquires its own claim via `acquire(deviceId)` and releases it when done;
the session is disposed only when the last claim goes. A session owns the
ya-webadb `Adb` instance and a watcher effect bound to the device's
streaming session. It authenticates over the `adb-channel` data channel
when the stream reports it ready (Cuttlefish's proxy answers CNXN directly,
no AUTH round) and invalidates when the stream drops, without ever closing
the channel itself (it belongs to the streaming session and cannot be
reopened).

Operations are plain composable functions over a connected `Adb` device,
obtained with `session.ensureConnected()`. Higher-level Android tooling
comes from `@yume-chan/android-bin`, but its classes are only ever
constructed inside these facade files:

- `adb-exec.ts`: `exec`, `execFull`, `shellPty`, `screenshot`
- `adb-files.ts`: directory listing, read/write/delete, mkdir
- `adb-packages.ts`: list/uninstall/launch/force-stop/clear
- `adb-install.ts`: streaming single-APK install and split-APK install
  sessions with byte-level progress, cancellation, and downgrade/test flags
- `install-plan.ts` / `apk-archive.ts`: expand `.apks`/`.xapk`/`.apkm`
  bundles (via the dependency-free `shared/utils/zip-reader.ts`) and group
  files into install units by parsed package
- `apk-info.ts` / `axml-parser.ts`: read package/version/split/ABI straight
  from an APK's binary `AndroidManifest.xml`, no device round-trip
- `install-split-select.ts`: pick the base + device-matching ABI/density
  splits for a full bundle (falls back to all splits on `MISSING_SPLIT`)
- `install-errors.ts`: classify `pm install` failures into actionable
  remedies (downgrade, uninstall-and-retry, install-all-splits, test)
- `device-install.service.ts`: per-device install-state registry so
  progress/cancel/remedies survive leaving the app panel; drives the
  app-manager overlay, the player-wide drop zone, and the header indicator
- `artifact-install.service.ts`: stream a GCS build artifact onto a device
  (headlessly bringing its stream up if needed) from the storage browser
- `adb-logcat.ts`: binary (`logcat -B`) streams with buffer/pid selection,
  structured-entry mapping, device-buffer clear, pid resolution
- `adb-diagnostics.ts`: bugreport capture, animation scales, adbd
  root/unroot (root is UNVERIFIED against a live Cuttlefish; requires a
  stream restart because the proxy's adb channel dies with adbd)

`webrtc-adb-connection.ts` bridges the RTCDataChannel to ya-webadb's packet
streams (24-byte header framing, magic validation with byte-skip resync,
outbound backpressure).

`LogcatService` follows the same shape: per-device `LogcatSession` holding
its own ADB claim, streaming structured binary entries, and exposing them
as signals with batched flushes (one emission per 100 ms, 10k-entry cap).
Changing buffers or the app filter restarts the underlying stream; display
filtering stays a `computed` over `logcat-model.ts`'s pure `applyLogFilter`.

Device screenshots default to an instant, ADB-free canvas capture of the
WebRTC frame (`video-display/capture-frame.ts`, per display); the
pixel-exact `screencap` path remains in the screenshot menu.

The framing, the no-AUTH handshake, and the claim lifecycle are pinned by
`adb/webrtc-adb-connection.spec.ts`, `adb/adb-lifecycle.spec.ts` (a fake
ADB daemon answering CNXN over a fake channel), `adb/adb-commands.spec.ts`
(escaping + split names), and `logcat-model.spec.ts` (entry mapping +
filtering). Run them after touching anything in `adb/`; as with streaming,
no live device can be assumed.

## Testing & Linting

```bash
npm test        # Vitest
npm run lint    # Biome + angular-eslint
npm run fix     # auto-fix
npm run build   # production build
```

Linting is **Biome + angular-eslint**. The project no longer uses gts.

## Local Development

```bash
npm start       # localhost:4200
```

The dev server proxies API calls to a local backend. Run the backend with
`CCO_DEV_MODE=true` and a `CCO_DEV_FALLBACK_EMAIL` (in your local `.env`;
see `.env.example`) so `/v2/me` resolves without IAP (the dev server is also
a trusted CSRF origin).
