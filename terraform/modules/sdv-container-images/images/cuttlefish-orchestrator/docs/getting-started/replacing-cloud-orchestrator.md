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

# Replacing Cloud Orchestrator with CCO

CCO is a drop-in replacement for Google's upstream
[cloud-android-orchestration](https://github.com/google/cloud-android-orchestration)
service: clients that speak the legacy Cloud Orchestrator API keep working
against CCO's byte-exact [`/v1` surface](../reference/legacy-api.md) with
**only a URL change**. Clients need no forks, patches or new credentials.
New clients can use the native `/v2` API while the legacy clients stay
unchanged.

## Matching deployments

Upstream ships two production deployment templates, distinguished by their
account manager. CCO's `auth` variable mirrors them one-to-one:

| Your upstream deployment | Account manager | CCO deployment |
|---|---|---|
| `scripts/gcp/cloudrun/` | `Type = "IAP"` | `auth = "iap"`, the [Public / Private / Relay scenarios](deployment-modes.md) |
| `scripts/on-premises/single-server/` | `Type = "username-only"` | `auth = "network"`, the [Lab scenario](deployment-modes.md#scenario-lab-no-authentication-vpc-only) |

Upstream's other two account managers are non-goals: `GCP` (App Engine's
identity header, where the migration is to `iap`, which gives the same
verified emails) and `unix` (single-user local development, where CCO's dev
mode is the equivalent).

## Clients

- **cvdr** works in both cells, with the credential shape it already has:
  - Under `iap`: `[Services.x.Authn.OIDCToken] TokenFile`. Mint the
    self-signed SA JWT as shown in the [Quick Start](quick-start.md).
  - Under `network`: `HTTPBasicAuthn` (or no authn at all). CCO ignores the
    header and serves the request.
- **OmniLab ATS** exists only in the username-only cell: it hardcodes
  `Authorization: Basic dXNlcjo=` with no flag or callback for a real
  credential, so it requires a `network` deployment. Point
  `--orchestration_service_url` at the **internal load balancer** the
  `network` deployment provisions (`terraform output internal_lb_url`),
  never at the run.app URL: the ATS client speaks HTTP/1.1 and sends its
  empty POSTs (every operation `:wait`) without a `Content-Length` header,
  which Google's front end rejects with 411 before CCO sees them. The
  load balancer accepts them. Start the worker with
  `mtt start --use_cloud_orchestrator --use_host_network` and the mtt
  `latest` image tag. The Cloud Orchestration client classes are absent
  from the `dogfood` and `stable` tags, and without host networking the
  console never comes up. ATS creates hosts with the docker request shape
  (`host_instance.docker`); CCO compiles that onto a GCE shape the
  operator controls with the `docker-host` setting, on the Settings page
  or with
  `cfctl admin config set docker-host machine_type=<type> ttl_hours=<n>`.
  ATS deletes each test's CVD but not its host; the `ttl_hours` value is
  what reclaims the VM.
- **cfctl / the console** are CCO-native and work in either cell (the
  console without sign-in in `network` mode). Under `network`, tell cfctl
  to send no credentials with `cfctl config set-auth none`, or set
  `CFCTL_AUTH_NONE=1`.

## Migration differences

Beyond the URL, two behaviors differ by design. CCO is single-tenant with
no per-user isolation, and both follow from that:

- **No per-user host views.** Upstream scopes GCE host listings to the
  caller (a `created_by` label filter) and refuses cross-user docker host
  deletion; each user sees only their own hosts. CCO's `/v1` lists every
  host under the zone token for every caller, and `created_by` is
  informational. This holds in *both* cells, so a multi-user cvdr org sees
  the whole fleet where upstream showed per-user slices.
- **Hosts are GCE VMs, not docker containers.** A `network`-cell migration
  moves the on-prem docker hosts onto GCE. The wire contract is
  unchanged and clients cannot observe the difference; an operator reading
  the GCP console will.

## See also

- [Deployment Modes](deployment-modes.md), the full topology matrix
- [Legacy API compatibility](../reference/legacy-api.md), what `/v1` serves
- [cfctl for cvdr Users](../guides/cvdr-to-cfctl.md), each cvdr command's cfctl equivalent
- [Authentication](../concepts/authentication.md), both front doors in detail
