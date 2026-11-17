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

# Cloud Cuttlefish Orchestrator - Terraform

The `modules/cco` module deploys the whole system: the Cloud Run service, its
Spanner instance and database, the VPC that Cuttlefish host VMs run in, the
artifacts bucket, and the IAM around all of it. This directory is a thin root
that configures the Google providers, passes every variable to the module and
re-exports every output. Run Terraform here, or call `modules/cco` from your
own configuration with your own provider settings.

**Full documentation**: See [docs/README.md](../docs/README.md)

## Quick Start

```bash
# Interactive bootstrap (recommended for first-time setup)
../scripts/provision/bootstrap.sh

# Or manual setup:
cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars with your values
terraform init
# The first apply has no service whose image it could keep, so it passes the
# placeholder. Later applies leave image empty or pass a new digest.
terraform apply -var "image=us-docker.pkg.dev/cloudrun/container/hello"
```

## Deployment shape

Three variables decide it.

| Variable | Values (default first) | What it decides |
|---|---|---|
| `ingress` | `public`, `private` | Cloud Run reachability |
| `auth` | `iap`, `network` | the front door |
| `device_ingress` | `public`, `private`, `relay` | where WebRTC device media meets the internet |

`auth = "iap"` uses built-in Cloud Run IAP, and the `iap_members` list is the
entire access model: everyone on it has full access, and there is no role
behind it. `auth = "network"` authenticates nothing, attributes every caller to
one synthetic principal, and requires `ingress = "private"`, so VPC
reachability is the access model instead. It also stands up an internal
Application Load Balancer, because legacy clients that carry no credential
cannot use the run.app URL; point them at the `internal_lb_url` output.

Those are the only two modes. There is no OAuth-client front door, no Identity
Platform, and no configuration file: every value the binary reads is a `CCO_*`
environment variable set on the service here.

Three constraints are enforced by preconditions on the Cloud Run service:

- `auth = "network"` requires `ingress = "private"`
- `device_ingress = "public"` requires `ingress = "public"`
- `device_ingress = "private"` requires a non-empty `client_cidrs`

## Variables worth knowing

| Variable | Default | Notes |
|---|---|---|
| `project_id`, `region` | none, none | the project and region to deploy into; both are required |
| `deployment` | `cco` | short name of this install, unique within the project. It labels every host VM (`cco-deployment`) and names the `<deployment>-host` and `<deployment>-cli` service accounts and the `<project_id>-<deployment>-artifacts` bucket. |
| `image` | `""` | the container image, in this deployment's own repository. Empty keeps the image the service runs now. A first apply passes the placeholder `us-docker.pkg.dev/cloudrun/container/hello`, since no service exists yet. |
| `iap_members` | `[]` | the IAP access list (`auth = "iap"`) |
| `cli_users` | `[]` | non-empty creates a `<deployment>-cli` service account for cfctl users to impersonate |
| `deployers` | `[]` | principals who run `scripts/build-image.sh`; builds run as the `<deployment>-builder` service account |
| `iap_oauth_client_id` | `""` | only for user-interactive programmatic access; a service account does not need it |
| `client_cidrs` | `[]` | ranges allowed to reach device media |
| `create_network`, `create_spanner`, `seed_database` | `true` | turn off to use existing infrastructure |
| `spanner_deletion_protection` | `true` | the database's deletion guard; a teardown applies `false` before `terraform destroy` |
| `relay_image` | `""` | non-empty stands up the TURN relay module |
| `turn_servers` | `[]` | TURN URIs advertised to WebRTC clients |
| `turn_shared_secret` | `""` | the secret ephemeral TURN credentials are derived from, mounted as a Secret Manager volume |
| `host_service_account` | `""` | the identity host VMs run as |
| `host_image_project` | `""` | the project host image families are read from (`CCO_HOST_IMAGE_PROJECT`); empty means `project_id` |

`modules/cco/variables.tf` carries the full list with a description and
default on each one.

## A second install in one project

Give it a different `deployment`. Its hosts then carry their own
`cco-deployment` label, and neither install's orphan sweep touches the other's
VMs. Names that are unique per project and do not derive from `deployment`
need their own values too: `network_name`, `firewall_rule_names`,
`artifact_repository_id`, `service_name` and `spanner_instance_name`. With
`relay_image` set, the relay module's names collide as well, so only one
install per project can run a managed relay.

## Outputs worth knowing

| Output | Use |
|---|---|
| `service_url` | the console and API URL |
| `internal_lb_url` | the URL credential-less legacy clients use, empty outside `auth = "network"` |
| `cfctl_audience` | what `cfctl config set-audience` takes, empty unless `iap_oauth_client_id` is set |
| `iap_audience` | what the backend expects on the IAP assertion header. Not a client setting. |
| `cli_service_account` | the identity `cli_users` impersonate |
| `build` | project, region, repository, builder service account and staging directory, read by `scripts/build-image.sh` |
| `next_steps` | the follow-up commands, printed after an apply |

## Key Files

| File | Description |
|------|-------------|
| `providers.tf` | provider settings and version constraints, with a commented `gcs` backend |
| `cco.tf` | the call to `modules/cco` |
| `inputs.tf`, `exports.tf` | the variables and outputs, forwarded to and from the module |
| `modules/cco/main.tf` | the deployment: service, database, network, IAM |
| `modules/cco/variables.tf` | input variables, with defaults and validation |
| `modules/cco/outputs.tf` | output values |
| `terraform.tfvars.example` | example configuration, copy to `terraform.tfvars` |

`terraform.tfvars` holds deployment-specific values and is not committed.
State is local unless you enable the `gcs` backend in `providers.tf`.

## Scripts

| Script | Description |
|--------|-------------|
| `../scripts/provision/bootstrap.sh` | Interactive setup wizard |
| `../scripts/provision/spanner-seed.sh` | Apply database schema |
| `../scripts/provision/teardown.sh` | Run by `terraform destroy`: deletes this deployment's host VMs and waits for the subnet to be released |
| `../scripts/build-image.sh` | Build the orchestrator image and print its image@digest |

## Modules

| Module | Description |
|--------|-------------|
| `modules/cco/` | the deployment itself. It declares provider requirements only, so it can be called with `count`, `for_each` or `depends_on`. |
| [`modules/relay/`](modules/relay/README.md) | the TURN relay, stood up by `modules/cco` when `relay_image` is set |
| `modules/wif/` | Workload Identity Federation for keyless CI authentication. The root configuration does not call it. |
