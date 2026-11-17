/**
 * Copyright 2026 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     https://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/**
 * Fixture data and response composers for the e2e API mock. Pure data, with no
 * Playwright import. `api-mock.ts` serves exactly what these compose, and
 * `scripts/gates/check-fixture-shapes.ts` parses the same output through the
 * console's own Zod schemas, so the fixtures cannot drift from the parse the
 * SPA runs without a gate going red.
 *
 * Every body backed by a registered schema carries that schema's *input* type,
 * pre-transform and snake_case as the backend sends it, which is the shape the
 * OpenAPI drift gate already pins. Two limits, so nobody reads more into the
 * static half than it carries: the entity schemas accept both casings as
 * separate optional fields, so a camelCase slip passes; and most fields are
 * `.optional()`, so what typing pins is the required set plus excess-property
 * checking on the literals. The runtime parse in check-fixture-shapes covers
 * the other half: enum values, string formats, and the envelopes as served.
 */

import type {z} from 'zod';
import type {ArtifactBucketsSchema} from '../../src/app/admin/models/artifact-buckets.model';
import type {AuditEntrySchema, AuditPageSchema} from '../../src/app/admin/models/audit.schema';
import type {
  AutoscalerSnapshotSchema,
  StandbyRecommendationSchema,
} from '../../src/app/admin/models/autoscaler.schema';
import type {CapacitySnapshotSchema} from '../../src/app/admin/models/capacity.schema';
import type {
  AdminZoneListSchema,
  AdminZoneSchema,
  CatalogFreshnessSchema,
} from '../../src/app/admin/models/catalog-admin.schema';
import type {
  ControlPlaneSnapshotSchema,
  FleetSettingsSchema,
} from '../../src/app/admin/models/control-plane.schema';
import type {
  HostImageConfigListResponseSchema,
  HostImageConfigSchema,
} from '../../src/app/admin/models/host-image-config.schema';
import type {
  AdminFamilyListSchema,
  AdminFamilySchema,
  FamilyShapeSchema,
} from '../../src/app/admin/models/machine-family.schema';
import type {
  ProvisioningStatsSchema,
  ReclaimableSchema,
} from '../../src/app/admin/models/operator-lens.schema';
import type {PlatformStatsSchema} from '../../src/app/admin/models/platform-stats.schema';
import type {
  BooleanSettingSchema,
  DefaultUpstreamBuildSchema,
  DockerHostSettingSchema,
  NumberSettingSchema,
  SpotMinUptimeSchema,
  StringSettingSchema,
} from '../../src/app/admin/models/settings.schema';
import type {
  FailedTaskListSchema,
  FailedTaskSchema,
  QueueStatsSchema,
} from '../../src/app/admin/models/task-admin.schema';
import type {UserSchema} from '../../src/app/auth/models/user.schema';
import type {
  BatchMemberSchema,
  BatchPlanSchema,
  BatchSchema,
} from '../../src/app/batches/models/batch.schema';
import type {BuildSchema} from '../../src/app/builds/models/build.schema';
import type {
  BugreportArtifactSchema,
  DeviceSchema,
  RecordingArtifactSchema,
  RecordingListSchema,
} from '../../src/app/devices/models/device.schema';
import type {IceConfigSchema} from '../../src/app/devices/models/ice.schema';
import type {
  OperationListSchema,
  OperationSchema,
} from '../../src/app/devices/models/operation.schema';
import type {SnapshotListSchema} from '../../src/app/devices/models/snapshot.schema';
import type {HostSchema} from '../../src/app/hosts/models/host.schema';
import type {MachineTypeSchema} from '../../src/app/machine-types/models/machine-type.schema';
import type {QuotaResponseSchema} from '../../src/app/quota/models/quota.schema';
import type {PlatformLifetimesSchema} from '../../src/app/shared/models/platform-lifetimes.schema';
import type {ResolutionSchema} from '../../src/app/shared/models/resolution.schema';
import type {ZoneAvailabilitySchema} from '../../src/app/shared/models/zone-availability.schema';
import type {
  ListBucketsResponseSchema,
  ListObjectsResponseSchema,
  StorageObjectSchema,
  VerifyAccessResponseSchema,
} from '../../src/app/storage/models/storage.schema';

const NOW = '2026-06-15T10:00:00Z';

/** Request bodies stay loose JSON. They are input shapes, not response wire. */
export type Json = Record<string, unknown>;

export type DeviceWire = z.input<typeof DeviceSchema>;
export type HostWire = z.input<typeof HostSchema>;
export type BuildWire = z.input<typeof BuildSchema>;
type UserWire = z.input<typeof UserSchema>;
/**
 * `id` duplicates `batch_id`: the generic item route looks resources up by `id`,
 * and the schema neither declares nor needs it. Widening here rather than at
 * each call site keeps the excess-property check on every other key.
 */
export type BatchWire = z.input<typeof BatchSchema> & {id?: string};
export type MachineTypeWire = z.input<typeof MachineTypeSchema>;
export type ZoneAvailabilityWire = z.input<typeof ZoneAvailabilitySchema>;
type AdminZoneWire = z.input<typeof AdminZoneSchema>;
type AdminFamilyWire = z.input<typeof AdminFamilySchema>;
export type FamilyShapeWire = z.input<typeof FamilyShapeSchema>;
type QuotaWire = z.input<typeof QuotaResponseSchema>;
type HostImageConfigWire = z.input<typeof HostImageConfigSchema>;
export type FailedTaskWire = z.input<typeof FailedTaskSchema>;
export type AuditEntryWire = z.input<typeof AuditEntrySchema>;
export type OperationWire = z.input<typeof OperationSchema>;
export type StorageObjectWire = z.input<typeof StorageObjectSchema>;
export type ArtifactBucketsWire = z.input<typeof ArtifactBucketsSchema>;
export type BatchMemberWire = z.input<typeof BatchMemberSchema>;
export type BatchPlanWire = z.input<typeof BatchPlanSchema>;
export type ResolutionWire = z.input<typeof ResolutionSchema>;
export type LifetimesWire = z.input<typeof PlatformLifetimesSchema>;

export type ControlPlaneWire = z.input<typeof ControlPlaneSnapshotSchema>;

export type CapacityWire = z.input<typeof CapacitySnapshotSchema>;
/** The cooldown-cell input shape, reached through the snapshot rather than by
 * exporting capacity sub-schemas nothing else imports. */
type CapacityCellWire = NonNullable<NonNullable<CapacityWire['zones']>[number]['cells']>[number];
type ZoneCooldownWire = NonNullable<CapacityCellWire['cooldown']>;

/** The Settings page state; key names are asserted by settings.spec.ts. */
interface SettingsState {
  zone_pin: string;
  quota_margin: number;
  quota_margin_is_set: boolean;
  v1_zone_hint: boolean;
  spot_auto_default: boolean;
  gpu_machine_type: string;
  upstream_branch: string;
  upstream_target: string;
  upstream_is_set: boolean;
  lifetimes: LifetimesWire;
  /** The fleet's schedule windows, replaced whole by their own PUT. */
  fleet_schedules: ScheduleWire[];
  /**
   * The machine shapes the fleet prefers, in order. Replaced whole by their own
   * PUT, exactly as the windows are.
   */
  fleet_shape_ladder: ShapeLadderWire;
}

/** One schedule window, as the wire carries it. */
type ScheduleWire = NonNullable<
  NonNullable<z.input<typeof FleetSettingsSchema>['tiers']>[string]['schedules']
>[number];

/** The shape order, as the wire carries it. */
type ShapeLadderWire = NonNullable<z.input<typeof FleetSettingsSchema>['shape_ladder']>;

