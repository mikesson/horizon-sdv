# Host VM Creation

This document explains how host virtual machines are created in the Cloud Cuttlefish
Orchestrator. A "host" is a Google Compute Engine (GCE) virtual machine that runs
the Cuttlefish Host Orchestrator software, capable of hosting multiple Android
virtual devices.

## Overview

The host creation process involves three main components working together:

```
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐
│                 │     │                 │     │                 │
│  Angular        │────>│  Go Backend     │────>│  Google Cloud   │
│  Frontend       │     │  (Orchestrator) │     │  Compute Engine │
│                 │     │                 │     │                 │
└─────────────────┘     └─────────────────┘     └─────────────────┘
        │                       │                       │
        │                       │                       │
        v                       v                       v
   User selects          Validates and            Creates the
   machine type,         provisions host          actual VM
   zone, and GPU                                  instance
```

## Machine Types

Machine types define the hardware configuration of a host VM. Rather than
hardcoding machine types in the application, they are stored in the Spanner
database for flexibility.

### Machine Type Categories

```
┌────────────────────────────────────────────────────────────────────┐
│                     Machine Type Categories                         │
├─────────────────────┬──────────────────────────────────────────────┤
│ General Purpose     │ N1, N2, N2D, N4 - Balanced compute/memory    │
├─────────────────────┼──────────────────────────────────────────────┤
│ Compute Optimized   │ C2, C3, C4, C4A - High CPU performance       │
│                     │ (C4A uses ARM/Google Axion processors)       │
├─────────────────────┼──────────────────────────────────────────────┤
│ Memory Optimized    │ M2, M3 - Large memory workloads              │
├─────────────────────┼──────────────────────────────────────────────┤
│ Accelerator (GPU)   │ G2 (L4), G4 (RTX PRO 6000), A2 (A100),       │
│                     │ A3 (H100) - Built-in GPU support             │
├─────────────────────┼──────────────────────────────────────────────┤
│ Storage Optimized   │ Z3 - High-throughput storage workloads       │
├─────────────────────┼──────────────────────────────────────────────┤
│ Cost Optimized      │ E2 - Budget-friendly options                 │
└─────────────────────┴──────────────────────────────────────────────┘
```

**Note:** ARM architecture (C4A with Google Axion) is indicated by the `architecture`
field in the database, not as a separate category.

### GPU Configuration

Machine types fall into two categories regarding GPU support:

```
┌─────────────────────────────────────────────────────────────────┐
│                                                                 │
│  Configurable GPU (N1 Family)                                   │
│  ─────────────────────────────                                  │
│  User selects machine type, then separately chooses:            │
│    - GPU type (T4, L4, RTX PRO 6000, V100, P4, P100, A100)     │
│    - GPU count (1, 2, 4, or 8)                                 │
│                                                                 │
│  Example: n1-standard-8 + 2x NVIDIA T4                         │
│                                                                 │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  Fixed GPU (G2, G4, A2, A3 Families)                            │
│  ────────────────────────────────────                           │
│  GPU is integrated into the machine type. User cannot change:   │
│    - G2: NVIDIA L4 GPU                                         │
│    - G4: NVIDIA RTX PRO 6000 Blackwell GPU                     │
│    - A2: NVIDIA A100 GPU                                       │
│    - A3: NVIDIA H100 GPU                                       │
│    - GPU count is fixed per machine size                       │
│                                                                 │
│  Example: g2-standard-8 always has 1x NVIDIA L4                │
│  Example: g4-standard-96 always has 2x NVIDIA RTX PRO 6000     │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Default Configuration

The default machine type for Cuttlefish is:

```
  Machine Type:  n1-standard-8
  vCPUs:         8
  Memory:        30 GB
  GPU Type:      NVIDIA Tesla T4
  GPU Count:     1
```

This configuration is recommended because prebuilt Cuttlefish host images
are optimized for the N1 family with T4 GPUs.

## Provisioning Models

Hosts can be created with two different provisioning models that affect
cost, availability, and reliability:

```
┌─────────────────────────────────────────────────────────────────┐
│                    Provisioning Models                           │
├─────────────────────┬───────────────────────────────────────────┤
│                     │                                           │
│  STANDARD           │  Regular on-demand VM                     │
│  ─────────          │  • Runs until explicitly stopped/deleted  │
│                     │  • Higher cost but guaranteed capacity    │
│                     │  • Recommended for production workloads   │
│                     │                                           │
├─────────────────────┼───────────────────────────────────────────┤
│                     │                                           │
│  SPOT               │  Discounted preemptible VM                │
│  ────               │  • Up to 60-91% cost savings              │
│                     │  • GCE may preempt at any time            │
│                     │  • VM is deleted when preempted           │
│                     │  • Ideal for fault-tolerant workloads     │
│                     │  • Can be retried after preemption        │
│                     │                                           │
└─────────────────────┴───────────────────────────────────────────┘
```

### Spot VM Behavior

When a Spot VM is preempted by GCE:

```
┌─────────────────────────────────────────────────────────────────┐
│                    Spot VM Preemption Flow                       │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  1. GCE needs capacity for higher-priority workloads            │
│                                                                 │
│  2. GCE terminates the Spot VM (InstanceTerminationAction=DELETE)│
│                                                                 │
│  3. The hostreconciler background job detects the preemption    │
│     via the GCE Operations API                                  │
│                                                                 │
│  4. Host status is updated to "preempted"                       │
│                                                                 │
│  5. User can retry:                                             │
│     • As Spot VM again (may succeed if capacity is available)  │
│     • As Standard VM (fallback option, always available)        │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### When to Use Spot VMs

