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

import {z} from 'zod';
import type {
  AutoscalerLimits,
  AutoscalerSnapshot,
  DemandSample,
  FleetHost,
  FleetOccupant,
  FleetPool,
  GateStatus,
  KeeperActivity,
  LadderRung,
  ShapeCensusCell,
  StandbyRecommendation,
} from './autoscaler.model';
import {FleetPanelSchema, FleetStatusSchema} from './control-plane.schema';

const GateReasonSchema = z.enum([
  'acting',
  'no_leader',
  'mode_off',
  'provision_only',
  'quota_reserved',
  'capped',
  'target_met',
]);

const HostClassSchema = z.enum(['reserved', 'fleet', 'dedicated', 'batch']);

const StandbyTermSchema = z.enum([
  'class',
  'placeable',
  'not_degraded',
  'shareable',
  'idle',
  'lease',
]);

export const AutoscalerLimitsSchema = z
  .object({
    max_standby_hosts: z.number(),
    max_topups_per_cycle: z.number(),
    max_shortens_per_cycle: z.number(),
    max_collapses_per_cycle: z.number(),
    quota_reserve_hosts: z.number(),
    cycle_seconds: z.number(),
    provision_lead_seconds: z.number(),
    host_image_backed_slots: z.number(),
    min_shorten_age_seconds: z.number(),
  })
  .transform(
    (d): AutoscalerLimits => ({
      maxStandbyHosts: d.max_standby_hosts,
      maxTopUpsPerCycle: d.max_topups_per_cycle,
      maxShortensPerCycle: d.max_shortens_per_cycle,
      maxCollapsesPerCycle: d.max_collapses_per_cycle,
      quotaReserveHosts: d.quota_reserve_hosts,
      cycleSeconds: d.cycle_seconds,
      provisionLeadSeconds: d.provision_lead_seconds,
      hostImageBackedSlots: d.host_image_backed_slots,
      minShortenAgeSeconds: d.min_shorten_age_seconds,
    })
  );

export const GateStatusSchema = z
  .object({
    reason: GateReasonSchema,
    deficit: z.number(),
    surplus: z.number(),
    quota_available: z.number(),
    quota_spendable: z.number(),
    leader_held: z.boolean(),
    leader_holder: z.string().optional(),
  })
  .transform(
    (d): GateStatus => ({
      reason: d.reason,
      deficit: d.deficit,
      surplus: d.surplus,
      quotaAvailable: d.quota_available,
      quotaSpendable: d.quota_spendable,
      leaderHeld: d.leader_held,
      leaderHolder: d.leader_holder ?? '',
    })
  );

const FleetOccupantSchema = z
  .object({
    name: z.string(),
    instances: z.number(),
    errored: z.boolean().optional(),
  })
  .transform(
    (d): FleetOccupant => ({
      name: d.name,
      instances: d.instances,
      errored: d.errored ?? false,
    })
  );

export const FleetHostSchema = z
  .object({
    id: z.string(),
    name: z.string(),
    zone: z.string(),
    machine_type: z.string().optional(),
    gpu_type: z.string().optional(),
    gpu_count: z.number(),
    status: z.string(),
    class: HostClassSchema,
    origin: z.enum(['user', 'auto', 'pool', 'batch']).optional(),
    pool: z.string(),
    max_devices: z.number(),
    backed_slots: z.number(),
    live_instances: z.number(),
    error_instances: z.number(),
    occupants: z.array(FleetOccupantSchema).nullable(),
    standby: z.boolean(),
    failing_term: StandbyTermSchema.optional(),
    shorten_order: z.number().optional(),
    expires_at: z.string().optional(),
    created_at: z.string(),
  })
  .transform(
    (d): FleetHost => ({
      id: d.id,
      name: d.name,
      zone: d.zone,
      machineType: d.machine_type ?? '',
      gpuType: d.gpu_type ?? '',
      gpuCount: d.gpu_count,
      status: d.status,
      // Renamed off the wire: `class` is a reserved word in enough contexts
      // that carrying it through the app is a trap with no upside.
      hostClass: d.class,
      origin: d.origin ?? '',
      pool: d.pool,
      maxDevices: d.max_devices,
      backedSlots: d.backed_slots,
      liveInstances: d.live_instances,
      errorInstances: d.error_instances,
      occupants: d.occupants ?? [],
      standby: d.standby,
      // Absent means counted. Defaulting it to a term would put a reason on
      // every host that has none.
      failingTerm: d.failing_term ?? '',
      shortenOrder: d.shorten_order ?? 0,
      expiresAt: d.expires_at ? new Date(d.expires_at) : null,
      createdAt: new Date(d.created_at),
    })
  );