export interface ApiState {
  user: UserWire;
  hosts: HostWire[];
  devices: DeviceWire[];
  builds: BuildWire[];
  batches: BatchWire[];
  machineTypes: MachineTypeWire[];
  zones: ZoneAvailabilityWire[];
  adminZones: AdminZoneWire[];
  /** The curation view of the families, which is what the Catalog page reads. */
  machineFamilies: AdminFamilyWire[];
  quota: QuotaWire;
  hostImageConfigs: HostImageConfigWire[];
  failedTasks: FailedTaskWire[];
  /** zone_health cells on europe-west4-b; the clear endpoint removes from here. */
  zoneCooldowns: ZoneCooldownWire[];
  /** Devices a batch admission can fit; below the requested count it clamps. */
  hostQuota: number;
  /** Degradation notes the batch plan returns. */
  batchPlanWarnings: string[];
  /**
   * Idempotency-Keys seen on create POSTs, in order, per resource ('' when the
   * request carried none). Every POST to the collection is recorded, plans
   * included, so specs can compare across entries: a retry of one intent must
   * reuse its key, an edited form must mint a new one, and a plan
   * (validate_only, which commits nothing) must carry none at all.
   */
  idempotencyKeys: {device: string[]; host: string[]; batch: string[]};
  /** In-flight operations the Control Plane panel lists. */
  openOperations: OperationWire[];
  /** The settings pairs with no other fixture home. */
  settings: SettingsState;
  auditEntries: AuditEntryWire[];
  artifactBuckets: ArtifactBucketsWire;
  artifactObjects: StorageObjectWire[];
  recordings: string[];
  /**
   * Requests no dedicated route answered, meaning the catch-all's list shape,
   * or an item route echoing an entity at a sub-resource path it does not model.
   * Recorded so a spec can assert the page it drives never leaned on a
   * wrong-shaped fallback.
   */
  unhandledRequests: string[];
}

export function host(overrides: Partial<HostWire> = {}): HostWire {
  return {
    id: 'host-1',
    // The label/binding pair the API actually returns: name is what the
    // operator typed, gce_instance_name is derived from it server-side.
    name: 'test-host',
    gce_instance_name: 'cf-test-host-a1b2c3d4',
    zone: 'europe-west4-a',
    status: 'running',
    machine_type: 'n1-standard-8',
    max_devices: 4,
    current_device_count: 0,
    provisioning_model: 'STANDARD',
    gpu_type: 'nvidia-tesla-t4',
    gpu_count: 1,
    ip_address: '34.10.20.30',
    // Provenance and the class the backend derives from it. Both are always
    // present on a host this API created, so a fixture omitting them would
    // exercise only the pre-column fallback.
    origin: 'user',
    class: 'reserved',
    created_by: 'tester@example.com',
    created_at: NOW,
    updated_at: NOW,
    ...overrides,
  };
}

export function device(overrides: Partial<DeviceWire> = {}): DeviceWire {
  return {
    id: 'device-1',
    name: 'my-device',
    host_id: 'host-1',
    host_name: 'test-host',
    host_zone: 'europe-west4-a',
    status: 'running',
    build_id: 'build-1',
    build_target: 'aosp_cf_x86_64_phone-userdebug',
    gpu_mode: 'gfxstream',
    created_by: 'tester@example.com',
    created_at: NOW,
    updated_at: NOW,
    // Optional on the wire, present here ON PURPOSE: an optional field
    // slips both the contract gate and typecheck:e2e, so the base fixture is
    // what keeps the browser suite parsing it. A booted verdict on a running
    // device renders nothing.
    boot_observation: {
      uboot_starts: 3,
      boot_completed: true,
      kernel_log_bytes: 440320,
      verdict: 'booted',
      observed_at: NOW,
    },
    // Present for the same reason: every interactive route is addressed by
    // instance, so a device with no instances streams nothing and the console
    // cannot build a target for it.
    instances: [
      {
        name: 'my-device',
        state: 'Running',
        webrtc_device_id: 'cvdgroup-my-device-1',
        adb_port: 6520,
        slot: 1,
      },
    ],
    ...overrides,
  };
}

export function build(overrides: Partial<BuildWire> = {}): BuildWire {
  return {
    id: 'build-1',
    name: 'AOSP Master Phone',
    source: 'android_build',
    branch: 'aosp-main',
    target: 'aosp_cf_x86_64_phone-trunk_staging-userdebug',
    is_default: true,
    created_by: 'tester@example.com',
    created_at: NOW,
    updated_at: NOW,
    ...overrides,
  };
}

// `id` duplicates batch_id so the generic item-route lookup works; the Zod
// schema ignores it.
export function batch(overrides: Partial<BatchWire> = {}): BatchWire {
  return {
    id: 'batch-1',
    batch_id: 'batch-1',
    name_prefix: 'ci-run',
    status: 'ready',
    total: 2,
    running: 2,
    errored: 0,
    reconciling: false,
    fill_policy: 'best_effort',
    placement: 'isolated',
    created_by: 'tester@example.com',
    created_at: NOW,
    ...overrides,
  };
}

function familyShape(
  overrides: Partial<FamilyShapeWire> & Pick<FamilyShapeWire, 'machine_type'>
): FamilyShapeWire {
  return {
    vcpus: 8,
    memory_gb: 30,
    fit: 3,
    viable: true,
    is_excluded: false,
    is_default: false,
    ...overrides,
  };
}

function machineFamily(
  overrides: Partial<AdminFamilyWire> & Pick<AdminFamilyWire, 'family'>
): AdminFamilyWire {
  return {
    name: `${overrides.family.toUpperCase()} General Purpose`,
    category: 'general_purpose',
    architecture: 'x86',
    gpu_mode: 'none',
    is_enabled: true,
    sort_order: 1,
    shape_count: overrides.shapes?.length ?? 0,
    viable_count: overrides.shapes?.filter(shape => shape.viable).length ?? 0,
    shapes: [],
    ...overrides,
  };
}

function machineType(overrides: Partial<MachineTypeWire> = {}): MachineTypeWire {
  return {
    id: 'n1-standard-8',
    family: 'n1',
    name: 'n1-standard-8',
    description: '8 vCPUs, 30 GB',
    category: 'general_purpose',
    architecture: 'x86',
    vcpus: 8,
    memory_gb: 30,
    gpu_support: true,
    max_gpus: 1,
    default_gpu_type: 'nvidia-tesla-t4',
    default_gpu_count: 1,
    is_default: true,
    is_enabled: true,
    sort_order: 1,
    zones: ['europe-west4-a', 'europe-west4-b'],
    ...overrides,
  };
}

function zone(overrides: Partial<ZoneAvailabilityWire> = {}): ZoneAvailabilityWire {
  return {
    zone: 'europe-west4-a',
    region: 'europe-west4',
    status: 'available',
    is_enabled: true,
    machine_type_available: true,
    gpu_available: true,
    gpu_max_count: 1,
    ...overrides,
  };
}