| Use Case | Recommended Model |
|----------|-------------------|
| Development/testing | Spot - cost savings, interruptions acceptable |
| CI/CD pipelines | Spot - jobs can be retried automatically |
| Production workloads | Standard - reliability required |
| Long-running tests | Standard - avoid interruption mid-test |
| Cost-sensitive exploration | Spot - significant savings |

### Quota Requirements

Spot VMs require preemptible quota in GCE:

```
┌─────────────────────────────────────────────────────────────────┐
│                    Quota Types for Spot VMs                      │
├─────────────────────┬───────────────────────────────────────────┤
│ Standard VM Quota   │ Spot VM Quota                             │
├─────────────────────┼───────────────────────────────────────────┤
│ CPUS                │ PREEMPTIBLE_CPUS                          │
│ NVIDIA_T4_GPUS      │ PREEMPTIBLE_NVIDIA_T4_GPUS               │
│ NVIDIA_L4_GPUS      │ PREEMPTIBLE_NVIDIA_L4_GPUS               │
│ NVIDIA_A100_GPUS    │ PREEMPTIBLE_NVIDIA_A100_GPUS             │
└─────────────────────┴───────────────────────────────────────────┘
```

If preemptible quota is not available, Spot VM creation will fail with
a `spot_quota_exceeded` error. Users can either request quota increases
or fall back to Standard VMs.

## Creation Flow

### Step 1: Frontend Selection

The user opens the host creation form and is presented with machine types
loaded from the backend API.

```
┌─────────────────────────────────────────────────────────────────┐
│                     Create New Host                              │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  Zone:         [ us-central1-a            v ]                   │
│                                                                 │
│  Machine Type: [ n1-standard-8 (8 vCPUs, 30GB)  v ]            │
│                ┌─────────────────────────────────┐              │
│                │ N1 General Purpose              │              │
│                │   n1-standard-4 (4 vCPUs)      │              │
│                │   n1-standard-8 (8 vCPUs)  <── │              │
│                │   n1-standard-16 (16 vCPUs)    │              │
│                │ G2 GPU (NVIDIA L4)             │              │
│                │   g2-standard-4 (4 vCPUs, 1 L4)│              │
│                │   g2-standard-8 (8 vCPUs, 1 L4)│              │
│                └─────────────────────────────────┘              │
│                                                                 │
│  GPU Type:     [ NVIDIA T4                  v ]                 │
│  GPU Count:    [ 1 ]                                            │
│  Max Devices:  [ 4 ]                                            │
│                                                                 │
│  VM Provisioning Model:                                         │
│                ┌─────────────────────────────────┐              │
│                │ ✓ Standard                      │              │
│                │   VM runs for any duration      │ <──          │
│                ├─────────────────────────────────┤              │
│                │ $ Spot                          │              │
│                │   Up to 60-91% discount         │              │
│                │   VM may be preempted           │              │
│                └─────────────────────────────────┘              │
│                                                                 │
│                              [ Cancel ]  [ Create Host ]        │
└─────────────────────────────────────────────────────────────────┘
```

### Step 2: API Request

When the user clicks "Create Host", the frontend sends a POST request:

```
POST /v2/hosts

{
  "name": "my-test-host",
  "zone": "us-central1-a",
  "machine_type": "n1-standard-8",
  "gpu_type": "nvidia-tesla-t4",
  "gpu_count": 1,
  "max_devices": 4,
  "provisioning_model": "STANDARD"
}
```

**Zone is optional.** Naming a zone (as above) **pins** the host to it: the
zone is used verbatim and automatic zone failover never moves the host.
Omitting `"zone"` lets the server **rank placement** across the enabled zone
catalog, using capacity headroom against synced regional quotas, recent
classified failures (`zone_health` cooldowns), and proximity when a location
preference is given. The chosen zone is a binding and not a pin. If
provisioning later hits a capacity failure there, the host fails over to the
next ranked zone automatically. The host row records the intent as
`requested_zone` (the pinned zone or hint; absent = auto) and `zone_pinned`.

Two optional VM-shape fields: `min_cpu_platform` (e.g. `"Intel Cascade Lake"`,
passed to GCE verbatim; only meaningful on machine families that support it)
and `boot_disk_size_gb` (10-65536; omit for the 400 GB default). Both are
recorded on the host and survive retries, Spot preemption recovery, and zone
failover, so a re-provisioned host always keeps the shape that was requested.
A platform the target zone cannot honor fails the provision with a classified
error, like any other zone-dependent constraint.

For Spot VMs, set `provisioning_model` to `"SPOT"`:

```
POST /v2/hosts

{
  "name": "my-spot-host",
  "zone": "us-central1-a",
  "machine_type": "n1-standard-8",
  "gpu_type": "nvidia-tesla-t4",
  "gpu_count": 1,
  "max_devices": 4,
  "provisioning_model": "SPOT"
}
```

### Step 3: Backend Processing

The Go backend receives the request and processes it through several stages:

```
                    ┌─────────────────────┐
                    │  Receive Request    │
                    └──────────┬──────────┘
                               │
                               v
                    ┌─────────────────────┐
                    │  Validate Machine   │
                    │  Type from Database │
                    └──────────┬──────────┘
                               │
              ┌────────────────┴────────────────┐
              │                                 │
              v                                 v
     ┌─────────────────┐               ┌─────────────────┐
     │ Machine type    │               │ Machine type    │
     │ found & enabled │               │ not found or    │
     │                 │               │ disabled        │
     └────────┬────────┘               └────────┬────────┘
              │                                 │
              v                                 v
     ┌─────────────────┐               ┌─────────────────┐
     │ Apply GPU       │               │ Return 400      │
     │ defaults from   │               │ Bad Request     │
     │ database        │               │                 │
     └────────┬────────┘               └─────────────────┘
              │
              v
     ┌─────────────────┐
     │ Catalog         │  ← Validates zone, machine type, and GPU
     │ Validation      │    availability against cached GCE data
     └────────┬────────┘
              │
     ┌────────┴────────┐
     │                 │
     v                 v
┌──────────┐    ┌─────────────────┐
│ Resources│    │ Return 400 with │
│ available│    │ structured error│
│          │    │ + alternatives  │
└────┬─────┘    └─────────────────┘
     │
     v
     ┌─────────────────┐
     │ Check Quota     │
     │ (if enabled)    │
     └────────┬────────┘
              │
              v
     ┌─────────────────┐
     │ Create Host     │
     │ Record in       │
     │ Spanner         │
     └────────┬────────┘
              │
              v
     ┌─────────────────┐
     │ Start Async     │
     │ GCE Provisioning│
     └────────┬────────┘
              │
              v
     ┌─────────────────┐
     │ Return Host     │
     │ (status:pending)│
     └─────────────────┘
```

### Step 4: GCE Provisioning

The actual VM creation happens asynchronously as a **durable queue task**
(`host.provision`), enqueued in the same transaction that created the host
row and executed by the worker pool with retries, crash recovery, and
idempotent GCE actuation. See
[control-plane.md](../development/control-plane.md).

```
┌──────────────────────────────────────────────────────────────────┐
│                     GCE Provisioning Flow                         │
├──────────────────────────────────────────────────────────────────┤
│                                                                  │
│  1. Update host status to "provisioning"                         │
│                                                                  │
│  2. Build GCE instance configuration:                            │
│     - Machine type (zones/{zone}/machineTypes/{type})           │
│     - Boot disk with Cuttlefish host image                      │
│     - Network interface with optional external IP               │
│     - GPU accelerators (if specified)                           │
│     - Network tags for firewall rules                           │
│     - Labels for resource tracking                              │
│     - Scheduling options (Standard vs Spot)                     │
│                                                                  │
│  3. For Spot VMs, configure scheduling:                          │
│     - ProvisioningModel: "SPOT"                                 │
│     - InstanceTerminationAction: "DELETE"                       │
│     - OnHostMaintenance: "TERMINATE"                            │
│                                                                  │
│  4. Call GCE API to insert instance                             │
│                                                                  │
│  5. Wait for operation to complete                               │
│                                                                  │
│     On a classified CAPACITY failure (stockout, regional quota, │
│     zone/type availability): record the zone in zone_health so  │
│     every pending placement steers away, and, unless the host   │
│     is pinned, move it to the next ranked zone with a fresh     │
│     incarnation before the retry (automatic zone failover)      │
│                                                                  │
│  6. Retrieve instance details (IP addresses)                     │
│                                                                  │
│  7. Update host record with:                                     │
│     - Status: "starting" (promoted to "running" when the Host   │
│       Orchestrator on the VM first answers)                     │
│     - External IP address                                       │
│     - Internal IP address                                       │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
```

**Note on Spot VM Termination:** We use `InstanceTerminationAction: DELETE` so that
preempted VMs are automatically deleted by GCE. This ensures clean state management
and prevents orphaned disks. When a user retries a preempted host, a new VM is
created with a fresh disk.

### Step 5: Status Updates

The frontend polls for status updates until the host reaches a terminal state:

```
Time     Host Status        Frontend Display
─────    ───────────        ────────────────
  0s     pending            "Creating host..."
  5s     provisioning       "Provisioning VM..."
 30s     provisioning       "Provisioning VM..."
 60s     starting           "VM up, Host Orchestrator booting..."
 90s     running            "Host ready" (with IP address)

         OR

 60s     error              "Failed: {error message}"
```

For Spot VMs, an additional status is possible during the host's lifetime:

```
Time     Host Status        Frontend Display
─────    ───────────        ────────────────
  ...    running            "Host ready" (Spot VM indicator)
 2hrs    preempted          "Host preempted - click Retry to recreate"
```

## GCE Resource Catalog

The orchestrator maintains a local cache of GCE resource availability to enable
pre-creation validation and provide actionable error messages with alternative
zone suggestions.

### Zone Availability

Before creating a host, the frontend can query zone availability to show users
which zones support their desired configuration:

```
GET /v2/zones/availability?machine_type=n1-standard-8&gpu_type=nvidia-tesla-t4

Response:
{
  "zones": [
    {
      "zone": "us-east4-a",
      "region": "us-east4",
      "display_name": "Virginia (A)",
      "status": "available",
      "is_enabled": true,
      "machine_type_available": true,
      "gpu_available": true,
      "gpu_max_count": 4
    },
    {
      "zone": "us-central1-a",
      "region": "us-central1",
      "display_name": "Iowa (A)",
      "status": "available",
      "is_enabled": true,
      "machine_type_available": true,
      "gpu_available": false
    }
  ]
}
```

