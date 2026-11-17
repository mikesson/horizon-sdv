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
  CapacityCell,
  CapacityFleet,
  CapacityPool,
  CapacityRegion,
  CapacitySnapshot,
  CapacityZone,
  SpotAdviceCell,
  SpotAdviceHealth,
  SpotCapacity,
  SpotFleetCount,
  ZoneCooldown,
} from './capacity.model';

const VERDICT = z.enum(['clear', 'cooling', 'blocked', 'constrained', 'no_signal', 'disabled']);
const POOL_STATE = z.enum(['unknown', 'open', 'near_reserve', 'at_wall']);

export const SpotAdviceCellSchema = z
  .object({
    machine_type: z.string(),
    obtainability: z.number(),
    band: z.enum(['high', 'medium', 'low']),
    uptime_tier_s: z.number(),
    synced_at: z.string().optional(),
  })
  .transform(
    (data): SpotAdviceCell => ({
      machineType: data.machine_type,
      obtainability: data.obtainability,
      band: data.band,
      uptimeTierSeconds: data.uptime_tier_s,
      syncedAt: data.synced_at ? new Date(data.synced_at) : null,
    })
  );

export const ZoneCooldownSchema = z
  .object({
    machine_family: z.string(),
    binding_metric: z.string(),
    metric_label: z.string(),
    regional: z.boolean(),
    consecutive_failures: z.number(),
    in_cooldown: z.boolean(),
    cooldown_until: z.string().optional(),
    last_failure_at: z.string().optional(),
    last_reason: z.string().optional(),
  })
  .transform(
    (data): ZoneCooldown => ({
      machineFamily: data.machine_family,
      bindingMetric: data.binding_metric,
      metricLabel: data.metric_label,
      regional: data.regional,
      consecutiveFailures: data.consecutive_failures,
      inCooldown: data.in_cooldown,
      cooldownUntil: data.cooldown_until ? new Date(data.cooldown_until) : null,
      lastFailureAt: data.last_failure_at ? new Date(data.last_failure_at) : null,
      lastReason: data.last_reason ?? '',
    })
  );

export const CapacityCellSchema = z
  .object({
    machine_family: z.string(),
    machines: z.number(),
    arriving: z.number(),
    devices: z.number(),
    verdict: VERDICT,
    verdict_detail: z.string().optional(),
    cooling_until: z.string().optional(),
    quota_pool: z.string().optional(),
    quota_state: POOL_STATE,
    spot: SpotAdviceCellSchema.optional(),
    cooldown: ZoneCooldownSchema.optional(),
  })
  .transform(
    (data): CapacityCell => ({
      machineFamily: data.machine_family,
      machines: data.machines,
      arriving: data.arriving,
      devices: data.devices,
      verdict: data.verdict,
      verdictDetail: data.verdict_detail ?? '',
      coolingUntil: data.cooling_until ? new Date(data.cooling_until) : null,
      quotaPool: data.quota_pool ?? '',
      quotaState: data.quota_state,
      spot: data.spot ?? null,
      cooldown: data.cooldown ?? null,
    })
  );

export const CapacityZoneSchema = z
  .object({
    zone: z.string(),
    region: z.string(),
    enabled: z.boolean(),
    status: z.string(),
    machines: z.number(),
    arriving: z.number(),
    devices: z.number(),
    verdict: VERDICT,
    verdict_detail: z.string().optional(),
    cooling_until: z.string().optional(),
    spot: SpotAdviceCellSchema.optional(),
    cells: z.array(CapacityCellSchema).nullable(),
  })
  .transform(
    (data): CapacityZone => ({
      zone: data.zone,
      region: data.region,
      enabled: data.enabled,
      status: data.status,
      machines: data.machines,
      arriving: data.arriving,
      devices: data.devices,
      verdict: data.verdict,
      verdictDetail: data.verdict_detail ?? '',
      coolingUntil: data.cooling_until ? new Date(data.cooling_until) : null,
      spot: data.spot ?? null,
      cells: data.cells ?? [],
    })
  );

