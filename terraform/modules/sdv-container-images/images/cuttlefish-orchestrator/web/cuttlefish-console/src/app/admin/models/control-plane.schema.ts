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
  Connectivity,
  ControlPlaneSnapshot,
  DemandSplit,
  FleetPanel,
  FleetPolicy,
  FleetSchedule,
  FleetSettings,
  FleetStatus,
  FleetTier,
  GpuDegradation,
  LeaderLease,
  LiveTask,
  QueueDepth,
  SweepPolicy,
  TypeStateCount,
} from './control-plane.model';

export const QueueDepthSchema = z
  .object({
    pending: z.number(),
    leased: z.number(),
    failed: z.number(),
    oldest_pending_age_seconds: z.number(),
  })
  .transform(
    (data): QueueDepth => ({
      pending: data.pending,
      leased: data.leased,
      failed: data.failed,
      oldestPendingAgeSeconds: data.oldest_pending_age_seconds,
    })
  );

export const TypeStateCountSchema = z
  .object({
    type: z.string(),
    state: z.enum(['pending', 'leased', 'failed']),
    count: z.number(),
  })
  .transform(
    (data): TypeStateCount => ({
      type: data.type,
      state: data.state,
      count: data.count,
    })
  );

export const LiveTaskSchema = z
  .object({
    id: z.string(),
    type: z.string(),
    state: z.enum(['pending', 'leased']),
    resource_id: z.string().optional(),
    attempts: z.number(),
    max_attempts: z.number(),
    priority: z.number(),
    available_at: z.string(),
    lease_expiry: z.string().optional(),
    lease_holder: z.string().optional(),
    last_error: z.string().optional(),
    updated_at: z.string(),
  })
  .transform(
    (data): LiveTask => ({
      id: data.id,
      type: data.type,
      state: data.state,
      resourceId: data.resource_id ?? '',
      attempts: data.attempts,
      maxAttempts: data.max_attempts,
      priority: data.priority,
      availableAt: new Date(data.available_at),
      leaseExpiry: data.lease_expiry ? new Date(data.lease_expiry) : null,
      leaseHolder: data.lease_holder ?? '',
      lastError: data.last_error ?? '',
      updatedAt: new Date(data.updated_at),
    })
  );

export const LeaderLeaseSchema = z
  .object({
    name: z.string(),
    holder: z.string().optional(),
    acquired_at: z.string().optional(),
    lease_expiry: z.string().optional(),
    renewed_at: z.string(),
  })
  .transform(
    (data): LeaderLease => ({
      name: data.name,
      holder: data.holder ?? '',
      acquiredAt: data.acquired_at ? new Date(data.acquired_at) : null,
      expiry: data.lease_expiry ? new Date(data.lease_expiry) : null,
      renewedAt: new Date(data.renewed_at),
    })
  );

/** The sweep policy, shared by the snapshot and the settings pair. */
export const SweepPolicySchema = z
  .object({
    delete_orphan_vms: z.boolean(),
    orphan_cvds: z.boolean(),
    zombie_recreate: z.boolean(),
    reaper_dry_run: z.boolean(),
    is_set: z.boolean().optional(),
  })
  .transform(
    (data): SweepPolicy => ({
      deleteOrphanVms: data.delete_orphan_vms,
      orphanCvds: data.orphan_cvds,
      zombieRecreate: data.zombie_recreate,
      reaperDryRun: data.reaper_dry_run,
      isSet: data.is_set ?? false,
    })
  );

/**
 * One schedule window. Registered on its own so the drift gate compares it
 * against the backend's Schedule component rather than only reaching it through
 * whichever parent happens to be checked.
 */
