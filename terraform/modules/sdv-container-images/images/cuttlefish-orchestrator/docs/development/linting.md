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

# Linting Guide

This project lints both the Go backend and the Angular frontend.

## Quick Start

```bash
# Backend (Go)
./bin/golangci-lint run

# Frontend (Angular) - from web/cuttlefish-console/
npm run lint        # Run Biome + ESLint
npm run fix         # Auto-fix all issues
```

---

## Backend (Go)

### Commands

```bash
# Run all linters
./bin/golangci-lint run

# Auto-fix issues
./bin/golangci-lint run --fix

# Lint a subset: DIRECTORIES, not import paths. Import paths match nothing
# and print a convincing "0 issues."
./bin/golangci-lint run internal/features/device

# Verbose output with timing
./bin/golangci-lint run -v

# Output as JSON
./bin/golangci-lint run --output.json.path=stdout
```

The tree reports zero findings and `scripts/ci.sh lint` gates on that: any
finding at all fails the run. There is no baseline and no per-file ratchet.
Those existed to tolerate a backlog that is gone. A finding you believe is
wrong gets a `//nolint` with a reason. Every other finding gets fixed.

The modules under `tools/` have their own `go.mod`, so a run from the repo
root never reaches them. `scripts/ci.sh lint` runs the linter once per tools
module as well; they share the root config, which the linter finds by walking
up.

### Enabled Linters

`.golangci.yml` is the authoritative list and groups them into nine tiers:
core correctness, Google style, modern Go idioms, bug detection, code quality,
complexity control, architecture (`depguard` layering and `goheader` license
headers), security, and logging consistency. A copy of the list here would
only go stale.

### Suppressing False Positives

Use `//nolint` comments sparingly. `nolintlint` requires a specific linter name
and an explanation, so a bare `//nolint` is itself a finding:

```go
//nolint:gosec // G402: InsecureSkipVerify required for self-signed certs
resp, err := client.Do(req)

//nolint:errcheck,gosec // The write is best-effort and the path is trusted.
doSomething()
```

Two suppressions are permanent and settled: the `revive` stutter on
`device.DeviceLifecycle` and `host.HostLifecycle`. Both names are published
OpenAPI component schemas, present verbatim in the spec-generated cfctl models,
so renaming the Go type renames the schema and the generated client with it.
Shortening both to `Lifecycle` would put two packages on one schema name,
which Huma rejects.

### Configuration

Config file: `.golangci.yml`

Key settings:
- **Timeout**: 5 minutes
- **Go version**: 1.26
- **Import grouping**: stdlib → third-party → local (`cloud-cuttlefish-orchestrator`)
- **Architecture**: domain imports stdlib and other domain packages only;
  features may import domain, platform and web but never another feature;
  platform may import domain; jobs may import domain and platform; only app
  crosses the firewall

`run` does not validate the config: a settings block for an option a linter
does not have is silently ignored. Run `./bin/golangci-lint config verify`
after every edit. `scripts/ci.sh lint` runs it before `run` for that reason.

### Troubleshooting

```bash
# Clear cache if linter behaves unexpectedly
./bin/golangci-lint cache clean

# Check config syntax
./bin/golangci-lint config verify

# Debug why a linter isn't running
./bin/golangci-lint run -v 2>&1 | grep -i "linter"
```

---

## Frontend (Angular/TypeScript)

The frontend uses a hybrid linting approach:

- **Biome** - Fast formatter + general TypeScript/JavaScript linting
- **ESLint 10** - Angular-specific rules with flat config
- **angular-eslint** - Component and template linting
- **typescript-eslint** - Type-aware TypeScript rules

### Commands

Run from `web/cuttlefish-console/`:

```bash
# Run all linters (Biome + ESLint)
npm run lint

# Run individual linters
npm run lint:biome    # Fast - formatting + general rules
npm run lint:eslint   # Angular-specific rules

# Auto-fix issues
npm run fix           # Fix both Biome and ESLint
npm run fix:biome     # Biome only
npm run fix:eslint    # ESLint only

# Format only (no linting)
npm run format
```