export function defaultState(): ApiState {
  return {
    user: {email: 'tester@example.com', display_name: 'Test Engineer'},
    hosts: [host()],
    devices: [device()],
    builds: [build()],
    batches: [batch()],
    hostQuota: 40,
    batchPlanWarnings: [],
    idempotencyKeys: {device: [], host: [], batch: []},
    settings: {
      zone_pin: 'europe-west4-a',
      quota_margin: 10,
      quota_margin_is_set: false,
      v1_zone_hint: true,
      spot_auto_default: false,
      gpu_machine_type: '',
      upstream_branch: 'aosp-android-latest-release',
      upstream_target: 'aosp_cf_x86_64_only_phone-userdebug',
      upstream_is_set: false,
      lifetimes: {
        device_ttl_hours: 4,
        extend_hours: 2,
        extend_count: 3,
        host_ttl_hours: 10,
        is_set: false,
        max_device_ttl_hours: 8,
        max_extend_hours: 4,
        max_extend_count: 10,
        max_host_ttl_hours: 168,
        max_devices_per_host: 16,
        host_image_backed_slots: 10,
        grace_period_seconds: 900,
      },
      // One window, because an empty list exercises neither the editor's
      // rendering nor the schema's array branch. The settings page reads the
      // windows out of this tier, so an absent one would pass while the
      // projection silently returned nothing.
      fleet_schedules: [
        {
          name: 'CI morning',
          hosts: 3,
          start: '07:00',
          duration_sec: 14400,
          days: [1, 2, 3, 4, 5],
          location: 'Europe/Stockholm',
        },
      ],
      // Two shapes for the same reason the window list has one entry: an empty
      // order exercises neither the editor's rows nor the schema's array branch.
      // Two rather than one, because the ORDER is the setting. A single rung
      // has no sequence to move, and every reordering assertion would pass
      // against a list that cannot be wrong.
      fleet_shape_ladder: ['n2-standard-8', 'n1-standard-8'],
    },
    openOperations: [
      {
        name: 'operations/op-1',
        done: false,
        metadata: {
          verb: 'device.restart',
          resource_type: 'device',
          resource_id: 'device-1',
          create_time: NOW,
          // Optional on the wire, present here ON PURPOSE: an optional field
          // slips both drift gates, so the fixture is what keeps the browser
          // suite parsing the instance a per-instance verb acted on.
          instance_name: 'my-device',
        },
      },
      {
        name: 'operations/op-2',
        done: false,
        metadata: {
          verb: 'host.powerwash',
          resource_type: 'host',
          resource_id: 'host-1',
          create_time: NOW,
          attempt: 3,
          last_error: 'host unreachable',
        },
      },
    ],
    // Three shapes, because the fleet's shape order is a preference BETWEEN
    // them: a catalog of one admits an order of one, where nothing can be
    // reordered and nothing can be added. The two extra rows are plain x86
    // general-purpose shapes. Every rule that refuses a rung is about a shape
    // the picker must not offer, and a fixture carrying one would only prove the
    // filter this catalog is too small to need.
    machineTypes: [
      machineType(),
      machineType({
        id: 'n2-standard-8',
        family: 'n2',
        name: 'n2-standard-8',
        description: '8 vCPUs, 32 GB',
        memory_gb: 32,
        gpu_support: false,
        max_gpus: 0,
        default_gpu_type: null,
        default_gpu_count: 0,
        is_default: false,
        sort_order: 2,
      }),
      machineType({
        id: 'n2-standard-16',
        family: 'n2',
        name: 'n2-standard-16',
        description: '16 vCPUs, 64 GB',
        vcpus: 16,
        memory_gb: 64,
        gpu_support: false,
        max_gpus: 0,
        default_gpu_type: null,
        default_gpu_count: 0,
        is_default: false,
        sort_order: 3,
      }),
    ],
    zones: [zone(), zone({zone: 'europe-west4-b'})],
    // Three families with three different things to say. N1 is admitted and
    // holds the default; N2 is admitted and withholds one of its shapes; C4A is
    // curated but withdrawn, and its shapes are still reported. A listing
    // built from the served catalog would carry neither the withdrawn family
    // nor the shape too small to offer.
    machineFamilies: [
      machineFamily({
        family: 'n1',
        cpu_type: 'Intel Skylake',
        gpu_mode: 'attachable',
        max_gpus: 4,
        default_gpu_type: 'nvidia-tesla-t4',
        default_gpu_count: 1,
        sort_order: 1,
        shapes: [
          familyShape({machine_type: 'n1-standard-8', is_default: true}),
          familyShape({machine_type: 'n1-highmem-16', vcpus: 16, memory_gb: 104, fit: 12}),
        ],
      }),
      machineFamily({
        family: 'n2',
        cpu_type: 'Intel Ice Lake',
        boot_disk_type: 'pd-balanced',
        sort_order: 2,
        shapes: [
          familyShape({machine_type: 'n2-standard-8', memory_gb: 32, fit: 3}),
          familyShape({
            machine_type: 'n2-standard-16',
            vcpus: 16,
            memory_gb: 64,
            fit: 7,
            is_excluded: true,
          }),
        ],
      }),
      machineFamily({
        family: 'c4a',
        name: 'C4A Axion',
        architecture: 'arm',
        cpu_type: 'Google Axion',
        boot_disk_type: 'hyperdisk-balanced',
        is_enabled: false,
        sort_order: 3,
        shapes: [
          familyShape({
            machine_type: 'c4a-highcpu-2',
            vcpus: 2,
            memory_gb: 4,
            fit: 0,
            viable: false,
          }),
          familyShape({
            machine_type: 'c4a-standard-48-metal',
            vcpus: 48,
            memory_gb: 192,
            fit: 23,
          }),
        ],
      }),
    ],
    // The curated shape list is three long and no zone stocks all of it: a
    // denominator taken from the widest offer instead of from the catalog would
    // read every zone as complete coverage, which is the case this set exists
    // to make expressible.
    adminZones: [
      {
        zone: 'europe-west4-a',
        region: 'europe-west4',
        status: 'UP',
        is_enabled: true,
        offered_shapes: ['n1-standard-8', 'n2-standard-8'],
        shapes_reported: true,
      },
      {
        zone: 'europe-west4-b',
        region: 'europe-west4',
        status: 'UP',
        is_enabled: true,
        offered_shapes: ['n1-standard-8'],
        shapes_reported: true,
      },
      {
        zone: 'us-east5-a',
        region: 'us-east5',
        status: 'UP',
        is_enabled: true,
        offered_shapes: ['n1-standard-8', 'n2-standard-8'],
        shapes_reported: true,
      },
      // Compute Engine said nothing about this one: not the same fact as
      // offering none of the curated shapes, and the page has to show both.
      {
        zone: 'us-central1-a',
        region: 'us-central1',
        status: 'DOWN',
        is_enabled: false,
        offered_shapes: [],
        shapes_reported: false,
      },
      // Offered, never used: no host has run there and no failure was recorded,
      // so it is not placement state and stays off the board, the case the
      // board's disclosure exists for.
      {
        zone: 'us-central1-f',
        region: 'us-central1',
        status: 'UP',
        is_enabled: false,
        offered_shapes: ['n1-standard-8', 'n2-standard-8'],
        shapes_reported: true,
      },
    ],
    quota: {
      scope: 'global',
      scope_id: 'global',
      max_hosts: 10,
      used_hosts: 1,
      available: 9,
      percentage: 10,
      high_water_used_hosts: 3,
      high_water_at: NOW,
    },
    hostImageConfigs: [
      {
        machine_family: 'n1',
        image_family: 'cco-host',
        source_project: 'example-image-project',
        description: 'Default Cuttlefish host image',
        enabled: true,
        created_at: NOW,
        updated_at: NOW,
        resolved: {
          ok: true,
          project: 'example-image-project',
          image_name: 'cco-host-20261018',
          self_link:
            'https://www.googleapis.com/compute/v1/projects/example-image-project/global/images/cco-host-20261018',
          architecture: 'x86',
        },
      },
      {
        machine_family: 'c4a',
        image_family: 'cco-host-arm64',
        description: 'Google Axion ARM processors (bare metal only)',
        enabled: true,
        created_at: NOW,
        updated_at: NOW,
        resolved: {
          ok: false,
          project: 'example-project',
          reason: 'not_found',
          message:
            'machine family c4a cannot boot a host: image family "cco-host-arm64" has no image in project "example-project"',
        },
      },
    ],
    failedTasks: [
      {
        id: 'task-1',
        type: 'device.create_cvd',
        resource_id: 'device-1',
        attempts: 10,
        max_attempts: 10,
        last_error: 'HO create failed: connection refused',
        updated_at: NOW,
      },
    ],
    zoneCooldowns: [
      {
        machine_family: 'n1',
        binding_metric: 'capacity',
        metric_label: 'capacity',
        regional: false,
        consecutive_failures: 2,
        in_cooldown: true,
        cooldown_until: '2026-06-15T10:20:00Z',
        last_failure_at: NOW,
        last_reason: 'zone stockout',
      },
      // Lapsed and regional: no countdown left, but the failure count still
      // decays the score, and clearing it reopens the whole region.
      {
        machine_family: 'n2',
        binding_metric: 'quota_cpu',
        metric_label: 'CPU quota',
        regional: true,
        consecutive_failures: 1,
        in_cooldown: false,
        last_failure_at: NOW,
        last_reason: 'CPUS quota exhausted',
      },
    ],
    artifactBuckets: {
      bugreport_bucket: '',
      recording_bucket: '',
      default_bucket: 'test-artifacts',
      capture_on_error: false,
      is_set: false,
    },
    artifactObjects: [],
    recordings: ['screen_record_0.webm'],
    auditEntries: [
      {
        id: 'audit-1',
        timestamp: NOW,
        actor_email: 'tester@example.com',
        action: 'device.create',
        resource_type: 'device',
        resource_id: 'device-1',
        metadata: {host_id: 'host-1', name: 'my-device'},
      },
      {
        id: 'audit-2',
        timestamp: NOW,
        actor_email: 'tester@example.com',
        action: 'quota.update',
        resource_type: 'quota',
        resource_id: 'global',
        metadata: {max_hosts_before: 5, max_hosts_after: 10},
      },
      // admin.config is emitted by catalog and host-image writes too, so the
      // Settings strip has one of each to tell apart by metadata kind.
      {
        id: 'audit-3',
        timestamp: NOW,
        actor_email: 'tester@example.com',
        action: 'admin.config',
        resource_type: 'system',
        resource_id: 'zone-pin',
        metadata: {kind: 'system_config', value: 'europe-west4-a'},
      },
      {
        id: 'audit-4',
        timestamp: NOW,
        actor_email: 'tester@example.com',
        action: 'admin.config',
        resource_type: 'system',
        resource_id: 'c4a-standard',
        metadata: {kind: 'host_image', op: 'create'},
      },
    ],
    unhandledRequests: [],
  };
}

