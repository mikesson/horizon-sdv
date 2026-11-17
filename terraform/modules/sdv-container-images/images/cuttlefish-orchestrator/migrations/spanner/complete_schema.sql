-- Copyright 2026 Google LLC
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- You may obtain a copy of the License at
--
--     https://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing, software
-- distributed under the License is distributed on an "AS IS" BASIS,
-- WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
-- See the License for the specific language governing permissions and
-- limitations under the License.

-- Cloud Cuttlefish Orchestrator - Complete Schema v87 (single-tenant)
--
-- The schema and seed data of a new database. Terraform applies this file once,
-- through scripts/provision/spanner-seed.sh, when it creates the database. An
-- existing database is brought to this version with the files in
-- migrations/spanner/upgrades/. The binary refuses to start against a database
-- whose schema.version row is older than its own.
--
-- created_by, pinned_by and actor_email record the caller's verified email for
-- display and audit. They grant nothing. Every authenticated caller has full
-- access.

-- =============================================================================
-- System Config
-- =============================================================================
-- Platform-wide settings, one row per key.
CREATE TABLE system_config (
    config_key STRING(100) NOT NULL,
    config_value STRING(MAX) NOT NULL,
    description STRING(500),
    created_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    updated_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (config_key);

-- =============================================================================
-- Hosts
-- =============================================================================
CREATE TABLE hosts (
    host_id STRING(36) NOT NULL DEFAULT (GENERATE_UUID()),
    -- The operator's display label. gce_instance_name is derived from it. The
    -- length matches the create request's limit.
    name STRING(63) NOT NULL,
    zone STRING(100) NOT NULL,
    gce_instance_name STRING(255),
    gce_project STRING(100),
    machine_type STRING(100),
    gpu_type STRING(100),
    gpu_count INT64 NOT NULL DEFAULT (1),
    -- Provisioning spec, applied again whenever the VM is rebuilt (retry,
    -- preemption recovery, zone failover). NULL means no CPU platform
    -- constraint and the default disk size.
    min_cpu_platform STRING(100),
    boot_disk_size_gb INT64,
    -- Self link of the image the current VM was created from, resolved from
    -- the machine family's image family at provision time. NULL until the
    -- first provision attempt.
    boot_image STRING(MAX),
    provisioning_model STRING(20) DEFAULT ('STANDARD'),
    status STRING(30) NOT NULL DEFAULT ('pending'),
    ip_address STRING(45),
    internal_ip STRING(45),
    host_orchestrator_port INT64 NOT NULL DEFAULT (2080),
    max_devices INT64 NOT NULL DEFAULT (4),
    current_device_count INT64 NOT NULL DEFAULT (0),
    created_by STRING(320),
    error_message STRING(1000),
    -- Structured error fields.
    error_type STRING(50),
    error_code STRING(100),
    error_suggestion STRING(1000),
    error_alternative_zones ARRAY<STRING(50)>,
    error_retryable BOOL,
    error_occurred_at TIMESTAMP,
    stopped_at TIMESTAMP,
    -- When GCE will stop the VM under its max_run_duration, read by the
    -- reconciler from the instance listing. NULL when not yet observed or when
    -- the VM is not running. A lease extension may not pass it.
    vm_terminates_at TIMESTAMP,
    -- Spot preemption tracking. last_preemption_id dedupes the preemption
    -- scan, so a preemption read twice does not preempt a recovered host again.
    preempted_at TIMESTAMP,
    -- The incarnation with an unrecovered preemption. The host's phase is
    -- 'preempted' while this equals incarnation. Recovery increments
    -- incarnation, which ends it.
    preempted_incarnation INT64,
    preemption_count INT64 NOT NULL DEFAULT (0),
    last_preemption_id STRING(64),
    -- Host poisoning. consecutive_create_fails counts device creates that
    -- failed with a poison signature (leaked taps, a stale device ID
    -- registration). At a threshold, degraded_at fences the host from
    -- placement and queues remediation while status stays running. A
    -- successful create clears both.
    consecutive_create_fails INT64 NOT NULL DEFAULT (0),
    last_create_fail_reason STRING(500),
    degraded_at TIMESTAMP,
    degraded_reason STRING(500),
    -- Set when GCE refuses this host's provision for capacity (a stockout, an
    -- exhausted quota, a zone outage). While set, automatic placement and the
    -- standby count skip the host unless its VM already exists. A successful
    -- provision or a retry clears it.
    provision_blocked_at TIMESTAMP,
    provision_blocked_reason STRING(500),
    -- Rebuild counter. The GCE instance name embeds it, so a rebuild always
    -- creates a new instance.
    incarnation INT64 NOT NULL DEFAULT (0),
    -- Spec: the state the user asked for (running, stopped or absent). Only
    -- intent paths write it. The phase derivation reads it.
    desired_state STRING(16),
    -- Observation: the raw GCE instance state, recorded even while an
    -- application-owned status holds the lifecycle status. Written by the
    -- reconciler's drift sweep, the provision read-back and a manual sync.
    vm_status STRING(32),
    vm_observed_at TIMESTAMP,
    -- Set while the host is stuck converging toward desired_state: a teardown
    -- whose delete keeps failing, or a recovery that finds no capacity.
    -- Independent of the derived phase.
    stalled_reason STRING(500),
    -- When the Host Orchestrator first answered on the current VM. A RUNNING
    -- VM derives 'starting' while it is NULL and 'running' once it is set. It
    -- is reset only for a new VM, so a later Host Orchestrator outage degrades
    -- device health and leaves the host phase alone.
    ho_ready_at TIMESTAMP,
    -- Placement intent, fixed at admission. requested_zone is the zone or
    -- region the caller gave, NULL for server-ranked placement. zone_pinned
    -- means the caller named an exact zone, which placement uses as given and
    -- failover never leaves. zone holds the current physical zone. Failover
    -- re-ranks from this pair, and /v1 lists hosts by it.
    requested_zone STRING(64),
    zone_pinned BOOL NOT NULL DEFAULT (FALSE),
    -- Automatic placement (declarative creates, batch packing) skips an
    -- exclusive host. A create that targets the host by ID ignores the flag.
    exclusive BOOL NOT NULL DEFAULT (FALSE),
    -- Set only by the /v1 docker-shaped create. That host's CVDs arrive through
    -- the Host Orchestrator proxy with no device rows, so the orphan-CVD census
    -- skips it.
    foreign_cvds BOOL NOT NULL DEFAULT (FALSE),
    -- The batch that provisioned this host, NULL otherwise. The reaper
    -- reclaims an emptied batch host before its TTL.
    batch_id STRING(36),
    -- Which path created the host: 'user' (/v2 or /v1), 'auto' (a declarative
    -- create that found no capacity), 'pool' (a fleet-keeper top-up) or
    -- 'batch'. Set at create and never changed. Together with exclusive and
    -- batch_id it derives the host's class, which decides whether the fleet
    -- may count the host as inventory or reclaim it. NULL derives the reserved
    -- class, which is never counted and never reclaimed.
    origin STRING(16),
    -- The fleet pool the host belongs to, set at create. Each pool has its own
    -- machine shapes, floor, standby buffer and capacity model. The default
    -- pool is stored as NULL. Reads map NULL to its name and writes map the
    -- name back to NULL.
    pool STRING(64),
    -- Lifecycle columns.
    lifecycle_mode STRING(20) DEFAULT ('ephemeral'),
    ttl_seconds INT64 DEFAULT (36000),
    expires_at TIMESTAMP,
    protected_device_count INT64 DEFAULT (0),
    created_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    updated_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    -- Set when the row becomes a tombstone (status deleted). It arms the row
    -- deletion policy, which also removes the interleaved device rows.
    tombstoned_at TIMESTAMP,
    -- name while the host is not deleted, NULL once it is. The list's name
    -- filter reads idx_hosts_live_name, so it never matches a deleted host.
    live_name STRING(63) AS (IF(status != 'deleted', name, NULL)) STORED,
    -- The display-name shape, enforced here so every writer is held to it. The
    -- reserved "cf-" prefix is checked by host.ValidateDisplayName, which can
    -- explain the refusal.
    CONSTRAINT chk_hosts_name_shape CHECK (REGEXP_CONTAINS(name, r'^[a-z][a-z0-9-]*$')),
) PRIMARY KEY (host_id),
  ROW DELETION POLICY (OLDER_THAN(tombstoned_at, INTERVAL 7 DAY));

