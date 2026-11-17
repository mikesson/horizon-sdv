# TURN relay

Media relay for deployments whose host VMs have no public address. Clients
reach the relay; the relay reaches hosts; nothing about a host is
internet-addressable.

```
   Browser ──turn:<external>:3478──▶ ┌───────────────┐ ──udp 15550-15599──▶ Host VM
                                     │  turn-server  │
   Device  ──turn:<internal>:3478──▶ └───────┬───────┘
                                             │ loopback gRPC
                                       ┌─────▼─────┐
                                       │ relayhook │──https──▶ Orchestrator
                                       └───────────┘
```

## Credential verification

This module has no shared-secret variable, by design.

The TURN REST API normally has the server derive a client's key from a secret
it shares with whoever mints credentials. turn-rs will do that, but it **does
not verify the credential's expiry**. Its source says so outright, and a
credential two days stale authenticates successfully. A TTL that nothing
enforces has no effect, so a leaked credential would work until the shared
secret was rotated.

So the relay delegates instead. `relayhook` serves turn-rs's authentication
hook on loopback and asks the orchestrator, which checks the expiry, checks the
device still exists, and returns only the derived key. The secret stays in
Cloud Run. A compromised relay VM yields no ability to mint anything, and
credentials expire when they say they do.

turn-rs resolves credentials in the order static credentials → static auth
secret → hook, returning early at each. Setting either static source means the
hook is **never called**, with no warning and no log line. The rendered config
is checked at plan time for exactly this, because it is the one misconfiguration
that silently removes the property relay mode exists for.

## Omitted controls

**No peer ACLs.** turn-rs binds one socket per interface and resolves a peer by
port lookup inside an authenticated session; it cannot address anything that is
not already one of its own clients. Relaying into the VPC (the top TURN threat,
and what the previous coturn module was wide open to) is not expressible.

**No relay port range in the firewall.** Relay ports are virtual for the same
reason. Ingress is 3478 and 443; the 49152-65535 opening a coturn deployment
carries has no equivalent.

**No load balancer.** A TURN allocation is per-instance state. A stateless L4
hash sends two peers with different source addresses to different backends,
which breaks the session; each instance therefore advertises its own address.

**No project roles.** The service account is created with none. turn-rs calls
no Google API, and the agent only proves the VM's identity, which the metadata
server does unaided. A stolen metadata token from this internet-facing VM buys
nothing in the project.

Two exceptions, both bindings on a resource rather than the project.

The account can create tokens for **itself**, which is how the agent signs the
JWT it presents to the orchestrator. A Google-signed identity token from the
metadata server would need no grant and does not work. Built-in Cloud Run IAP
rejects it whatever audience it carries. That was checked against a live
deployment, which answers "Invalid JWT audience" for the service URL, the URL
with a path wildcard, and the direct Cloud Run audience alike. IAP wants a token
the account signed for itself. The grant confers nothing beyond acting as an
identity that is already on the access list.

The second exists only where a certificate issuer requires external account
binding. It grants accessor on the single secret holding that HMAC key. The key
is read at use and never written to disk or metadata. Putting a credential in
instance metadata is what made the previous relay module unsalvageable.

## Usage

```hcl
module "relay" {
  source = "./modules/relay"

  project_id       = var.project_id
  region           = var.region
  zones            = ["us-central1-a", "us-central1-b"]
  network          = google_compute_network.network.id
  subnetwork       = google_compute_subnetwork.subnet.id
  orchestrator_url = google_cloud_run_v2_service.orchestrator.uri
  realm            = var.turn_realm
  image            = "projects/${var.project_id}/global/images/family/cco-relay"
}
```

The service account must be on the orchestrator's IAP access list, or the agent
cannot authorize anything:

```hcl
iap_members = [..., "serviceAccount:${module.relay.service_account_email}"]
```

And `realm` must equal the orchestrator's `CCO_TURN_REALM`. It is part of the
long-term key, so a mismatch rejects every credential. Each refusal is logged
with its reason, so the cause is visible in the logs.

## Image

Built by `deployment/packer/relay-image.pkr.hcl`: turn-rs compiled from a
pinned revision, plus `relayhook` compiled from this repository. Neither is
fetched at boot. The image records both revisions in `/etc/turn-server-revision`
and `/etc/cco-relayhook-revision`.

```bash
packer build \
  -var project_id=<project> \
  -var zone=<zone> \
  -var cco_revision="$(git rev-parse HEAD)" \
  deployment/packer/relay-image.pkr.hcl
```

## TURNS on 443

Set `turns_domain` to a name pointing at a relay address, with `acme_email`, and
the relay obtains a certificate and serves TURNS on 443.

Enterprise egress routinely blocks UDP and 3478 while leaving 443 open, so for
the clients relay mode exists to serve, 443 is often the only port that leaves
at all. In practice TURNS is the primary path.
It is TCP, so packet loss stalls media instead of
degrading it; size for that and expect relay to feel worse than direct.

Certificates come from Google Trust Services by default, over ACME with external
account binding. Its root is cross-signed to an old and universally present one,
which matters because native WebRTC validates against a root list compiled into
the binary rather than the system trust store. `acme_directory` points elsewhere
if you prefer another issuer.

HTTP-01 is the only challenge available. TLS-ALPN-01 wants 443, which turn-rs is
holding, and DNS-01 would mean giving the zero-role account DNS credentials. The
firewall rule that opens port 80 is created only where `turns_domain` is set,
and a deployment serving plain TURN never has it.

**Renewal restarts the relay and drops live sessions.** turn-rs has no
certificate reload (no signal handler, no file watch), so the new certificate
only takes effect on restart. At 90-day certificates that is four scheduled
interruptions a year, which is the accepted cost of the server choice. It is
also why short-lived certificates are not used here.

A relay with no `turns_domain` serves plain `turn:` on 3478 and keeps a
self-signed placeholder on 443, so the listener exists but no browser will trust
it. Certificates for a bare address, which would remove the need for a DNS name,
are not wired. Whether browsers accept `turns:` against one is untested, and
that open question gates the path.

## Status

A relay is stood up wherever `relay_image` is set, and nothing stands one up on
its own.

The rest of the lifecycle is manual. Building the image, pointing a DNS name at
an instance, and standing the relay back down are all operator steps, and there
is no automation around them.

Removing the relay takes the TURN shared secret with it. The secret is managed
while `device_ingress = "relay"`, `turn_shared_secret` or `relay_image` is set,
so an apply that clears all three destroys it. Supply `turn_shared_secret`
again on the next stand-up.