/** The house list envelope (devices, hosts, builds; limit/offset required). */
export const collection = <T>(
  items: T[]
): {items: T[]; total: number; limit: number; offset: number} => ({
  items,
  total: items.length,
  limit: 50,
  offset: 0,
});

/**
 * Applies the backend's created_by/mine list scoping: mine resolves to the
 * mocked signed-in user, so fixtures owned by others disappear from
 * default-Mine views exactly as they would in production.
 */
export function byCreator<T extends {created_by?: string}>(
  items: T[],
  url: URL,
  state: ApiState
): T[] {
  const creator =
    url.searchParams.get('mine') === 'true' ? state.user.email : url.searchParams.get('created_by');
  return creator ? items.filter(i => i.created_by === creator) : items;
}

// ---------------------------------------------------------------------------
// Response composers. One per body a route serves; check-fixture-shapes.ts
// parses each one through the schema named in its return type.
// ---------------------------------------------------------------------------

export function zoneAvailabilityBody(state: ApiState): {zones: ZoneAvailabilityWire[]} {
  return {zones: state.zones};
}

export function machineTypeListBody(state: ApiState): {items: MachineTypeWire[]} {
  return {items: state.machineTypes};
}

export function hostImageConfigListBody(
  state: ApiState
): z.input<typeof HostImageConfigListResponseSchema> {
  return {
    items: state.hostImageConfigs,
    limit: 50,
    offset: 0,
    total: state.hostImageConfigs.length,
  };
}

export function stringSettingBody(
  value: string,
  isSet: boolean
): z.input<typeof StringSettingSchema> {
  return {value, is_set: isSet};
}

export function numberSettingBody(
  value: number,
  isSet: boolean
): z.input<typeof NumberSettingSchema> {
  return {value, is_set: isSet};
}

export function booleanSettingBody(
  value: boolean,
  isSet: boolean
): z.input<typeof BooleanSettingSchema> {
  return {value, is_set: isSet};
}

export function lifetimesBody(state: ApiState): LifetimesWire {
  return state.settings.lifetimes;
}

export function dockerHostBody(): z.input<typeof DockerHostSettingSchema> {
  return {machine_type: 'n1-standard-8', ttl_hours: 12, max_ttl_hours: 168, is_set: false};
}

/**
 * The whole /fleet body. Two schemas read it, the operations card's complete
 * one and the settings page's two-field projection, so the fixture serves what
 * the backend does rather than what either reader happens to need.
 */
export function fleetSettingsBody(state: ApiState): z.input<typeof FleetSettingsSchema> {
  return {
    mode: 'off',
    // The windows come from state so the schedules PUT is observable: served
    // from a literal, a spec could assert the request went out but never that
    // the server kept what it sent.
    tiers: {
      default: {
        standby: 0,
        floor: 2,
        ladder: state.settings.fleet_shape_ladder,
        schedules: state.settings.fleet_schedules,
      },
    },
    // The default pool's order again, projected beside the map as the backend
    // serves it.
    shape_ladder: state.settings.fleet_shape_ladder,
    // Non-zero, and not equal to the standby count: a fixture where the two
    // bounds carry the same figure lets a control bound to the wrong one look
    // right in every assertion.
    floor: 2,
    demand_horizon_sec: 3600,
    drain_grace_sec: 480,
    is_set: false,
    max_standby: 16,
    max_floor: 16,
    max_demand_horizon_sec: 86400,
    min_drain_grace_sec: 120,
    max_drain_grace_sec: 599,
    max_schedules: 8,
    max_ladder_rungs: 8,
  };
}

/**
 * The rule a shape order breaks, or '' when the server would take it.
 *
 * The refusal is the half of this setting that matters. The catalog is the only
 * thing that knows which shapes can run Cuttlefish here, so a fixture that took
 * any string would let the console ship a picker whose refusals were never
 * rendered. The wording names the rung and the rule, matching what the backend
 * returns, because that sentence is what the page has to have room for.
 */
export function shapeLadderRefusal(state: ApiState, rungs: string[]): string {
  if (rungs.length > 8) {
    return 'validating the fleet shape ladder: validation error for shape_ladder: must not exceed 8 rungs';
  }
  const refused = (index: number, reason: string): string =>
    `validating the fleet shape ladder: validation error for shape_ladder[${index}]: ${reason}`;
  const seen = new Set<string>();
  for (const [index, id] of rungs.entries()) {
    const row = state.machineTypes.find(mt => mt.id === id);
    if (seen.has(id)) {
      return refused(
        index,
        `"${id}" appears more than once; a repeated rung re-probes a stockout the walk already found`
      );
    }
    seen.add(id);
    if (!row) {
      return refused(
        index,
        `"${id}" is not in the machine-type catalog, which is the only authority on what can run Cuttlefish here; add the catalog row first`
      );
    }
    if (!row.is_enabled) {
      return refused(index, `"${id}" is disabled in the machine-type catalog`);
    }
    if (row.architecture !== 'x86') {
      return refused(
        index,
        `"${id}" is ${row.architecture}; the ladder provisions the x86 CPU path`
      );
    }
    if (row.gpu_mode === 'integrated') {
      return refused(
        index,
        `"${id}" has an integrated GPU, which would attach an accelerator to a host that never asked for one; the ladder provisions the CPU path`
      );
    }
    if (id.endsWith('-metal')) {
      return refused(index, `"${id}" is bare metal, which GCE never offers as Spot capacity`);
    }
  }
  return '';
}

/** An RFC 9457 problem body, the shape Huma refuses a write with. */
export function problemBody(detail: string, status = 400): Json {
  return {title: 'Bad Request', status, detail};
}

export function spotMinUptimeBody(): z.input<typeof SpotMinUptimeSchema> {
  return {single_seconds: 600, batch_seconds: 3600, is_set: false};
}

export function defaultUpstreamBuildBody(
  state: ApiState
): z.input<typeof DefaultUpstreamBuildSchema> {
  return {
    branch: state.settings.upstream_branch,
    target: state.settings.upstream_target,
    is_set: state.settings.upstream_is_set,
  };
}

export function adminStatsBody(state: ApiState): z.input<typeof PlatformStatsSchema> {
  return {
    hosts: {total: state.hosts.length, active: 1, max_quota: 10},
    devices: {total: state.devices.length, active: 1},
    hosts_by_status: {running: 1},
    devices_by_status: {running: 1},
    timestamp: NOW,
  };
}

