#!/usr/bin/env npx tsx

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
 * Fixture Shape Gate
 *
 * The e2e mock is a third copy of every wire shape: the drift gate compares
 * Zod schema ↔ OpenAPI spec, but nothing compared fixture ↔ schema, so a
 * required field added to a schema silently broke whichever fixture feeds it,
 * and only the opt-in browser suite could say so, as an "element not found"
 * seven layers from the cause. This gate parses exactly what the mock serves
 * (the composers in e2e/fixtures/api-fixtures.ts) through the schemas the SPA
 * parses with, so that class fails in seconds inside `npm run lint`.
 *
 * Static typing on the fixtures already pins the required key set and typo'd
 * optional keys; this runtime half covers what types cannot: enum and format
 * values, envelope composition, and the rows a `safeParseList` would silently
 * drop (the list schemas validate elements leniently, so each element is
 * parsed here through its item schema).
 *
 * Completeness rides the same registry the drift gate uses: every schema in
 * ENTITY_SCHEMAS must be exercised by a row below or excused in EXCEPTIONS
 * with a written reason. Otherwise a newly registered schema would start
 * life outside this gate without anyone deciding that.
 *
 * Usage: npm run check:fixture-shapes  (a leg of `npm run lint`)
 */

import {
  adminFamiliesBody,
  adminStatsBody,
  adminZonesBody,
  auditPageBody,
  autoscalerBody,
  autoscalerFleetBody,
  batchAdmissionBody,
  batchMembersBody,
  booleanSettingBody,
  bugreportBody,
  capacityBody,
  catalogFreshnessBody,
  collection,
  controlPlaneBody,
  createdBatchBody,
  createdBuildBody,
  createdDeviceBody,
  createdHostBody,
  defaultState,
  defaultUpstreamBuildBody,
  devicePlanBody,
  dockerHostBody,
  extendedHostBody,
  failedTasksBody,
  fleetSettingsBody,
  hostImageConfigListBody,
  iceConfigBody,
  leasedHost,
  lifetimesBody,
  machineTypeListBody,
  numberSettingBody,
  operationsBody,
  provisioningStatsBody,
  reclaimableBody,
  recordingFetchBody,
  recordingsBody,
  type StorageObjectWire,
  snapshotListBody,
  spotMinUptimeBody,
  standbyRecommendationBody,
  storageBucketsBody,
  storageObjectsBody,
  stringSettingBody,
  taskStatsBody,
  verifyAccessBody,
  zoneAvailabilityBody,
} from '../../e2e/fixtures/api-fixtures';
import {ArtifactBucketsSchema} from '../../src/app/admin/models/artifact-buckets.model';
import {AuditEntrySchema, AuditPageSchema} from '../../src/app/admin/models/audit.schema';
import {
  AutoscalerSnapshotSchema,
  StandbyRecommendationSchema,
} from '../../src/app/admin/models/autoscaler.schema';
import {CapacitySnapshotSchema} from '../../src/app/admin/models/capacity.schema';
import {
  AdminZoneListSchema,
  AdminZoneSchema,
  CatalogFreshnessSchema,
} from '../../src/app/admin/models/catalog-admin.schema';
import {
  ControlPlaneSnapshotSchema,
  FleetSettingsSchema,
} from '../../src/app/admin/models/control-plane.schema';
import {
  HostImageConfigListResponseSchema,
  HostImageConfigSchema,
  HostImageStatusSchema,
} from '../../src/app/admin/models/host-image-config.schema';
import {
  AdminFamilyListSchema,
  AdminFamilySchema,
  FamilyShapeSchema,
} from '../../src/app/admin/models/machine-family.schema';
import {
  ProvisioningStatsSchema,
  ReclaimableSchema,
} from '../../src/app/admin/models/operator-lens.schema';
import {PlatformStatsSchema} from '../../src/app/admin/models/platform-stats.schema';
import {
  BooleanSettingSchema,
  DefaultUpstreamBuildSchema,
  DockerHostSettingSchema,
  NumberSettingSchema,
  SpotMinUptimeSchema,
  StringSettingSchema,
} from '../../src/app/admin/models/settings.schema';
import {
  FailedTaskListSchema,
  FailedTaskSchema,
  QueueStatsSchema,
} from '../../src/app/admin/models/task-admin.schema';
import {UserSchema} from '../../src/app/auth/models/user.schema';
import {
  BatchListResponseSchema,
  BatchMemberSchema,
  BatchMembersResponseSchema,
  BatchPlanSchema,
  BatchSchema,
} from '../../src/app/batches/models/batch.schema';
import {BuildListResponseSchema, BuildSchema} from '../../src/app/builds/models/build.schema';
import {
  BugreportArtifactSchema,
  CreatePlanResponseSchema,
  DeviceListResponseSchema,
  DeviceSchema,
  RecordingArtifactSchema,
  RecordingListSchema,
} from '../../src/app/devices/models/device.schema';
import {IceConfigSchema} from '../../src/app/devices/models/ice.schema';
import {OperationListSchema, OperationSchema} from '../../src/app/devices/models/operation.schema';
import {SnapshotListSchema} from '../../src/app/devices/models/snapshot.schema';
import {HostListResponseSchema, HostSchema} from '../../src/app/hosts/models/host.schema';
import {
  MachineTypeListResponseSchema,
  MachineTypeSchema,
} from '../../src/app/machine-types/models/machine-type.schema';
import {QuotaResponseSchema} from '../../src/app/quota/models/quota.schema';
import {PlatformLifetimesSchema} from '../../src/app/shared/models/platform-lifetimes.schema';
import {
  ZoneAvailabilityListSchema,
  ZoneAvailabilitySchema,
} from '../../src/app/shared/models/zone-availability.schema';
import {
  ListBucketsResponseSchema,
  ListObjectsResponseSchema,
  StorageObjectSchema,
  VerifyAccessResponseSchema,
} from '../../src/app/storage/models/storage.schema';
import {ENTITY_SCHEMAS} from './schema-registry';

