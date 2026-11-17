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

# Cloud Cuttlefish Orchestrator

**Single-tenant platform for running Android Cuttlefish virtual devices on
Google Cloud.**

CCO runs Android virtual devices on a fleet of Compute Engine VMs that it
creates and deletes on demand. You state what you need (a build, an
interaction mode, optionally a region). The platform picks a zone and machine
shape, provisions a host, boots the Cuttlefish device, streams it to your
browser, and reclaims it when its lease expires. One engineer debugging a
device and a CI job creating two hundred use the same API.

There are three deliverables: a Go backend, an Angular console, and the
`cfctl` CLI.

## Capabilities

### Devices
Declarative or imperative create; start, stop, restart, powerwash, power
button; extend and pin against expiry; snapshots (take, list, restore,
delete); bug reports and screen recordings captured to Cloud Storage;
add, remove and screenshot virtual displays. A device runs one to eight
Android instances, or the instances a CVD canonical configuration describes.

### Hosts and capacity
Zone and machine-type ranking from a synced GCE catalog, with live quota
headroom and zone-health cooldowns. Automatic host provisioning, GPU-capable
shapes, Spot capacity with an on-demand policy, warm pools that prefetch build
artifacts, and per-family host image configuration. The fleet partitions into
pools (`default`, `gpu`, `arm`), each with its own autoscaler bounds and its
own machine-shape ladder. How many devices a host accepts is derived from the
shape's cores and memory, and an operator can override that per shape. Hosts
carry a TTL, which a reaper enforces.