export const ShapeCensusCellSchema = z
  .object({
    machine_type: z.string(),
    gpu_type: z.string().optional(),
    gpu_count: z.number(),
    class: HostClassSchema,
    status: z.string(),
    count: z.number(),
  })
  .transform(
    (d): ShapeCensusCell => ({
      machineType: d.machine_type,
      gpuType: d.gpu_type ?? '',
      gpuCount: d.gpu_count,
      hostClass: d.class,
      status: d.status,
      count: d.count,
    })
  );

export const LadderRungSchema = z
  .object({
    machine_type: z.string(),
    machine_family: z.string(),
    slots: z.number(),
    zones: z.number(),
    stocked_out_zones: z.number(),
    newest_stockout_at: z.string().optional(),
    gpu_type: z.string().optional(),
    gpu_count: z.number().optional(),
    landing: z.boolean().optional(),
    refused: z.string().optional(),
  })
  .transform(
    (d): LadderRung => ({
      machineType: d.machine_type,
      machineFamily: d.machine_family,
      slots: d.slots,
      zones: d.zones,
      stockedOutZones: d.stocked_out_zones,
      newestStockoutAt: d.newest_stockout_at ? new Date(d.newest_stockout_at) : null,
      gpuType: d.gpu_type ?? '',
      gpuCount: d.gpu_count ?? 0,
      landing: d.landing ?? false,
      refused: d.refused ?? '',
    })
  );

export const FleetPoolSchema = z
  .object({
    pool: z.string(),
    status: FleetStatusSchema,
    deficit: z.number(),
    surplus: z.number(),
    reason: GateReasonSchema,
    // Omitted when the pool stores no order, which is the catalog's own choice
    // of shape rather than a missing value.
    ladder: z.array(LadderRungSchema).optional(),
  })
  .transform(
    (d): FleetPool => ({
      pool: d.pool,
      status: d.status,
      deficit: d.deficit,
      surplus: d.surplus,
      reason: d.reason,
      ladder: d.ladder ?? [],
    })
  );

export const KeeperActivitySchema = z
  .object({
    provisions: z.number(),
    rescues: z.number(),
    shortens: z.number(),
    collapses: z.number(),
  })
  .transform(
    (d): KeeperActivity => ({
      provisions: d.provisions,
      rescues: d.rescues,
      shortens: d.shortens,
      collapses: d.collapses,
    })
  );

export const AutoscalerSnapshotSchema = z
  .object({
    now: z.string(),
    fleet: FleetPanelSchema,
    limits: AutoscalerLimitsSchema,
    gate: GateStatusSchema,
    pools: z.array(FleetPoolSchema).nullable(),
    hosts: z.array(FleetHostSchema).nullable(),
    shapes: z.array(ShapeCensusCellSchema).nullable(),
    // Absent rather than null, and absent means no order is stored, so the
    // catalog's default shape applies, which density_machine_type names.
    ladder: z.array(LadderRungSchema).optional(),
    activity: KeeperActivitySchema,
    activity_window_sec: z.number(),
    arm_image_ready: z.boolean(),
    effective_density: z.number(),
    density_machine_type: z.string().optional(),
    demand_horizon_sec: z.number(),
  })
  .transform(
    (d): AutoscalerSnapshot => ({
      now: new Date(d.now),
      fleet: d.fleet,
      limits: d.limits,
      gate: d.gate,
      pools: d.pools ?? [],
      hosts: d.hosts ?? [],
      shapes: d.shapes ?? [],
      ladder: d.ladder ?? [],
      activity: d.activity,
      activityWindowSec: d.activity_window_sec,
      armImageReady: d.arm_image_ready,
      effectiveDensity: d.effective_density,
      densityMachineType: d.density_machine_type ?? '',
      demandHorizonSec: d.demand_horizon_sec,
    })
  );

const DemandSampleSchema = z
  .object({at: z.string(), peak: z.number()})
  .transform((d): DemandSample => ({at: new Date(d.at), peak: d.peak}));

export const StandbyRecommendationSchema = z
  .object({
    standby: z.number(),
    sufficient: z.boolean(),
    peak_from: z.string().optional(),
    peak_to: z.string().optional(),
    servable: z.number(),
    unservable: z.number(),
    series: z.array(DemandSampleSchema).nullable(),
    from: z.string(),
    to: z.string(),
    partial: z.boolean().optional(),
  })
  .transform(
    (d): StandbyRecommendation => ({
      standby: d.standby,
      sufficient: d.sufficient,
      peakFrom: d.peak_from ? new Date(d.peak_from) : null,
      peakTo: d.peak_to ? new Date(d.peak_to) : null,
      servable: d.servable,
      unservable: d.unservable,
      series: d.series ?? [],
      from: new Date(d.from),
      to: new Date(d.to),
      partial: d.partial ?? false,
    })
  );
