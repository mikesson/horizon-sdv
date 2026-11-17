# Deployment Checklist

A pre/post-deployment checklist for the single-tenant Cloud Run IAP
architecture.

## Before `terraform apply`

- [ ] `project_id` and `region` set in `terraform.tfvars`
- [ ] `ingress` chosen: `public` (internet-reachable) or `private` (internal
  ingress only)
- [ ] `auth` chosen:
  - `iap` is built-in Cloud Run IAP (the default); `iap_members` lists every
    user/group/service account that needs access (it is the entire access
    model)
  - `network` turns authentication off (requires `ingress = "private"`); the
    VPC boundary is the entire access model, and the console is reachable
    without sign-in from inside it
- [ ] `device_ingress` chosen: `public` (external-IP hosts, requires
  `ingress = "public"`), `private` (internal hosts + VPN, requires
  `client_cidrs`), or `relay` (internal hosts + a TURN relay; set `relay_image`
  and `turns_domain`, or supply your own `turn_servers`); see
  [Deployment Modes](deployment-modes.md)
- [ ] Constraints satisfied: `auth = "network"` ⇒ `ingress = "private"`;
  `device_ingress = "public"` ⇒ `ingress = "public"`;
  `device_ingress = "private"` ⇒ `client_cidrs` non-empty
- [ ] GPU quota available in the target region/zones (only if hosts will
  attach GPUs; hosts are CPU-only by default)
- [ ] `cli_users` set if anyone will use `cfctl`. A non-empty list creates the
  `cco-cli` signing service account, puts it on the IAP access list, and
  grants each listed principal `roles/iam.serviceAccountTokenCreator` on it
  plus `roles/iap.tunnelResourceAccessor` for `cfctl host ssh`
- [ ] `host_service_account` set if devices will be created from `gs://` build
  sources, including builds pushed with `cfctl build push`. Host VMs fetch
  those artifacts as their own service account, and a VM launches without one
  when this is empty
- [ ] Host images published in the image project (`host_image_project`,
  default `project_id`): image family `cco-host`. An arm64 machine family
  such as `c4a` needs its row pointed at an arm64 image family first. A
  family without a bootable image of its own architecture refuses every host
  create with 422
- [ ] No stale `terraform.tfstate` from another project in `terraform/`

## After `terraform apply`

- [ ] `terraform output service_url` resolves
- [ ] Container built and deployed: `scripts/build-image.sh`, then
  `terraform apply -var image=<digest>` with the digest it prints (the image
  is Terraform-managed; `terraform output next_steps` repeats both steps)
- [ ] Browser check: opening the service URL triggers Google sign-in; a
  member of `iap_members` reaches the console; a non-member is blocked
- [ ] Bypass check: the run.app URL must NOT serve unauthenticated
  traffic (there is no `allUsers` invoker binding)
- [ ] `GET /v2/me` returns your email in the console's profile page
- [ ] Spanner database seeded: machine types appear in **Hosts → Create**
- [ ] Zone catalog curated: after the first resource sync, enable your
  networked region's zones (**Administration → Capacity**), because every
  discovered zone starts disabled. The enabled catalog is the placement
  space: automatic placement and zone failover rank across it, and your
  VPC subnet is regional
- [ ] Create a host end-to-end; device creation and streaming work
- [ ] CLI check: with `cli_users` set, take the email from
  `terraform output -raw cli_service_account`; otherwise add
  `serviceAccount:SA_EMAIL` to `iap_members` and re-apply. Then
  `cfctl config set-server $(terraform output -raw service_url)` +
  `cfctl config set-impersonate SA_EMAIL` (leave the audience unset) and
  confirm `cfctl auth whoami`. With the Google-managed IAP client, the SA
  authenticates via a self-signed JWT (aud = service URL), so no custom OAuth
  client is needed; see [Authentication](../concepts/authentication.md).
- [ ] `cfctl doctor` reports clean: it checks connectivity, auth, quota, that
  every enabled machine family resolves to a bootable host image, and storage
  access in one call

## Production Hardening

- [ ] `device_ingress = "relay"` with the TURN module (or `"private"` + VPN)
  for internal-only host VMs
- [ ] `cloud_run_min_instances >= 1` to avoid cold starts
- [ ] TURN shared secret supplied via `turn_shared_secret` (Secret Manager
  volume) and never committed; set it in a local, gitignored `terraform.tfvars`
- [ ] `spanner_deletion_protection` left at its default, `true`. Only a
  teardown sets it to `false`
- [ ] Periodic review of `iap_members` and `cli_users`

## Teardown

Run these in `terraform/`, with gcloud signed in as an account that can list
and delete Compute Engine instances and list addresses in the project
(`roles/compute.instanceAdmin.v1` plus `compute.addresses.list`, or
`roles/compute.admin`). Terraform's destroy step calls
`scripts/provision/teardown.sh`, which uses that gcloud login.

1. Lift the database guard:
   `terraform apply -var spanner_deletion_protection=false`. The provider reads
   the guard from state, so destroy alone cannot remove the database.
2. Run `terraform destroy` once. It deletes the Cloud Run service first, then
   the host VMs this deployment created (only those labelled with its
   `deployment`, so another install in the same project keeps its hosts),
   then the rest. When Terraform created the network, destroy then waits for
   Google to release Cloud Run's serverless address from the subnet before it
   deletes the subnet. That wait is usually 1 to 2 hours and is bounded at
   2.5 hours, with progress printed every minute.
3. If destroy stops partway, for example when a wait runs out, run
   `terraform destroy` again. It resumes from what is left.
4. Destroy does not remove the host images, the enabled APIs, the IAP service
   identity, the logs, or the Workload Identity Federation module if you
   applied it separately. Delete those by hand if the project stays.

## Legacy `/v1` API (optional)

The native API is at `/v2`; `/v1` byte-exactly serves the legacy CO/HO contract
for existing integrations and needs no extra configuration. See
[Legacy API compatibility](../reference/legacy-api.md).

## Rotating Access

Access changes are IAM-only:

```bash
# Add: edit iap_members in terraform.tfvars, then
terraform apply

# Emergency removal outside Terraform:
gcloud iap web remove-iam-policy-binding \
  --resource-type=cloud-run --service=cuttlefish-orchestrator \
  --region=<region> \
  --member=user:left@example.com \
  --role=roles/iap.httpsResourceAccessor
```