### Tool Responsibilities

| Tool | Handles |
|------|---------|
| **Biome** | Formatting, import organization, unused variables, naming conventions, code complexity |
| **ESLint** | Angular component rules, template linting, type-aware rules, accessibility |

### Biome Rules

Configuration: `biome.json`

**Formatting (Google TypeScript Style):**
- 2-space indentation
- Single quotes
- Semicolons always
- 100 character line width
- ES5 trailing commas

**Key Linting Rules:**
- `noExplicitAny` - Disallow `any` type
- `noUnusedVariables` / `noUnusedImports` - Remove dead code
- `useConst` - Prefer const over let
- `useNamingConvention` - Enforce camelCase/PascalCase/UPPER_CASE
- `noConsole` - Warn on console.log (allow error/warn/info)

### ESLint Rules

Configuration: `eslint.config.js` (flat config)

**TypeScript Rules:**
- `explicit-function-return-type` - Require return types
- `consistent-type-imports` - Use `import type` for type-only imports
- `prefer-readonly` - Mark unmodified properties as readonly
- `naming-convention` - Google style naming (no I-prefix on interfaces)
- `no-explicit-any` - Disallow any type

**Angular Component Rules:**
- `prefer-standalone` - Require standalone components
- `prefer-on-push-component-change-detection` - Require OnPush
- `use-lifecycle-interface` - Require lifecycle interfaces
- `prefer-signals` - Prefer signal-based inputs/outputs

**Angular Template Rules:**
- `prefer-control-flow` - Use `@if`/`@for` over `*ngIf`/`*ngFor`
- `prefer-self-closing-tags` - Use `<app-foo />` for empty elements
- `no-duplicate-attributes` - Detect duplicate attributes
- `use-track-by-function` - Require trackBy in loops
- Accessibility rules (alt-text, ARIA, keyboard events)

### Suppressing Issues

**Biome:**
```typescript
// biome-ignore lint/suspicious/noExplicitAny: Required for legacy API
const data: any = response;

// biome-ignore format: Keep manual formatting
const matrix = [
  [1, 2, 3],
  [4, 5, 6],
];
```

**ESLint:**
```typescript
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const data: any = response;

/* eslint-disable @angular-eslint/prefer-on-push-component-change-detection */
@Component({...})
export class LegacyComponent {}
/* eslint-enable */
```

### Configuration Files

| File | Purpose |
|------|---------|
| `biome.json` | Biome formatter + linter config |
| `eslint.config.js` | ESLint flat config for Angular |
| `tsconfig.json` | TypeScript config (used by ESLint for type-aware rules) |

### Why This Setup?

**Why Biome as well as ESLint?**
- Biome is 10-100x faster for formatting and basic linting
- It reduces ESLint's workload to Angular-specific rules only

**Why ESLint as well as Biome?**
- Biome lacks Angular template support
- Biome has known issues with Angular DI patterns
- angular-eslint provides essential component/template rules

**Why not gts (Google TypeScript Style)?**
- gts uses ESLint 8.x, incompatible with the angular-eslint the console is on
- We implement Google style rules directly in Biome + ESLint

---

## Shell scripts

`scripts/ci.sh shellcheck` runs shellcheck over every tracked script under
`scripts/`. `SC2317` is suppressed for the whole run: `ci.sh` dispatches its
jobs indirectly as `"job_$job"`, and shellcheck reads every one of them as
unreachable code. Nothing else is suppressed, and a missing shellcheck binary
fails the job instead of passing silently.

---

## CI Integration

There is no hosted CI. `scripts/ci.sh` is the gate and it runs on this machine:

```bash
scripts/ci.sh lint weblint shellcheck   # just the lint gates
scripts/ci.sh                           # every gate except e2e
```

Exit status is the verdict, and the run's last line states it in words: a
`RESULT: FAIL` line naming the jobs that failed. The summary prints
`/tmp/cco-ci-logs/lint.log` beside the failure. The lint gates are read-only,
so they run without the machine lock and stay usable while a full run is in
flight.