CREATE INDEX idx_hosts_zone ON hosts(zone);
CREATE INDEX idx_hosts_status ON hosts(status);
CREATE INDEX idx_hosts_gce_instance ON hosts(gce_project, gce_instance_name);
CREATE INDEX idx_hosts_lifecycle_expires ON hosts(lifecycle_mode, expires_at, protected_device_count, status);
CREATE INDEX idx_hosts_created_at ON hosts(created_at DESC);
CREATE INDEX idx_hosts_provisioning_model ON hosts(provisioning_model);
-- Holds only live hosts. Backs the list's name filter.
CREATE NULL_FILTERED INDEX idx_hosts_live_name ON hosts(live_name);

-- =============================================================================
-- Devices
-- =============================================================================
CREATE TABLE devices (
    host_id STRING(36) NOT NULL,
    device_id STRING(36) NOT NULL DEFAULT (GENERATE_UUID()),
    -- The operator's display label. cvd_id is derived from it.
    name STRING(63) NOT NULL,
    cvd_id STRING(50),
    status STRING(30) NOT NULL DEFAULT ('pending'),
    build_id STRING(255),
    build_target STRING(255),
    adb_port INT64,
    webrtc_port INT64,
    gpu_mode STRING(50) NOT NULL DEFAULT ('auto'),
    created_by STRING(320),
    error_message STRING(1000),
    started_at TIMESTAMP,
    stopped_at TIMESTAMP,
    -- Labels set at create, for display and search.
    tags ARRAY<STRING(64)>,
    created_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    updated_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    -- Health columns.
    health_status STRING(30) DEFAULT ('unknown'),
    last_health_check TIMESTAMP,
    consecutive_fails INT64 DEFAULT (0),
    last_failure_reason STRING(500),
    last_healthy_at TIMESTAMP,
    -- Observation: the raw CVD status last seen on the host (Running,
    -- Unreachable, Boot failed, and so on). Written by the device controller,
    -- and during the create window by the create-wait sampler. The phase
    -- derivation reads it.
    cvd_status STRING(32),
    -- Observation: boot progress sampled from the CVD's logs, as opaque JSON.
    -- Written in the same mutation as cvd_status, so the derivation never pairs
    -- a new status with an old sample.
    boot_observation STRING(MAX),
    -- Observation: the instances the Host Orchestrator reports for the
    -- device's group, as opaque JSON. Written from the create response and
    -- refreshed by the device controller. NULL until the Host Orchestrator
    -- answers. The desired count is desired_spec's instance_count.
    instances STRING(MAX),
    -- Lifecycle columns.
    lifecycle_mode STRING(20) DEFAULT ('ephemeral'),
    ttl_seconds INT64 DEFAULT (14400),
    expires_at TIMESTAMP,
    pinned_at TIMESTAMP,
    pinned_by STRING(320),
    pinned_reason STRING(50),
    pinned_reason_detail STRING(500),
    pinned_until TIMESTAMP,
    extend_count INT64 DEFAULT (0),
    -- The create-time spec (env_config, mode, instance_count) as opaque JSON,
    -- so device.reconcile can rebuild a stranded create from the row.
    desired_spec STRING(MAX),
    -- Zombie recovery by recreation from desired_spec. recreate_count counts
    -- recreations in the rolling window that opens at recreate_window_start.
    -- Past the budget the device is failed.
    recreate_count INT64 NOT NULL DEFAULT (0),
    recreate_window_start TIMESTAMP,
    -- Zombie recovery by in-place restart, tried first for a CVD that is
    -- present but Unreachable. restart_count counts restarts in the window
    -- that opens at restart_window_start. Past the budget, recovery moves on
    -- to a recreate, which resets the count.
    restart_count INT64 NOT NULL DEFAULT (0),
    restart_window_start TIMESTAMP,
    -- Set when capture-on-error has captured a bug report for this failed
    -- device, or has found that it cannot. The failed-capture detector only
    -- enqueues rows where it is NULL.
    bugreport_captured_at TIMESTAMP,
    -- Set when the row becomes a tombstone (status deleted). It arms the row
    -- deletion policy.
    tombstoned_at TIMESTAMP,
    -- The batch this device belongs to, NULL for a standalone device. Batch
    -- membership and status are derived from these rows.
    batch_id STRING(36),
    -- name while the device is not deleted, NULL once it is, as in
    -- hosts.live_name. Batches reuse names, so the filter must never match a
    -- deleted device.
    live_name STRING(63) AS (IF(status != 'deleted', name, NULL)) STORED,
    -- The same shape rule as hosts.name.
    CONSTRAINT chk_devices_name_shape CHECK (REGEXP_CONTAINS(name, r'^[a-z][a-z0-9-]*$')),
) PRIMARY KEY (host_id, device_id),
    INTERLEAVE IN PARENT hosts ON DELETE CASCADE,
    ROW DELETION POLICY (OLDER_THAN(tombstoned_at, INTERVAL 7 DAY));