export function taskStatsBody(state: ApiState): z.input<typeof QueueStatsSchema> {
  return {
    pending: 2,
    leased: 1,
    failed: state.failedTasks.length,
    oldest_pending_age_seconds: 45,
  };
}

export function failedTasksBody(tasks: FailedTaskWire[]): z.input<typeof FailedTaskListSchema> {
  return {tasks};
}

export function controlPlaneBody(state: ApiState): ControlPlaneWire {
  return {
    now: NOW,
    queue: {
      pending: 2,
      leased: 1,
      failed: state.failedTasks.length,
      oldest_pending_age_seconds: 45,
    },
    type_counts: [
      {type: 'device.create_cvd', state: 'pending', count: 1},
      {type: 'device.reconcile', state: 'pending', count: 1},
      {type: 'host.provision', state: 'leased', count: 1},
    ],
    live_tasks: [
      {
        id: 'task-leased-1',
        type: 'host.provision',
        state: 'leased',
        resource_id: 'host-1',
        attempts: 1,
        max_attempts: 10,
        priority: 0,
        available_at: NOW,
        lease_expiry: '2026-06-15T10:00:12Z',
        lease_holder: 'instance-e2e-1',
        updated_at: NOW,
      },
      {
        id: 'task-pending-1',
        type: 'device.create_cvd',
        state: 'pending',
        resource_id: 'device-1',
        attempts: 0,
        max_attempts: 10,
        priority: 0,
        available_at: NOW,
        updated_at: NOW,
      },
      {
        id: 'task-pending-2',
        type: 'device.reconcile',
        state: 'pending',
        resource_id: 'device-1',
        attempts: 3,
        max_attempts: 10,
        priority: -100,
        available_at: '2026-06-15T10:00:45Z',
        last_error: 'teardown still converging',
        updated_at: NOW,
      },
    ],
    // One lease: every periodic loop runs on whichever instance holds it.
    leaders: [
      {
        name: 'background-plane',
        holder: 'instance-e2e-1',
        acquired_at: NOW,
        lease_expiry: '2026-06-15T10:00:12Z',
        renewed_at: NOW,
      },
    ],
    workers_per_instance: 8,
    // Required by ControlPlaneSnapshotSchema: without it the whole snapshot
    // fails to parse and every panel on the page disappears behind one
    // validation banner.
    sweeps: {
      delete_orphan_vms: false,
      orphan_cvds: false,
      zombie_recreate: false,
      reaper_dry_run: true,
      is_set: false,
    },
    fleet: {
      status: {
        mode: 'off',
        target: 0,
        standby: 0,
        draining: 0,
        // The peak counts the servable miss alone, while the split below accounts
        // for all four the window held. The two disagreeing is the normal case,
        // not a broken fixture, and showing that distinction is the card's job.
        misses: 1,
        demand: {
          servable: 1,
          unservable: 3,
          // Four misses, five axis hits: the multi-axis one is counted twice, so
          // a fixture whose axes summed to unservable would silently assert the
          // opposite of the rule.
          gpu_required: 2,
          needs_larger_host: 2,
          oversized_group: 1,
        },
        schedule: 0,
        floor: 2,
        // Three machines running against a floor of two and nothing free: the
        // free-machine promise is met at zero and the fleet is above its floor,
        // so the two bounds disagree about which one is deciding.
        total: 3,
        signal: 'standby',
      },
      policy: {
        mode: 'off',
        tiers: {default: {standby: 0, floor: 2}},
        floor: 2,
        demand_horizon_sec: 3600,
        drain_grace_sec: 480,
        is_set: false,
      },
      device_slots: 0,
      gpu_degradation: {degraded: 2, wanted: 5},
    },
    connectivity: {
      ingress_mode: 'relay',
      transport_policy: 'relay',
      stun_servers: ['stun:stun.l.google.com:19302'],
      turn_client_servers: ['turns:relay.example.com:443?transport=tcp'],
      turn_device_servers: ['turn:10.164.0.9:3478?transport=udp'],
      turn_realm: 'cco',
      credential_ttl_seconds: 28800,
      credentials_mintable: true,
      problems: [],
    },
  };
}

/**
 * The Capacity page's snapshot: the placement space with a verdict per zone,
 * the regional quota behind those verdicts, and the Spot block.
 *
 * Not uniform, on purpose. One region is comfortable and one is spent to its
 * reserve; one zone is clear, one is cooling on the cells the state holds, one
 * is disabled but still carries a machine, and one CPU pool has no series at
 * all. A board where every tile agreed would render every stripe, word and
 * dashed track the same way while proving none of them.
 */