interface ParseResult {
  success: boolean;
  error?: {issues: Array<{path: Array<string | number>; message: string}>};
}

interface Parseable {
  safeParse(data: unknown): ParseResult;
}

interface Row {
  name: string;
  schema: unknown;
  body: unknown;
}

// One state, driven the way the mock drives it: the created entities are
// pushed so the list envelopes and element loops cover them too.
const state = defaultState();
const createdHost = createdHostBody(state, {
  name: 'gate-host',
  zone: 'us-central1-a',
  machine_type: 'n1-standard-8',
  max_devices: 4,
});
state.hosts.push(createdHost);
const createdDevice = createdDeviceBody(state, {name: 'gate-device'});
state.devices.push(createdDevice);
const createdBuild = createdBuildBody(state, {
  name: 'Gate Build',
  branch: 'aosp-main',
  target: 'aosp_cf_x86_64_phone-userdebug',
});
state.builds.push(createdBuild);
const admission = batchAdmissionBody(state, 8);
const createdBatch = createdBatchBody(state, {name_prefix: 'gate'}, admission);
state.batches.push(createdBatch);

const sampleObject: StorageObjectWire = {
  name: 'artifacts/my-device-device-1/bugreports/20260615-100000.zip',
  display_name: '20260615-100000.zip',
  bucket: 'test-artifacts',
  size: 4_200_000,
  content_type: 'application/zip',
  updated_at: '2026-06-15T10:00:00Z',
};

const each = <T>(name: string, schema: unknown, items: T[]): Row[] =>
  items.map((body, i) => ({name: `${name}[${i}]`, schema, body}));

