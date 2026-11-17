<!-- Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. -->

# Third-Party Notices

Cloud Cuttlefish Orchestrator includes third-party open source and source-available
software. This file lists the components that ship in the product containers
(`cuttlefish-orchestrator-api`, `cuttlefish-console`, and `cuttlefish-workflow`),
runtime browser assets served by the API, and the license each one carries.

The Go list covers every module reachable from the shipped binaries
(`./cmd/orchestrator`, `./cmd/cfctl`, `./cmd/relayhook`). The separate modules
under `tools/` and packages imported only by tests are developer tooling,
they are not linked into a shipped binary, and they are excluded here.

The npm list covers the production dependency tree of the Angular console from
`web/cuttlefish-console/package-lock.json` (excluding `"dev": true` and
`"devOptional": true` entries). Build-only dependencies such as `typescript@6.0.3`
(`Apache-2.0`, marked `"devOptional": true` in `package-lock.json`) do not ship in
the compiled browser bundle and are excluded from the runtime count. The API
documentation endpoint (`/docs`) additionally loads `@scalar/api-reference@1.44.20`
(`MIT`) from unpkg with Subresource Integrity (`sha384-...`) in `internal/router/scalar.go`.

Regenerated 2026-11-17. The verification commands are in
[Generating Updated Notices](#generating-updated-notices).

## License Summary

### Backend (Go modules)

| License | Count |
|---|---|
| Apache-2.0 | 43 |
| BSD-3-Clause | 13 |
| MIT | 7 |
| ISC | 1 |

62 unique Go modules, 64 license entries. `github.com/go-jose/go-jose/v4` appears
twice (`Apache-2.0`, plus `BSD-3-Clause` for its vendored `json` package), and
`go.yaml.in/yaml/v3` appears twice (libyaml C ports under `MIT`, plus remaining
Go files under `Apache-2.0`).

### Frontend (npm packages)

| License | Count |
|---|---|
| MIT | 161 |
| ISC | 9 |
| Apache-2.0 | 3 |
| BSD-2-Clause | 3 |
| 0BSD | 1 |
| BSD-3-Clause | 1 |
| CC-BY-4.0 | 1 |
| CC0-1.0 | 1 |
| Python-2.0 | 1 |
| AFL-2.1 or BSD-3-Clause | 1 |

182 unique production `name@version` tuples (**179** unique package names;
`convert-source-map`, `string-width`, and `entities` each appear under two
versions). `typescript@6.0.3` (`Apache-2.0`, marked `"devOptional": true` in
`package-lock.json`) is build-time compilation tooling and is excluded from the
runtime production table. Two runtime entries need a word of explanation:

- `json-schema` offers a choice of AFL-2.1 or BSD-3-Clause. The BSD-3-Clause
  option applies here.
- `valid-url` declares no `license` field in its `package.json`, so
  `license-checker` reported `MIT*` and read the text from the package's LICENSE
  file. That text is the MIT license, and the package is listed as MIT below.

### Container Images, Workflow Tooling, and Bundled / Runtime Assets

| Component | Version | License | Container / Location | Purpose |
|---|---|---|---|---|
| Distroless Base (`gcr.io/distroless/base-debian12:nonroot`) | Debian 12 (bookworm) | Apache-2.0 / Debian DFSG | `Dockerfile` (`cuttlefish-orchestrator-api`) | Minimal non-root runtime base image |
| Scalar API Reference (`@scalar/api-reference`) | 1.44.20 | MIT | `internal/router/scalar.go` (`cuttlefish-orchestrator-api` `/docs` CDN w/ SRI) | Interactive OpenAPI reference UI (`sha384-tMz7GAo6dMy55x9tLFtH+sHtogji6Scmb+feBR31TAHmvSPRUTboK9H3M5NFaP4R`) |
| Nginx (`nginx`) | 1.31.2-alpine3.23 | 2-clause BSD (Alpine packages vary) | `web/cuttlefish-console/Dockerfile` (`cuttlefish-console`) | Serves compiled Angular SPA and reverse proxies API requests |
| Debian 12 Slim (`debian:12-slim`) + APT tools (`android-tools-adb`, `openjdk-17-jre-headless`, `openssh-client`, `python3`, `jq`, `unzip`, `curl`, `ca-certificates`, `gnupg`) | Debian 12 (bookworm) | Debian DFSG-compliant OSS licenses (`GPL-2.0-with-classpath-exception` for OpenJDK 17) | `Dockerfile.workflow` (`cuttlefish-workflow`) | Argo Workflow runner base image and CLI/CTS dependencies |
| Google Cloud CLI (`google-cloud-cli`) | APT (`cloud-sdk-bookworm`) | Apache-2.0 | `Dockerfile.workflow` (`cuttlefish-workflow`) | GCE/GCS/OS Login automation in CCO workflows |
| HashiCorp Packer (`packer`) | 1.15.0 | BSL-1.1 | `Dockerfile.workflow` (`cuttlefish-workflow`) | Builds `cco-host` / `cco-host-arm64` Debian 13 GCE host images |
| Packer Google Compute Plugin (`hashicorp/googlecompute`) | v1.2.4 | MPL-2.0 | `Dockerfile.workflow` (`cuttlefish-workflow`) | Packer GCE builder plugin (`use_iap = true`) |
| Self-hosted Google Fonts (`google-sans*.woff2`, `google-sans-code*.woff2`, `material-symbols-outlined.woff2`) | Bundled static assets | SIL OFL-1.1 / Apache-2.0 | `web/cuttlefish-console/src/assets/fonts/` | Local typography and Material Symbols icons bundled without CDN calls |

## Dependencies

### Go modules

#### Apache-2.0

| Module | Version | Repository |
|---|---|---|
| `cel.dev/expr` | v0.25.2 | https://github.com/cel-expr/cel-spec |
| `cloud.google.com/go` | v0.123.0 | https://github.com/googleapis/google-cloud-go |
| `cloud.google.com/go/auth` | v0.22.0 | https://github.com/googleapis/google-cloud-go |
| `cloud.google.com/go/auth/oauth2adapt` | v0.2.8 | https://github.com/googleapis/google-cloud-go |
| `cloud.google.com/go/compute` | v1.64.0 | https://github.com/googleapis/google-cloud-go |
| `cloud.google.com/go/compute/metadata` | v0.9.0 | https://github.com/googleapis/google-cloud-go |
| `cloud.google.com/go/iam` | v1.11.0 | https://github.com/googleapis/google-cloud-go |
| `cloud.google.com/go/monitoring` | v1.29.0 | https://github.com/googleapis/google-cloud-go |
| `cloud.google.com/go/spanner` | v1.92.0 | https://github.com/googleapis/google-cloud-go |
| `cloud.google.com/go/storage` | v1.63.0 | https://github.com/googleapis/google-cloud-go |
| `github.com/GoogleCloudPlatform/grpc-gcp-go/grpcgcp` | v1.6.0 | https://github.com/GoogleCloudPlatform/grpc-gcp-go |
| `github.com/GoogleCloudPlatform/opentelemetry-operations-go/detectors/gcp` | v1.35.0 | https://github.com/GoogleCloudPlatform/opentelemetry-operations-go |
| `github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/metric` | v0.58.0 | https://github.com/GoogleCloudPlatform/opentelemetry-operations-go |
| `github.com/GoogleCloudPlatform/opentelemetry-operations-go/internal/resourcemapping` | v0.58.0 | https://github.com/GoogleCloudPlatform/opentelemetry-operations-go |
| `github.com/cncf/xds/go` | v0.0.0-20260202195803-dba9d589def2 | https://github.com/cncf/xds |
| `github.com/envoyproxy/go-control-plane/envoy` | v1.37.0 | https://github.com/envoyproxy/go-control-plane |
| `github.com/envoyproxy/protoc-gen-validate` | v1.3.3 | https://github.com/envoyproxy/protoc-gen-validate |
| `github.com/go-jose/go-jose/v4` | v4.1.4 | https://github.com/go-jose/go-jose |
| `github.com/go-logr/logr` | v1.4.4 | https://github.com/go-logr/logr |
| `github.com/go-logr/stdr` | v1.2.2 | https://github.com/go-logr/stdr |
| `github.com/google/s2a-go` | v0.1.9 | https://github.com/google/s2a-go |
| `github.com/googleapis/enterprise-certificate-proxy` | v0.3.19 | https://github.com/googleapis/enterprise-certificate-proxy |
| `github.com/oapi-codegen/runtime` | v1.6.0 | https://github.com/oapi-codegen/runtime |
| `github.com/spiffe/go-spiffe/v2` | v2.8.1 | https://github.com/spiffe/go-spiffe |
| `go.opencensus.io` | v0.24.0 | https://github.com/census-instrumentation/opencensus-go |
| `go.opentelemetry.io/auto/sdk` | v1.2.1 | https://github.com/open-telemetry/opentelemetry-go-instrumentation |
| `go.opentelemetry.io/contrib/detectors/gcp` | v1.45.0 | https://github.com/open-telemetry/opentelemetry-go-contrib |
| `go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc` | v0.70.0 | https://github.com/open-telemetry/opentelemetry-go-contrib |
| `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` | v0.70.0 | https://github.com/open-telemetry/opentelemetry-go-contrib |
| `go.opentelemetry.io/otel` | v1.45.0 | https://github.com/open-telemetry/opentelemetry-go |
| `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc` | v1.45.0 | https://github.com/open-telemetry/opentelemetry-go |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace` | v1.45.0 | https://github.com/open-telemetry/opentelemetry-go |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc` | v1.45.0 | https://github.com/open-telemetry/opentelemetry-go |
| `go.opentelemetry.io/otel/metric` | v1.45.0 | https://github.com/open-telemetry/opentelemetry-go |
| `go.opentelemetry.io/otel/sdk` | v1.45.0 | https://github.com/open-telemetry/opentelemetry-go |
| `go.opentelemetry.io/otel/sdk/metric` | v1.45.0 | https://github.com/open-telemetry/opentelemetry-go |
| `go.opentelemetry.io/otel/trace` | v1.45.0 | https://github.com/open-telemetry/opentelemetry-go |
| `go.opentelemetry.io/proto/otlp` | v1.11.0 | https://github.com/open-telemetry/opentelemetry-proto-go |
| `go.yaml.in/yaml/v3` | v3.0.5 | https://github.com/yaml/go-yaml |
| `google.golang.org/genproto` | v0.0.0-20260706201446-f0a921348800 | https://github.com/googleapis/go-genproto |
| `google.golang.org/genproto/googleapis/api` | v0.0.0-20260803160001-6ac0973c030d | https://github.com/googleapis/go-genproto |
| `google.golang.org/genproto/googleapis/rpc` | v0.0.0-20260803160001-6ac0973c030d | https://github.com/googleapis/go-genproto |
| `google.golang.org/grpc` | v1.83.2 | https://github.com/grpc/grpc-go |

#### BSD-3-Clause

| Module | Version | Repository |
|---|---|---|
| `github.com/go-jose/go-jose/v4/json` | v4.1.4 | https://github.com/go-jose/go-jose |
| `github.com/google/uuid` | v1.6.0 | https://github.com/google/uuid |
| `github.com/googleapis/gax-go/v2` | v2.23.0 | https://github.com/googleapis/gax-go |
| `github.com/grpc-ecosystem/grpc-gateway/v2` | v2.29.0 | https://github.com/grpc-ecosystem/grpc-gateway |
| `golang.org/x/crypto` | v0.57.0 | https://cs.opensource.google/go/x/crypto |
| `golang.org/x/net` | v0.60.0 | https://cs.opensource.google/go/x/net |
| `golang.org/x/oauth2` | v0.36.0 | https://cs.opensource.google/go/x/oauth2 |
| `golang.org/x/sync` | v0.23.0 | https://cs.opensource.google/go/x/sync |
| `golang.org/x/sys` | v0.48.0 | https://cs.opensource.google/go/x/sys |
| `golang.org/x/text` | v0.42.0 | https://cs.opensource.google/go/x/text |
| `golang.org/x/time` | v0.15.0 | https://cs.opensource.google/go/x/time |
| `google.golang.org/api` | v0.291.0 | https://github.com/googleapis/google-api-go-client |
| `google.golang.org/protobuf` | v1.36.11 | https://github.com/protocolbuffers/protobuf-go |

#### MIT

| Module | Version | Repository |
|---|---|---|
| `github.com/alecthomas/kong` | v1.16.0 | https://github.com/alecthomas/kong |
| `github.com/cenkalti/backoff/v5` | v5.0.3 | https://github.com/cenkalti/backoff |
| `github.com/cespare/xxhash/v2` | v2.3.0 | https://github.com/cespare/xxhash |
| `github.com/danielgtaylor/huma/v2` | v2.38.0 | https://github.com/danielgtaylor/huma |
| `github.com/felixge/httpsnoop` | v1.1.0 | https://github.com/felixge/httpsnoop |
| `github.com/go-chi/chi/v5` | v5.3.1 | https://github.com/go-chi/chi |
| `go.yaml.in/yaml/v3` | v3.0.5 | https://github.com/yaml/go-yaml |

#### ISC

| Module | Version | Repository |
|---|---|---|
| `github.com/coder/websocket` | v1.8.15 | https://github.com/coder/websocket |

### npm packages

#### MIT

| Package | Version | Repository |
|---|---|---|
| `@angular/cdk` | 22.2.1 | https://github.com/angular/components |
| `@angular/common` | 22.2.1 | https://github.com/angular/angular |
| `@angular/compiler` | 22.2.1 | https://github.com/angular/angular |
| `@angular/compiler-cli` | 22.2.1 | https://github.com/angular/angular |
| `@angular/core` | 22.2.1 | https://github.com/angular/angular |
| `@angular/forms` | 22.2.1 | https://github.com/angular/angular |
| `@angular/localize` | 22.2.1 | https://github.com/angular/angular |
| `@angular/material` | 22.2.1 | https://github.com/angular/components |
| `@angular/platform-browser` | 22.2.1 | https://github.com/angular/angular |
| `@angular/router` | 22.2.1 | https://github.com/angular/angular |
| `@babel/code-frame` | 8.0.6 | https://github.com/babel/babel |
| `@babel/compat-data` | 8.0.5 | https://github.com/babel/babel |
| `@babel/core` | 8.0.1 | https://github.com/babel/babel |
| `@babel/generator` | 8.0.6 | https://github.com/babel/babel |
| `@babel/helper-compilation-targets` | 8.0.6 | https://github.com/babel/babel |
| `@babel/helper-globals` | 8.0.6 | https://github.com/babel/babel |
| `@babel/helper-string-parser` | 8.0.6 | https://github.com/babel/babel |
| `@babel/helper-validator-identifier` | 8.0.6 | https://github.com/babel/babel |
| `@babel/helper-validator-option` | 8.0.0 | https://github.com/babel/babel |
| `@babel/helpers` | 8.0.5 | https://github.com/babel/babel |
| `@babel/parser` | 8.0.6 | https://github.com/babel/babel |
| `@babel/template` | 8.0.0 | https://github.com/babel/babel |
| `@babel/traverse` | 8.0.6 | https://github.com/babel/babel |
| `@babel/types` | 8.0.6 | https://github.com/babel/babel |
| `@codemirror/autocomplete` | 6.20.3 | git+https://code.haverbeke.berlin/codemirror/autocomplete |
| `@codemirror/commands` | 6.10.4 | git+https://code.haverbeke.berlin/codemirror/commands |
| `@codemirror/lang-json` | 6.0.2 | https://github.com/codemirror/lang-json |
| `@codemirror/lang-yaml` | 6.1.3 | https://github.com/codemirror/lang-yaml |
| `@codemirror/language` | 6.12.4 | git+https://code.haverbeke.berlin/codemirror/language |
| `@codemirror/lint` | 6.9.7 | git+https://code.haverbeke.berlin/codemirror/lint |
| `@codemirror/state` | 6.7.1 | git+https://code.haverbeke.berlin/codemirror/state |
| `@codemirror/view` | 6.43.6 | git+https://code.haverbeke.berlin/codemirror/view |
| `@jridgewell/gen-mapping` | 0.4.0-beta.0 | https://github.com/jridgewell/sourcemaps |
| `@jridgewell/resolve-uri` | 3.1.2 | https://github.com/jridgewell/resolve-uri |
| `@jridgewell/sourcemap-codec` | 1.6.0 | https://github.com/jridgewell/sourcemaps |
| `@jridgewell/trace-mapping` | 0.3.31 | https://github.com/jridgewell/sourcemaps |
| `@lezer/common` | 1.5.2 | https://github.com/lezer-parser/common |
| `@lezer/highlight` | 1.2.3 | https://github.com/lezer-parser/highlight |
| `@lezer/json` | 1.0.3 | https://github.com/lezer-parser/json |
| `@lezer/lr` | 1.4.10 | git+https://code.haverbeke.berlin/lezer/lr |
| `@lezer/yaml` | 1.0.4 | https://github.com/lezer-parser/yaml |
| `@marijn/find-cluster-break` | 1.0.3 | git+https://code.haverbeke.berlin/marijn/find-cluster-break |
| `@ngrx/operators` | 21.1.1 | https://github.com/ngrx/platform |
| `@ngrx/signals` | 21.1.1 | https://github.com/ngrx/platform |
| `@sagold/json-pointer` | 5.1.2 | https://github.com/sagold/json-pointer |
| `@sagold/json-query` | 6.2.0 | https://github.com/sagold/json-query |
| `@shikijs/core` | 1.29.2 | https://github.com/shikijs/shiki |
| `@shikijs/engine-javascript` | 1.29.2 | https://github.com/shikijs/shiki |
| `@shikijs/engine-oniguruma` | 1.29.2 | https://github.com/shikijs/shiki |
| `@shikijs/langs` | 1.29.2 | https://github.com/shikijs/shiki |
| `@shikijs/markdown-it` | 1.29.2 | https://github.com/shikijs/shiki |
| `@shikijs/themes` | 1.29.2 | https://github.com/shikijs/shiki |
| `@shikijs/types` | 1.29.2 | https://github.com/shikijs/shiki |
| `@shikijs/vscode-textmate` | 10.0.2 | https://github.com/shikijs/vscode-textmate |
| `@standard-schema/spec` | 1.1.0 | https://github.com/standard-schema/standard-schema |
| `@types/gensync` | 1.0.5 | https://github.com/DefinitelyTyped/DefinitelyTyped |
| `@types/hast` | 3.0.5 | https://github.com/DefinitelyTyped/DefinitelyTyped |
| `@types/jsesc` | 2.5.1 | https://github.com/DefinitelyTyped/DefinitelyTyped |
| `@types/mdast` | 4.0.4 | https://github.com/DefinitelyTyped/DefinitelyTyped |
| `@types/node` | 24.13.3 | https://github.com/DefinitelyTyped/DefinitelyTyped |
| `@types/unist` | 3.0.3 | https://github.com/DefinitelyTyped/DefinitelyTyped |
| `@xterm/addon-fit` | 0.11.0 | https://github.com/xtermjs/xterm.js/tree/master/addons/addon-fit |
| `@xterm/addon-search` | 0.16.0 | https://github.com/xtermjs/xterm.js/tree/master/addons/addon-search |
| `@xterm/addon-webgl` | 0.19.0 | https://github.com/xtermjs/xterm.js/tree/master/addons/addon-webgl |
| `@xterm/xterm` | 6.0.0 | https://github.com/xtermjs/xterm.js |
| `@yume-chan/adb` | 2.6.0 | https://github.com/yume-chan/ya-webadb |
| `@yume-chan/android-bin` | 2.1.0 | https://github.com/yume-chan/ya-webadb |
| `@yume-chan/async` | 4.1.3 | https://github.com/yume-chan/async |
| `@yume-chan/event` | 2.0.0 | https://github.com/yume-chan/ya-webadb |
| `@yume-chan/no-data-view` | 2.0.0 | https://github.com/yume-chan/ya-webadb |
| `@yume-chan/stream-extra` | 2.6.1 | https://github.com/yume-chan/ya-webadb |
| `@yume-chan/struct` | 2.3.2 | https://github.com/yume-chan/ya-webadb |
| `ansi-regex` | 6.2.2 | https://github.com/chalk/ansi-regex |
| `ansi-styles` | 6.2.3 | https://github.com/chalk/ansi-styles |
| `browserslist` | 4.28.7 | https://github.com/browserslist/browserslist |
| `ccount` | 2.0.1 | https://github.com/wooorm/ccount |
| `character-entities-html4` | 2.1.0 | https://github.com/wooorm/character-entities-html4 |
| `character-entities-legacy` | 3.0.0 | https://github.com/wooorm/character-entities-legacy |
| `chokidar` | 5.0.0 | https://github.com/paulmillr/chokidar |
| `codemirror-json-schema` | 0.8.1 | https://github.com/acao/codemirror-json-schema |
| `codemirror-json5` | 1.0.3 | https://github.com/dimfeld/codemirror-json5 |
| `comma-separated-tokens` | 2.0.3 | https://github.com/wooorm/comma-separated-tokens |
| `commander` | 2.20.3 | https://github.com/tj/commander.js |
| `convert-source-map` | 1.9.0 | https://github.com/thlorenz/convert-source-map |
| `convert-source-map` | 2.0.0 | https://github.com/thlorenz/convert-source-map |
| `crelt` | 1.0.7 | git+https://code.haverbeke.berlin/marijn/crelt |
| `deepmerge` | 4.3.1 | https://github.com/TehShrike/deepmerge |
| `dequal` | 2.0.3 | https://github.com/lukeed/dequal |
| `devlop` | 1.1.0 | https://github.com/wooorm/devlop |
| `discontinuous-range` | 1.0.0 | https://github.com/dtudury/discontinuous-range |
| `ebnf` | 1.9.1 | https://github.com/menduz/node-ebnf |
| `emoji-regex` | 10.6.0 | https://github.com/mathiasbynens/emoji-regex |
| `emoji-regex-xs` | 1.0.0 | https://github.com/slevithan/emoji-regex-xs |
| `empathic` | 2.1.0 | https://www.npmjs.com/package/empathic |
| `escalade` | 3.2.0 | https://github.com/lukeed/escalade |
| `fast-copy` | 3.1.0 | https://github.com/planttheidea/fast-copy |
| `fast-deep-equal` | 3.1.3 | https://github.com/epoberezkin/fast-deep-equal |
| `fdir` | 6.5.0 | https://github.com/thecodrr/fdir |
| `flru` | 1.0.2 | https://www.npmjs.com/package/flru |
| `gensync` | 1.0.0-beta.2 | https://github.com/loganfsmyth/gensync |
| `get-east-asian-width` | 1.6.0 | https://github.com/sindresorhus/get-east-asian-width |
| `hast-util-to-html` | 9.0.5 | https://github.com/syntax-tree/hast-util-to-html |
| `hast-util-whitespace` | 3.0.0 | https://github.com/syntax-tree/hast-util-whitespace |
| `html-void-elements` | 3.0.0 | https://github.com/wooorm/html-void-elements |
| `import-meta-resolve` | 4.2.0 | https://www.npmjs.com/package/import-meta-resolve |
| `js-tokens` | 10.0.0 | https://github.com/lydell/js-tokens |
| `jsesc` | 3.1.0 | https://github.com/mathiasbynens/jsesc |
| `json-schema-library` | 9.3.5 | https://github.com/sagold/json-schema-library |
| `json5` | 2.2.3 | https://github.com/json5/json5 |
| `lezer-json5` | 2.0.2 | https://github.com/dimfeld/lezer-json5 |
| `linkify-it` | 5.0.2 | https://github.com/markdown-it/linkify-it |
| `loglevel` | 1.9.2 | https://github.com/pimterry/loglevel |
| `markdown-it` | 14.3.2 | https://github.com/markdown-it/markdown-it |
| `mdast-util-to-hast` | 13.2.1 | https://github.com/syntax-tree/mdast-util-to-hast |
| `mdurl` | 2.1.0 | https://github.com/markdown-it/mdurl |
| `micromark-util-character` | 2.1.1 | https://github.com/micromark/micromark/tree/main/packages/micromark-util-character |
| `micromark-util-encode` | 2.0.1 | https://github.com/micromark/micromark/tree/main/packages/micromark-util-encode |
| `micromark-util-sanitize-uri` | 2.0.1 | https://github.com/micromark/micromark/tree/main/packages/micromark-util-sanitize-uri |
| `micromark-util-symbol` | 2.0.1 | https://github.com/micromark/micromark/tree/main/packages/micromark-util-symbol |
| `micromark-util-types` | 2.0.2 | https://github.com/micromark/micromark/tree/main/packages/micromark-util-types |
| `nearley` | 2.20.1 | https://github.com/hardmath123/nearley |
| `node-releases` | 2.0.51 | https://github.com/chicoxyzzy/node-releases |
| `obug` | 2.1.4 | https://www.npmjs.com/package/obug |
| `oniguruma-to-es` | 2.3.0 | https://github.com/slevithan/oniguruma-to-es |
| `parse5` | 8.0.1 | https://github.com/inikulin/parse5 |
| `picomatch` | 4.0.7 | https://github.com/micromatch/picomatch |
| `property-information` | 7.2.0 | https://github.com/wooorm/property-information |
| `punycode.js` | 2.3.1 | https://github.com/mathiasbynens/punycode.js |
| `randexp` | 0.4.6 | https://github.com/fent/randexp.js |
| `readdirp` | 5.0.0 | https://github.com/paulmillr/readdirp |
| `regex` | 5.1.1 | https://github.com/slevithan/regex |
| `regex-recursion` | 5.1.1 | https://github.com/slevithan/regex-recursion |
| `regex-utilities` | 2.3.0 | https://github.com/slevithan/regex-utilities |
| `ret` | 0.1.15 | https://github.com/fent/ret.js |
| `shiki` | 1.29.2 | https://github.com/shikijs/shiki |
| `smtp-address-parser` | 1.0.10 | https://github.com/gene-hightower/smtp-address-parser |
| `space-separated-tokens` | 2.0.2 | https://github.com/wooorm/space-separated-tokens |
| `string-width` | 7.2.0 | https://github.com/sindresorhus/string-width |
| `string-width` | 8.2.2 | https://github.com/sindresorhus/string-width |
| `stringify-entities` | 4.0.4 | https://github.com/wooorm/stringify-entities |
| `strip-ansi` | 7.2.0 | https://github.com/chalk/strip-ansi |
| `style-mod` | 4.1.3 | https://github.com/marijnh/style-mod |
| `tinyglobby` | 0.2.17 | https://github.com/SuperchupuDev/tinyglobby |
| `trim-lines` | 3.0.1 | https://github.com/wooorm/trim-lines |
| `uc.micro` | 2.1.0 | https://github.com/markdown-it/uc.micro |
| `undici-types` | 7.18.2 | https://github.com/nodejs/undici |
| `unist-util-is` | 6.0.1 | https://github.com/syntax-tree/unist-util-is |
| `unist-util-position` | 5.0.0 | https://github.com/syntax-tree/unist-util-position |
| `unist-util-stringify-position` | 4.0.0 | https://github.com/syntax-tree/unist-util-stringify-position |
| `unist-util-visit` | 5.1.0 | https://github.com/syntax-tree/unist-util-visit |
| `unist-util-visit-parents` | 6.0.2 | https://github.com/syntax-tree/unist-util-visit-parents |
| `update-browserslist-db` | 1.2.3 | https://github.com/browserslist/update-db |
| `valid-url` | 1.0.9 | https://github.com/ogt/valid-url |
| `verkit` | 0.3.2 | https://www.npmjs.com/package/verkit |
| `vfile` | 6.0.3 | https://github.com/vfile/vfile |
| `vfile-message` | 4.0.3 | https://github.com/vfile/vfile-message |
| `w3c-keyname` | 2.2.8 | https://github.com/marijnh/w3c-keyname |
| `wrap-ansi` | 9.0.2 | https://github.com/chalk/wrap-ansi |
| `yargs` | 18.1.0 | https://github.com/yargs/yargs |
| `zod` | 4.4.3 | https://github.com/colinhacks/zod |
| `zwitch` | 2.0.4 | https://github.com/wooorm/zwitch |

#### ISC

| Package | Version | Repository |
|---|---|---|
| `@ungap/structured-clone` | 1.3.3 | https://github.com/ungap/structured-clone |
| `cliui` | 9.0.1 | https://github.com/yargs/cliui |
| `electron-to-chromium` | 1.5.396 | https://github.com/Kilian/electron-to-chromium |
| `get-caller-file` | 2.0.5 | https://github.com/stefanpenner/get-caller-file |
| `picocolors` | 1.1.1 | https://github.com/alexeyraspopov/picocolors |
| `semver` | 7.8.5 | https://github.com/npm/node-semver |
| `y18n` | 5.0.8 | https://github.com/yargs/y18n |
| `yaml` | 2.9.0 | https://github.com/eemeli/yaml |
| `yargs-parser` | 22.0.0 | https://github.com/yargs/yargs-parser |

#### Apache-2.0

| Package | Version | Repository |
|---|---|---|
| `baseline-browser-mapping` | 2.11.4 | https://github.com/web-platform-dx/baseline-browser-mapping |
| `reflect-metadata` | 0.2.2 | https://github.com/rbuckton/reflect-metadata |
| `rxjs` | 7.8.2 | https://github.com/reactivex/rxjs |

#### BSD-2-Clause

| Package | Version | Repository |
|---|---|---|
| `best-effort-json-parser` | 1.5.1 | https://github.com/beenotung/best-effort-json-parser |
| `entities` | 4.5.0 | https://github.com/fb55/entities |
| `entities` | 8.0.0 | https://github.com/fb55/entities |

#### AFL-2.1 or BSD-3-Clause

| Package | Version | Repository |
|---|---|---|
| `json-schema` | 0.4.0 | https://github.com/kriszyp/json-schema |

#### 0BSD

| Package | Version | Repository |
|---|---|---|
| `tslib` | 2.8.1 | https://github.com/Microsoft/tslib |

#### BSD-3-Clause

| Package | Version | Repository |
|---|---|---|
| `moo` | 0.5.3 | https://github.com/tjvr/moo |

#### CC-BY-4.0

| Package | Version | Repository |
|---|---|---|
| `caniuse-lite` | 1.0.30001806 | https://github.com/browserslist/caniuse-lite |

#### CC0-1.0

| Package | Version | Repository |
|---|---|---|
| `railroad-diagrams` | 1.0.0 | https://github.com/tabatkins/railroad-diagrams |

#### Python-2.0

| Package | Version | Repository |
|---|---|---|
| `argparse` | 2.0.1 | https://github.com/nodeca/argparse |

## Full License Texts

The four licenses below cover the Go list (`./cmd/...`), `@scalar/api-reference`
(`MIT`), and 174 of the 182 production npm `name@version` tuples (plus three
`BSD-2-Clause` tuples, and one each under `0BSD`, `CC-BY-4.0`, `CC0-1.0`, and
`Python-2.0`; plus `json-schema` dual-licensed under `AFL-2.1` or `BSD-3-Clause`).
In `cuttlefish-workflow`, HashiCorp `packer` (`1.15.0`) is source-available under
`BSL-1.1` (`https://github.com/hashicorp/packer/blob/main/LICENSE`, converting to
`MPL-2.0` after its change date), whereas the `hashicorp/googlecompute` plugin
(`v1.2.4`) is licensed under `MPL-2.0` (`https://www.mozilla.org/en-US/MPL/2.0/`).
Debian runtime packages (including OpenJDK 17 under `GPL-2.0-with-classpath-exception`)
follow Debian DFSG-compliant upstream licenses. Canonical SPDX license texts are
published at `https://spdx.org/licenses/<identifier>.html`, and every upstream
package ships its own license file.

### MIT License

```
MIT License

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### Apache License 2.0

The full text is at https://www.apache.org/licenses/LICENSE-2.0. It is also
the license of this project, so the complete text is in
[LICENSE](LICENSE) at the repository root.

### BSD 3-Clause License

```
BSD 3-Clause License

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice,
   this list of conditions and the following disclaimer.

2. Redistributions in binary form must reproduce the above copyright notice,
   this list of conditions and the following disclaimer in the documentation
   and/or other materials provided with the distribution.

3. Neither the name of the copyright holder nor the names of its contributors
   may be used to endorse or promote products derived from this software
   without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE
LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
POSSIBILITY OF SUCH DAMAGE.
```

### ISC License

```
ISC License

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES WITH
REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF MERCHANTABILITY
AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY SPECIAL, DIRECT,
INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES WHATSOEVER RESULTING FROM
LOSS OF USE, DATA OR PROFITS, WHETHER IN AN ACTION OF CONTRACT, NEGLIGENCE OR
OTHER TORTIOUS ACTION, ARISING OUT OF OR IN CONNECTION WITH THE USE OR
PERFORMANCE OF THIS SOFTWARE.
```

## Generating Updated Notices

### Backend (Go)

Run from the `cuttlefish-orchestrator` image directory. The first command
downloads `go-licenses` on first use. Normalize subpackage paths to canonical
`go.mod` module roots (retaining separate rows where a module has dual-licensed
files or subpackages, such as `github.com/go-jose/go-jose/v4/json` and
`go.yaml.in/yaml/v3`).

```bash
go run github.com/google/go-licenses@latest report ./cmd/... 2>/dev/null
```

Its report has no version column, so the versions in the tables above come
from the build list of the same binaries:

```bash
go list -deps -f '{{if .Module}}{{.Module.Path}} {{.Module.Version}}{{end}}' ./cmd/... | sort -u
```

### Frontend (npm)

```bash
cd web/cuttlefish-console
npx --yes license-checker --production --json
```

Cross-check against `package-lock.json` to exclude `"dev": true` and
`"devOptional": true` packages (such as `typescript@6.0.3`, which is used only at
TypeScript/Angular build time).

## Questions

If you have questions about the licensing of any component, please open an
issue on the repository issue tracker.
