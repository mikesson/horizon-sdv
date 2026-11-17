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

# Quick Start

Deploy the Cloud Cuttlefish Orchestrator from scratch and create your first
virtual device. This walkthrough deploys the `iap` front door, where the
orchestrator is a single Cloud Run service protected by built-in Cloud Run
IAP. There is no load balancer, no Identity Platform, and no role model.
Anyone on the IAP access list has full access. The other front door is
`auth = "network"`, which has no credential gate at all; see
[Deployment Modes](deployment-modes.md).

## Prerequisites

- A GCP project with billing enabled
- `gcloud` (authenticated: `gcloud auth login`)
- `terraform` >= 1.5
- Optional: project quota for GPU VMs (e.g. `nvidia-tesla-t4`) if you want
  GPU-accelerated hosts; hosts are CPU-only unless a GPU is requested

## 1. Configure

```bash
cd terraform
cp terraform.tfvars.example terraform.tfvars
```

Edit `terraform.tfvars`. The essentials:

```hcl
project_id = "your-project-id"
region     = "us-central1"    # required, no default

# Deployment topology, three atomic knobs. This is the Public reference
# scenario (public/iap/public); see docs/getting-started/deployment-modes.md
# for the full matrix and the Private / Relay / Lab alternatives.
ingress        = "public" # CCO reachability
auth           = "iap"    # front door (iap_members below is the access model)
device_ingress = "public" # WebRTC media meets the internet at the host

# The IAP access list, the ENTIRE access model.
iap_members = [
  "user:you@example.com",
]

# Optional: who uses cfctl. A non-empty list makes Terraform create the
# "cco-cli" signing service account, put it on the IAP access list, and grant
# each principal here the impersonation and IAP-tunnel roles cfctl needs.
# Leave it empty to manage that identity by hand (step 7).
cli_users = [
  "user:you@example.com",
]
```

Alternatively, run the interactive wizard:

```bash
../scripts/provision/bootstrap.sh --project your-project-id
```

## 2. Provision Infrastructure

```bash
terraform init
terraform apply -var "image=us-docker.pkg.dev/cloudrun/container/hello"
```

This creates the VPC, Spanner instance (seeded from
`migrations/spanner/complete_schema.sql`), Artifact Registry, and the
IAP-protected Cloud Run service. All application configuration is delivered as
`CCO_*` environment variables set from your `terraform.tfvars`. There is no
config file. The service starts on a placeholder image until step 3. Pass the
placeholder on this first apply only: later applies without `image` keep the
image the service runs.

## 3. Build and Deploy the Container

The container image is Terraform-managed, so a deploy is two steps: build the
image, then `terraform apply` with the new image pinned by digest (which rolls
out the image and any config changes in one atomic revision).

`scripts/build-image.sh` reads the project, region and repository from the
Terraform outputs, builds the image with Cloud Build, and prints the image
digest. `terraform output next_steps` shows the same two steps.

```bash
cd ..
scripts/build-image.sh                                 # prints the image digest
terraform -chdir=terraform apply -var image=<digest>   # rolls it out
```

## 4. Build a Host Image

Host VMs boot from a GCE image in the `cco-host` image family, read from the
deployment's own project unless `host_image_project` names another. CCO ships
no image. Build it with the upstream Android Cuttlefish tooling, as
[Building Host Images](../guides/building-host-images.md) describes. Until the
family holds an image, every host create is refused with an error that names
the missing family.

## 5. Open the Console

```bash
cd terraform && terraform output service_url
```

Open the URL in a browser. IAP signs you in with your Google account; you
must be on the `iap_members` list. The full console, covering hosts, devices,
builds, batches and the Administration pages (Capacity, Control Plane,
Autoscaler, Catalog, Settings, Activity), is available to every authenticated
user.

## 6. Curate Zones, Then Create a Host and Device

**First, curate the zone catalog.** The resource sync crawls the GCE catalog,
but every discovered zone starts **disabled**, and the **enabled catalog is
the placement space**. Server-side zone ranking (automatic placement, zone
failover) considers every enabled zone, and your VPC subnet is regional. In
the console under **Administration → Capacity**, enable the zones inside your
deployment region (e.g. `us-central1-a/b/c`) with the toggle in each zone's
tile; a zone Compute Engine offers that the fleet has never used is one
disclosure below the board. Until at least one zone is enabled, host creation
fails with an explanatory error.

Then, in the console:

1. **Hosts → Create Host**, picking a zone and machine type
   (the seeded default is `n1-standard-8`; optionally attach a GPU).
   Provisioning takes a few minutes.
2. **Devices → Create Device**, picking the host and an Android build, then
   connect to the device stream from the device detail page.

Or from the CLI. Both CLIs authenticate as a service account, so create one
first (next section), then:

```bash
# One command: places on existing capacity or auto-provisions a host in a
# capacity-ranked zone, resolving the default AOSP build.
./cfctl device create --name my-device --interaction headless --wait

# Or imperatively: an explicit host first (omit --zone for ranked placement,
# name one to pin the host to it), then a device on it.
./cfctl host create --name my-host --zone us-central1-a --wait
./cfctl device create --host <host-id> --name my-device
```

### Build sources on Cloud Storage

Builds registered against `gs://` URLs, including anything `cfctl build push`
uploads, are fetched by the host VM itself as its own service account. Set the
Terraform variable `host_service_account` to give host VMs one, and grant it
read on the bucket holding the artifacts. `create_host_service_account` (on by
default) provisions a `cco-host` account and grants it read on the artifacts
bucket, but nothing is attached to a VM until `host_service_account` names it.
Attaching a service account also changes how the Host Orchestrator fetches, so
pair it with a host image whose `cvd` handles `gs://` build sources. See
[Testing Local Builds](../guides/local-builds.md).