CREATE INDEX idx_devices_status ON devices(status);
CREATE INDEX idx_devices_build ON devices(build_id);
CREATE INDEX idx_devices_health ON devices(health_status, last_health_check);
CREATE INDEX idx_devices_lifecycle_expires ON devices(lifecycle_mode, expires_at, status);
CREATE INDEX idx_devices_created_at ON devices(created_at DESC);
-- Holds only batch members. Backs the batch roll-up, member listing and
-- cascade teardown.
CREATE NULL_FILTERED INDEX idx_devices_batch_id ON devices(batch_id);
-- Holds only live devices. Backs the list's name filter.
CREATE NULL_FILTERED INDEX idx_devices_live_name ON devices(live_name);

-- =============================================================================
-- Snapshots
-- =============================================================================
-- The record of device snapshots. The Host Orchestrator keeps only a directory
-- per snapshot, so this table records what exists, where, and whether it can
-- still be restored.
--
-- The bytes live on the host's local disk, so the table is interleaved in
-- hosts and deleting the host deletes the rows. A path that destroys the disk
-- but keeps the host row (re-provision, preemption recovery, zone failover, a
-- lost VM) sets state to 'lost'. host_incarnation records which disk holds the
-- bytes.
CREATE TABLE snapshots (
    host_id STRING(36) NOT NULL,
    snapshot_id STRING(36) NOT NULL,
    device_id STRING(36) NOT NULL,
    -- The CVD addressing at the time of the snapshot, used again to restore it.
    cvd_group STRING(50) NOT NULL,
    cvd_name STRING(50) NOT NULL,
    host_incarnation INT64 NOT NULL,
    -- creating, ready or lost. Deleting a snapshot deletes its row.
    state STRING(16) NOT NULL DEFAULT ('creating'),
    created_by STRING(320),
    created_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    updated_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    -- Set when state becomes 'lost'. A lost snapshot stays listed for a week.
    lost_at TIMESTAMP,
) PRIMARY KEY (host_id, snapshot_id),
    INTERLEAVE IN PARENT hosts ON DELETE CASCADE,
    ROW DELETION POLICY (OLDER_THAN(lost_at, INTERVAL 7 DAY));

-- Backs listing by device, the per-device count limit and the device-delete
-- cascade.
CREATE INDEX idx_snapshots_device ON snapshots(device_id);