### Zone Status Indicators

The frontend displays zone availability with visual indicators:

```
┌─────────────────────────────────────────────────────────────────┐
│  Zone Selection                                                  │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ● Virginia (us-east4-a)                  4 GPUs               │
│  ● Iowa (us-central1-a)                  ⚠ GPU not available   │
│  ● Taiwan (asia-east1-a)                 - Unavailable         │
│                                                                 │
│  Legend:  ● Available  ● Degraded  ● Unavailable              │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Pre-Creation Validation

When a host creation request is received, the backend validates the configuration
against the cached catalog before attempting to create the GCE instance:

```
                    ┌─────────────────────┐
                    │  Receive Request    │
                    └──────────┬──────────┘
                               │
                               v
                    ┌─────────────────────┐
                    │  Catalog Validation │
                    │  (Pre-creation)     │
                    └──────────┬──────────┘
                               │
              ┌────────────────┴────────────────┐
              │                                 │
              v                                 v
     ┌─────────────────┐               ┌─────────────────┐
     │ Zone & resources│               │ Validation      │
     │ available       │               │ failed          │
     │                 │               │                 │
     └────────┬────────┘               └────────┬────────┘
              │                                 │
              v                                 v
     ┌─────────────────┐               ┌─────────────────┐
     │ Proceed with    │               │ Return 400 with │
     │ GCE API call    │               │ structured error│
     │                 │               │ + alternatives  │
     └─────────────────┘               └─────────────────┘
```

### Structured Error Response

When validation fails, the API returns actionable information:

```json
{
  "type": "about:blank",
  "title": "Bad Request",
  "status": 400,
  "detail": "GPU nvidia-tesla-t4 is not available in zone us-central1-a",
  "error_type": "resource_unavailable",
  "suggestion": "Try creating the host in a different zone where this GPU is available",
  "alternative_zones": ["us-east4-a", "us-east4-b", "us-central1-b"],
  "retryable": true
}
```

The frontend displays this as an actionable error card:

```
┌─────────────────────────────────────────────────────────────────┐
│  ⚠ Resource Unavailable                                        │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  GPU nvidia-tesla-t4 is not available in zone us-central1-a    │
│                                                                 │
│  Try creating the host in a different zone where this           │
│  GPU is available                                               │
│                                                                 │
│  Available in these zones:                                      │
│  ┌────────────────┐ ┌────────────────┐ ┌────────────────┐      │
│  │ us-east4-a     │ │ us-east4-b     │ │ us-central1-b  │      │
│  └────────────────┘ └────────────────┘ └────────────────┘      │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Error Types

Provisioning failures are classified (`internal/platform/gce`) so recovery
can react to each one. The quota classes are split by the metric that bound,
and the capacity/availability classes are what trigger automatic zone
failover on unpinned hosts:

| Error Type | Meaning |
|------------|---------|
| `resource_exhausted` | Zone capacity exhausted (stockout) |
| `quota_cpu_exceeded` / `quota_gpu_exceeded` / `quota_ip_exceeded` / `quota_ssd_exceeded` / `quota_instances_exceeded` | Regional GCE quota bound, by metric |
| `zone_not_found` / `zone_down` | Zone missing or temporarily unavailable |
| `machine_type_not_in_zone` / `gpu_not_in_zone` | Shape not offered in the zone |
| `gpu_count_exceeded` / `gpu_not_attachable` | Invalid GPU configuration |
| `permission_denied` | IAM permissions insufficient |
| `resource_not_ready` | Transient (rate limit, timeout, 5xx); retried |
| `image_not_found` / `network_not_found` / `subnet_not_found` | Configuration references missing resources |
| `unknown` | Unclassified |

Admission-time catalog validation uses its own related code set
(`zone_disabled`, `machine_type_invalid`, `spot_quota_cpu_exceeded`, …)
returned in the structured 400/422 body with ranked alternative zones.

### The catalog snapshot

What Compute Engine offers is held in memory, per instance, and never stored.
Two background refreshers rebuild an immutable snapshot and swap it in:

```
┌─────────────────────────────────────────────────────────────────┐
│                    Catalog snapshot                              │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  Plane              │ Refresh    │ API source                    │
│  ───────────────────┼────────────┼─────────────────────────────  │
│  Zones              │ 6 hours    │ compute.zones.list            │
│  Machine types      │ 6 hours    │ machineTypes.aggregatedList   │
│  GPU accelerators   │ 6 hours    │ acceleratorTypes.aggregated   │
│  Regional quotas    │ 60 seconds │ compute.regions.list          │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

The structural calls are aggregated and server-side filtered to the curated
machine types, so one pass answers for every zone Google offers at the same
cost as one zone. The quota call is narrowed to the regions holding an enabled
zone.

Absence never removes a placement candidate. A zone Compute Engine did not
report is not a zone that offers nothing: only a positive "reported, and did
not list it" excludes a zone from ranking, and a cold instance with no
snapshot yet ranks every enabled zone. Being wrong permissively costs one
doomed create that returns a clear GCE error and is remembered in
`zone_health`; being wrong restrictively leaves the fleet unable to provision
with nothing to explain it.

`cfctl admin catalog-freshness` reports when the snapshot was built and which
scopes Compute Engine did not report.

### Which zones may take a host

Zone enablement is the operator's decision and the one durable half of the
zone catalog: a `zones.enabled` key in `system_config` holding the zone names,
read whole. It is the placement space and the regional-network guard in one:
subnets are regional, so ranking never wanders past it.

```bash
cfctl admin zone list                        # every zone Compute Engine offers
cfctl admin zone enable us-central1-a        # add one to the placement space
cfctl admin zone disable us-central1-a       # remove it
```

Writes are validated against the snapshot: a zone Compute Engine does not
offer this project is refused, because storing it would leave an operator with
a zone that looks admitted and silently ranks nothing. Before the first
refresh lands there is nothing to validate against, so any well-formed zone
name is accepted, because an empty deployment has to be configurable.

The image family host provisioning boots is not part of this: it comes from
`host_image_configs` per machine family, resolved in the project set by
`CCO_HOST_IMAGE_PROJECT` (see How Image Resolution Works).

## Database Schema

### Machine Types Table

The machine_types table stores all available GCE machine configurations:

```
┌─────────────────────────────────────────────────────────────────┐
│                      machine_types                               │
├──────────────────┬───────────────┬──────────────────────────────┤
│ Column           │ Type          │ Description                   │
├──────────────────┼───────────────┼──────────────────────────────┤
│ id               │ STRING(100)   │ Machine type ID (primary key)│
│ family           │ STRING(50)    │ Family (n1, g2, c4, etc.)    │
│ name             │ STRING(255)   │ Display name                  │
│ description      │ STRING(1000)  │ Detailed description          │
│ category         │ STRING(50)    │ Workload category             │
│ architecture     │ STRING(20)    │ x86 or arm                    │
│ cpu_type         │ STRING(255)   │ CPU description               │
│ vcpus            │ INT64         │ Number of vCPUs               │
│ memory_gb        │ INT64         │ Memory in GB                  │
│ gpu_support      │ BOOL          │ Can attach GPUs?              │
│ max_gpus         │ INT64         │ Maximum GPU count             │
│ default_gpu_type │ STRING(100)   │ Default GPU type              │
│ default_gpu_count│ INT64         │ Default GPU count             │
│ is_default       │ BOOL          │ Default machine type?         │
│ is_enabled       │ BOOL          │ Available for use?            │
│ sort_order       │ INT64         │ Display order                 │
│ zones            │ ARRAY<STRING> │ Available zones (null=all)    │
└──────────────────┴───────────────┴──────────────────────────────┘
```

### Host Image Configs Table

The host_image_configs table maps machine families to GCE image families:

```
┌─────────────────────────────────────────────────────────────────┐
│                    host_image_configs                            │
├──────────────────┬───────────────┬──────────────────────────────┤
│ Column           │ Type          │ Description                   │
├──────────────────┼───────────────┼──────────────────────────────┤
│ machine_family   │ STRING(50)    │ Family prefix (primary key)  │
│ image_family     │ STRING(100)   │ GCE image family name        │
│ source_project   │ STRING(100)   │ Project override (optional)  │
│ description      │ STRING(500)   │ Configuration description    │
│ enabled          │ BOOL          │ Is this config active?       │
│ created_at       │ TIMESTAMP     │ When created                 │
│ updated_at       │ TIMESTAMP     │ Last modification            │
│ created_by       │ STRING(255)   │ Creator email                │
│ updated_by       │ STRING(255)   │ Last modifier email          │
└──────────────────┴───────────────┴──────────────────────────────┘
```

There is no stored default image project. A row with no `source_project`
resolves in the deployment's `CCO_HOST_IMAGE_PROJECT`.

### Hosts Table

The hosts table tracks all provisioned host VMs:

```
┌─────────────────────────────────────────────────────────────────┐
│                          hosts                                   │
├──────────────────────────┬───────────────┬──────────────────────┤
│ Column                   │ Type          │ Description          │
├──────────────────────────┼───────────────┼──────────────────────┤
│ host_id                  │ STRING(36)    │ UUID (primary key)   │
│ name                     │ STRING(255)   │ User-provided name   │
│ zone                     │ STRING(100)   │ GCE zone (binding)   │
│ requested_zone           │ STRING(64)    │ Placement intent     │
│ zone_pinned              │ BOOL          │ Never failed over    │
│ gce_instance_name        │ STRING(255)   │ GCE VM name          │
│ gce_project              │ STRING(100)   │ GCP project ID       │
│ machine_type             │ STRING(100)   │ Machine type ID      │
│ gpu_type                 │ STRING(100)   │ GPU accelerator type │
│ gpu_count                │ INT64         │ Number of GPUs       │
│ min_cpu_platform         │ STRING(100)   │ CPU platform floor   │
│ boot_disk_size_gb        │ INT64         │ NULL = 400 default   │
│ status                   │ STRING(30)    │ Current status       │
│ provisioning_model       │ STRING(20)    │ STANDARD or SPOT     │
│ ip_address               │ STRING(45)    │ External IP          │
│ internal_ip              │ STRING(45)    │ Internal IP          │
│ host_orchestrator_port   │ INT64         │ HO service port      │
│ max_devices              │ INT64         │ Instance capacity    │
│ current_device_count     │ INT64         │ Instances in use     │
│ created_by               │ STRING(320)   │ Creator email (info) │
│ error_message            │ STRING(1000)  │ Error details        │
│ error_type               │ STRING(50)    │ Classified error type│
│ error_code               │ STRING(100)   │ GCE error code       │
│ error_suggestion         │ STRING(1000)  │ Actionable suggestion│
│ error_alternative_zones  │ ARRAY<STRING> │ Available zones      │
│ error_retryable          │ BOOL          │ Can retry operation  │
│ error_occurred_at        │ TIMESTAMP     │ When error occurred  │
│ preempted_at             │ TIMESTAMP     │ Last preemption time │
│ preemption_count         │ INT64         │ Total preemptions    │
│ lifecycle_mode           │ STRING(20)    │ ephemeral/protected  │
│ ttl_seconds              │ INT64         │ Host lease length    │
│ expires_at               │ TIMESTAMP     │ Auto-deletion time   │
│ protected_device_count   │ INT64         │ Pinned devices held  │
└──────────────────────────┴───────────────┴──────────────────────┘
```

**Spot VM Columns:**
- `provisioning_model`: Either "STANDARD" (default) or "SPOT"
- `preempted_at`: Timestamp of the most recent preemption event
- `preemption_count`: Running total of how many times this host has been preempted

## Host Lifecycle States

A host progresses through the following states:

```
                    ┌─────────┐
                    │ pending │
                    └────┬────┘
                         │
                         v
               ┌──────────────────┐
               │  provisioning    │◄─────────────────┐
               └────────┬─────────┘                  │
                        │                            │
           ┌────────────┴────────────┐               │
           │                         │               │
           v                         v               │
    ┌───────────┐             ┌───────────┐          │
    │  running  │             │   error   │──────────┤ (retry)
    └─────┬─────┘             └───────────┘          │
          │                                          │
    ┌─────┼─────────────────────┐                    │
    │     │                     │                    │
    v     v                     v                    │