## 7. CLI Access: a Service Account for cfctl and cvdr

Browsers sign in through IAP interactively; CLIs cannot. Instead, both
`cfctl` (the native `/v2` CLI) and the upstream `cvdr` (the legacy `/v1`
CLI) authenticate as a **GCP service account**: the SA's email goes on the
IAP access list, and the CLI presents a self-signed JWT
(`aud = <service-url>/*`) that built-in IAP accepts with its Google-managed
OAuth client. No custom OAuth client, consent screen, or key file is
required.

### Let Terraform create the service account

Setting `cli_users` in step 1 already did all of this. Terraform creates a
`cco-cli` service account, adds it to the IAP access list, and grants every
principal in `cli_users` both `roles/iam.serviceAccountTokenCreator` on it
(the one grant impersonation needs) and `roles/iap.tunnelResourceAccessor`
(which `cfctl host ssh` needs). Read the email back with:

```bash
terraform -chdir=terraform output -raw cli_service_account
```

Revoke a user by removing them from `cli_users` and re-applying. The service
account stays for everyone else. If you took this route, skip ahead to the
cfctl section below.

### Or create the service account by hand

```bash
gcloud iam service-accounts create cco-cli \
  --project your-project-id --display-name "CCO CLI access"
```

Put it on the access list in `terraform.tfvars` and re-apply:

```hcl
iap_members = [
  "user:you@example.com",
  "serviceAccount:cco-cli@your-project-id.iam.gserviceaccount.com",
]
```

```bash
terraform -chdir=terraform apply
```

Then allow yourself (and any CI identity) to mint tokens *as* the SA. This is
keyless impersonation, with no downloaded key:

```bash
gcloud iam service-accounts add-iam-policy-binding \
  cco-cli@your-project-id.iam.gserviceaccount.com \
  --member "user:you@example.com" \
  --role roles/iam.serviceAccountTokenCreator
```

IAP IAM changes can take a minute to propagate, so a brief 401/403 on the
first call right after `terraform apply` is normal.

Where impersonation is impossible, a downloaded key
(`gcloud iam service-accounts keys create`) works too, and `cfctl setup
./key.json` configures everything from it. Prefer impersonation.

### cfctl (native `/v2` API)

```bash
go build -o cfctl ./cmd/cfctl
./cfctl config set-server "$(terraform -chdir=terraform output -raw service_url)"
./cfctl auth login   # Application Default Credentials, once per machine
./cfctl config set-impersonate cco-cli@your-project-id.iam.gserviceaccount.com
./cfctl auth whoami  # should print the SA's email
```

Leave the audience unset (`config set-audience` is only for a custom IAP
OAuth client). With no audience, cfctl signs the
self-signed JWT automatically on every call. See
[Using the CLI](../guides/using-the-cli.md) for the full authentication
ladder and CI/Workload Identity Federation options.

### cvdr (legacy `/v1` API)

The upstream `cvdr` from
[cloud-android-orchestration](https://github.com/google/cloud-android-orchestration)
(`go build ./cmd/cvdr`) talks to CCO's `/v1` compatibility surface
unchanged (see [Legacy API](../reference/legacy-api.md)). It reads a bearer
token from a file, so mint the same self-signed JWT with `gcloud` and
point cvdr's config at it:

```bash
SA=cco-cli@your-project-id.iam.gserviceaccount.com
URL="$(terraform -chdir=terraform output -raw service_url)"
NOW=$(date +%s)
printf '{"iss":"%s","sub":"%s","aud":"%s/*","iat":%d,"exp":%d}' \
  "$SA" "$SA" "$URL" "$NOW" $((NOW + 3000)) > /tmp/claims.json
gcloud iam service-accounts sign-jwt /tmp/claims.json ~/.cvdr-token \
  --iam-account "$SA" --project your-project-id
```

Write the config file. Keys are PascalCase and the decoder is strict.
cvdr has no default config location; it reads the path from
`CVDR_USER_CONFIG_PATH`:

```toml
# ~/.config/cvdr.toml
SystemDefaultService = "cco"

[Services.cco]
ServiceURL = "https://<your-service-url>"
Zone = "us-central1-a"
BuildAPICredentialsSource = "none"
ConnectAgent = "webrtc"

[Services.cco.Authn.OIDCToken]
TokenFile = "/home/you/.cvdr-token"
```

```bash
export CVDR_USER_CONFIG_PATH=~/.config/cvdr.toml
cvdr host list
cvdr create --branch aosp-android-latest-release \
  --build_target aosp_cf_x86_64_only_phone-userdebug
```

By default the configured `Zone` is a placement hint. The server picks a
capacity-ranked zone, accepts a zone it has never cataloged, and keeps listing
the host under the configured zone (see
[Legacy API](../reference/legacy-api.md)). A host create that names no
machine type gets the catalog default.

The token expires (50 minutes with the claims above; IAM caps `exp` at
12 hours). Re-run the `sign-jwt` command to refresh; cvdr re-reads the
file on every command. This is the one operational difference from cfctl,
which mints a fresh token per call by itself.

## Granting Access to Others

Add principals to `iap_members` and re-apply:

```hcl
iap_members = [
  "user:you@example.com",
  "group:android-team@example.com",
  "serviceAccount:ci@your-project-id.iam.gserviceaccount.com",
]
```

That list is the access model. There is no user provisioning and there are no
roles.

## Next Steps

- [Deployment Checklist](deployment-checklist.md)
- [Authentication](../concepts/authentication.md)
- [Creating Devices](../guides/creating-devices.md)
- [CI/CD Integration](../guides/cicd-integration.md)