export const FleetScheduleSchema = z
  .object({
    name: z.string(),
    hosts: z.number(),
    start: z.string(),
    duration_sec: z.number(),
    days: z.array(z.number()).optional(),
    location: z.string(),
  })
  .transform(
    (data): FleetSchedule => ({
      name: data.name,
      hosts: data.hosts,
      start: data.start,
      durationSec: data.duration_sec,
      // Absent is every day, which is the stored meaning. It is not a missing
      // value to be defaulted to "no days", which would render as a dead window.
      days: data.days ?? [],
      location: data.location,
    })
  );

/**
 * One pool's intent. Registered on its own so the drift gate compares it
 * against the backend's FleetTier component rather than only reaching it
 * through the policy that carries the map.
 */
export const FleetTierSchema = z
  .object({
    standby: z.number(),
    floor: z.number(),
    // Both lists are omitted when the pool stores none, which is the ordinary
    // state: no windows, and the catalog's own choice of shape.
    ladder: z.array(z.string()).optional(),
    schedules: z.array(FleetScheduleSchema).optional(),
  })
  .transform(
    (data): FleetTier => ({
      standby: data.standby,
      floor: data.floor,
      ladder: data.ladder ?? [],
      schedules: data.schedules ?? [],
    })
  );

/** The autoscaler policy, shared by the snapshot and the settings endpoint. */
export const FleetPolicySchema = z
  .object({
    mode: z.enum(['off', 'provision_only', 'on']),
    tiers: z.record(z.string(), FleetTierSchema).nullable(),
    floor: z.number(),
    demand_horizon_sec: z.number(),
    drain_grace_sec: z.number(),
    // Omitted when no order is stored, which is the catalog default rather than
    // a missing value, so it reads as the empty list every consumer treats as
    // "the catalog picks the shape".
    shape_ladder: z.array(z.string()).optional(),
    is_set: z.boolean().optional(),
  })
  .transform(
    (data): FleetPolicy => ({
      mode: data.mode,
      tiers: data.tiers ?? {},
      floor: data.floor,
      demandHorizonSec: data.demand_horizon_sec,
      drainGraceSec: data.drain_grace_sec,
      shapeLadder: data.shape_ladder ?? [],
      isSet: data.is_set ?? false,
    })
  );

/**
 * The settings endpoint's body: the policy plus its compiled bounds. Written
 * as its own object rather than extending the policy schema, because a
 * transform yields a ZodEffects that cannot be extended.
 */
export const FleetSettingsSchema = z
  .object({
    mode: z.enum(['off', 'provision_only', 'on']),
    tiers: z.record(z.string(), FleetTierSchema).nullable(),
    floor: z.number(),
    demand_horizon_sec: z.number(),
    drain_grace_sec: z.number(),
    shape_ladder: z.array(z.string()).optional(),
    is_set: z.boolean().optional(),
    max_standby: z.number(),
    max_floor: z.number(),
    max_demand_horizon_sec: z.number(),
    min_drain_grace_sec: z.number(),
    max_drain_grace_sec: z.number(),
    max_schedules: z.number(),
    max_ladder_rungs: z.number(),
  })
  .transform(
    (data): FleetSettings => ({
      mode: data.mode,
      tiers: data.tiers ?? {},
      floor: data.floor,
      demandHorizonSec: data.demand_horizon_sec,
      drainGraceSec: data.drain_grace_sec,
      shapeLadder: data.shape_ladder ?? [],
      isSet: data.is_set ?? false,
      maxStandby: data.max_standby,
      maxFloor: data.max_floor,
      maxDemandHorizonSec: data.max_demand_horizon_sec,
      minDrainGraceSec: data.min_drain_grace_sec,
      maxDrainGraceSec: data.max_drain_grace_sec,
      maxSchedules: data.max_schedules,
      maxLadderRungs: data.max_ladder_rungs,
    })
  );

/**
 * The demand window's misses, attributed. Registered on its own so the drift
 * gate compares it against the backend's DemandSplit component rather than only
 * reaching it through the status that carries it.
 */