┌────────┐ ┌────────┐    ┌───────────┐              │
│stopping│ │starting│    │ preempted │──────────────┘
└───┬────┘ └───┬────┘    └─────┬─────┘ (Spot VMs only)
    │          │               │
    v          │               v
┌────────┐     │          ┌────────┐
│stopped │─────┘          │deleting│ (user chooses delete)
└───┬────┘                └───┬────┘
    │                         │
    v                         v
┌────────┐                ┌────────┐
│deleting│                │deleted │
└───┬────┘                └────────┘
    │
    v
┌────────┐
│deleted │
└────────┘
```

### Preemption (Spot VMs Only)

Spot VMs can transition to the `preempted` state at any time while running.
This occurs when GCE reclaims the capacity for higher-priority workloads.

```
┌─────────────────────────────────────────────────────────────────┐
│                    Preemption Detection                          │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  The hostreconciler background job monitors GCE Operations API │
│  for preemption events every sync cycle (default: 1 minute).    │
│                                                                 │
│  Detection method:                                              │
│    - Query: operationType="compute.instances.preempted"        │
│    - Filter: Operations within the last sync window            │
│    - Match: Instance name to host record                       │
│                                                                 │
│  When detected:                                                 │
│    1. Host status → "preempted"                                │
│    2. preempted_at timestamp is recorded                       │
│    3. preemption_count is incremented                          │
│    4. The host is automatically re-provisioned (bounded per    │
│       host and suspended zone-wide during a mass reclaim);     │
│       past the recovery budget it stays preempted for a        │
│       manual retry                                             │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Retry After Preemption

Users can retry a preempted host using the retry endpoint:

```
POST /v2/hosts/{id}/retry

{
  "fallback_to_standard": false
}
```

Options:
- `fallback_to_standard: false` - Retry as Spot VM (may be preempted again)
- `fallback_to_standard: true` - Retry as Standard VM (guaranteed capacity)

## Error Handling

### Pre-Creation Validation Errors

With the GCE Resource Catalog, many errors are caught before the GCE API is called:

```
┌─────────────────────────────────────────────────────────────────┐
│ Scenario              │ Error Response                          │
├───────────────────────┼─────────────────────────────────────────┤
│ GPU not in zone       │ 400 Bad Request                         │
│ (pre-validated)       │ error_type: "resource_unavailable"      │
│                       │ alternative_zones: ["zone-a", "zone-b"] │
├───────────────────────┼─────────────────────────────────────────┤
│ Machine type not      │ 400 Bad Request                         │
│ in zone               │ error_type: "resource_unavailable"      │
│                       │ suggestion: "Try a different zone"      │
├───────────────────────┼─────────────────────────────────────────┤
│ Zone disabled         │ 400 Bad Request                         │
│                       │ error_type: "zone_unavailable"          │
│                       │ alternative_zones: [enabled zones]      │
├───────────────────────┼─────────────────────────────────────────┤
│ GCE quota exceeded    │ 400 Bad Request                         │
│                       │ error_type: "quota_exceeded"            │
│                       │ suggestion: "Request quota increase"    │
└───────────────────────┴─────────────────────────────────────────┘
```

### Runtime Errors

Some errors can only be detected during GCE provisioning:

```
┌─────────────────────────────────────────────────────────────────┐
│ Scenario              │ Error Response                          │
├───────────────────────┼─────────────────────────────────────────┤
│ Invalid machine type  │ 400 Bad Request                         │
│                       │ "machine type 'xyz' is not enabled"     │
├───────────────────────┼─────────────────────────────────────────┤
│ Platform quota        │ 429 Too Many Requests                   │
│ exceeded              │ "host quota exceeded: requested N,      │
│                       │  available X/Y"                         │
├───────────────────────┼─────────────────────────────────────────┤
│ GCE API failure       │ Host created, status=error              │
│                       │ error_type: "internal_error"            │
│                       │ retryable: true                         │
├───────────────────────┼─────────────────────────────────────────┤
│ Image not found       │ Host created, status=error              │
│                       │ error_type: "configuration_error"       │
│                       │ "source image not found"                │
└───────────────────────┴─────────────────────────────────────────┘
```

