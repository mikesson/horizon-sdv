# Horizon Developer Portal (GitOps)

Child chart deployed by [`gitops/templates/horizon-dev-portal.yaml`](../templates/horizon-dev-portal.yaml).

- **Image**: built from [`terraform/modules/sdv-container-images/images/horizon-dev-portal/horizon-dev-portal`](../../../terraform/modules/sdv-container-images/images/horizon-dev-portal/horizon-dev-portal) (Vite SPA + Go proxy). The parent Application sets the container image from [`gitops/values.yaml`](../../values.yaml) `config.containerImages.horizondevelopmentportal` (same pattern as other workloads; Terraform supplies the full ref per environment).
- **Public path**: `config.publicPath` in this chart (default `/developer-portal`). The parent passes values from `config.horizonDevelopmentPortal.publicPath` so the same value drives the Gateway HTTPRoute, Keycloak redirect URIs, and `PUBLIC_PATH` in the portal ConfigMap. Changing a live cluster may require updating Keycloak redirect URIs for an existing client.
- **Keycloak**: set in **this** chart’s `values.yaml` under `config`, or via parent `config.horizonDevelopmentPortal`: `keycloakRealm`, `keycloakHttpPathPrefix`, `keycloakClientId`, `keycloakTokenPath`, plus full `oidcIssuerUrl` / `keycloakTokenUrl` when not using the parent Application (the parent template derives them from domain + realm + path). `horizonApiCiClientId` must match the Keycloak confidential client used by the Go proxy (`HORIZON_API_CI_CLIENT_ID` in the ConfigMap).
- **In-cluster dependencies**: `moduleManagerBaseUrl` and `horizonApiBaseUrl` default to `http://module-manager.{prefix}module-manager...` and `http://horizon-api.{prefix}horizon-api...`. Override via parent [`gitops/values.yaml`](../../values.yaml) `config.horizonDevelopmentPortal.moduleManagerInternalBaseUrl` / `horizonApiInternalBaseUrl` when Service names differ.
- **Secrets**: the `keycloak-post-horizon-api` post-job writes `HORIZON_API_CI_CLIENT_SECRET` into Secret `{{namespacePrefix}}horizon-dev-portal-secrets` (same Keycloak client as `horizonApiCiClientId`). Optionally set Helm `secret.horizonApiCiClientSecret` to manage that Secret from GitOps instead.

```bash
helm lint .
```