export const DemandSplitSchema = z
  .object({
    servable: z.number(),
    unservable: z.number(),
    gpu_required: z.number(),
    needs_larger_host: z.number(),
    oversized_group: z.number(),
  })
  .transform(
    (data): DemandSplit => ({
      servable: data.servable,
      unservable: data.unservable,
      gpuRequired: data.gpu_required,
      needsLargerHost: data.needs_larger_host,
      oversizedGroup: data.oversized_group,
    })
  );

export const GpuDegradationSchema = z
  .object({degraded: z.number(), wanted: z.number()})
  .transform((data): GpuDegradation => ({degraded: data.degraded, wanted: data.wanted}));

/** The composed status, registered for drift on its own. */
export const FleetStatusSchema = z
  .object({
    mode: z.enum(['off', 'provision_only', 'on']),
    target: z.number(),
    standby: z.number(),
    draining: z.number(),
    misses: z.number(),
    demand: DemandSplitSchema,
    schedule: z.number(),
    floor: z.number(),
    total: z.number(),
    signal: z.enum(['standby', 'schedule', 'demand', 'floor']),
    limited_by: z.string().optional(),
    partial: z.boolean().optional(),
  })
  .transform(
    (data): FleetStatus => ({
      mode: data.mode,
      target: data.target,
      standby: data.standby,
      draining: data.draining,
      misses: data.misses,
      demand: data.demand,
      schedule: data.schedule,
      floor: data.floor,
      total: data.total,
      signal: data.signal,
      limitedBy: data.limited_by ?? '',
      partial: data.partial ?? false,
    })
  );

export const FleetPanelSchema = z
  .object({
    status: FleetStatusSchema,
    policy: FleetPolicySchema,
    device_slots: z.number(),
    gpu_degradation: GpuDegradationSchema,
  })
  .transform(
    (data): FleetPanel => ({
      status: data.status,
      policy: data.policy,
      deviceSlots: data.device_slots,
      gpuDegradation: data.gpu_degradation,
    })
  );

const ConnectivitySchema = z
  .object({
    ingress_mode: z.enum(['public', 'private', 'relay']),
    transport_policy: z.enum(['all', 'relay']),
    stun_servers: z.array(z.string()).nullable(),
    turn_client_servers: z.array(z.string()).nullable(),
    turn_device_servers: z.array(z.string()).nullable(),
    turn_realm: z.string().optional(),
    credential_ttl_seconds: z.number(),
    credentials_mintable: z.boolean(),
    problems: z.array(z.string()).nullable(),
  })
  .transform(
    (data): Connectivity => ({
      ingressMode: data.ingress_mode,
      transportPolicy: data.transport_policy,
      stunServers: data.stun_servers ?? [],
      turnClientServers: data.turn_client_servers ?? [],
      turnDeviceServers: data.turn_device_servers ?? [],
      turnRealm: data.turn_realm ?? '',
      credentialTtlSeconds: data.credential_ttl_seconds,
      credentialsMintable: data.credentials_mintable,
      problems: data.problems ?? [],
    })
  );

export const ControlPlaneSnapshotSchema = z
  .object({
    now: z.string(),
    queue: QueueDepthSchema,
    type_counts: z.array(TypeStateCountSchema).nullable(),
    live_tasks: z.array(LiveTaskSchema).nullable(),
    leaders: z.array(LeaderLeaseSchema).nullable(),
    workers_per_instance: z.number(),
    sweeps: SweepPolicySchema,
    fleet: FleetPanelSchema,
    connectivity: ConnectivitySchema,
  })
  .transform(
    (data): ControlPlaneSnapshot => ({
      now: new Date(data.now),
      queue: data.queue,
      typeCounts: data.type_counts ?? [],
      liveTasks: data.live_tasks ?? [],
      leaders: data.leaders ?? [],
      workersPerInstance: data.workers_per_instance,
      sweeps: data.sweeps,
      fleet: data.fleet,
      connectivity: data.connectivity,
    })
  );
