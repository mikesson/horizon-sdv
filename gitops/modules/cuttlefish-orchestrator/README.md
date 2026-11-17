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

# Cuttlefish Orchestrator Module

Deploys Cloud Cuttlefish Orchestrator (CCO) as a toggleable Horizon SDV module
behind Horizon Keycloak OIDC authentication and GKE Gateway ingress.

## Components

- **Parent Overview (`templates/module-overview-http.yaml`)**: Hardened Nginx
  deployment serving `portal/overview.html` inside the Module Manager namespace.
- **Orchestrator Child App (`orchestrator/`)**:
  - Four Workload Identity Kubernetes ServiceAccounts (`cuttlefish-orchestrator-api`,
    `cco-spanner-seed`, `cco-cleanup`, and unannotated Direct WI `cuttlefish-orchestrator-sa`).
  - Namespace-scoped External Secrets Operator `SecretStore` and `ExternalSecret`
    (`cuttlefish-console-oauth2-proxy-creds`) reading GSM secrets `s20` and `s21`.
  - Cluster-mode Config Connector `SpannerInstance` (Wave 2, 100 PU, regional)
    and `SpannerDatabase` (Wave 3, schema v87, 57 DDL statements).
  - Wave-4 `Sync` hook Job (`cco-spanner-seed`) applying idempotent `INSERT OR IGNORE`
    seed rows and seeding `zones.enabled` from `config.zone`.
  - Wave-5 `cuttlefish-orchestrator-api` (`:8080`, `CCO_AUTH_MODE=proxy`,
    `CCO_DEVICE_INGRESS=private`) and `cuttlefish-console` (`:8080`, bridging
    `X-Forwarded-*` headers to `X-Auth-Request-*`).
  - GKE Gateway `HTTPRoute` (`/cuttlefish-orchestrator` 301 redirect and
    `/cuttlefish-orchestrator/` prefix route), `HealthCheckPolicy` (`/ping`),
    and `GCPBackendPolicy` (`timeoutSec: 86400` for WebSocket streams).
  - Conditional `NetworkPolicy` set (`default-deny-all`, DNS, `oauth2-proxy`,
    console, API, Spanner seed, and cleanup Job).
  - Suspended `CronJob` (`cco-cleanup`) triggered by Module Manager on disable
    after scaling `cuttlefish-orchestrator-api` to zero and waiting for API
    pods to exit, removing managed GCE host VMs before child/parent teardown.
- **OAuth2 Proxy Child App (`oauth2-proxy` Helm chart `8.3.0`)**:
  - Protects `/cuttlefish-orchestrator/` via Horizon Keycloak OIDC and restricts
    access to the `administrators` and `developers` groups.

## Security Notes

- `config.enableNetworkPolicies: true` is required in production deployments for
  `CCO_AUTH_MODE=proxy` header isolation. Because `CCO_TRUSTED_PROXIES` permits
  Pod and VPC CIDRs (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`),
  `NetworkPolicy/allow-cuttlefish-orchestrator-api` is the enforcement boundary
  that restricts API `:8080` ingress strictly to `cuttlefish-console`,
  `<prefix>workflows`, and configured `probeSourceCIDRs`.
