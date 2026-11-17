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

# Deployment Modes

CCO's deployment shape is set by **three independent Terraform variables**. Each
answers one question, and they combine into a small set of valid topologies.

- **`ingress`** sets *CCO reachability*: `public` (the `run.app` URL is
  reachable from the internet) or `private` (internal ingress only). Built-in
  Cloud Run IAP works with **either** value.
- **`auth`** sets the *front-door mechanism*: `iap` (built-in Cloud Run IAP) or
  `network` (no authentication, where VPC reachability is the entire access
  model, for legacy clients that carry no credential).
- **`device_ingress`** sets *where WebRTC device media meets the internet*:
  `public` (at the host, where host VMs get external IPs), `private` (nowhere,
  since hosts are internal-only and clients connect over a VPN), or `relay`
  (at a TURN server, with internal-only hosts and TURN required).

All three are Terraform variables (`terraform.tfvars`). Terraform sets them,
and every other deployment value, as `CCO_*` environment variables on the
Cloud Run service (there is no config file). Only two of the three reach the
**application**, as `CCO_DEVICE_INGRESS` and `CCO_AUTH_MODE`. The application
never observes `ingress`, and cannot, because that variable is Cloud Run ingress
settings and IAM. CCO↔host traffic always uses **internal** IPs in every mode
(Cloud Run VPC egress is `PRIVATE_RANGES_ONLY`), and WebRTC signaling is
always proxied through the orchestrator; `device_ingress` only changes how
**media** flows between the client and the device.

## Constraints

Two rules make some combinations invalid (Terraform rejects them with a clear
precondition error):

- **C1.** `auth = "network"` requires `ingress = "private"`. Network mode
  has no credential gate, so the VPC boundary must be the access model.
- **C2.** `device_ingress = "public"` requires `ingress = "public"`. If host
  media meets the internet, CCO must too.

Two more: `device_ingress = "private"` requires a non-empty `client_cidrs`, and
`device_ingress = "relay"` requires configured TURN servers. Terraform
sets `CCO_TURN_SERVERS` and mounts the shared secret. The app refuses to start
in relay mode when either is missing, with the message naming which one.

## Valid topologies

The seven valid `(ingress / auth) × device_ingress` cells:

| ingress / auth ↓ \ device_ingress → | `public` | `private` | `relay` |
|---|---|---|---|
| **`public` / `iap`** | **Public** (reference) | ✓ | **Relay** |
| **`private` / `iap`** | ✗ (C2) | **Private** | ✓ |
| **`private` / `network`** | ✗ (C2) | **Lab** | ✓ |

(`public / network` is excluded entirely by C1.)

The four **named scenarios** in bold are the ones worth deploying on purpose;
the plain ✓ cells are valid but rarely what you want. The auth rows mirror
upstream Cloud Orchestrator's two production account managers (`iap` matches
its IAP deployment, `network` its username-only deployment), so an org
replacing upstream lands in the cell it already occupies (see
[Replacing Cloud Orchestrator](replacing-cloud-orchestrator.md)).

## Media confidentiality

WebRTC media is **DTLS-SRTP end-to-end encrypted** in *every* topology,
including `relay`. A TURN server forwards encrypted packets; it **cannot
decrypt the media**. The modes differ in **attack surface and choke points**,
meaning which IPs are internet-reachable and where the media path crosses the
public internet. Media confidentiality is the same in each.

## Browser access in iap mode

In `iap` mode the orchestrator refuses any request that carries no valid IAP
assertion, and the browser sign-in is Cloud Run's built-in IAP with the
Google-managed OAuth client, which admits only users of the organization that
owns the project. An outside user on `iap_members` still cannot open the
console. Principals outside the organization use cfctl through the CLI service
account instead: list them in `cli_users`, and Terraform creates the
`<deployment>-cli` service account, puts it on the IAP access list and grants
each of them `roles/iam.serviceAccountTokenCreator` on it. Each user then runs
`cfctl config set-impersonate` with the address from
`terraform output cli_service_account`, and cfctl sends a token signed as that
service account, which IAP accepts without a browser sign-in. An organization
policy that limits IAM grants to its own domains
(`constraints/iam.allowedPolicyMemberDomains`) also blocks the grant to an
outside user. CCO adds no load balancer or custom OAuth client to widen
browser access.

## Scenario: Public (reference)

`ingress = "public"`, `auth = "iap"`, `device_ingress = "public"`.

Host VMs get external IPs; a firewall rule (`allow-cuttlefish-webrtc`) opens
the WebRTC media ports (to `client_cidrs`, or `0.0.0.0/0` when empty). The
client connects media directly to the host's external IP via STUN; configured
TURN servers are offered only as a fallback for restrictive client networks.

This is the **validated reference**. Host provisioning, device streaming,
ADB, and logcat are all proven end-to-end on this topology. The others are
supported by construction but exercised less.

**Deploy it:** follow the [Quick Start](quick-start.md) as-is. Optionally set
`client_cidrs` to tighten the host firewall below `0.0.0.0/0`.

## Scenario: Private (VPN, no TURN)