export function capacityBody(state: ApiState): CapacityWire {
  const n1Cooldown = state.zoneCooldowns.find(c => c.machine_family === 'n1');
  const n2Cooldown = state.zoneCooldowns.find(c => c.machine_family === 'n2');
  const westBCells: NonNullable<CapacityWire['zones']>[number]['cells'] = [
    {
      machine_family: 'n1',
      machines: 1,
      arriving: 0,
      devices: 3,
      verdict: n1Cooldown?.in_cooldown ? 'cooling' : 'clear',
      verdict_detail: n1Cooldown?.in_cooldown
        ? 'capacity failed here 2 time(s) in a row on n1'
        : '',
      cooling_until: n1Cooldown?.in_cooldown ? n1Cooldown.cooldown_until : undefined,
      quota_pool: 'CPUS',
      quota_state: 'open',
      spot: {
        machine_type: 'n1-standard-8',
        obtainability: 0.2,
        band: 'low',
        uptime_tier_s: 60,
        synced_at: NOW,
      },
      cooldown: n1Cooldown,
    },
    {
      machine_family: 'n2',
      machines: 0,
      arriving: 0,
      devices: 0,
      verdict: 'clear',
      quota_pool: 'N2_CPUS',
      quota_state: 'open',
      cooldown: n2Cooldown,
    },
  ];
  const coolingLeft = westBCells.filter(c => c.cooldown !== undefined);
  // Enablement is written through the zone PUT, so the board reads it back
  // from the state that write lands in: switching a zone off has to move the
  // tile as well as the toggle on it.
  const placed = placementSpace(state, [
    {
      zone: 'europe-west4-a',
      region: 'europe-west4',
      enabled: true,
      status: 'UP',
      machines: 2,
      arriving: 1,
      devices: 6,
      verdict: 'clear',
      spot: {
        machine_type: 'n1-standard-8',
        obtainability: 0.9,
        band: 'high',
        uptime_tier_s: 3600,
        synced_at: NOW,
      },
      cells: [
        {
          machine_family: 'n1',
          machines: 2,
          arriving: 1,
          devices: 6,
          verdict: 'clear',
          quota_pool: 'CPUS',
          quota_state: 'open',
          spot: {
            machine_type: 'n1-standard-8',
            obtainability: 0.9,
            band: 'high',
            uptime_tier_s: 3600,
            synced_at: NOW,
          },
        },
      ],
    },
    {
      zone: 'europe-west4-b',
      region: 'europe-west4',
      enabled: true,
      status: 'UP',
      machines: 1,
      arriving: 0,
      devices: 3,
      verdict: coolingLeft.some(c => c.cooldown?.in_cooldown) ? 'cooling' : 'constrained',
      verdict_detail: coolingLeft.some(c => c.cooldown?.in_cooldown)
        ? 'capacity failed here 2 time(s) in a row on n1'
        : 'Spot obtainability is low for n1 here; on-demand is unaffected',
      cooling_until: n1Cooldown?.in_cooldown ? n1Cooldown.cooldown_until : undefined,
      spot: {
        machine_type: 'n1-standard-8',
        obtainability: 0.2,
        band: 'low',
        uptime_tier_s: 60,
        synced_at: NOW,
      },
      cells: westBCells,
    },
    // Switched off and still holding a machine: placement state, so it stays
    // on the board rather than vanishing with its host.
    {
      zone: 'us-central1-a',
      region: 'us-central1',
      enabled: true,
      status: 'DOWN',
      machines: 1,
      arriving: 0,
      devices: 2,
      verdict: 'no_signal',
      verdict_detail: 'nothing has been observed about this zone yet',
      cells: [
        {
          machine_family: 'n2',
          machines: 1,
          arriving: 0,
          devices: 2,
          verdict: 'no_signal',
          verdict_detail: 'nothing has been observed about this cell yet',
          quota_state: 'unknown',
        },
      ],
    },
    {
      zone: 'us-east5-a',
      region: 'us-east5',
      enabled: true,
      status: 'UP',
      machines: 0,
      arriving: 0,
      devices: 0,
      verdict: 'blocked',
      verdict_detail: 'CPUS in us-east5 is spent to its reserve, 2,912 of 3,000 used',
      cells: [
        {
          machine_family: 'n1',
          machines: 0,
          arriving: 0,
          devices: 0,
          verdict: 'blocked',
          verdict_detail: 'CPUS in us-east5 is spent to its reserve, 2,912 of 3,000 used',
          quota_pool: 'CPUS',
          quota_state: 'at_wall',
        },
      ],
    },
  ]);
  return {
    now: NOW,
    fleet: fleetCounts(placed),
    regions: [
      {
        region: 'europe-west4',
        machines: 3,
        devices: 9,
        zones_enabled: 2,
        zones: 2,
        quota_state: 'comfortable',
      },
      {
        region: 'us-central1',
        machines: 1,
        devices: 2,
        zones_enabled: 0,
        zones: 1,
        quota_state: 'unknown',
      },
      {
        region: 'us-east5',
        machines: 0,
        devices: 0,
        zones_enabled: 1,
        zones: 1,
        quota_state: 'blocked',
        quota_pool: 'CPUS',
        quota_usage: 2912,
        quota_limit: 3000,
      },
    ],
    zones: placed,
    pools: [
      {
        region: 'europe-west4',
        pool: 'CPUS',
        kind: 'cpu',
        families: ['n1'],
        spot: false,
        state: 'open',
        usage: 24,
        limit: 96,
        reserve: 9,
        synced_at: NOW,
      },
      {
        region: 'europe-west4',
        pool: 'SSD_TOTAL_GB',
        kind: 'disk',
        families: ['n1'],
        spot: false,
        state: 'open',
        usage: 1600,
        limit: 40960,
        reserve: 4096,
        synced_at: NOW,
      },
      {
        region: 'europe-west4',
        pool: 'PREEMPTIBLE_CPUS',
        kind: 'cpu',
        families: ['n1', 'n2'],
        spot: true,
        state: 'open',
        usage: 32,
        limit: 5000,
        reserve: 500,
        synced_at: NOW,
      },
      // The two pools Google publishes nothing for until the family is first
      // used. Dashed, never zero, because placement treats them as open.
      {
        region: 'europe-west4',
        pool: 'CPUS_PER_VM_FAMILY:C4',
        kind: 'cpu',
        families: ['c4'],
        spot: false,
        state: 'unknown',
      },
      {
        region: 'europe-west4',
        pool: 'HDB_TOTAL_GB',
        kind: 'disk',
        families: ['c4'],
        spot: false,
        state: 'unknown',
      },
      {
        region: 'us-east5',
        pool: 'CPUS',
        kind: 'cpu',
        families: ['n1'],
        spot: false,
        state: 'at_wall',
        usage: 2912,
        limit: 3000,
        reserve: 300,
        synced_at: NOW,
      },
    ],
    spot: {
      fleet: [
        {model: 'SPOT', status: 'running', count: 2},
        {model: 'STANDARD', status: 'running', count: 1},
        {model: 'STANDARD', status: 'provisioning', count: 1},
      ],
      spot_machines: 2,
      standard_machines: 2,
      preempted_7d: 3,
      recovered_7d: 3,
      unrecovered_7d: 0,
      typical_recovery_seconds: 34,
      advice: {enabled: true, cells: 36, newest_synced_at: NOW, stale: false},
    },
    reserve_margin_percent: 10,
  };
}

/** One zone of the placement space, as the snapshot carries it. */
type CapacityZoneWire = NonNullable<CapacityWire['zones']>[number];

/**
 * Applies the enabled set the zone PUT writes: a zone switched off answers
 * "disabled" whatever its cells hold, which is what lets a spec watch an
 * enablement write come back through the snapshot.
 */
function placementSpace(state: ApiState, zones: CapacityZoneWire[]): CapacityZoneWire[] {
  const enabled = new Set(state.adminZones.filter(z => z.is_enabled).map(z => z.zone));
  return zones.map(zone =>
    enabled.has(zone.zone)
      ? zone
      : {
          ...zone,
          enabled: false,
          verdict: 'disabled' as const,
          verdict_detail: 'not in the placement space, ranking never picks this zone',
          cooling_until: undefined,
        }
  );
}

/** The KPI strip's counts, derived from the tiles as the handler derives them. */
function fleetCounts(zones: CapacityZoneWire[]): CapacityWire['fleet'] {
  const live = zones.filter(zone => zone.enabled);
  const verdicts = (verdict: string): number =>
    live.filter(zone => zone.verdict === verdict).length;
  return {
    machines: zones.reduce((total, zone) => total + zone.machines, 0),
    devices: zones.reduce((total, zone) => total + zone.devices, 0),
    regions: new Set(zones.map(zone => zone.region)).size,
    zones_enabled: live.length,
    zones_clear: verdicts('clear'),
    zones_cooling: verdicts('cooling'),
    zones_blocked: verdicts('blocked'),
    zones_constrained: verdicts('constrained'),
  };
}

/**
 * The Autoscaler page's snapshot.
 *
 * The fleet is mixed on purpose: a host that IS standby, one carrying devices,
 * one whose slots are held by a dead row, one that over-admitted past what the
 * image backs, and one that is not the fleet's at all. A uniform fixture would
 * let every notch state and every exclusion reason go unrendered while the page
 * still looked right.
 */
