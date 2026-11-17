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
 * The registry of every Zod schema the console parses responses with, keyed by
 * the OpenAPI component name it corresponds to. Two gates consume it:
 * check-schema-drift.ts compares each schema against the spec, and
 * check-fixture-shapes.ts requires each one to be either exercised by an e2e
 * fixture or excused with a written reason. Registering a schema here is what
 * puts it under both gates. An unregistered schema is silently unchecked.
 */

import {ArtifactBucketsSchema} from '../../src/app/admin/models/artifact-buckets.model';
import {AuditEntrySchema, AuditPageSchema} from '../../src/app/admin/models/audit.schema';
import {
  AutoscalerLimitsSchema,
  AutoscalerSnapshotSchema,
  FleetHostSchema,
  FleetPoolSchema,
  GateStatusSchema,
  KeeperActivitySchema,
  LadderRungSchema,
  ShapeCensusCellSchema,
  StandbyRecommendationSchema,
} from '../../src/app/admin/models/autoscaler.schema';
import {
  CapacityCellSchema,
  CapacityFleetSchema,
  CapacityPoolSchema,
  CapacityRegionSchema,
  CapacitySnapshotSchema,
  CapacityZoneSchema,
  SpotAdviceCellSchema,
  SpotAdviceHealthSchema,
  SpotCapacitySchema,
  SpotFleetCountSchema,
  ZoneCooldownSchema,
} from '../../src/app/admin/models/capacity.schema';
import {
  AdminZoneListSchema,
  AdminZoneSchema,
  CatalogFreshnessSchema,
  EnabledZonesSchema,
} from '../../src/app/admin/models/catalog-admin.schema';
import {
  ControlPlaneSnapshotSchema,
  DemandSplitSchema,
  FleetPanelSchema,
  FleetPolicySchema,
  FleetScheduleSchema,
  FleetSettingsSchema,
  FleetStatusSchema,
  FleetTierSchema,
  GpuDegradationSchema,
  LeaderLeaseSchema,
  LiveTaskSchema,
  QueueDepthSchema,
  SweepPolicySchema,
  TypeStateCountSchema,
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
  ProvisioningOutcomeSchema,
  ProvisioningStatsSchema,
  ReclaimableDeviceSchema,
  ReclaimableHostSchema,
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
  BatchSchema,
} from '../../src/app/batches/models/batch.schema';
import {
  ArtifactSourceSchema,
  BuildListResponseSchema,
  BuildSchema,
} from '../../src/app/builds/models/build.schema';
import {
  BOOT_OBSERVATION_SCHEMA,
  BugreportArtifactSchema,
  DEVICE_LIFECYCLE_SCHEMA,
  DeviceListResponseSchema,
  DeviceSchema,
  InstanceSchema,
  RecordingArtifactSchema,
  RecordingListSchema,
} from '../../src/app/devices/models/device.schema';
import {IceConfigSchema, IceServerSchema} from '../../src/app/devices/models/ice.schema';
import {
  OperationListSchema,
  OperationMetadataSchema,
  OperationProblemSchema,
  OperationSchema,
} from '../../src/app/devices/models/operation.schema';
import {SnapshotListSchema, SnapshotSchema} from '../../src/app/devices/models/snapshot.schema';
import {
  HOST_LIFECYCLE_SCHEMA,
  HostListResponseSchema,
  HostSchema,
} from '../../src/app/hosts/models/host.schema';
import {
  MachineTypeListResponseSchema,
  MachineTypeSchema,
} from '../../src/app/machine-types/models/machine-type.schema';
import {QuotaResponseSchema} from '../../src/app/quota/models/quota.schema';
import {PlatformLifetimesSchema} from '../../src/app/shared/models/platform-lifetimes.schema';
import {ResolutionSchema} from '../../src/app/shared/models/resolution.schema';
import {
  ZoneAvailabilityListSchema,
  ZoneAvailabilitySchema,
} from '../../src/app/shared/models/zone-availability.schema';
import {
  AccessCheckResultSchema,
  BucketSchema,
  ListBucketsResponseSchema,
  ListObjectsResponseSchema,
  StorageObjectSchema,
  VerifyAccessResponseSchema,
} from '../../src/app/storage/models/storage.schema';