const ROWS: Row[] = [
  {name: 'GET /v2/me', schema: UserSchema, body: state.user},
  {name: 'GET /v2/quota', schema: QuotaResponseSchema, body: state.quota},
  {name: 'GET /v2/devices', schema: DeviceListResponseSchema, body: collection(state.devices)},
  ...each('device', DeviceSchema, state.devices),
  {name: 'GET /v2/hosts', schema: HostListResponseSchema, body: collection(state.hosts)},
  ...each('host', HostSchema, state.hosts),
  {name: 'GET /v2/builds', schema: BuildListResponseSchema, body: collection(state.builds)},
  ...each('build', BuildSchema, state.builds),
  {
    name: 'GET /v2/batches',
    schema: BatchListResponseSchema,
    body: {items: state.batches, total: state.batches.length},
  },
  ...each('batch', BatchSchema, state.batches),
  {name: 'POST /v2/batches (validate_only)', schema: BatchPlanSchema, body: admission},
  {
    name: 'GET /v2/batches/{id}/devices',
    schema: BatchMembersResponseSchema,
    body: batchMembersBody(),
  },
  ...each('batch member', BatchMemberSchema, batchMembersBody().devices),
  {
    name: 'POST /v2/devices (validate_only)',
    schema: CreatePlanResponseSchema,
    body: devicePlanBody(state, {name: 'planned', requirements: {interaction: 'headless'}}),
  },
  {
    name: 'GET /v2/machine-types',
    schema: MachineTypeListResponseSchema,
    body: machineTypeListBody(state),
  },
  ...each('machine type', MachineTypeSchema, state.machineTypes),
  {
    name: 'GET /v2/zones/availability',
    schema: ZoneAvailabilityListSchema,
    body: zoneAvailabilityBody(state),
  },
  ...each('zone availability', ZoneAvailabilitySchema, state.zones),
  {
    name: 'GET /v2/admin/host-image-configs',
    schema: HostImageConfigListResponseSchema,
    body: hostImageConfigListBody(state),
  },
  ...each('host image config', HostImageConfigSchema, state.hostImageConfigs),
  ...each(
    'host image status',
    HostImageStatusSchema,
    state.hostImageConfigs.flatMap(c => (c.resolved ? [c.resolved] : []))
  ),
  {
    name: 'GET .../zone-pin',
    schema: StringSettingSchema,
    body: stringSettingBody(state.settings.zone_pin, true),
  },
  {
    name: 'GET .../quota-margin',
    schema: NumberSettingSchema,
    body: numberSettingBody(state.settings.quota_margin, false),
  },
  {
    name: 'GET .../v1-zone-hint',
    schema: BooleanSettingSchema,
    body: booleanSettingBody(state.settings.v1_zone_hint, true),
  },
  {name: 'GET .../lifetimes', schema: PlatformLifetimesSchema, body: lifetimesBody(state)},
  {name: 'GET .../docker-host', schema: DockerHostSettingSchema, body: dockerHostBody()},
  {name: 'GET .../fleet', schema: FleetSettingsSchema, body: fleetSettingsBody(state)},
  {name: 'GET .../spot-min-uptime', schema: SpotMinUptimeSchema, body: spotMinUptimeBody()},
  {
    name: 'GET .../default-upstream-build',
    schema: DefaultUpstreamBuildSchema,
    body: defaultUpstreamBuildBody(state),
  },
  {name: 'GET .../artifact-buckets', schema: ArtifactBucketsSchema, body: state.artifactBuckets},
  {name: 'GET /v2/admin/stats', schema: PlatformStatsSchema, body: adminStatsBody(state)},
  {name: 'GET /v2/admin/tasks/stats', schema: QueueStatsSchema, body: taskStatsBody(state)},
  {
    name: 'GET /v2/admin/tasks/failed',
    schema: FailedTaskListSchema,
    body: failedTasksBody(state.failedTasks),
  },
  ...each('failed task', FailedTaskSchema, state.failedTasks),
  {
    name: 'GET /v2/admin/control-plane',
    schema: ControlPlaneSnapshotSchema,
    body: controlPlaneBody(state),
  },
  {
    name: 'GET /v2/admin/capacity',
    schema: CapacitySnapshotSchema,
    body: capacityBody(state),
  },
  {
    name: 'GET /v2/admin/autoscaler',
    schema: AutoscalerSnapshotSchema,
    body: autoscalerBody(state),
  },
  {
    name: 'GET /v2/admin/autoscaler (fleet past the cluster breakpoint)',
    schema: AutoscalerSnapshotSchema,
    body: autoscalerFleetBody(state, 60),
  },
  {
    name: 'GET /v2/admin/autoscaler/recommendation',
    schema: StandbyRecommendationSchema,
    body: standbyRecommendationBody(),
  },
  {
    name: 'GET /v2/admin/catalog/freshness',
    schema: CatalogFreshnessSchema,
    body: catalogFreshnessBody(),
  },
  {name: 'GET /v2/admin/zones', schema: AdminZoneListSchema, body: adminZonesBody(state)},
  ...each('admin zone', AdminZoneSchema, state.adminZones),
  {
    name: 'GET /v2/admin/machine-families',
    schema: AdminFamilyListSchema,
    body: adminFamiliesBody(state),
  },
  ...each('machine family', AdminFamilySchema, state.machineFamilies),
  ...each(
    'family shape',
    FamilyShapeSchema,
    state.machineFamilies.flatMap(family => family.shapes)
  ),
  {name: 'GET /v2/admin/audit', schema: AuditPageSchema, body: auditPageBody(state.auditEntries)},
  ...each('audit entry', AuditEntrySchema, state.auditEntries),
  {name: 'GET /v2/admin/reclaimable', schema: ReclaimableSchema, body: reclaimableBody()},
  {
    name: 'GET /v2/admin/provisioning-stats',
    schema: ProvisioningStatsSchema,
    body: provisioningStatsBody(),
  },
  {
    name: 'GET /v2/operations',
    schema: OperationListSchema,
    body: operationsBody(state.openOperations),
  },
  ...each('operation', OperationSchema, state.openOperations),
  {
    name: 'GET /v2/storage/buckets/{b}/objects',
    schema: ListObjectsResponseSchema,
    body: storageObjectsBody([sampleObject]),
  },
  {name: 'storage object', schema: StorageObjectSchema, body: sampleObject},
  {name: 'GET /v2/storage/buckets', schema: ListBucketsResponseSchema, body: storageBucketsBody()},
  {
    name: 'POST /v2/storage/verify-access',
    schema: VerifyAccessResponseSchema,
    body: verifyAccessBody(),
  },
  {
    name: 'POST /v2/devices/{id}/bugreport',
    schema: BugreportArtifactSchema,
    body: bugreportBody('test-artifacts', sampleObject.name),
  },
  {
    name: 'POST /v2/devices/{id}/recordings/{n}/fetch',
    schema: RecordingArtifactSchema,
    body: recordingFetchBody('test-artifacts', 'artifacts/my-device-device-1/recordings/a.webm'),
  },
  {
    name: 'GET /v2/devices/{id}/recordings',
    schema: RecordingListSchema,
    body: recordingsBody(state),
  },
  {name: 'GET /v2/devices/{id}/snapshots', schema: SnapshotListSchema, body: snapshotListBody()},
  {name: 'GET /v2/devices/{id}/ice-config', schema: IceConfigSchema, body: iceConfigBody()},
  {name: 'GET /v2/hosts/{id} (leased)', schema: HostSchema, body: leasedHost()},
  {name: 'host lifecycle', schema: ENTITY_SCHEMAS['HostLifecycle'], body: leasedHost().lifecycle},
  {
    name: 'POST /v2/hosts/{id}/extend',
    schema: HostSchema,
    body: extendedHostBody(leasedHost(), '2099-01-02T12:00:00Z'),
  },
];