export function autoscalerBody(state: ApiState): z.input<typeof AutoscalerSnapshotSchema> {
  const control = controlPlaneBody(state);
  // The stored order, judged: the preferred shape out of capacity in every
  // zone it could go to, and the one below it carrying what would be built.
  // Both states at once, because a ladder where nothing has fallen through
  // draws the same as one nothing has been observed about.
  const judgedLadder = [
    {
      machine_type: 'n2-standard-8',
      machine_family: 'n2',
      slots: 3,
      zones: 2,
      stocked_out_zones: 2,
      newest_stockout_at: '2026-06-15T09:54:00Z',
    },
    {
      machine_type: 'n1-standard-8',
      machine_family: 'n1',
      slots: 3,
      zones: 2,
      stocked_out_zones: 0,
      landing: true,
    },
  ];
  return {
    now: NOW,
    fleet: control.fleet,
    // One pool, and its entry is the fleet block split the way the keeper acts
    // on it: the same composed status and the same judged order, so the region,
    // the masthead row and the fleet panel reconcile by construction.
    pools: [
      {
        pool: 'default',
        status: control.fleet.status,
        deficit: 0,
        surplus: 0,
        reason: 'mode_off',
        ladder: judgedLadder,
      },
    ],
    limits: {
      max_standby_hosts: 16,
      max_topups_per_cycle: 2,
      max_shortens_per_cycle: 2,
      max_collapses_per_cycle: 2,
      quota_reserve_hosts: 1,
      cycle_seconds: 30,
      provision_lead_seconds: 600,
      host_image_backed_slots: 10,
      min_shorten_age_seconds: 600,
    },
    gate: {
      reason: 'mode_off',
      deficit: 0,
      surplus: 0,
      quota_available: 7,
      quota_spendable: 6,
      leader_held: true,
      leader_holder: 'instance-e2e-1',
    },
    hosts: [
      {
        id: 'host-fleet-idle',
        name: 'cf-auto-7a3',
        zone: 'europe-west4-a',
        machine_type: 'n1-standard-8',
        gpu_count: 0,
        status: 'running',
        class: 'fleet',
        origin: 'auto',
        pool: 'default',
        max_devices: 3,
        backed_slots: 3,
        live_instances: 0,
        error_instances: 0,
        occupants: [],
        standby: true,
        created_at: NOW,
      },
      {
        id: 'host-fleet-busy',
        name: 'cf-pool-2b1',
        zone: 'europe-west4-a',
        machine_type: 'n1-standard-8',
        gpu_count: 0,
        status: 'running',
        class: 'fleet',
        origin: 'pool',
        pool: 'default',
        max_devices: 3,
        backed_slots: 3,
        // One device over two slots: the case a unit-square renderer gets wrong.
        live_instances: 2,
        error_instances: 0,
        occupants: [{name: 'perf-1', instances: 2}],
        standby: false,
        failing_term: 'idle',
        created_at: NOW,
      },
      {
        id: 'host-fleet-haunted',
        name: 'cf-pool-8d0',
        zone: 'europe-west4-b',
        machine_type: 'n1-standard-8',
        gpu_count: 0,
        status: 'running',
        class: 'fleet',
        origin: 'pool',
        pool: 'default',
        // Promises more than the image backs AND holds a dead row, so the two
        // states that are easiest to conflate are both on one host.
        max_devices: 12,
        backed_slots: 10,
        live_instances: 0,
        error_instances: 1,
        occupants: [{name: 'stranded-1', instances: 1, errored: true}],
        standby: false,
        failing_term: 'idle',
        created_at: NOW,
      },
      {
        id: 'host-fleet-degraded',
        name: 'cf-pool-3c9',
        zone: 'europe-west4-b',
        machine_type: 'n1-standard-8',
        gpu_count: 0,
        status: 'running',
        class: 'fleet',
        origin: 'pool',
        pool: 'default',
        max_devices: 3,
        backed_slots: 3,
        live_instances: 0,
        error_instances: 0,
        occupants: [],
        // Idle and still not counted, the case where the drawing alone cannot
        // say why, so the machine has to.
        standby: false,
        failing_term: 'not_degraded',
        created_at: NOW,
      },
      {
        id: 'host-user',
        name: 'cf-user-5e2',
        zone: 'europe-west4-a',
        machine_type: 'n1-standard-8',
        gpu_count: 0,
        status: 'running',
        class: 'reserved',
        origin: 'user',
        pool: 'default',
        max_devices: 4,
        backed_slots: 4,
        live_instances: 0,
        error_instances: 0,
        occupants: [],
        standby: false,
        failing_term: 'class',
        created_at: NOW,
      },
    ],
    shapes: [
      {machine_type: 'n1-standard-8', gpu_count: 0, class: 'fleet', status: 'running', count: 4},
      {machine_type: 'n1-standard-8', gpu_count: 0, class: 'reserved', status: 'running', count: 1},
    ],
    // The default pool's order again, projected beside the pools as the
    // backend serves it.
    ladder: judgedLadder,
    activity: {provisions: 2, rescues: 1, shortens: 0, collapses: 0},
    activity_window_sec: 86400,
    arm_image_ready: false,
    effective_density: 3,
    density_machine_type: 'n1-standard-8',
    demand_horizon_sec: 3600,
  };
}

/**
 * The same snapshot with a fleet of `count` interchangeable machines.
 *
 * The canvas picks its fidelity from the machine count alone, so the only way
 * to exercise the compact and cluster tiers in a browser is to serve a fleet
 * that crosses their breakpoints.
 */
export function autoscalerFleetBody(
  state: ApiState,
  count: number
): z.input<typeof AutoscalerSnapshotSchema> {
  return {
    ...autoscalerBody(state),
    hosts: Array.from({length: count}, (_, i) => ({
      id: `host-fleet-${i}`,
      name: `cf-pool-${String(i).padStart(2, '0')}`,
      zone: 'europe-west4-a',
      machine_type: 'n1-standard-8',
      gpu_count: 0,
      status: 'running',
      class: 'fleet' as const,
      origin: 'pool' as const,
      pool: 'default',
      max_devices: 3,
      backed_slots: 3,
      live_instances: 0,
      error_instances: 0,
      occupants: [],
      standby: true,
      created_at: NOW,
    })),
  };
}

/**
 * The observation window's decision. Served as SUFFICIENT with a located peak,
 * so the page renders the branch that offers a number. The quiet-week branch is
 * asserted as a unit test, where the threshold can be crossed without a fixture
 * that would then never exercise the other one.
 */
export function standbyRecommendationBody(): z.input<typeof StandbyRecommendationSchema> {
  const from = '2026-06-08T10:00:00Z';
  return {
    standby: 3,
    sufficient: true,
    peak_from: '2026-06-11T09:12:00Z',
    peak_to: '2026-06-11T09:26:00Z',
    servable: 29,
    unservable: 12,
    series: [
      {at: from, peak: 0},
      {at: '2026-06-09T10:00:00Z', peak: 1},
      {at: '2026-06-11T09:00:00Z', peak: 3},
      {at: '2026-06-14T09:00:00Z', peak: 1},
    ],
    from,
    to: NOW,
  };
}

export function catalogFreshnessBody(): z.input<typeof CatalogFreshnessSchema> {
  return {built_at: NOW, partial: ['machine types: us-central1-a']};
}

export function adminZonesBody(state: ApiState): z.input<typeof AdminZoneListSchema> {
  return {
    zones: state.adminZones,
    curated_shapes: state.machineTypes.map(mt => mt.id),
  };
}

export function adminFamiliesBody(state: ApiState): z.input<typeof AdminFamilyListSchema> {
  return {families: state.machineFamilies};
}

/**
 * What resolving one machine type by name answers, standing in for Compute
 * Engine: a listed shape, a custom shape synthesized from a legal name, or the
 * sentence Compute Engine refuses an illegal one with.
 *
 * The refusal text is Google's, verbatim in shape, because the console renders
 * it as the whole explanation. A paraphrase here would let a regression that
 * swapped it for the console's own words still pass.
 */
export function resolveMachineTypeBody(
  state: ApiState,
  id: string
): {shape: MachineTypeWire | null; refusal: string} {
  const listed = state.machineTypes.find(mt => mt.id === id);
  if (listed) {
    return {shape: listed, refusal: ''};
  }
  const custom = /^([a-z0-9]+)-custom-(\d+)-(\d+)$/.exec(id);
  if (!custom) {
    return {shape: null, refusal: `Invalid value for field 'machineType': '${id}'.`};
  }
  const [, family, vcpus, memoryMb] = custom;
  if (Number(memoryMb) % 256 !== 0) {
    return {
      shape: null,
      refusal:
        `Invalid value for field 'resource.machineType': '${id}'. ` +
        `Memory should be a multiple of 256MiB, while ${memoryMb}MiB was specified.`,
    };
  }
  const memoryGb = Math.floor(Number(memoryMb) / 1024);
  return {
    shape: machineType({
      id,
      family,
      name: id,
      description: `${vcpus} vCPUs, ${memoryGb} GB RAM`,
      vcpus: Number(vcpus),
      memory_gb: memoryGb,
      fit: Math.floor((memoryGb * 1024 - 4096) / 8192),
      gpu_support: false,
      max_gpus: 0,
      default_gpu_type: null,
      default_gpu_count: 0,
      is_default: false,
      zones: null,
    }),
    refusal: '',
  };
}

export function enabledZonesBody(state: ApiState): {zones: string[]} {
  return {zones: state.adminZones.filter(z => z.is_enabled).map(z => z.zone)};
}

export function auditPageBody(entries: AuditEntryWire[]): z.input<typeof AuditPageSchema> {
  return {items: entries, total: entries.length, limit: 50, offset: 0};
}

export function storageObjectsBody(
  objects: StorageObjectWire[]
): z.input<typeof ListObjectsResponseSchema> {
  return {objects, prefixes: []};
}

export function storageBucketsBody(): z.input<typeof ListBucketsResponseSchema> {
  // Empty on purpose: nothing served buckets before this route existed, so an
  // empty listing keeps the GCS picker rendering exactly what it always has.
  return {buckets: []};
}

