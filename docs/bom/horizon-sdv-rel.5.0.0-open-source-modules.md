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

# Horizon SDV Rel.5.0.0 Open Source Modules

This Bill of Materials (BOM) summarizes the open-source, source-available, and closed-source components integrated into **Horizon SDV Release 5.0.0** (pinned in `versions.yaml` as `components.horizoncore: "5.0.0"`), including the **Cloud Cuttlefish Orchestrator (`cuttlefish-orchestrator`)** module and its workflow runner image (`cuttlefish-workflow`).

## Summary of Changes Since Rel.3.0.0

Relative to `docs/bom/horizon-sdv-rel.3.0.0-open-source-modules.md`, Horizon SDV 5.0.0 introduces:

- **Cloud Cuttlefish Orchestrator (`cuttlefish-orchestrator`)**:
  - Added parent module chart `0.1.0` (`appVersion: "1.0.0"`), `orchestrator` subchart, and `argo-workflows` subchart.
  - Added three first-party container images: `cuttlefish-orchestrator-api:1.0.3` (Go 1.26.9 on `gcr.io/distroless/base-debian12:nonroot`, serving `@scalar/api-reference@1.44.20` [MIT] via CDN with SRI on `/docs`), `cuttlefish-console:1.0.3` (Angular 22 SPA on `nginx:1.31.2-alpine3.23`), and `cuttlefish-workflow:1.0.3` (`cfctl` + `google-cloud-cli` + HashiCorp `packer 1.15.0` [BSL-1.1] + `hashicorp/googlecompute v1.2.4` [MPL-2.0] + `android-tools-adb` + `openjdk-17-jre-headless` on `debian:12-slim`).
  - Added per-module `oauth2-proxy` Helm release (`8.3.0`) for Keycloak SSO gateway authentication.
  - Detailed third-party dependency inventory (62 unique Go modules / 64 Go notice entries across `./cmd/...`, 182 unique production `name@version` npm packages across 179 unique package names and 10 SPDX licenses excluding `devOptional` `typescript@6.0.3`, runtime `@scalar/api-reference@1.44.20` [MIT] CDN dependency, and workflow/font assets) is maintained in `terraform/modules/sdv-container-images/images/cuttlefish-orchestrator/THIRD_PARTY_NOTICES.md`.
- **Platform Core and Module Additions / Upgrades**:
  - `horizon-dev-portal:5.0.0` (chart `1.1.0`), `module-manager-app:0.3.7` (chart `0.3.3`), `horizon-api-app:1.1.1` (chart `0.2.0`), `storage-gcs-module-app:1.0.2` (chart `1.0.0`), `cloud-workstations-module-app:0.2.1` (chart `0.1.0`), `workflow-namespace-drain-app:1.0.1` (chart `1.0.0`), `gerrit-mcp-server-app:1.0.1` (chart `1.0.0`), and `kcc-webhook-cert-monitor:1.0.0`.
  - Upgraded platform chart pins: Terraform `1.14.2`, `hashicorp/google` and `google-beta` `7.12.0`, `argo-cd` `9.1.4`, `argo-events` `2.3.2`, `argo-workflows` `0.47.4`, `external-secrets` `0.10.4`, `keycloakx` `7.1.4`, `postgresql` `17.0.1`, `zookeeper` `13.8.7`, `kubescape-operator` `1.29.11`, `jenkins` `5.9.63`, `grafana` `9.3.0`, `headlamp` `0.36.0`, `oauth2-proxy` `8.3.0`, `external-dns` `1.19.0`, `configconnector-operator` `1.156.0`, and `mcp-gateway-registry` `1.0.0`.
  - Updated ABFS module suite (`abfs-service-module` `0.2.0`, `abfs-data-module` `0.1.0`, `abfs-client-module-cos` `0.1.0`, `abfs-client-module-ubuntu` `0.1.0`) for closed-source ABFS `0.1.14`.

---

## Applications (Open Source)