`ingress = "private"`, `auth = "iap"`, `device_ingress = "private"`.

Host VMs are internal-only (no external IP) and no client-facing firewall
rule is opened to the internet. Clients reach both CCO and device media over
a **VPN into the VPC**; no TURN infrastructure is needed. `client_cidrs` is
required and names the VPN / private ranges.

**Prerequisites:**

- A VPN (or interconnect) with a route into the VPC **and** into the host
  subnet. Media flows client → host over the private ranges in
  `client_cidrs`.
- Reach `run.app` over private ingress: resolve `*.run.app` via
  `private.googleapis.com` (restricted/private Google Access) or a Private
  Service Connect endpoint from inside the VPC.
- The internet is still required for **Google sign-in**, since IAP
  authenticates the browser and user at Google even though the service itself
  is internal.

**Deploy it:** set the three variables above, `client_cidrs = ["<vpn-range>"]`,
and your VPN routing, then `terraform apply`.

## Scenario: Relay (TURN)

`ingress = "public"`, `auth = "iap"`, `device_ingress = "relay"`.

Host VMs are internal-only; all client media is relayed through a TURN server
that has a public front and a leg into the VPC. No client-facing host firewall
rule is created. The orchestrator **fails fast at startup** without TURN
servers plus a shared secret (`CCO_TURN_SERVERS` env and the mounted secret
volume, both set by Terraform). Without a relay there is no media path to a
private host.

> **Proven end to end (2026-07-25).** A relay was provisioned beside a live
> deployment and device media crossed it: the console selected a relay↔relay
> candidate pair and decoded video, and a credential minted by `/v1` allocated
> on the real relays. **One half is still unexercised.** No ACME certificate
> has ever been issued, so `turns:` on 443, the renewal timer, and the
> restart-on-renewal path are code that has never run. Treat first issuance as
> an experiment, and run [`tests/livesmoke`](../development/testing.md) against
> the deployment either side of it.

**Deploy it, in order:**

1. Build the relay image (once per release):

   ```bash
   packer build -var project_id=your-project-id \
     -var zone=<zone> \
     -var cco_revision="$(git rev-parse HEAD)" \
     deployment/packer/relay-image.pkr.hcl
   ```

   `zone` has no default; pick one in your deployment region. Every other
   packer variable has one. `network` is `cuttlefish-network`, `subnetwork`
   is `cuttlefish-network-subnet` and `image_family` is `cco-relay`. Override
   the first two if your VPC differs; the project has no default network, so
   the build fails instead of falling back.

2. Point a DNS name at the relay's address and set, in `terraform.tfvars`:

   ```hcl
   device_ingress = "relay"
   relay_image    = "projects/your-project-id/global/images/family/cco-relay"
   turns_domain   = "relay.example.com"   # what clients dial for TURNS
   acme_email     = "ops@example.com"
   ```

   Without `turns_domain` the relay serves plain `turn:` on 3478 only, which
   fails for exactly the clients relay mode exists to serve.

   `relay_instance_count`, `relay_machine_type`, `relay_zones` and
   `turn_realm` size and place the relays; all four have defaults. An ACME
   account that needs external binding also takes `acme_eab_kid` and
   `acme_eab_hmac_secret`.

3. `terraform apply` (with the image pinned, per the [Quick Start](quick-start.md)).
   No public WebRTC firewall rule is created, and the relay's service account
   is added to the IAP access list automatically.

To use a TURN server you already run instead, leave `relay_image` empty and set
`turn_servers` plus `turn_shared_secret`; the orchestrator will mint credentials
for it. That path needs a server that honours the TURN REST API **and enforces
the credential expiry**, which not every implementation does.

The orchestrator mints ephemeral credentials per device (standard TURN REST API
HMAC), so the shared secret never reaches clients. A relay stood up here holds
no secret either: it asks the orchestrator to authorize each client, which is
what makes the credential lifetime real. Private hosts still reach Google APIs
(Build API artifact fetches, GCS) via Private Google Access; provision Cloud NAT
yourself for any other outbound traffic.

Relay mode has these limits:

- **TURNS is TCP.** For most enterprise clients 443 is the only port that
  leaves, and TCP means loss stalls media rather than degrading it. Relay does
  not feel as good as direct.
- **Certificate renewal restarts the relay** and drops live sessions, because
  turn-rs cannot reload one. At 90-day certificates that is four scheduled
  interruptions a year.
- **The credential TTL is a hard session ceiling** of 12 hours. The expiry is
  baked into the username, so renewing means a new allocation, and no client
  CCO serves can re-gather without rebuilding its peer connection. A session
  outlives its credential only by reconnecting. This does not widen the
  revocation window, because the relay's agent re-asks the orchestrator every
  five minutes, so a deleted device stops relaying within five minutes.
- **Each end of a connection gets a different server list.** A browser reaches
  the relay at its external address, a host VM only at the internal one (there
  is no NAT), so `GET /v2/devices/{id}/ice-config` answers with `client_servers`
  and `device_servers` under one shared credential, plus the transport policy.
  A caller sends the *device* list on in `request-offer`; sending the client
  list produces a connection that never establishes. `/v1` gets the two
  flattened into the single list its wire admits.