### Host Error Details

When a host enters the `error` state, detailed error information is stored:

```
┌─────────────────────────────────────────────────────────────────┐
│                    Host Error Fields                             │
├──────────────────────┬──────────────────────────────────────────┤
│ error_message        │ Human-readable error description         │
│ error_type           │ Classified error type                    │
│ error_code           │ GCE error code (if applicable)           │
│ error_suggestion     │ Actionable fix suggestion                │
│ error_alternative_zones │ Zones where resource is available     │
│ error_retryable      │ Whether retry might succeed              │
│ error_occurred_at    │ Timestamp of the error                   │
└──────────────────────┴──────────────────────────────────────────┘
```

### Quota Management

The platform tracks host counts against a single global limit
(Scope: "platform", ScopeID: "global"). `used_hosts` is incremented
atomically in the host-create transaction and decremented when a host is
deleted or marked lost; exceeding `max_hosts` returns 429. Declarative
device creates that auto-provision hosts consume the same quota, making it
the fleet-size governor for CI.

## Host Image Configuration

The orchestrator uses a database-driven approach to determine which GCE image family
is used when provisioning host VMs for each machine type. This allows platform
administrators to update image mappings without requiring backend redeployment.

### How Image Resolution Works

Host images are built outside CCO with the upstream android-cuttlefish
`tools/baseimage` tooling and published as a GCE image family (see
[building-host-images.md](building-host-images.md)). CCO only finds, checks
and boots them.

The project image families resolve in is one deployment value,
`CCO_HOST_IMAGE_PROJECT`. Terraform sets it from `host_image_project`, which
defaults to the deployment's own project; when it names another project,
Terraform grants the orchestrator service account `roles/compute.imageUser`
there (this works only where the operator holds IAM in that project). A
machine family's `source_project` overrides it for that family alone.

The seeded rows map every machine family to the image family `cco-host`.
Resolution checks the image's architecture against the machine family's, so
`c4a` (arm64) is refused until you build an arm64 image and point its row at
that image family with `cfctl admin host-image update c4a --image-family
FAMILY`. Disable the rows you do not serve.

When a host is created, the system resolves the image from the machine type's
family prefix:

```
┌─────────────────────────────────────────────────────────────────┐
│                    Image Resolution Flow                         │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  1. Extract machine family from machine type                    │
│     • "n1-standard-8" → "n1"                                   │
│     • "g2-standard-4" → "g2"                                   │
│     • "c4a-standard-8" → "c4a"                                 │
│     • "custom-4-16384" → "n1" (legacy custom types)            │
│                                                                 │
│  2. Look up image configuration in database                     │
│     • Table: host_image_configs                                 │
│     • Key: machine_family                                       │
│                                                                 │
│  3. Resolve source project                                      │
│     • Use config's source_project if specified                 │
│     • Otherwise use CCO_HOST_IMAGE_PROJECT                     │
│                                                                 │
│  4. Read the image the family points at, and check that its     │
│     architecture matches the machine family (arm64 for c4a)     │
│                                                                 │
│  5. The provision task resolves again and inserts that image    │
│     by name; the host records it as boot_image                  │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Image Configuration Table

Each machine family maps to a GCE image family. The seeded rows:

| Machine family | Image family | Architecture |
|---|---|---|
| n1, n2, n4 | `cco-host` | x86 |
| c3, c4 | `cco-host` | x86 |
| g2 | `cco-host` | x86 |
| c4a | `cco-host` (refused until mapped to an arm64 family) | arm64 (bare metal only) |

### Managing Image Configurations

Platform administrators can manage image configurations through:

1. **Web UI**: Navigate to Admin > Host Images
2. **API**: Use the host image config endpoints (see Related Endpoints)

Common operations:
- View all configured machine families, their image mappings and the image
  each one resolves to now (or why it resolves to none)
- Update the image family for a specific machine family
- Set a custom source project for specific machine families
- Enable/disable configurations

`cfctl doctor` runs the same check for every enabled family and fails when one
of them cannot boot.

### Refused creates

A family with no enabled row, an image family with no image in its project, an
image the orchestrator may not read, and an x86 image behind an arm64 family
are all refused when the host is created, before any row or task is written.
`/v2/hosts` and `/v1` answer 422 with a message naming the machine family, the
image family, the project and the setting that fixes it: `CCO_HOST_IMAGE_PROJECT`,
`cfctl admin host-image update FAMILY --source-project PROJECT` or
`--image-family`, or creating the family's row. A provision that later finds
no image fails the host at once instead of retrying.

### Caching

To minimize database load, the image resolver uses a per-replica cache:

```
┌─────────────────────────────────────────────────────────────────┐
│                    Image Config Cache                            │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  Cache TTL:        90 seconds, successful resolutions only      │
│  Cache Scope:      Per replica (not shared)                     │
│  Invalidation:     TTL expiry, and on write in the replica      │
│                    that served the write                        │
│  Thundering Herd:  Prevented via singleflight                   │
│                                                                 │
│  A refusal is never cached, so publishing a missing image is    │
│  seen by the next create on every replica. Moving a working     │
│  family to another working image can take up to 90 seconds.     │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Semantic Validation