| Component | Chart / Version | Source / Repository | License | Notes |
| --- | --- | --- | --- | --- |
| Argo CD | `argo-cd` `9.1.4` | `https://argoproj.github.io/argo-helm` | Apache-2.0 | Installed by `terraform/modules/sdv-gke-apps/main.tf` |
| Argo Events | `argo-events` `2.3.2` | `https://argoproj.github.io/argo-helm` | Apache-2.0 | EventBus and webhook/trigger automation |
| Argo Workflows | `argo-workflows` `0.47.4` | `https://argoproj.github.io/argo-helm` | Apache-2.0 | Runs Android, OpenBSW, and Cuttlefish Orchestrator workflows |
| Cloud Cuttlefish Orchestrator | Chart `0.1.0` / App `1.0.0` | Horizon SDV (`gitops/modules/cuttlefish-orchestrator`) | Apache-2.0 | Go API, Angular console, `cuttlefish-workflow`; see `THIRD_PARTY_NOTICES.md` |
| Config Connector Operator | Chart `1.0.0` / Operator `1.156.0` | `gitops/apps/configconnector-operator` | Apache-2.0 | Vendored KCC operator + CRDs |
| Dynamic PVC Provisioner | `dynamic-pvc-provisioner` `0.1.1` | `https://plumber-cd.github.io/helm/` | MIT / Apache-2.0 | Storage automation helper |
| ExternalDNS | `external-dns` `1.19.0` | `https://kubernetes-sigs.github.io/external-dns/` | Apache-2.0 | Cloud DNS record synchronization |
| External Secrets Operator | `external-secrets` `0.10.4` | `https://charts.external-secrets.io` | Apache-2.0 | Syncs GCP Secret Manager secrets to GKE |
| Gerrit & k8s-gerrit Operator | Operator `7c0185d9` (`v0.1-767-g7c0185d9`) | `https://github.com/GerritCodeReview/k8s-gerrit` | Apache-2.0 | Source code review service |
| Grafana | `grafana` `9.3.0` | `https://grafana.github.io/helm-charts` | AGPL-3.0 / Apache-2.0 (chart) | Cluster dashboards and observability |
| Headlamp | `headlamp` `0.36.0` | `https://kubernetes-sigs.github.io/headlamp/` | Apache-2.0 | UI dashboard (`quay.io/kubescape/headlamp-plugin:v0.9.1` initContainer commented out / disabled in `gitops/templates/headlamp.yaml:38-53`) |
| Jenkins | `jenkins` `5.9.63` | `https://charts.jenkins.io` | MIT / Apache-2.0 | CI/CD runner for `workloads-android` |
| Keycloak | `keycloakx` `7.1.4` | `https://codecentric.github.io/helm-charts` | Apache-2.0 | Identity provider and OIDC/Keycloak realm broker |
| Kubescape Operator | `kubescape-operator` `1.29.11` | `https://kubescape.github.io/helm-charts/` | Apache-2.0 | Kubernetes security posture scanning |
| MCP Gateway Registry | Chart `1.0.0` | `gitops/apps/mcp-gateway-registry` | Apache-2.0 | Registry, auth server, and MCP gateway proxy |
| Monitoring Tools (`kube-state-metrics`, `node-exporter`) | `v2.12.0` / `v1.8.2` | `gitops/apps/monitoring-tools` | Apache-2.0 | Node and cluster state telemetry |
| OAuth2 Proxy | `oauth2-proxy` `8.3.0` | `https://oauth2-proxy.github.io/manifests` | MIT | Authentication proxy for Headlamp and Cuttlefish Orchestrator |
| PostgreSQL | `postgresql` `17.0.1` (`bitnamilegacy/postgresql`) | `https://github.com/bitnami/charts` | Apache-2.0 | Backing DB for Keycloak/Horizon services |
| Reclaimable PV Releaser | `reclaimable-pv-releaser` `0.1.1` | `https://plumber-cd.github.io/helm/` | MIT / Apache-2.0 | Reclaimable PV lifecycle automation |
| ZooKeeper | `zookeeper` `13.8.7` (`bitnamilegacy/zookeeper`) | `https://github.com/bitnami/charts` | Apache-2.0 | Coordination backend for distributed GitOps workloads |

---

## Applications (Closed Source / Source-Available / Third-Party Binaries)

