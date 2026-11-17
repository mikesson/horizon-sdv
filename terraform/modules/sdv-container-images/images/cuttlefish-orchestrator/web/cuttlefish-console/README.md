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

# Cuttlefish console

The Angular single-page app for the Cloud Cuttlefish Orchestrator. It calls the
orchestrator's `/v2` REST API and covers hosts, devices, builds, batches,
storage, machine types, quota and the admin pages.

The production build is copied into the orchestrator image and served from the
same origin as the API, so `apiBaseUrl` in `src/environments/environment.ts` is
empty and there is no dev proxy. `npm start` on its own serves the app with no
backend behind it; the browser tests mock the API at the network layer instead.

Run every command below from this directory.

## Layout

`src/app/` has one directory per business feature: `hosts/`, `devices/`,
`builds/`, `batches/`, `storage/`, `machine-types/`, `quota/`, `admin/`,
`dashboard/`, `auth/`, plus `shared/` for what more than one feature uses.
Feature directories share a shape.

| Path | Contents |
|---|---|
| `components/` | standalone components, templates and styles inline |
| `models/` | Zod schemas and the types inferred from them |
| `services/` | HTTP access, one service per resource |
| `store/` | the feature's NgRx Signal Store (`signalStore`) |
| `<feature>.routes.ts` | lazy routes, mounted from `src/app/app.routes.ts` |

Zod sits at the API boundary. A response is parsed with the feature's schema
before a store sees it, so a wire shape that no longer matches fails at the
call rather than three components later. The schemas are compared against the
backend's OpenAPI spec by a gate, so they cannot drift from it unnoticed.

## Develop

```bash
npm start          # dev server on http://localhost:4200
npm run watch      # development build, rebuilt on change
npm run build      # production build into dist/cuttlefish-console
npm run analyze    # production build plus a bundle stats file
```

## Unit tests

```bash
npm test                             # run the suite
npm test -- --watch=false --coverage # what scripts/ci.sh runs
```

Vitest runs through the Angular builder. Coverage floors live in
`angular.json`, under the `test` target's `coverageThresholds`, and the builder
fails the run when one is unmet. They are set just under measured reality, so
deleting a covered file can fail the run as readily as adding an uncovered one.

## Lint

`npm run lint` runs six legs and ORs their exit codes, so one failure does not
hide the next.

| Leg | What it checks |
|---|---|
| `lint:biome` | Biome over `src/`, `scripts/`, `e2e/`, warnings are errors |
| `lint:eslint` | type-aware ESLint, `--max-warnings=0` |
| `lint:css` | the CSS gate self-test, then Stylelint over `src/**/*.scss` and inline component styles |
| `typecheck:e2e` | `tsc` over the e2e sources against `e2e/tsconfig.json` |
| `check:fixture-shapes` | every e2e response fixture parses through the schema the app uses |
| `check:launch-templates` | the console's launch-template defaults against the backend's copy |

`npm run fix` applies what Biome and ESLint can fix on their own.

## End-to-end tests

```bash
npm run e2e         # headless Playwright run, starts npm start itself
npm run e2e:ui      # interactive mode
npm run e2e:report  # open the report from the last run
```

Chromium only, with the backend mocked at the network layer, so the suite needs
no orchestrator and no cloud project. See [e2e/README.md](e2e/README.md) for
the fixture model and for adding a journey.

## Gates

`scripts/gates/` holds every check that a lint leg or a `scripts/ci.sh` job
runs. `scripts/audit/` holds the on-demand tools, described in
[scripts/audit/README.md](scripts/audit/README.md).

| Gate | Command |
|---|---|
| `dead-css.mjs` | `npm run audit:dead-css` |
| knip, unused files, exports and dependencies | `npm run audit:knip` |
| `check-schema-drift.ts` | `npm run check:schema-drift` against a spec served by `tests/specgen` |
| `check-fixture-shapes.ts` | `npm run check:fixture-shapes` |
| `check-launch-templates.ts` | `npm run check:launch-templates` |
| `check-settings-cards.ts` | `npx tsx scripts/gates/check-settings-cards.ts` |
| `css-gate-selftest.mjs` | part of `npm run lint:css` |

`npm run audit:deps` is `npm audit --omit=dev`. The shipped dependency tree is
clean, so any advisory reaching it is news. The dev tree carries advisories
whose only offered fix is a major downgrade of the Angular CLI, which is why
the check is production-only.

The repository runner is [../../scripts/ci.sh](../../scripts/ci.sh). Its
console jobs are `webbuild`, `webtest`, `weblint`, `deadcss`, `knip`, `deps`
and `cards`, plus the opt-in `e2e`. Read its last line for the verdict.

More on the console's design is in
[../../docs/development/frontend.md](../../docs/development/frontend.md).