/**
 * Registry schemas with no row of their own. Each carries the reason; an entry
 * whose reason stops being true belongs in ROWS instead.
 */
const EXCEPTIONS: Array<{name: string; schema: unknown; reason: string}> = [
  {
    name: 'DeviceLifecycle',
    schema: ENTITY_SCHEMAS['DeviceLifecycle'],
    reason: 'optional sub-object of Device; the base fixture omits it on purpose',
  },
  {
    name: 'BootObservation',
    schema: ENTITY_SCHEMAS['BootObservation'],
    reason: 'parsed inside every device row, and the base fixture carries boot_observation',
  },
  {
    name: 'Instance',
    schema: ENTITY_SCHEMAS['Instance'],
    reason: 'parsed inside every device row, and the base fixture carries instances',
  },
  {
    name: 'Resolution',
    schema: ENTITY_SCHEMAS['Resolution'],
    reason: 'parsed inside the device plan and batch plan rows',
  },
  {
    name: 'Server (ICE)',
    schema: ENTITY_SCHEMAS['Server'],
    reason: 'parsed inside the ice-config row',
  },
  {
    name: 'Metadata (operation)',
    schema: ENTITY_SCHEMAS['Metadata'],
    reason: 'parsed inside every operation row',
  },
  {
    name: 'Problem (operation)',
    schema: ENTITY_SCHEMAS['Problem'],
    reason: 'optional error sub-object; no fixture carries a failed operation',
  },
  {
    name: 'Snapshot',
    schema: ENTITY_SCHEMAS['Snapshot'],
    reason: 'the inventory is served empty on purpose, and a populated one would change the card',
  },
  {
    name: 'ArtifactSpec',
    schema: ENTITY_SCHEMAS['ArtifactSpec'],
    reason: 'sub-object of url-source builds; the fixtures carry android_build sources only',
  },
  {
    name: 'CapacityFleetItem',
    schema: ENTITY_SCHEMAS['CapacityFleetItem'],
    reason: 'parsed inside the capacity row',
  },
  {
    name: 'CapacityRegionItem',
    schema: ENTITY_SCHEMAS['CapacityRegionItem'],
    reason: 'parsed inside the capacity row, and the fixture carries three rails',
  },
  {
    name: 'CapacityZoneItem',
    schema: ENTITY_SCHEMAS['CapacityZoneItem'],
    reason: 'parsed inside the capacity row, and the fixture carries four tiles',
  },
  {
    name: 'CapacityCellItem',
    schema: ENTITY_SCHEMAS['CapacityCellItem'],
    reason: 'parsed inside every capacity zone row',
  },
  {
    name: 'CapacityPoolItem',
    schema: ENTITY_SCHEMAS['CapacityPoolItem'],
    reason: 'parsed inside the capacity row, and the fixture carries six pools',
  },
  {
    name: 'CapacitySpotCell',
    schema: ENTITY_SCHEMAS['CapacitySpotCell'],
    reason: 'parsed inside the capacity zones and cells that carry advice',
  },
  {
    name: 'ZoneCooldownItem',
    schema: ENTITY_SCHEMAS['ZoneCooldownItem'],
    reason: 'parsed inside the capacity cells the fixture cools',
  },
  {
    name: 'SpotPanelItem',
    schema: ENTITY_SCHEMAS['SpotPanelItem'],
    reason: 'parsed inside the capacity row',
  },
  {
    name: 'SpotFleetCountItem',
    schema: ENTITY_SCHEMAS['SpotFleetCountItem'],
    reason: "parsed inside the capacity row's Spot block",
  },
  {
    name: 'SpotAdviceHealth',
    schema: ENTITY_SCHEMAS['SpotAdviceHealth'],
    reason: "parsed inside the capacity row's Spot block",
  },
  {
    name: 'SweepPolicy',
    schema: ENTITY_SCHEMAS['SweepPolicy'],
    reason: 'parsed inside the control-plane row',
  },
  {
    name: 'FleetPolicy',
    schema: ENTITY_SCHEMAS['FleetPolicy'],
    reason: 'parsed inside the control-plane row',
  },
  {
    name: 'FleetStatus',
    schema: ENTITY_SCHEMAS['FleetStatus'],
    reason: 'parsed inside the control-plane row',
  },
  {
    name: 'FleetPanelItem',
    schema: ENTITY_SCHEMAS['FleetPanelItem'],
    reason: 'parsed inside the control-plane row',
  },
  {
    name: 'DemandSplit',
    schema: ENTITY_SCHEMAS['DemandSplit'],
    reason: 'parsed inside the fleet status, which the control-plane row carries',
  },
  {
    name: 'GPUDegradation',
    schema: ENTITY_SCHEMAS['GPUDegradation'],
    reason: 'parsed inside the fleet panel, which the control-plane row carries',
  },
  {
    name: 'FleetSchedule',
    schema: ENTITY_SCHEMAS['FleetSchedule'],
    reason: 'parsed inside the fleet tier, which both fleet rows above carry',
  },
  {
    name: 'FleetTier',
    schema: ENTITY_SCHEMAS['FleetTier'],
    reason: 'parsed inside the fleet policy, which both fleet rows above carry',
  },
  {
    name: 'FleetPoolItem',
    schema: ENTITY_SCHEMAS['FleetPoolItem'],
    reason: 'parsed inside the autoscaler row, and the fixture carries the default pool',
  },
  {
    name: 'AutoscalerLimits',
    schema: ENTITY_SCHEMAS['AutoscalerLimits'],
    reason: 'parsed inside the autoscaler row',
  },
  {
    name: 'GateStatus',
    schema: ENTITY_SCHEMAS['GateStatus'],
    reason: 'parsed inside the autoscaler row',
  },
  {
    name: 'FleetHostItem',
    schema: ENTITY_SCHEMAS['FleetHostItem'],
    reason: 'parsed inside the autoscaler row, and the fixture carries four hosts',
  },
  {
    name: 'ShapeCensusItem',
    schema: ENTITY_SCHEMAS['ShapeCensusItem'],
    reason: 'parsed inside the autoscaler row',
  },
  {
    name: 'LadderRungItem',
    schema: ENTITY_SCHEMAS['LadderRungItem'],
    reason:
      'parsed inside the autoscaler row, and the fixture carries a stocked-out shape and the one it fell back to',
  },
  {
    name: 'KeeperActivity',
    schema: ENTITY_SCHEMAS['KeeperActivity'],
    reason: 'parsed inside the autoscaler row',
  },
  {
    name: 'QueueDepth',
    schema: ENTITY_SCHEMAS['QueueDepth'],
    reason: 'parsed inside the control-plane row',
  },
  {
    name: 'TypeStateCountItem',
    schema: ENTITY_SCHEMAS['TypeStateCountItem'],
    reason: 'parsed inside the control-plane row',
  },
  {
    name: 'LiveTaskItem',
    schema: ENTITY_SCHEMAS['LiveTaskItem'],
    reason: 'parsed inside the control-plane row',
  },
  {
    name: 'LeaderLeaseItem',
    schema: ENTITY_SCHEMAS['LeaderLeaseItem'],
    reason: 'parsed inside the control-plane row',
  },
  {
    name: 'SetEnabledZonesOutputBody',
    schema: ENTITY_SCHEMAS['SetEnabledZonesOutputBody'],
    reason: 'the zone-enablement PUT is mocked as a route, not as a fixture body',
  },
  {
    name: 'ReclaimableDevice',
    schema: ENTITY_SCHEMAS['ReclaimableDevice'],
    reason: 'the reclaimable panel is served empty on purpose',
  },
  {
    name: 'ReclaimableHost',
    schema: ENTITY_SCHEMAS['ReclaimableHost'],
    reason: 'the reclaimable panel is served empty on purpose',
  },
  {
    name: 'ProvisioningOutcome',
    schema: ENTITY_SCHEMAS['ProvisioningOutcome'],
    reason: 'parsed inside the provisioning-stats row',
  },
  {
    name: 'Bucket',
    schema: ENTITY_SCHEMAS['Bucket'],
    reason: 'the bucket listing is served empty on purpose, and nothing served buckets before',
  },
  {
    name: 'AccessCheckResult',
    schema: ENTITY_SCHEMAS['AccessCheckResult'],
    reason: 'parsed inside the verify-access row',
  },
];