export const CapacityRegionSchema = z
  .object({
    region: z.string(),
    machines: z.number(),
    devices: z.number(),
    zones_enabled: z.number(),
    zones: z.number(),
    quota_state: z.enum(['comfortable', 'tight', 'blocked', 'unknown']),
    quota_pool: z.string().optional(),
    quota_usage: z.number().optional(),
    quota_limit: z.number().optional(),
  })
  .transform(
    (data): CapacityRegion => ({
      region: data.region,
      machines: data.machines,
      devices: data.devices,
      zonesEnabled: data.zones_enabled,
      zones: data.zones,
      quotaState: data.quota_state,
      quotaPool: data.quota_pool ?? '',
      quotaUsage: data.quota_usage ?? 0,
      quotaLimit: data.quota_limit ?? 0,
    })
  );

export const CapacityPoolSchema = z
  .object({
    region: z.string(),
    pool: z.string(),
    kind: z.enum(['cpu', 'disk']),
    families: z.array(z.string()).nullable(),
    spot: z.boolean(),
    state: POOL_STATE,
    usage: z.number().optional(),
    limit: z.number().optional(),
    reserve: z.number().optional(),
    synced_at: z.string().optional(),
  })
  .transform(
    (data): CapacityPool => ({
      region: data.region,
      pool: data.pool,
      kind: data.kind,
      families: data.families ?? [],
      spot: data.spot,
      state: data.state,
      usage: data.usage ?? 0,
      limit: data.limit ?? 0,
      reserve: data.reserve ?? 0,
      syncedAt: data.synced_at ? new Date(data.synced_at) : null,
    })
  );

export const CapacityFleetSchema = z
  .object({
    machines: z.number(),
    devices: z.number(),
    regions: z.number(),
    zones_enabled: z.number(),
    zones_clear: z.number(),
    zones_cooling: z.number(),
    zones_blocked: z.number(),
    zones_constrained: z.number(),
  })
  .transform(
    (data): CapacityFleet => ({
      machines: data.machines,
      devices: data.devices,
      regions: data.regions,
      zonesEnabled: data.zones_enabled,
      zonesClear: data.zones_clear,
      zonesCooling: data.zones_cooling,
      zonesBlocked: data.zones_blocked,
      zonesConstrained: data.zones_constrained,
    })
  );

export const SpotFleetCountSchema = z
  .object({
    model: z.enum(['SPOT', 'STANDARD']),
    status: z.string(),
    count: z.number(),
  })
  .transform(
    (data): SpotFleetCount => ({model: data.model, status: data.status, count: data.count})
  );

export const SpotAdviceHealthSchema = z
  .object({
    enabled: z.boolean(),
    cells: z.number(),
    newest_synced_at: z.string().optional(),
    stale: z.boolean(),
  })
  .transform(
    (data): SpotAdviceHealth => ({
      enabled: data.enabled,
      cells: data.cells,
      newestSyncedAt: data.newest_synced_at ? new Date(data.newest_synced_at) : null,
      stale: data.stale,
    })
  );

export const SpotCapacitySchema = z
  .object({
    fleet: z.array(SpotFleetCountSchema).nullable(),
    spot_machines: z.number(),
    standard_machines: z.number(),
    preempted_7d: z.number(),
    recovered_7d: z.number(),
    unrecovered_7d: z.number(),
    typical_recovery_seconds: z.number(),
    advice: SpotAdviceHealthSchema,
  })
  .transform(
    (data): SpotCapacity => ({
      fleet: data.fleet ?? [],
      spotMachines: data.spot_machines,
      standardMachines: data.standard_machines,
      preempted7d: data.preempted_7d,
      recovered7d: data.recovered_7d,
      unrecovered7d: data.unrecovered_7d,
      typicalRecoverySeconds: data.typical_recovery_seconds,
      advice: data.advice,
    })
  );

export const CapacitySnapshotSchema = z
  .object({
    now: z.string(),
    fleet: CapacityFleetSchema,
    regions: z.array(CapacityRegionSchema).nullable(),
    zones: z.array(CapacityZoneSchema).nullable(),
    pools: z.array(CapacityPoolSchema).nullable(),
    spot: SpotCapacitySchema,
    reserve_margin_percent: z.number(),
  })
  .transform(
    (data): CapacitySnapshot => ({
      now: new Date(data.now),
      fleet: data.fleet,
      regions: data.regions ?? [],
      zones: data.zones ?? [],
      pools: data.pools ?? [],
      spot: data.spot,
      reserveMarginPercent: data.reserve_margin_percent,
    })
  );
