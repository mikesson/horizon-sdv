# Cloud Cuttlefish Orchestrator - Quota System

## Overview

The quota system enforces a **single global limit** on how many host VMs can
be provisioned. There is no per-organization or per-team subdivision, because
the platform is single-tenant.

Device limits are handled separately through host capacity settings
(`hosts.max_devices`), not through this quota system.

## Design

1. **Simplicity**: one global counter (`scope='platform'`,
   `scope_id='global'`) in the `quotas` Spanner table.
2. **Atomic operations**: quota is consumed in the same transaction that
   checks the limit, preventing race conditions.
3. **Fail-safe**: quota is consumed before the GCE instance is created and
   released after deletion; a failed create rolls the counter back.
4. **Self-healing**: the host reconciler periodically recounts the counter
   against the actual number of active hosts (statuses `running`,
   `pending`, `provisioning`, `starting`, `stopped`) and corrects drift.

This platform quota is distinct from **GCE regional quotas** (CPUs, GPUs,
addresses), which CCO syncs from GCE and consults during zone ranking and
admission validation. See [Managing Hosts](../guides/managing-hosts.md).

## API

| Endpoint | Description |
|----------|-------------|
| `GET /v2/quota` | Current global quota (max, used, available) |
| `GET /v2/quota/platform` | Same data (kept for compatibility) |
| `PUT /v2/quota/platform` | Update the global `max_hosts` limit |

All endpoints require authentication only. Any authenticated caller can
read and update the quota.

## Configuration

The database is seeded with a global quota row (`max_hosts = 100`) by
`complete_schema.sql`. Adjust it via the API/console or directly:

```bash
gcloud spanner databases execute-sql orchestrator \
  --instance=cuttlefish-orchestrator \
  --sql="UPDATE quotas SET max_hosts = 200 WHERE scope = 'platform' AND scope_id = 'global'"
```

## Behavior When Exhausted

Host creation returns **HTTP 429 Too Many Requests** with a message of the
form:

```
host quota exceeded: requested 1, available 0/100
```

Delete unused hosts (or raise the limit) to free capacity. The reaper
automatically removes expired ephemeral hosts, releasing their quota.

## The autoscaler's reserve

The [fleet autoscaler](autoscaler.md) spends this quota too, and it never
spends the last slot: its per-cycle provisioning budget is capped at
`available − 1`. Standing capacity is an optimization, while a create that
hits the ceiling returns a 429 to a real caller. A surplus host costs drain
minutes; a refused create cannot be un-refused.

Near the ceiling this is silent by design: the autoscaler buys nothing,
increments `cco.fleetkeeper.quota_blocked`, logs *"standby top-up held back"*,
and the autoscaler page reports `quota_reserved`. A fleet that stops growing
one host below its limit is following this rule and is not faulty.