Host VMs boot from GCE images the operator builds with the upstream
[android-cuttlefish](https://github.com/google/android-cuttlefish) tooling.
CCO ships no image pipeline. See
[Building Host Images](docs/guides/building-host-images.md).

### Builds
A registry over Android CI references (`@ab/<build>/<target>`, or a branch to
track) and over operator-supplied artifact sets on `gs://` or `https://`,
addressed by role: `host_package`, `default_build`, `kernel_build`,
`boot_build`, `bootloader_build` and `system_image_build`. A `gs://` artifact
is either a single object or a directory the host lists and picks its
artifacts out of. `cfctl build push` uploads local build output and registers
it as a build. Host VMs read `gs://` artifacts as their own service account,
so a deployment that uses them sets the Terraform variable
`host_service_account`.

### Access
WebRTC streaming with touch and keyboard input in the browser, ADB over
WebSocket, and a TURN relay for networks where a direct path does not exist.
The default front door is built-in Cloud Run IAP. `cfctl` and CI use Google
ID tokens.

### Governance
Platform-wide host-capacity quota, device lifetimes with operator-set defaults
and compiled caps, batches for fleet creates, long-running operations for every
async verb, an audit trail, and an admin control plane covering system
configuration, task queue health, provisioning statistics and zone policy.

### Compatibility
`/v1` re-implements the upstream Cloud Orchestrator and Host Orchestrator APIs
byte-for-byte, so `cvdr` and OmniLab ATS Cloud Orchestration mode work against
CCO unchanged. Native clients use `/v2` (OpenAPI at `/openapi.json`).

## Architecture

```
   Browser console          cfctl / CI (WIF, SA)        cvdr / OmniLab ATS
          │                          │                          │
          │  Google sign-in          │  Google ID token         │  /v1 legacy
          └────────────┬─────────────┴─────────────┬────────────┘
                       ▼                           ▼
             ┌────────────────────────────────────────────────┐
             │          Cloud Run  ·  built-in IAP            │
             │             cuttlefish-orchestrator            │
             │   /v2 native (Huma, OpenAPI)  ·  /v1 compat    │
             │   placement · quota · leases · task queue      │
             └────────┬──────────────────────────┬────────────┘
                      │                          │
                      ▼                          ▼
             ┌────────────────┐      ┌──────────────────────────┐
             │  Cloud Spanner │      │  GCE host VMs            │
             │  fleet state,  │      │  Host Orchestrator       │
             │  tasks, audit  │      │    └─ Cuttlefish devices │
             └────────────────┘      └────────────┬─────────────┘
                                                  │  WebRTC media
                                    direct  ◀─────┴─────▶  TURN relay
```

The orchestrator is one Cloud Run service behind built-in IAP. The only load
balancer is the internal one that `auth = "network"` provisions for legacy
HTTP/1.1 clients. There is no config file. Every deployment value is a `CCO_*`
environment variable set by Terraform, and secrets are mounted Secret Manager
volumes.

**There is no RBAC.** There are no roles, permissions, organizations or teams.
The IAP access list is the entire access model, and everyone on it can do
everything. `created_by` columns are informational.

## Deployment shape

Three independent Terraform variables set the topology.

| Variable | Values | Question it answers |
|---|---|---|
| `ingress` | `public`, `private` | Is CCO reachable from the internet? |
| `auth` | `iap`, `network` | What is the front door? |
| `device_ingress` | `public`, `private`, `relay` | Where does WebRTC media meet the internet? |

`auth = "network"` has no credential gate. VPC reachability is the whole
access model, for legacy clients that carry no credentials. Terraform
therefore forces `ingress = "private"` with it. See
[Deployment Modes](docs/getting-started/deployment-modes.md) for the valid
combinations.

## Quick start

Prerequisites: a GCP project with billing, authenticated `gcloud`, and
Terraform >= 1.5. Run the commands from the repository root.

```bash
cp terraform/terraform.tfvars.example terraform/terraform.tfvars
# set project_id, region, the three topology variables, and iap_members
terraform -chdir=terraform init
terraform -chdir=terraform apply -var "image=us-docker.pkg.dev/cloudrun/container/hello"
```

Or run the interactive wizard, which writes `terraform.tfvars` for you and
applies it:

```bash
scripts/provision/bootstrap.sh --project your-project-id
```

The first apply starts the service on a placeholder image. Build the real one
and pin it by digest:

```bash
scripts/build-image.sh                                 # prints the image digest
terraform -chdir=terraform apply -var image=<digest>
```

Two things remain before a device can boot. Build a host image into the
`cco-host` family of the project
([Building Host Images](docs/guides/building-host-images.md)). Then open the
Cloud Run service URL, sign in as a member of the IAP access list, and enable
the zones in your region under **Administration → Capacity**. Every zone
starts disabled, and placement only uses enabled zones. Full walkthrough:
[Quick Start](docs/getting-started/quick-start.md).

## Using the CLI

```bash
cfctl setup                     # interactive: server URL and identity
cfctl doctor                    # connectivity, auth, quota, storage access

# declarative: the platform places the device and provisions a host if needed
cfctl device create --name ci-1 --interaction headless \
    --build-id 12345678 --build-target aosp_cf_x86_64_phone-trunk_staging-userdebug

# imperative: you name the host
cfctl device create --name dbg-1 --host cf-host-3

cfctl device list
```

Without a build flag, `device create` uses the deployment's default build.
`cfctl --help` lists the full command tree. Reference:
[CLI](docs/reference/cli.md).

## Documentation

| Guide | Description |
|---|---|
| [Documentation index](docs/README.md) | Everything, organized |
| [Quick Start](docs/getting-started/quick-start.md) | Deploy from scratch |
| [Deployment Modes](docs/getting-started/deployment-modes.md) | The three topology variables and the valid combinations |
| [Building Host Images](docs/guides/building-host-images.md) | The GCE images host VMs boot from |
| [Replacing Cloud Orchestrator](docs/getting-started/replacing-cloud-orchestrator.md) | Drop-in migration, clients unchanged |
| [Architecture](docs/concepts/architecture.md) | System architecture and components |
| [Authentication](docs/concepts/authentication.md) | The IAP access model, browser and programmatic |
| [WebRTC Streaming](docs/concepts/webrtc-streaming.md) | Device streaming and TURN relay |
| [Using the CLI](docs/guides/using-the-cli.md) | `cfctl` setup and everyday commands |
| [Creating Devices](docs/guides/creating-devices.md) | Device workflows |
| [Testing Local Builds](docs/guides/local-builds.md) | Push a workstation build, boot a cloud device from it |
| [Fleet Autoscaler](docs/operations/autoscaler.md) | Standing capacity, the pools, and the switch that stops it |
| [CI/CD Integration](docs/guides/cicd-integration.md) | Workload Identity Federation setup |
| [API Reference](docs/reference/api.md) | The `/v2` REST API |
| [Legacy API](docs/reference/legacy-api.md) | The `/v1` byte-exact CO/HO surface |
| [Code Architecture](docs/development/code-architecture.md) | Codebase structure and patterns |

## Project structure

```
cmd/                  shipped binaries: orchestrator, cfctl, relayhook
internal/
  app/                composition root, all wiring
  domain/             per-aggregate entities, state machines, ports
  features/           vertical slices (device, host, build, batch, quota, …)
  platform/           adapters: spanner, gce, gcs, hoclient, turn, ice,
                      relay, audit, cloudmonitoring
  jobs/               device controller, host reconciler, reaper, catalog
                      sync, task-queue workers, fleet keeper, warm plane,
                      leader election
  compat/legacy/      the /v1 Cloud + Host Orchestrator surface
  router/ web/ middleware/ config/ telemetry/
web/cuttlefish-console/   Angular console, one directory per feature
terraform/            the deployment (modules/cco), plus relay and wif modules
deployment/packer/    the TURN relay VM image
migrations/spanner/   schema
scripts/              ci.sh (the gate), build-image.sh, the provision wizards
tests/                verification infrastructure and harnesses
tools/                developer tooling, each in its own module
examples/             CI recipes: GitHub Actions, GitLab CI, Argo Workflows
```

## Technology

| Component | Technology |
|---|---|
| Backend | Go 1.26, Huma v2 over chi |
| Frontend | Angular 22 (zoneless, signals), NgRx Signal Store, Zod |
| Database | Cloud Spanner |
| Auth | Built-in Cloud Run IAP, Google ID tokens |
| Infrastructure | Terraform, Cloud Run, Compute Engine |
| Streaming | WebRTC, TURN relay (turn-rs) |

## Building and testing

```bash
go build ./cmd/orchestrator
SPANNER_EMULATOR_HOST=localhost:9010 go test ./...
scripts/ci.sh                    # the full gate; read its final RESULT line
```

The persistence layer is covered only by emulator-backed tests, which skip
silently without `SPANNER_EMULATOR_HOST`. See
[Testing](docs/development/testing.md).
