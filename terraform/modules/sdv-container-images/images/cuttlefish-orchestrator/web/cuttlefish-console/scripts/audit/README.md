<!--
Copyright 2026 Google LLC
SPDX-License-Identifier: Apache-2.0
-->

# Frontend audit toolkit

Tools to catch dead code, bloat, and bundle weight. Run from
`web/cuttlefish-console/`. Layout: anything a `scripts/ci.sh` job or an
`npm run lint` leg executes lives in `scripts/gates/`; this directory keeps
only the on-demand halves (`coverage.mjs`, `css-usage-report.mjs`). The npm
script names below are the stable interface either way.

| Command | What it finds | Gates |
|---|---|---|
| `npm run audit:dead-css` | dead custom properties, dead selectors, palette values that were deleted | yes, `deadcss` in `scripts/ci.sh` |
| `npm run audit:knip` | unused files / exports / dependencies | yes |
| `npm run audit:css-usage` | rules the browser parsed but nothing ever matched | no, needs the e2e suite |
| `npm run audit` | dead-css + Knip, one shot, non-gating | no |
| `npm run analyze` | bundle treemap (raw + which module owns the bytes) | no |
| `npm run coverage:ui` | JS bytes the app never executes (see the caveat below) | no |

## Three questions, three tools

Most unused-CSS tooling (PurgeCSS, `stylelint-no-unused-selectors`) assumes a
component is a template file plus a co-located stylesheet. This console keeps
both **inline** in the `@Component` decorator and applies classes dynamically
(`[class.x]`, `[class]="signal()"`), which those tools can't follow. So the CSS
story here splits by what each question can actually be answered with:

- **Did anyone write the name?** `audit:dead-css`, static and inline-aware.
  Diffs selectors *defined* in a component's `styles` against class names
  *referenced* anywhere in the same component, and does the same for the global
  stylesheets under the opposite rule: a class there is alive if any template
  anywhere mentions it. The global list is read from `angular.json`, following
  each sheet's `@use` graph, so a sheet added to the build is covered without
  editing the gate. No other dead-code check looks at them. Zero deps, fast,
  gating.
- **Did anything ever match it?** `audit:css-usage`, runtime. Rides the
  Playwright suite and asks the browser which parsed rules found an element.
  Catches names assembled at runtime and rules aimed at Material's own DOM,
  which the static pass skips by construction.
- **Did the declaration win?** Neither of them. A rule can match a real
  element and still be overridden, and a custom property can be set to a name
  nothing reads. Only a computed value settles it: `e2e/theme-accent.spec.ts`
  asserts the rendered accent colour on ten Material controls for this reason.

## What each tool cannot decide

- **`audit:dead-css` and `--mat-*`.** CCO declares those for Material to read,
  so a local `var()` search sees no consumer whether the override is live or
  dead. The pass judges only `--cco-*`, where this repo owns both ends. A dead
  `--mat-*` name is invisible to every static tool and was how 47 overrides
  came to ship inert.
- **`audit:dead-css` and framework selectors.** `mat-`, `mdc-`, `cdk-`, `ng-`,
  `cm-` and `xterm` prefixes target library-rendered DOM and are skipped. Add
  new prefixes to `FRAMEWORK_PREFIXES` in `scripts/gates/dead-css.mjs`.
- **`audit:css-usage` and coverage.** A rule is unused there only if the suite
  never rendered its element. Read it as a lower bound on liveness, and widen
  the suite before deleting anything it reports. Unmounted components drop out
  of the CSSOM, so a rule seen in no test appears in neither column.
- **Knip dependency findings** need judgment: a flagged dep may be a
  peer/transitive requirement, and a "used but unlisted" import should be
  *added* to `package.json`, not ignored.
- **`coverage:ui` walks routes against a live dev server, and nothing mocks the
  API.** `environment.ts` sets `apiBaseUrl: ''` and there is no proxy in
  `angular.json`, so every `/v2` call gets `index.html` back and each route
  renders its error or empty state. Its CSS numbers therefore describe an app
  in permanent failure; use `audit:css-usage` for CSS. The JS side is still
  informative because the bundle is fetched either way.

## Findings that gate, and findings that ratchet

`audit:dead-css` fails on **certain** findings: a `--cco-*` property nothing
reads, a `var(--cco-*)` nothing declares, a `var(--cco-*, fallback)`, any
`--mdc-*` name, a deleted light-ramp hex. These are closed questions and are
never allow-listed.

The fallback rule is the least obvious of the five. Because this pass fails
when a `--cco-*` property is read and not declared, every one of them *is*
declared on `:root`, so the fallback arm can never be taken. What it can do is
carry a second, unmaintained copy of the palette: four of the eighteen
fallbacks in the tree held colours the palette had already moved off, which
reads as the rendered value and is not. Write `var(--cco-error)`, never
`var(--cco-error, #f28b82)`, and never `var(--cco-error, var(--cco-error))`,
which a mechanical hex-to-token sweep will otherwise leave behind.

Its **selector** findings are heuristic, so they ratchet against
`.deadcss-allow` (`path<TAB>.selector`, reason required). A new one fails; a
recorded one does not.

## Running the runtime sweep

```bash
scripts/ci.sh --fix-env           # from the repo root, restores the browsers
npm run audit:css-usage           # runs the suite with recording on, then reports
node scripts/audit/css-usage-report.mjs --all   # include Material's own rules
```

`CCO_CSS_USAGE=1` is what arms the fixture; without it the suite behaves
exactly as before. Per-test results land in `test-results/*/css-usage.json`.

Recording is not free. It enables the CSS domain on every page and costs
wall-clock. Keep it opt-in: an earlier version fetched each stylesheet's text
as Angular injected it, one round-trip per component, and that alone took the
suite from 5.5 to 8.5 minutes and pushed eight tests past their expect
timeouts. `playwright.config.ts` now raises the `expect` timeout while
recording for that reason. If tests start failing only under recording, suspect
the fixture before the assertion.