| Component | Version | Source / Reference | License / Classification | Notes |
| --- | --- | --- | --- | --- |
| Android Build File System (ABFS) | `0.1.14` | Horizon SDV `abfs-service-module` (`0.2.0`), `abfs-data-module` (`0.1.0`), `abfs-client-module-*` (`0.1.0`) | Closed Source (Google Commercial / Preview) | GitOps Helm orchestration is Apache-2.0; ABFS runtime server/uploader binaries are closed source |
| HashiCorp Packer | `1.15.0` | `https://releases.hashicorp.com/packer/` | BSL-1.1 | Bundled in `cuttlefish-workflow:1.0.3` (verified by SHA256SUMS during image build) |
| HashiCorp Packer Google Compute Plugin | `hashicorp/googlecompute` `v1.2.4` | `https://github.com/hashicorp/packer-plugin-googlecompute` | MPL-2.0 | GCE builder plugin bundled in `cuttlefish-workflow:1.0.3` (`use_iap = true`) |
| HashiCorp Terraform CLI | `1.14.2` | `https://releases.hashicorp.com/terraform/` | BSL-1.1 | Pinned in `terraform/modules/base/version.tf` (`>= 1.14.2`) and deployment container |
| MediaTek MTK Connect | `v1.13.0` (with `mongo:8.2.12-noble`) | `gitops/apps/mtk-connect` (`chart 1.0.0`) | Commercial / Closed Source (`mtk-connect`) + SSPL (`mongo`) | Hardware-in-the-loop device lab bridge |

---

## Terraform Providers and Modules (`terraform/modules/base/version.tf` and `sdv-network`)

| Component | Constraint / Version | License |
| --- | --- | --- |
| `hashicorp/google` | `7.12.0` | MPL-2.0 |
| `hashicorp/google-beta` | `7.12.0` | MPL-2.0 |
| `hashicorp/helm` | `~> 3.0` | MPL-2.0 |
| `hashicorp/kubernetes` | `~> 2.0` | MPL-2.0 |
| `kreuzwerker/docker` | `3.6.2` | MPL-2.0 |
| `gavinbunney/kubectl` | `~> 1.14` | MPL-2.0 |
| `hashicorp/tls` | `>= 4.0.0` | MPL-2.0 |
| `hashicorp/local` | `>= 2.4.0` | MPL-2.0 |
| `hashicorp/external` | `>= 2.3.0` | MPL-2.0 |
| `terraform-google-modules/network/google` | `~> 15.1` | Apache-2.0 |

---

## Deployment Container (`tools/scripts/deployment/container/Dockerfile`)

| Tool / Base | Pinned Version | Notes |
| --- | --- | --- |
| Ubuntu base image | `ubuntu:24.04` | Deployment execution environment |
| Terraform | `1.14.2` | `TERRAFORM_VERSION=1.14.2` |
| Docker CLI | `29.1.3` | `DOCKER_VERSION=29.1.3` |
| Google Cloud CLI + GKE auth plugin | `549.0.1` (`549.0.1-0`) | `GCLOUD_VERSION=549.0.1` |

---

## Platform, Module, and Post-Job Container Images (`terraform/modules/base/locals.tf`)

