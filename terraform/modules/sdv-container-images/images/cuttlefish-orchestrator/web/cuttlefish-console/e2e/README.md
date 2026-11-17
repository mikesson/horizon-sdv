<!--
Copyright 2026 Google LLC
SPDX-License-Identifier: Apache-2.0
-->

# End-to-end tests (Playwright)

Full user-journey tests that drive the real Angular app in a browser with the
backend **mocked at the network layer**. They cover what the Vitest unit tests
can't: routing, the auth guard, multi-step forms, store→API wiring, and that a
page actually renders its data.

| Command | What it does |
|---|---|
| `npm run e2e` | Run the suite headless (auto-starts `npm start`). |
| `npm run e2e:ui` | Interactive Playwright UI mode (watch, time-travel). |
| `npm run e2e:report` | Open the HTML report from the last run. |

```bash
# Headless, full run
npm run e2e

# A single spec / grep
npx playwright test e2e/host-create.spec.ts
npx playwright test -g "wizard"

# Against an already-running app instead of auto-starting one
E2E_BASE_URL=http://localhost:4200 npm run e2e
```

## Coverage

| Spec | What it covers |
|---|---|
| `smoke` | every route renders, binds its data, no uncaught errors |
| `host-create` | the create journey, landing on the new host's detail page |
| `device-create` | the interview (Android/Usage/Duration/Plan incl. the validate_only dry run): automatic placement, empty-fleet register+provision plan, `?hostId` pre-answered imperative, lifecycle carry |
| `build-create` | the build registration journey |
| `empty-states` | each list's empty state (collection returns `[]`) |
| `error-states` | list-load failure + create failure surface error UI (`mockError`) |
| `lifecycle` | host/device start·stop·delete (+ confirm dialogs), build set-as-default, build-row launch action, tracking-vs-pinned chip, running-row player link |
| `device-create-chooser` | the mode chooser: each card's route, handoff params carried into a mode (the SDV card drops a handoff build), Back and Change mode, the SDV preset entry |
| `device-create-advanced` / `-flows` | the seeded Simple→Advanced eject, the device name field, the two-pane layout with a long JSON line, instance management, submitted payload shape, import + drafts |
| `device-player` / `admin-image-config` | stubbed-WebRTC player boot; admin host-image config flows |
| `batch-create` / `device-instances` / `idempotency` / `owner-scope` | batch creation, a device's named instances, the `Idempotency-Key` on create, owner-scoped list defaults |
| `settings` / `activity-filters` / `operations-panel` / `artifacts` | the settings page, server-side activity filtering, the fleet-wide operations panel, bugreport capture |
| `capacity-board` / `capacity-spot` / `capacity-zones` / `catalog-families` / `autoscaler` | the capacity board, the Spot card, zone curation, family curation, the autoscaler canvas |
| `theme-light` / `theme-surfaces` / `theme-accent` / `appearance` / `button-contrast` | themes, surfaces and contrast, all asserted on computed colour |
| `accessibility` / `container-queries` / `layout-containment` / `list-truncation` | an axe scan per route, and the layout rules a unit test cannot see |

Test helpers (from the `api-mock` fixture):
- `mockError(page, /\/v2\/hosts/, 500)` forces a route to fail. Register it before navigating.
- For an empty state, set `api.hosts = []` (etc.) before navigating. The list endpoint reads state at request time.
- DELETE is stateful. A deleted row disappears from the next list read.

## How it works

- **No backend.** `e2e/fixtures/api-mock.ts` intercepts every `/v2/**` call
  with `page.route`; the data and every response body live as composers in
  `e2e/fixtures/api-fixtures.ts`, typed `z.input<typeof Schema>` against the
  schemas the app parses with. The app validates responses with `.parse()`
  (which throws on mismatch), so fixtures are shaped exactly like the real
  API. Two gates hold them there. `typecheck:e2e` catches a required-field or
  typo'd-key drift, and `npm run check:fixture-shapes` (a `npm run lint` leg)
  parses each composed body through the real schema.
- **Fallbacks are recorded.** A request no dedicated route answers lands in
  `api.unhandledRequests`, so a spec can assert its page never rendered
  against a body no schema blessed. That covers the catch-all's generic list
  shape and an item route echoing an entity at a sub-path it does not model.
- **Stateful.** POSTs append to in-memory collections, so a resource created in
  a journey shows up in later list/detail reads. The created body is asserted
  directly (the definitive "the flow completed" signal).
- **Auto-mocked.** The `api` fixture is `auto`, so `/v2/me` is mocked before the
  app boots. Otherwise the auth guard redirects to `/unauthorized`.
- **Error gate.** The `pageErrors` fixture collects uncaught exceptions; the
  smoke specs assert there were none.

The mocked backend can't exercise real GCE provisioning or device streaming
(those need live infrastructure), so these tests stop at the API boundary. They
prove the frontend issues the right request and reacts to the response.

## Adding a journey

1. Seed any extra state by editing `defaultState()` in
   `e2e/fixtures/api-fixtures.ts`, or mutate `api` inside a test before
   navigating. A new response body gets a composer there plus a parse row in
   `scripts/gates/check-fixture-shapes.ts`. The gate fails a registered schema
   that has neither.
2. Prefer role/text selectors (`getByRole`, `getByLabel`, `getByText`). The app
   has no `data-testid`s.
3. Assert the **request** for create/mutate flows (`page.waitForRequest`) plus a
   user-visible result (navigation or snackbar).

## Browser setup

The suite needs two things beyond `node_modules`: the Chromium binary
Playwright downloads (into `~/.cache/ms-playwright`) and the OS libraries that
binary links against. A machine whose system packages were reset, such as a
fresh container or a reimaged host, keeps the downloaded browser and loses the
libraries, and `npm run e2e` then fails at launch.

One entry point restores whatever is missing:

```bash
scripts/ci.sh --fix-env      # from the repo root; then: npm run e2e
```

It runs this project's `scripts/e2e-bootstrap.sh`, which is idempotent: `npm
ci` only when `node_modules` is absent, the Chromium download only when the
binary is absent, then the OS packages every time. The manual equivalent:

```bash
npm ci                                   # only if node_modules is missing
npx playwright install chromium
sudo env "PATH=$PATH" npx playwright install-deps chromium
```

## Recording which CSS rules matched

`CCO_CSS_USAGE=1` arms the `cssUsage` fixture. It asks the browser which parsed
CSS rules found an element and writes one `css-usage.json` per test under
`test-results/`. `npm run audit:css-usage` runs the suite that way and then
prints the report. Recording is not free, so `playwright.config.ts` raises the
`expect` timeout while it is on; nothing else about the suite changes. See
[../scripts/audit/README.md](../scripts/audit/README.md) for what the numbers
can and cannot decide.