export function verifyAccessBody(): z.input<typeof VerifyAccessResponseSchema> {
  return {
    gs_uri: 'gs://test-artifacts/',
    orchestrator_access: {service_account: 'orchestrator', has_access: true, checked_at: NOW},
    can_proceed: true,
  };
}

export function reclaimableBody(): z.input<typeof ReclaimableSchema> {
  return {
    total: 0,
    pinned_devices: [],
    stopped_devices: [],
    stopped_hosts: [],
    expiring_hosts: [],
  };
}

export function provisioningStatsBody(): z.input<typeof ProvisioningStatsSchema> {
  return {
    window_days: 7,
    devices: {created: 12, errored: 1},
    hosts: {created: 4, errored: 0},
  };
}

export function operationsBody(items: OperationWire[]): z.input<typeof OperationListSchema> {
  return {operations: items, count: items.length};
}

export function snapshotListBody(): z.input<typeof SnapshotListSchema> {
  // Empty on purpose: the route existing with the right shape is the fix (the
  // device item route used to answer this path with a device entity); a
  // populated inventory would change what the snapshots card renders.
  return {snapshots: []};
}

export function iceConfigBody(): z.input<typeof IceConfigSchema> {
  return {
    client_servers: [{urls: ['stun:stun.l.google.com:19302']}],
    device_servers: [{urls: ['stun:stun.l.google.com:19302']}],
    ice_transport_policy: 'all',
  };
}

export function bugreportBody(
  bucket: string,
  object: string
): z.input<typeof BugreportArtifactSchema> {
  return {
    bucket,
    object,
    gs_uri: `gs://${bucket}/${object}`,
    download_path: `/v2/storage/buckets/${bucket}/objects/${object}`,
  };
}

export function recordingFetchBody(
  bucket: string,
  object: string
): z.input<typeof RecordingArtifactSchema> {
  return {
    bucket,
    object,
    gs_uri: `gs://${bucket}/${object}`,
    download_path: `/v2/storage/buckets/${bucket}/objects/${object}`,
  };
}

export function recordingsBody(state: ApiState): z.input<typeof RecordingListSchema> {
  return {recordings: state.recordings};
}

/**
 * A display capture: a 1x1 transparent PNG, which is a real image the browser
 * decodes rather than a placeholder string. The player shows one of these for
 * every instance it is not streaming.
 */
export function screenshotBody(): {screenshot_bytes: string; screenshot_mime_type: string} {
  return {
    screenshot_bytes:
      'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==',
    screenshot_mime_type: 'image/png',
  };
}

export function batchMembersBody(): {devices: BatchMemberWire[]} {
  return {
    devices: [
      {
        device_id: 'device-1',
        name: 'ci-run-000',
        status: 'running',
        host_id: 'host-1',
        adb_serial: '203.0.113.7:6520',
      },
      {
        device_id: 'device-2',
        name: 'ci-run-001',
        status: 'running',
        host_id: 'host-1',
        adb_serial: '203.0.113.7:6521',
      },
    ],
  };
}

/**
 * The pool the backend's derivation answers, in the backend's order: the pool
 * the create named, then its architecture, then a hard GPU requirement, then
 * the default. A create that named no pool still resolves to one.
 */
function derivedPool(requirements: Json): string {
  const named = requirements.pool;
  if (typeof named === 'string' && named !== '') {
    return named;
  }
  if (requirements.architecture === 'arm') {
    return 'arm';
  }
  return requirements.gpu_required === true ? 'gpu' : 'default';
}

/**
 * Admission against the fixture's host quota: the plan clamps to what fits,
 * which is what makes a short-fleet warning reachable in a test.
 */
export function batchAdmissionBody(
  state: ApiState,
  count: number,
  requirements: Json = {}
): BatchPlanWire {
  const admitted = Math.min(count, state.hostQuota);
  const perHost = 4;
  return {
    requested: count,
    admitted,
    new_hosts: Math.ceil(admitted / perHost),
    resolution: {
      build_name: state.builds[0]?.name ?? 'AOSP Latest',
      build_action: 'use',
      host_action: 'provision',
      machine_type: 'n1-standard-8',
      zone: 'europe-west4-a',
      pool: derivedPool(requirements),
      placement_reasons: ['most free host capacity', 'no active cooldown'],
    },
    warnings: state.batchPlanWarnings,
  };
}

/**
 * AIP-163 dry run: the would-be device (no id) plus, for declarative requests,
 * a resolution mirroring the backend's placement logic against the fixture
 * state (place on the first host, else provision).
 */
export function devicePlanBody(
  state: ApiState,
  body: Json
): {name?: unknown; status: string; resolution?: ResolutionWire} {
  if (!body.requirements) {
    return {name: body.name, status: 'pending'};
  }
  const targetHost = state.hosts[0];
  const defaultBuild = state.builds[0];
  const resolution: ResolutionWire = {
    pool: derivedPool(body.requirements as Json),
    ...(defaultBuild
      ? {build_id: defaultBuild.id, build_name: defaultBuild.name, build_action: 'use' as const}
      : {build_name: 'AOSP Latest Release Phone', build_action: 'register' as const}),
    ...(targetHost
      ? {host_id: targetHost.id, host_name: targetHost.name, host_action: 'place' as const}
      : {
          host_action: 'provision' as const,
          machine_type: 'n1-standard-8',
          zone: 'europe-west4-a',
        }),
  };
  return {name: body.name, status: 'pending', resolution};
}

export function createdHostBody(state: ApiState, body: Partial<HostWire> & Json): HostWire {
  return host({
    id: `host-${state.hosts.length + 1}`,
    name: body.name,
    zone: body.zone,
    machine_type: body.machine_type,
    max_devices: body.max_devices,
    provisioning_model: body.provisioning_model ?? 'STANDARD',
    status: 'provisioning',
    current_device_count: 0,
  });
}

/**
 * A reserved host with a lease and an observed VM stop time, the one shape
 * the console offers an extend on. The base fixture carries no lifecycle.
 */
export function leasedHost(overrides: Partial<HostWire> = {}): HostWire {
  return host({
    lifecycle: {
      mode: 'ephemeral',
      ttl_seconds: 36000,
      expires_at: '2099-01-01T12:00:00Z',
      protected_device_count: 0,
    },
    vm_terminates_at: '2099-01-20T12:00:00Z',
    ...overrides,
  });
}

/** What POST /v2/hosts/{id}/extend answers: the host with its lease raised. */
export function extendedHostBody(base: HostWire, expiresAt: string): HostWire {
  return {
    ...base,
    lifecycle: {...(base.lifecycle ?? {}), expires_at: expiresAt},
  };
}

export function createdDeviceBody(state: ApiState, body: Partial<DeviceWire> & Json): DeviceWire {
  return device({
    id: `device-${state.devices.length + 1}`,
    // Declarative creates resolve their placement server-side.
    name: body.name,
    host_id: body.host_id ?? state.hosts[0]?.id ?? 'auto-host',
    build_id: body.build_id ?? state.builds[0]?.id,
    gpu_mode: body.gpu_mode ?? 'auto',
    status: 'creating',
  });
}

export function createdBuildBody(state: ApiState, body: Partial<BuildWire> & Json): BuildWire {
  return build({
    id: `build-${state.builds.length + 1}`,
    name: body.name,
    source: body.source ?? 'android_build',
    branch: body.branch,
    target: body.target,
    is_default: body.is_default ?? false,
  });
}

export function createdBatchBody(
  state: ApiState,
  body: Partial<BatchWire> & Json,
  admission: BatchPlanWire
): BatchWire {
  return batch({
    id: `batch-${state.batches.length + 1}`,
    batch_id: `batch-${state.batches.length + 1}`,
    name_prefix: body.name_prefix,
    status: 'provisioning',
    total: admission.admitted,
    running: 0,
    fill_policy: body.fill_policy,
    placement: body.placement,
    created_by: state.user.email,
    ...admission,
  });
}