const green = '\x1b[32m';
const red = '\x1b[31m';
const reset = '\x1b[0m';

let failures = 0;

for (const row of ROWS) {
  const result = (row.schema as Parseable).safeParse(row.body);
  if (!result.success) {
    failures++;
    console.log(`${red}✗${reset} ${row.name}`);
    for (const issue of result.error?.issues ?? []) {
      console.log(`    ${issue.path.join('.') || '(root)'}: ${issue.message}`);
    }
  }
}

const covered = new Set<unknown>(ROWS.map(r => r.schema));
for (const e of EXCEPTIONS) {
  if (e.schema === undefined) {
    failures++;
    console.log(`${red}✗${reset} exception "${e.name}" names no registry entry, so it is stale`);
    continue;
  }
  covered.add(e.schema);
}
for (const [name, schema] of Object.entries(ENTITY_SCHEMAS)) {
  if (!covered.has(schema)) {
    failures++;
    console.log(
      `${red}✗${reset} ${name}: registered schema with no fixture-parse row. ` +
        'Add a row (or a written exception) in scripts/gates/check-fixture-shapes.ts'
    );
  }
}

if (failures > 0) {
  console.log(`\n${red}✗ ${failures} fixture shape problem(s)${reset}`);
  console.log('The e2e mock serves these bodies; the SPA parses them with the schemas named');
  console.log('above. Fix the fixture in e2e/fixtures/api-fixtures.ts (or the schema).');
  process.exit(1);
}

console.log(
  `${green}✓ ${ROWS.length} fixture bodies parse; all ${
    Object.keys(ENTITY_SCHEMAS).length
  } registered schemas covered or excused${reset}`
);