| Image | Tag | Base / Runtime Images | License | Notes |
| --- | --- | --- | --- | --- |
| `cuttlefish-orchestrator-api` | `1.0.3` | Builder: `golang:1.26.9-alpine`; Runtime: `gcr.io/distroless/base-debian12:nonroot` | Apache-2.0 | CCO REST API, Spanner DAL, WebSocket/WebRTC & host/device orchestrator; `/docs` loads `@scalar/api-reference@1.44.20` (`MIT`) from unpkg CDN w/ SRI |
| `cuttlefish-console` | `1.0.3` | Builder: `node:24-alpine`; Runtime: `nginx:1.31.2-alpine3.23` | Apache-2.0 | Angular 22 frontend + nginx API/WebSocket reverse proxy |
| `cuttlefish-workflow` | `1.0.3` | Builder: `golang:1.26.9-alpine`; Runtime: `debian:12-slim` | Apache-2.0 + BSL-1.1 (packer) + MPL-2.0 (hashicorp/googlecompute) + Debian DFSG / GPL-2.0-with-classpath-exception (OpenJDK 17) | `cfctl` CLI, `google-cloud-cli`, `packer 1.15.0`, `hashicorp/googlecompute v1.2.4`, `android-tools-adb`, `openjdk-17-jre-headless` |
| `horizon-dev-portal` | `5.0.0` (`horizoncore`) | Builder: `golang:1.27.2-alpine`, `node:22-alpine`; Runtime: `gcr.io/distroless/static-debian12:nonroot` | Apache-2.0 | Developer Portal UI, Go proxy, and `horizon` CLI distribution |
| `module-manager-app` | `0.3.7` | Builder: `golang:1.27.2-alpine`; Runtime: `alpine:3.19` | Apache-2.0 | Module lifecycle controller and teardown orchestrator |
| `horizon-api-app` | `1.1.1` | Builder: `golang:1.27.2-alpine`; Runtime: `alpine:3.19` | Apache-2.0 | Horizon REST API backend |
| `storage-gcs-module-app` | `1.0.2` | Builder: `golang:1.27.2-bookworm`; Runtime: `gcr.io/distroless/static-debian12:nonroot` | Apache-2.0 | GCS module helper |
| `cloud-workstations-module-app` | `0.2.1` | Builder: `golang:1.25-alpine`; Runtime: `gcr.io/distroless/static-debian12:nonroot` | Apache-2.0 | Cloud Workstations module helper |
| `workflow-namespace-drain-app` | `1.0.1` | Builder: `golang:1.27.2-alpine`; Runtime: `alpine:3.19` | Apache-2.0 | Argo Workflows namespace drain controller |
| `gerrit-mcp-server-app` | `1.0.1` | `python:3.12-slim-bookworm` | Apache-2.0 | Gerrit MCP server |
| `kcc-webhook-cert-monitor` | `1.0.0` | `alpine:3.20` | Apache-2.0 | Config Connector webhook certificate monitor |
| `gerrit-post` | `1.0.2` | `debian:12.13` | Apache-2.0 | Gerrit post-install job |
| `mtk-connect-post`, `mtk-connect-post-key` | `1.0.1` | `debian:12.13` / `python:3.13-slim-bookworm` | Apache-2.0 | MTK Connect post-install hooks |
| `grafana-post`, `keycloak-post`, `keycloak-post-*` (`argocd`, `gerrit`, `grafana`, `headlamp`, `jenkins`, `mcp-gateway-registry`, `mtk-connect`) | `1.0.1` | `node:22-alpine` | Apache-2.0 | Grafana and Keycloak OIDC configuration post-jobs |
| `keycloak-post-argo-workflows`, `keycloak-post-horizon-api` | `1.0.0` | `node:22-alpine` | Apache-2.0 | Keycloak client post-jobs for Argo Workflows and Horizon API |
| `curlimages/curl` | `8.11.0` (`8.5.0` in `storage-gcs-module`) | Upstream `curlimages/curl` | curl (MIT-derivative) | Init/sync HTTP wait containers in `gitops/templates/keycloak-post-apps.yaml` and `keycloak-post-horizon-dev-portal-client.yaml` |

---

## Cuttlefish Orchestrator Dependency Breakdown (`THIRD_PARTY_NOTICES.md`)

For the complete package-by-package software bill of materials covering:
- **62** unique third-party Go modules (**64** entries across `cmd/orchestrator`, `cmd/cfctl`, `cmd/relayhook`, accounting for dual-licensed `github.com/go-jose/go-jose/v4` and `go.yaml.in/yaml/v3`), compiled into `cuttlefish-orchestrator-api:1.0.3` and `cfctl` inside `cuttlefish-workflow:1.0.3`,
- **182** unique production `name@version` npm packages (**179** unique package names, excluding `devOptional` `typescript@6.0.3`; bundled into `cuttlefish-console:1.0.3`: `MIT` 161, `ISC` 9, `Apache-2.0` 3, `BSD-2-Clause` 3, `0BSD` 1, `BSD-3-Clause` 1, `CC-BY-4.0` 1, `CC0-1.0` 1, `Python-2.0` 1, `AFL-2.1 or BSD-3-Clause` 1),
- Runtime CDN asset `@scalar/api-reference@1.44.20` (`MIT`, loaded with SRI on `/docs`), base images, `packer 1.15.0` (`BSL-1.1`), `hashicorp/googlecompute v1.2.4` (`MPL-2.0`), APT runtime packages in `cuttlefish-workflow:1.0.3` (including OpenJDK 17 `GPL-2.0-with-classpath-exception`), and self-hosted Google Sans, Google Sans Code, and Material Symbols `.woff2` fonts (`SIL OFL-1.1` / `Apache-2.0`),

see [`terraform/modules/sdv-container-images/images/cuttlefish-orchestrator/THIRD_PARTY_NOTICES.md`](../../terraform/modules/sdv-container-images/images/cuttlefish-orchestrator/THIRD_PARTY_NOTICES.md).