## Scenario: Lab (no authentication, VPC-only)

`ingress = "private"`, `auth = "network"`, `device_ingress = "private"`
(or `relay`).

No cryptographic gate at any layer: Cloud Run's invoker IAM check is
disabled, IAP is off, and the app validates nothing. **VPC reachability,
meaning firewall rules and VPN membership, is the entire access model.** This is
the cell for legacy clients that cannot present a credential (OmniLab ATS
hardcodes `Authorization: Basic dXNlcjo=` with no way to send anything
else), and it mirrors upstream Cloud Orchestrator's `username-only`
deployment, which makes the same trust assumption.

Choosing it has these consequences:

- **Attribution collapses to one name.** Every request is attributed to the
  same synthetic principal (`anonymous@network.internal`), and a
  self-asserted username is never echoed into the audit log. So `mine=true`
  on listings means *everything*, `created_by` is uniform, `/v2/me` returns
  the synthetic identity, and the audit log has one writer.
- **The console is reachable without sign-in** by anything with VPC
  reachability, which includes every host VM and every Android guest under
  test. Mandatory private ingress is the only mitigation.
- **Access review means reviewing firewall rules and VPN membership.** There
  is no IAM list. Revocation is a firewall or VPN change.

Required tfvars: `ingress = "private"`, `auth = "network"`, plus
`client_cidrs` for the private device media. For cfctl, run
`cfctl config set-auth none` (or set `CFCTL_AUTH_NONE=1`); smoke the
deployment with `tests/livesmoke -auth-none`.

This mode also provisions an **internal Application Load Balancer** in
front of the service (`terraform output internal_lb_url`). Legacy HTTP/1.1
clients, OmniLab ATS in particular, must use that URL and not run.app.
Their empty POSTs carry no `Content-Length` header, and Google's front end
rejects those with 411 before the service sees them. The load balancer
accepts them; cfctl and cvdr work against either URL.

## Horizon SDV GKE Module (`CCO_AUTH_MODE=proxy`)

When deployed inside Horizon SDV via `gitops/modules/cuttlefish-orchestrator`, CCO runs as two Kubernetes Deployments in `<prefix>cuttlefish-orchestrator` (`cuttlefish-orchestrator-api` and `cuttlefish-console`) backed by Cloud Config Connector Spanner (`100` processing units):

- **Front-door auth (`CCO_AUTH_MODE=proxy`)**: GKE Gateway terminates TLS and routes `/cuttlefish-orchestrator/` through `oauth2-proxy` (`8.3.0`, Keycloak OIDC for `administrators` and `developers`). `cuttlefish-console` forwards `X-Forwarded-Email` / `X-Forwarded-User` to `X-Auth-Request-*`, and `NetworkPolicy/allow-cuttlefish-orchestrator-api` enforces `CCO_TRUSTED_PROXIES` so only `cuttlefish-console`, `<prefix>workflows`, and kubelet probes reach API `:8080`.
- **Device media (`CCO_DEVICE_INGRESS=private`)**: Host VMs have internal IPs only (`cco_webrtc_client_cidrs` + cluster pod/subnet CIDRs reach WebRTC ports `15550-15599`).
- **Argo Workflows (`argo-workflows/` subchart)**: Exposes `cco-host-image-build` (Debian 13 + `cuttlefish-* >= 1.58.0` over IAP, `cco-host` / `cco-host-arm64`), `cco-cvd-run`, and `cco-cts-run` using `cuttlefish-workflow` (`cfctl`, `gcloud`, `packer`, `adb`, Java 17 JRE).

## Local development

The backend is configured entirely by `CCO_*` environment variables (there is
no config file). For local development, copy `.env.example` to `.env`, fill it
in, and source it into your shell:

```bash
cp .env.example .env
# edit .env with your values, then:
set -a; source .env; set +a

# Optional: Spanner emulator instead of a real instance
docker run -d -p 9010:9010 gcr.io/cloud-spanner-emulator/emulator
export SPANNER_EMULATOR_HOST=localhost:9010

go run ./cmd/orchestrator
```

Set `CCO_DEV_MODE=true` and `CCO_DEV_FALLBACK_EMAIL` in `.env`: requests
without an identity header are attributed to the fallback email. This path is
for local use only. The server **refuses to start** with dev mode on Cloud
Run, or anywhere else on Google Cloud that the metadata server answers, so a
deployed CCO is always `auth = "iap"` or `auth = "network"`. Creating real
hosts additionally needs Application Default Credentials with Compute
permissions.

The container serves the built console from `/web/cuttlefish-console`; a local
backend serves the API only. Frontend development runs against the
network-mocked unit and e2e suites (`npm test`, `npm run e2e`). The
`ng serve` dev server is not currently proxied to a local backend.

## See also

- [Quick Start](quick-start.md), the reference Public walkthrough
- [Deployment Checklist](deployment-checklist.md)
- [Authentication](../concepts/authentication.md), IAP and CLI details
- [WebRTC Streaming](../concepts/webrtc-streaming.md), how media flows in each mode