When creating or updating an enabled image configuration, the system resolves
it the same way a host create does (`compute.images.getFromFamily` in the
effective project, then the architecture check) and refuses the write with
422 when it cannot boot. A disabled row is stored unchecked.

## GCE Instance Configuration

When a host is provisioned, the following GCE configuration is applied:

```
┌─────────────────────────────────────────────────────────────────┐
│                    GCE Instance Details                          │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  Instance Name:    cf-{name}-{uuid-prefix}[-i{n}]               │
│                    Example: cf-my-test-host-a1b2c3d4            │
│                    Re-provisions (retry, preemption recovery,   │
│                    zone failover) append -i1, -i2, … so each    │
│                    attempt targets a fresh instance             │
│                                                                 │
│  Boot Disk:                                                     │
│    - Image: Resolved from host_image_configs table              │
│    - Size: 400 GB                                               │
│    - Auto-delete: Yes                                           │
│                                                                 │
│  Network:                                                       │
│    - VPC network and subnetwork from configuration             │
│    - External IP: Optional (based on config)                   │
│                                                                 │
│  GPU (if specified):                                            │
│    - Accelerator type: nvidia-tesla-t4, nvidia-l4,             │
│      nvidia-rtx-pro-6000, nvidia-a100-40gb, etc.               │
│    - On-host maintenance: TERMINATE (required for GPU)         │
│                                                                 │
│  Scheduling (Standard VM):                                      │
│    - OnHostMaintenance: TERMINATE                               │
│                                                                 │
│  Scheduling (Spot VM):                                          │
│    - ProvisioningModel: SPOT                                    │
│    - InstanceTerminationAction: DELETE                          │
│    - OnHostMaintenance: TERMINATE                               │
│                                                                 │
│  Tags:                                                          │
│    - cuttlefish-host                                            │
│    - cuttlefish-webrtc                                          │
│                                                                 │
│  Labels:                                                        │
│    - managed-by: cloud-cuttlefish-orchestrator                 │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

## Placement Policy

Server-side zone ranking is governed by the enabled zone catalog plus three
operator policies (all runtime-settable, no redeploy):

- **The enabled catalog is the placement space.** Ranking only ever
  considers zones enabled under **Administration → Capacity**, which
  doubles as the network guard, since VPC subnets are regional. Curate it to
  your networked region(s); zones the resource sync discovers start disabled
  and join the placement space only when enabled here.
- **`placement.zone_pin`** (`/v2/admin/system-config/zone-pin`) restricts
  all ranked placement to a single zone (the small-deployment operating
  mode). Empty = rank across the whole enabled catalog.
- **`placement.quota_margin_percent`** (`…/quota-margin`, default 10) is the
  fraction of each regional quota limit ranking keeps in reserve when
  judging headroom.
- **`placement.v1_zone_is_hint`** (`…/v1-zone-hint`, default true) sets
  whether the legacy `/v1` zone token is a placement hint or a literal pin (see
  [Legacy API](../reference/legacy-api.md)).

The Capacity page's **zone board** shows all of this live: one tile per zone
carrying a server-composed verdict, the machines and devices it holds, its Spot
advice, and the regional quota headroom behind the verdict, which are the
same signals ranking places by. `cfctl admin capacity` prints the same board.

Failovers are audited as `host.zone_failover` and counted by the
`cco.placement.*` metrics; active cooldowns are visible as
`cco.zone_health.cooldowns_active`.

## Related Endpoints

### Host Management

| Method | Path | Description |
|--------|------|-------------|
| POST | /v2/hosts | Create a new host |
| GET | /v2/hosts | List all hosts |
| GET | /v2/hosts/{id} | Get host details |
| DELETE | /v2/hosts/{id} | Delete a host |
| POST | /v2/hosts/{id}/start | Start a stopped host |
| POST | /v2/hosts/{id}/stop | Stop a running host |
| POST | /v2/hosts/{id}/sync | Sync host state with GCE |
| POST | /v2/hosts/{id}/retry | Retry a preempted/failed host |

### Machine Types

| Method | Path | Description |
|--------|------|-------------|
| GET | /v2/machine-types | List available machine types |
| GET | /v2/machine-types/{id} | Get specific machine type |
| GET | /v2/machine-types/default | Get default machine type |
| GET | /v2/machine-families | List machine families |

### GCE Resource Catalog

| Method | Path | Description |
|--------|------|-------------|
| GET | /v2/zones/availability | List zone availability with resource info |

### Host Image Configuration (Platform Admin)

| Method | Path | Description |
|--------|------|-------------|
| GET | /v2/admin/host-image-configs | List all image configurations |
| GET | /v2/admin/host-image-configs/{family} | Get config for a machine family |
| POST | /v2/admin/host-image-configs | Create a new image configuration |
| PUT | /v2/admin/host-image-configs/{family} | Update an image configuration |
| DELETE | /v2/admin/host-image-configs/{family} | Delete an image configuration |

### Placement Policy (Platform Admin)

| Method | Path | Description |
|--------|------|-------------|
| GET/PUT | /v2/admin/system-config/zone-pin | Restrict ranked placement to one zone |
| GET/PUT | /v2/admin/system-config/quota-margin | Quota reserve margin for ranking |
| GET/PUT | /v2/admin/system-config/v1-zone-hint | Legacy /v1 zone-token mode |