-- =============================================================================
-- Host Artifact Cache
-- =============================================================================
-- Which pinned builds each host's cvd download cache holds, recorded when a
-- CVD create completes on the host. Placement prefers a host that already holds
-- the wanted build, which skips the artifact fetch.
--
-- The record is advisory. The host can evict artifacts under a row, which costs
-- one ordinary fetch. Rows are deleted with their host, and every path that
-- replaces the host's disk deletes them in the commit that increments the
-- incarnation.
CREATE TABLE host_artifact_cache (
    host_id STRING(36) NOT NULL,
    -- The cvd fetch key as the create payload sends it:
    -- @ab/<android_build_id>/<target>. Only pinned keys are recorded. A branch
    -- key resolves to a build ID on the host, so its contents are unknown here.
    fetch_key STRING(200) NOT NULL,
    -- The registry build the key came from, informational.
    build_id STRING(36),
    -- The disk the artifacts were fetched onto.
    host_incarnation INT64 NOT NULL,
    fetched_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (host_id, fetch_key),
    INTERLEAVE IN PARENT hosts ON DELETE CASCADE;

-- =============================================================================
-- Batches
-- =============================================================================
-- Batch policy and the admission anchor. Membership and status are derived
-- from the member device rows (devices.batch_id). This row holds what no single
-- member can: the fill policy, the ready and failed markers the batch settle
-- job writes, and an anchor a keyed admission retry can find before the first
-- member exists.
CREATE TABLE batches (
    batch_id STRING(36) NOT NULL,
    name_prefix STRING(255) NOT NULL,
    -- The admitted target. It cannot be derived from member rows after a
    -- partial admission crashes.
    requested_count INT64 NOT NULL,
    fill_policy STRING(20) NOT NULL,
    placement STRING(20) NOT NULL,
    -- Atomic batches only: fail the batch if it has not filled within this
    -- many seconds of creation. NULL means no deadline.
    ready_timeout_seconds INT64,
    -- SHA-256 of the admission request (spec and requirements). A keyed retry
    -- resumes only when its fingerprint matches.
    spec_fingerprint STRING(64) NOT NULL DEFAULT (''),
    created_by STRING(320),
    created_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    -- Set by the batch settle job when every requested member is running. A
    -- filled atomic batch is settled: losing a member later degrades it and
    -- never fails it.
    ready_at TIMESTAMP,
    -- Set when the atomic policy trips: a member failed permanently, or
    -- ready_timeout_seconds passed before the batch filled. The members are
    -- torn down, and the row stays readable as failed until it is deleted or
    -- ages out.
    failed_at TIMESTAMP,
    failure_reason STRING(1000),
) PRIMARY KEY (batch_id),
  ROW DELETION POLICY (OLDER_THAN(created_at, INTERVAL 90 DAY));

-- =============================================================================
-- Builds
-- =============================================================================
CREATE TABLE builds (
    build_id STRING(36) NOT NULL DEFAULT (GENERATE_UUID()),
    name STRING(255),
    android_build_id STRING(255),
    target STRING(255),
    branch STRING(255),
    flavor STRING(100),
    fingerprint STRING(255),
    created_by STRING(320),
    source STRING(50) NOT NULL,
    description STRING(1000),
    is_default BOOL DEFAULT (false),
    artifact_specs JSON,
    metadata JSON,
    created_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    updated_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (build_id);

CREATE INDEX idx_builds_android_id ON builds(android_build_id);
CREATE INDEX idx_builds_target ON builds(target);
CREATE INDEX idx_builds_branch ON builds(branch);
CREATE INDEX idx_builds_created_at ON builds(created_at DESC);

-- =============================================================================
-- Quotas
-- =============================================================================
-- The single global host quota (scope 'platform', scope_id 'global'). The
-- device limit of each host is hosts.max_devices.
CREATE TABLE quotas (
    scope       STRING(20) NOT NULL,
    scope_id    STRING(36) NOT NULL,
    max_hosts   INT64 NOT NULL,
    used_hosts  INT64 NOT NULL DEFAULT (0),

    -- Rolling 7-day peak of used_hosts, maintained by the host-create quota
    -- increment, which is the only path that raises used_hosts.
    high_water_used_hosts INT64,
    high_water_at         TIMESTAMP,

    created_at  TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    updated_at  TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (scope, scope_id);

-- =============================================================================
-- Audit Log
-- =============================================================================
-- Entries are kept 90 days. The Activity page and the 7-day provisioning
-- counters read within that window.
CREATE TABLE audit_logs (
    log_id STRING(36) NOT NULL DEFAULT (GENERATE_UUID()),
    timestamp TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    actor_email STRING(320),
    action STRING(100) NOT NULL,
    resource_type STRING(50) NOT NULL,
    resource_id STRING(36),
    request_id STRING(36),
    changes JSON,
    metadata JSON,
    -- The generated columns below are facts the fleet autoscaler's demand
    -- signal reads, copied out of metadata because Spanner cannot index JSON.
    -- Generating them keeps them equal to metadata. SAFE_CAST turns a
    -- malformed value into NULL, so an audit entry never fails the write of the
    -- operation it records. host.create entries stamp origin, pool,
    -- max_devices, density, gpu_required and fits_default_host. Every other
    -- action leaves them NULL.
    origin STRING(MAX) AS (JSON_VALUE(metadata, '$.origin')) STORED,
    -- The pool the create was judged in. NULL is the default pool, as in
    -- hosts.pool. A GPU requirement is unservable in a pool without GPUs and
    -- ordinary demand in a GPU pool.
    pool STRING(MAX) AS (JSON_VALUE(metadata, '$.pool')) STORED,
    -- The slots the created host has, and the slots a standby host would have
    -- offered. A ceiling above the standby figure is a create no standby buffer
    -- could have absorbed. Both numbers are stamped because the comparison
    -- depends on a packing setting an operator can change. Comparing against
    -- the current setting would reclassify past entries.
    max_devices INT64 AS (SAFE_CAST(JSON_VALUE(metadata, '$.max_devices') AS INT64)) STORED,
    density INT64 AS (SAFE_CAST(JSON_VALUE(metadata, '$.density') AS INT64)) STORED,
    -- gpu_required is the create's hard GPU requirement. A create that only
    -- prefers a GPU can be placed on a CPU standby host, so it does not set
    -- it. fits_default_host is whether the default CPU host template held the
    -- create's shape when it was resolved.
    gpu_required BOOL AS (SAFE_CAST(JSON_VALUE(metadata, '$.gpu_required') AS BOOL)) STORED,
    fits_default_host BOOL AS (SAFE_CAST(JSON_VALUE(metadata, '$.fits_default_host') AS BOOL)) STORED,
    -- Stamped on device.create entries for creates that wanted a GPU: true when
    -- the device was placed without one, false otherwise. NULL on every other
    -- entry, which keeps idx_audit_gpu_degraded to the creates that wanted a
    -- GPU.
    gpu_degraded BOOL AS (SAFE_CAST(JSON_VALUE(metadata, '$.gpu_degraded') AS BOOL)) STORED,
) PRIMARY KEY (log_id),
  ROW DELETION POLICY (OLDER_THAN(timestamp, INTERVAL 90 DAY));

CREATE INDEX idx_audit_timestamp ON audit_logs(timestamp DESC);
-- Backs the Activity page's actor filter.
CREATE INDEX idx_audit_actor_email ON audit_logs(actor_email);
CREATE INDEX idx_audit_action ON audit_logs(action);
CREATE INDEX idx_audit_resource ON audit_logs(resource_type, resource_id);
-- The demand signal's index. Its query seeks one (action, origin) prefix and
-- ranges over timestamp. The stamped facts are STORED, so the scan reads no
-- base rows. NULL_FILTERED applies to key columns only: it keeps the index to
-- entries with an origin, and a NULL stored column does not drop a row.
CREATE NULL_FILTERED INDEX idx_audit_host_create
  ON audit_logs(action, origin, timestamp DESC)
  STORING (pool, max_devices, density, gpu_required, fits_default_host);
-- gpu_degraded is a key column here, so NULL_FILTERED keeps exactly the creates
-- that wanted a GPU, and the degradation ratio is one scan.
CREATE NULL_FILTERED INDEX idx_audit_gpu_degraded
  ON audit_logs(action, gpu_degraded, timestamp DESC);

-- =============================================================================
-- Machine Curation
-- =============================================================================
-- The operator curates machine families. The shapes of a family, with their
-- CPUs and memory, come from Compute Engine's machine-type list, which is held
-- in memory and composed on every read. A new series is admitted by adding one
-- row here. Shape sizes are never stored.
CREATE TABLE machine_families (
    family STRING(32) NOT NULL,
    -- Display name. NULL derives one from the series and its category.
    name STRING(255),
    -- Text that every shape of the family starts its description with.
    description STRING(1000),
    category STRING(50) NOT NULL DEFAULT ('general_purpose'),
    -- Used only for a shape Compute Engine reports without an architecture.
    architecture STRING(20) NOT NULL DEFAULT ('x86'),
    cpu_type STRING(255),
    -- Boot disk type the family requires. NULL takes the zonal default. C4 and
    -- N4 accept only hyperdisk-balanced, and Compute Engine does not advertise
    -- that, so it is curated here.
    boot_disk_type STRING(50),
    -- How the family gets a GPU when its shapes report none: 'attachable' (N1)
    -- or 'none'. An integrated GPU is read from the shape's own accelerator
    -- list.
    gpu_mode STRING(20) NOT NULL DEFAULT ('none'),
    max_gpus INT64,
    default_gpu_type STRING(100),
    default_gpu_count INT64,
    is_enabled BOOL NOT NULL DEFAULT (true),
    sort_order INT64 NOT NULL DEFAULT (0),
    created_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    updated_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY(family);

CREATE INDEX idx_machine_families_enabled ON machine_families(is_enabled);

-- Per-shape curation over the family's, and the only place a single shape is
-- named. A row exists only where an operator disabled a shape, chose the
-- default, or set a capacity. A NULL column means the family's value applies.
-- Display order and GPU policy come from the family alone.
CREATE TABLE machine_type_overrides (
    machine_type STRING(100) NOT NULL,
    is_enabled BOOL,
    is_default BOOL NOT NULL DEFAULT (false),
    -- The operator's device capacity for the shape. NULL uses the capacity
    -- computed from the shape's CPUs and memory.
    max_devices INT64,
    created_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    updated_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY(machine_type);

CREATE INDEX idx_machine_type_overrides_default ON machine_type_overrides(is_default);

-- =============================================================================
-- Placement
-- =============================================================================
-- CCO stores only the placement facts it alone knows: the stockouts it hit and
-- the Spot advice it fetched from a rate-limited API. What Compute Engine
-- offers is cached in memory and never stored.

-- Capacity failures per (zone, machine family), written by the provision
-- failure path and read by zone ranking. A cell in cooldown scores lower and is
-- softly filtered out until cooldown_until passes. The next successful
-- provision in the cell clears it. binding_metric names the quota or capacity
-- that failed. A quota failure applies to every zone in the region, since quota
-- is regional, and the metric keeps a GPU quota cooldown from blocking a
-- CPU-only request. A cell no failure touches for a week is deleted.
CREATE TABLE zone_health (
    zone                 STRING(64)  NOT NULL,
    machine_family       STRING(32)  NOT NULL,
    binding_metric       STRING(64)  NOT NULL,
    consecutive_failures INT64       NOT NULL DEFAULT (0),
    last_failure_at      TIMESTAMP,
    last_reason          STRING(200),
    cooldown_until       TIMESTAMP,
    updated_at           TIMESTAMP   NOT NULL OPTIONS (allow_commit_timestamp=true),
) PRIMARY KEY (zone, machine_family),
  ROW DELETION POLICY (OLDER_THAN(updated_at, INTERVAL 7 DAY));

-- Spot capacity advice per (zone, machine type) from the Compute Engine
-- advice.capacity API: obtainability (0.0 to 1.0, how likely a Spot create
-- succeeds) and an estimated uptime tier in seconds (60, 600 or 3600). Written
-- by the catalog sync when CCO_SPOT_ADVICE is on, and read by zone ranking for
-- Spot requests. Ranking falls back to quota and health signals when a cell is
-- missing, and a row is deleted after a day.
CREATE TABLE spot_advice (
    zone          STRING(64)  NOT NULL,
    machine_type  STRING(64)  NOT NULL,
    obtainability FLOAT64     NOT NULL,
    uptime_tier_s INT64       NOT NULL,
    synced_at     TIMESTAMP   NOT NULL OPTIONS (allow_commit_timestamp=true),
) PRIMARY KEY (zone, machine_type),
  ROW DELETION POLICY (OLDER_THAN(synced_at, INTERVAL 1 DAY));

-- =============================================================================
-- Host Image Configs
-- =============================================================================
-- The image family each machine family's hosts boot from. A NULL
-- source_project means CCO_HOST_IMAGE_PROJECT.
CREATE TABLE host_image_configs (
    machine_family STRING(50) NOT NULL,
    image_family STRING(100) NOT NULL,
    source_project STRING(100),
    description STRING(500),
    enabled BOOL NOT NULL DEFAULT (true),
    created_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    updated_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    created_by STRING(320),
    updated_by STRING(320),
) PRIMARY KEY (machine_family);

CREATE INDEX idx_host_image_configs_enabled ON host_image_configs (enabled);

-- =============================================================================
-- Task Queue
-- =============================================================================
-- The durable work queue. A producer enqueues a task in the transaction that
-- creates its resource, so queued work is never lost or orphaned. Workers on
-- every instance claim tasks in a serializable transaction, at most one worker
-- per task, so the pool scales without leader election. Delivery is at least
-- once, and handlers are idempotent. See docs/development/control-plane.md.
CREATE TABLE tasks (
    task_id STRING(36) NOT NULL DEFAULT (GENERATE_UUID()),
    -- Anti-hotspot bucket, fnv32a(task_id) % numShards, set by the producer.
    -- It prefixes the claim index, so claims spread across splits.
    shard INT64 NOT NULL,
    -- Dispatch key, such as 'device.create_cvd' or 'host.provision'.
    task_type STRING(64) NOT NULL,
    -- pending, leased, done or failed. A failed task is a dead letter and is
    -- not retried automatically.
    state STRING(16) NOT NULL DEFAULT ('pending'),
    -- The device or host the task acts on, and its only argument. Handlers
    -- read everything else from the resource row.
    resource_id STRING(36),
    -- W3C traceparent at enqueue. The attempt's root trace links to it.
    trace_context STRING(64),
    -- Earliest claim time (initial delay plus retry backoff).
    available_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    -- Visibility timeout while leased. The sweeper reclaims expired leases.
    lease_expiry TIMESTAMP,
    -- The instance holding the lease. Only that instance can complete or fail
    -- the task.
    lease_holder STRING(64),
    attempts INT64 NOT NULL DEFAULT (0),
    max_attempts INT64 NOT NULL DEFAULT (10),
    -- Claim tier: 0 for user intent, -100 for reconciler work.
    priority INT64 NOT NULL DEFAULT (0),
    -- Dedup key for pending reconcile tasks, NULL otherwise. Its unique index
    -- allows one pending reconcile per resource, alongside a leased one.
    reconcile_key STRING(MAX) AS (IF(state = 'pending' AND task_type IN ('host.reconcile', 'device.reconcile'), CONCAT(task_type, '/', resource_id), NULL)) STORED,
    -- At most one pending or leased transition task per (type, resource). The
    -- value stays the same across claim, postpone and requeue, and is NULL once
    -- failed, so dead letters never collide. ensureTransitionTask inserts and
    -- treats the unique index's AlreadyExists as success.
    transition_key STRING(MAX) AS (IF(state IN ('pending', 'leased') AND task_type IN ('host.provision', 'host.deprovision', 'device.create_cvd', 'device.cleanup_cvd'), CONCAT(task_type, '/', resource_id), NULL)) STORED,
    last_error STRING(2000),
    created_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    updated_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (task_id),
  ROW DELETION POLICY (OLDER_THAN(updated_at, INTERVAL 90 DAY));

-- Claim path: one shard, highest priority, oldest eligible task. The index
-- does not cover the claim's columns, so the claim query names it with
-- FORCE_INDEX.
CREATE INDEX idx_tasks_claimable_v2 ON tasks(shard, state, priority DESC, available_at);
-- Finds a leased sibling for the claim's per-key serialization with an index
-- seek, so the claim never scans the table while holding locks.
CREATE NULL_FILTERED INDEX idx_tasks_by_resource ON tasks(task_type, resource_id, state);
-- At most one pending reconcile task per resource.
CREATE UNIQUE NULL_FILTERED INDEX idx_tasks_reconcile_key ON tasks(reconcile_key);
-- At most one pending or leased transition task per (type, resource).
CREATE UNIQUE NULL_FILTERED INDEX idx_tasks_transition_key ON tasks(transition_key);
-- Sweep path. Holds only leased tasks, since lease_expiry is NULL otherwise.
CREATE NULL_FILTERED INDEX idx_tasks_lease_expiry ON tasks(lease_expiry);

-- Per-task step journal. An attempt records the remote operation it started,
-- so a redelivery resumes polling that operation and never starts a second
-- one. Only successful steps are recorded. Rows are deleted with their task.
CREATE TABLE task_steps (
    task_id STRING(36) NOT NULL,
    -- Step ordinal within the task (0, 1, ...).
    step_id INT64 NOT NULL,
    -- Step identity, such as 'cvd_load'.
    step_name STRING(64) NOT NULL,
    -- The step's result. For an operation step, the remote operation name to
    -- resume polling.
    output JSON,
    created_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (task_id, step_id),
  INTERLEAVE IN PARENT tasks ON DELETE CASCADE;

-- =============================================================================
-- Leader Leases
-- =============================================================================
-- Coordination for the periodic background jobs. Every instance runs them, but
-- only the holder of the 'background-plane' lease acts, so scaling out does not
-- multiply GCE calls, scans or deletes. A lease is acquired and renewed in a
-- read-write transaction guarded on
-- (holder IS NULL OR holder = @me OR lease_expiry < NOW()). Lease rows are
-- updated in place and never deleted.
CREATE TABLE leader_leases (
    lease_name STRING(64) NOT NULL,
    holder STRING(64),
    acquired_at TIMESTAMP,
    lease_expiry TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (lease_name);

-- =============================================================================
-- Idempotency Keys
-- =============================================================================
-- Maps a client's Idempotency-Key on POST /v2/devices to the device it created.
-- The row commits with the device, so a retry either reads the key and returns
-- that device, or loses the primary-key race and then reads it.
-- request_fingerprint hashes the request, and a key reused with a different
-- request is rejected. A key only has to outlive a client's retry window, so 30
-- days is ample.
CREATE TABLE idempotency_keys (
    idempotency_key STRING(255) NOT NULL,
    resource_id STRING(36) NOT NULL,
    request_fingerprint STRING(64) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (idempotency_key),
  ROW DELETION POLICY (OLDER_THAN(created_at, INTERVAL 30 DAY));

-- =============================================================================
-- Operations
-- =============================================================================
-- Durable handles for device verbs whose result the device row cannot show. A
-- restart or powerwash leaves the row unchanged, so neither progress nor
-- outcome can be read from it. One row per admitted verb, inserted in the
-- transaction that enqueues its task, so a handle always names queued work.
-- While an operation runs, its progress is read from the task row (attempts,
-- last_error, available_at).
CREATE TABLE operations (
    operation_id STRING(36) NOT NULL DEFAULT (GENERATE_UUID()),
    -- The verb, such as 'device.restart'. Clients choose the shape of response
    -- by the verb, which differs from the task type
    -- (device.restart_operation). Values come from the verb registry in
    -- internal/domain/operation.
    verb STRING(64) NOT NULL,
    resource_type STRING(16) NOT NULL,
    resource_id STRING(36) NOT NULL,
    -- The instance the verb acts on, or NULL for a device-wide verb (start,
    -- stop, delete and the group verbs). The device controller uses it to
    -- exempt only that instance from settling. Admission still allows one
    -- exclusive operation per device, so it is not part of exclusive_key.
    instance_name STRING(64),
    -- The task running the verb. Task rows are deleted on completion, so this
    -- can point at nothing. It is a lookup key through idx_operations_task and
    -- is never joined. It stays the same across retries and requeues.
    task_id STRING(36),
    done BOOL NOT NULL DEFAULT (false),
    -- The caller's parameters. A task carries only a resource ID, so the verb's
    -- arguments are kept here. Values such as the host URL are resolved when
    -- the verb runs.
    request JSON,
    -- RFC 9457 problem document, set only when the verb is done and failed.
    error JSON,
    -- The verb's result, when it has one.
    response JSON,
    -- Whether the verb excludes other exclusive operations on the resource. A
    -- column, so the set of exclusive verbs can change without altering the
    -- generated key below. FALSE by default, so a row written without the
    -- registry is never exclusive.
    exclusive BOOL NOT NULL DEFAULT (FALSE),
    -- Whether the verb converges the device toward its desired state, which the
    -- API's reconciling field reports. Kept separate from exclusive so the two
    -- sets of verbs can differ.
    mutating BOOL NOT NULL DEFAULT (FALSE),
    -- The caller. Workers run without a principal, so the audit entry takes its
    -- actor from here.
    created_by STRING(256),
    created_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    updated_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
    -- Admission key, NULL once done. Its unique index holds only in-flight
    -- exclusive operations, so a second exclusive admission on the same
    -- resource cannot commit.
    exclusive_key STRING(MAX) AS (IF(done = false AND exclusive,
        CONCAT(resource_type, '/', resource_id), NULL)) STORED,
    -- The same key for mutating operations.
    mutating_key STRING(MAX) AS (IF(done = false AND mutating,
        CONCAT(resource_type, '/', resource_id), NULL)) STORED,
    -- created_at while the operation is in flight, NULL once done. It indexes
    -- every in-flight operation in age order, which neither key above covers.
    open_at TIMESTAMP AS (IF(done = false, created_at, NULL)) STORED,
) PRIMARY KEY (operation_id),
  -- Kept 90 days, like tasks, audit_logs and batches. Retention must exceed
  -- idempotency_keys' 30 days. Deletion policies run independently per table,
  -- so at equal retention a surviving key could point at a deleted operation.
  ROW DELETION POLICY (OLDER_THAN(created_at, INTERVAL 90 DAY));

-- At most one in-flight exclusive operation per resource.
CREATE UNIQUE NULL_FILTERED INDEX idx_operations_exclusive ON operations(exclusive_key);
-- Which resources have an in-flight mutating operation. Read once per device
-- list.
CREATE NULL_FILTERED INDEX idx_operations_mutating ON operations(mutating_key);
-- In-flight operations in age order, for the orphan sweep and ?done=false
-- listings.
CREATE NULL_FILTERED INDEX idx_operations_open ON operations(open_at);
-- Finds the operation for a claimed task.
CREATE UNIQUE NULL_FILTERED INDEX idx_operations_task ON operations(task_id);
-- Per-resource history for the device detail view and listings by resource.
-- It spans the full retention, so every reader paginates.
CREATE INDEX idx_operations_by_resource ON operations(resource_id, created_at DESC);

-- =============================================================================
-- Seed Data: System Configuration
-- =============================================================================
-- The schema version this database holds. The binary checks it before serving,
-- and each upgrade file raises it.
INSERT INTO system_config (config_key, config_value, description)
VALUES ('schema.version', '87',
        'Schema version this database holds, checked by the binary before it serves');

-- The build a declarative create uses when no build is registered.
-- Deployment-specific keys such as host.default_gpu_machine_type are not
-- seeded. Without them, the resolver uses the enabled catalog.
INSERT INTO system_config (config_key, config_value, description)
VALUES ('build.default_upstream_branch', 'aosp-android-latest-release',
        'AOSP branch used when a declarative device create finds no registered builds');

INSERT INTO system_config (config_key, config_value, description)
VALUES ('build.default_upstream_target', 'aosp_cf_x86_64_only_phone-userdebug',
        'AOSP build target paired with build.default_upstream_branch');

-- =============================================================================
-- Seed Data: Global Quota
-- =============================================================================
INSERT INTO quotas (scope, scope_id, max_hosts, used_hosts)
VALUES ('platform', 'global', 100, 0);

-- =============================================================================
-- Seed Data: Machine Families
-- =============================================================================
-- The machine catalog. Every shape of these families is discovered from
-- Compute Engine and admitted if it holds at least one default-sized Android
-- VM.
--
-- E2 is excluded because Compute Engine's nested virtualization does not
-- support it, so an E2 host cannot expose /dev/kvm. The same restriction
-- excludes memory-optimized (M*), AMD (N2D, T2D) and Arm VMs. C4A is admitted
-- for its bare-metal shapes, where virtualization is native.
--
-- gpu_mode is 'attachable' for N1 only. A GPU on N1 is attached only when a
-- create asks for one, so default_gpu_count is 0 and default_gpu_type is the
-- type to attach. G2 shapes carry an integrated GPU, read from the shapes.
INSERT INTO machine_families (family, name, description, category, architecture, cpu_type, boot_disk_type, gpu_mode, max_gpus, default_gpu_type, default_gpu_count, is_enabled, sort_order)
VALUES
('n1', 'N1 General Purpose', 'General-purpose, GPU-capable', 'general_purpose', 'x86', 'Intel Skylake/Broadwell/Haswell', 'pd-balanced', 'attachable', 4, 'nvidia-tesla-t4', 0, true, 100),
('n2', 'N2 General Purpose', 'General-purpose, CPU-only', 'general_purpose', 'x86', 'Intel Cascade Lake/Ice Lake', 'pd-balanced', 'none', NULL, NULL, NULL, true, 110),
('n4', 'N4 General Purpose', 'General-purpose, CPU-only', 'general_purpose', 'x86', 'Intel Emerald Rapids', 'hyperdisk-balanced', 'none', NULL, NULL, NULL, true, 120),
('c3', 'C3 Compute Optimized', 'Compute-optimized, CPU-only', 'compute_optimized', 'x86', 'Intel Sapphire Rapids', 'pd-balanced', 'none', NULL, NULL, NULL, true, 200),
('c4', 'C4 Compute Optimized', 'Compute-optimized, CPU-only', 'compute_optimized', 'x86', 'Intel Emerald Rapids', 'hyperdisk-balanced', 'none', NULL, NULL, NULL, true, 210),
('g2', 'G2 GPU (L4)', 'GPU optimized with L4', 'accelerator_optimized', 'x86', 'Intel Cascade Lake', 'pd-balanced', 'none', NULL, NULL, NULL, true, 400),
('c4a', 'C4A ARM Compute Optimized', 'ARM, bare metal only. Required for ARM-based Cuttlefish devices. Powered by Google Axion processors.', 'compute_optimized', 'arm', 'Google Axion', 'hyperdisk-balanced', 'none', NULL, NULL, NULL, true, 420);

-- The default machine type, the one per-shape fact a family row cannot carry.
INSERT INTO machine_type_overrides (machine_type, is_default)
VALUES ('n1-standard-8', true);

-- =============================================================================
-- Seed Data: Host Image Configs
-- =============================================================================
-- Every family starts on the x86 image family cco-host in
-- CCO_HOST_IMAGE_PROJECT, so c4a (arm64) is refused as arch_mismatch until its
-- row names an arm64 image family.
INSERT INTO host_image_configs (machine_family, image_family, source_project, description, enabled, created_by, updated_by) VALUES
('n1', 'cco-host', NULL, 'Intel Haswell/Skylake general purpose', true, 'schema-seed', 'schema-seed'),
('n2', 'cco-host', NULL, 'Intel Cascade Lake general purpose', true, 'schema-seed', 'schema-seed'),
('n4', 'cco-host', NULL, 'Intel Emerald Rapids general purpose', true, 'schema-seed', 'schema-seed'),
('c3', 'cco-host', NULL, 'Intel Sapphire Rapids compute-optimized', true, 'schema-seed', 'schema-seed'),
('c4', 'cco-host', NULL, 'Intel Emerald Rapids compute-optimized', true, 'schema-seed', 'schema-seed'),
('g2', 'cco-host', NULL, 'NVIDIA L4 GPU instances', true, 'schema-seed', 'schema-seed'),
('c4a', 'cco-host', NULL, 'Google Axion ARM processors (bare metal only)', true, 'schema-seed', 'schema-seed');