/** OpenAPI component name → the Zod schema the console parses it with. */
export const ENTITY_SCHEMAS: Record<string, unknown> = {
  GetMeOutputBody: UserSchema,
  Device: DeviceSchema,
  DeviceLifecycle: DEVICE_LIFECYCLE_SCHEMA,
  BootObservation: BOOT_OBSERVATION_SCHEMA,
  Instance: InstanceSchema,
  ListDevicesOutputBody: DeviceListResponseSchema,
  Resolution: ResolutionSchema,
  // The ICE configuration served for a device's media, and one server entry.
  Configuration: IceConfigSchema,
  Server: IceServerSchema,
  // Operation handles. `response` is untyped in the spec by design (one type
  // serves every verb), so only the envelope is comparable, which is exactly
  // the part that would silently break the card if it drifted.
  Operation: OperationSchema,
  Metadata: OperationMetadataSchema,
  Problem: OperationProblemSchema,
  ListOperationsOutputBody: OperationListSchema,
  Snapshot: SnapshotSchema,
  ListSnapshotsOutputBody: SnapshotListSchema,
  Host: HostSchema,
  HostLifecycle: HOST_LIFECYCLE_SCHEMA,
  ListHostsOutputBody: HostListResponseSchema,
  Build: BuildSchema,
  ArtifactSpec: ArtifactSourceSchema,
  ListBuildsOutputBody: BuildListResponseSchema,
  BatchSummary: BatchSchema,
  BatchMember: BatchMemberSchema,
  ListBatchesOutputBody: BatchListResponseSchema,
  MachineType: MachineTypeSchema,
  ListMachineTypesOutputBody: MachineTypeListResponseSchema,
  ZoneAvailability: ZoneAvailabilitySchema,
  ListZoneAvailabilityOutputBody: ZoneAvailabilityListSchema,
  QuotaResponse: QuotaResponseSchema,
  HostImageConfig: HostImageConfigSchema,
  ImageStatus: HostImageStatusSchema,
  ListHostImageConfigsOutputBody: HostImageConfigListResponseSchema,
  // The eight settings pairs the Settings page reads. Registering them is what
  // puts is_set under the gate: an unregistered schema is never compared, so
  // the check stays green while the field silently disappears.
  // ArtifactBucketsBody is registered further down, with the storage schemas.
  GetZonePinOutputBody: StringSettingSchema,
  GetDefaultGPUMachineTypeOutputBody: StringSettingSchema,
  GetQuotaMarginOutputBody: NumberSettingSchema,
  GetV1ZoneHintOutputBody: BooleanSettingSchema,
  GetSpotAutoDefaultOutputBody: BooleanSettingSchema,
  SpotMinUptimeBody: SpotMinUptimeSchema,
  DockerHostBody: DockerHostSettingSchema,
  GetDefaultUpstreamBuildOutputBody: DefaultUpstreamBuildSchema,
  LifetimesBody: PlatformLifetimesSchema,
  StatsOutputBody: PlatformStatsSchema,
  TaskStatsOutputBody: QueueStatsSchema,
  FailedTaskItem: FailedTaskSchema,
  ListFailedTasksOutputBody: FailedTaskListSchema,
  ControlPlaneOutputBody: ControlPlaneSnapshotSchema,
  // The Capacity page's snapshot and every part of it. Registering each one is
  // what puts it under BOTH gates. The board's verdicts are the page's whole
  // answer, and an unregistered cell schema would let one disappear unnoticed.
  CapacityOutputBody: CapacitySnapshotSchema,
  CapacityFleetItem: CapacityFleetSchema,
  CapacityRegionItem: CapacityRegionSchema,
  CapacityZoneItem: CapacityZoneSchema,
  CapacityCellItem: CapacityCellSchema,
  CapacityPoolItem: CapacityPoolSchema,
  CapacitySpotCell: SpotAdviceCellSchema,
  ZoneCooldownItem: ZoneCooldownSchema,
  SpotPanelItem: SpotCapacitySchema,
  SpotFleetCountItem: SpotFleetCountSchema,
  SpotAdviceHealth: SpotAdviceHealthSchema,
  // The Autoscaler page's two bodies and their parts. Registering each one is
  // what puts it under BOTH gates. An unregistered schema is silently
  // unchecked, which is how a field disappears from a page nobody was watching.
  AutoscalerOutputBody: AutoscalerSnapshotSchema,
  AutoscalerLimits: AutoscalerLimitsSchema,
  GateStatus: GateStatusSchema,
  FleetHostItem: FleetHostSchema,
  FleetPoolItem: FleetPoolSchema,
  ShapeCensusItem: ShapeCensusCellSchema,
  LadderRungItem: LadderRungSchema,
  KeeperActivity: KeeperActivitySchema,
  StandbyRecommendation: StandbyRecommendationSchema,
  SweepPolicy: SweepPolicySchema,
  FleetPolicy: FleetPolicySchema,
  FleetPolicyBody: FleetSettingsSchema,
  FleetStatus: FleetStatusSchema,
  FleetTier: FleetTierSchema,
  FleetPanelItem: FleetPanelSchema,
  DemandSplit: DemandSplitSchema,
  GPUDegradation: GpuDegradationSchema,
  FleetSchedule: FleetScheduleSchema,
  QueueDepth: QueueDepthSchema,
  TypeStateCountItem: TypeStateCountSchema,
  LiveTaskItem: LiveTaskSchema,
  LeaderLeaseItem: LeaderLeaseSchema,
  AdminZone: AdminZoneSchema,
  ListAdminZonesOutputBody: AdminZoneListSchema,
  SetEnabledZonesOutputBody: EnabledZonesSchema,
  CatalogFreshnessOutputBody: CatalogFreshnessSchema,
  // The curation view of the machine families. Registering the shape schema
  // too is what puts `fit` and `viable` under both gates: the matrix renders
  // server numbers, so a field that silently vanished would take the page's
  // whole capacity claim with it.
  AdminFamily: AdminFamilySchema,
  FamilyShape: FamilyShapeSchema,
  ListMachineFamiliesAdminOutputBody: AdminFamilyListSchema,
  AuditEntryItem: AuditEntrySchema,
  ListAuditOutputBody: AuditPageSchema,
  ReclaimableDevice: ReclaimableDeviceSchema,
  ReclaimableHost: ReclaimableHostSchema,
  ReclaimableOutputBody: ReclaimableSchema,
  ProvisioningOutcome: ProvisioningOutcomeSchema,
  ProvisioningStatsOutputBody: ProvisioningStatsSchema,
  Bucket: BucketSchema,
  ListBucketsResponse: ListBucketsResponseSchema,
  Object: StorageObjectSchema,
  ListObjectsResponse: ListObjectsResponseSchema,
  AccessCheckResult: AccessCheckResultSchema,
  VerifyAccessResponse: VerifyAccessResponseSchema,
  BugreportDeviceOutputBody: BugreportArtifactSchema,
  ArtifactBucketsBody: ArtifactBucketsSchema,
  FetchRecordingOutputBody: RecordingArtifactSchema,
  ListRecordingsOutputBody: RecordingListSchema,
};
