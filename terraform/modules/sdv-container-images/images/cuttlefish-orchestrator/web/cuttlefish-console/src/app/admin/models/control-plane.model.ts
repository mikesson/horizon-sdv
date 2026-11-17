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

/** Queue depth by state, from GET /v2/admin/control-plane. */
export interface QueueDepth {
  pending: number;
  leased: number;
  failed: number;
  oldestPendingAgeSeconds: number;
}

/** One cell of the task-type × state breakdown. */
export interface TypeStateCount {
  type: string;
  state: 'pending' | 'leased' | 'failed';
  count: number;
}

/** A pending or leased task, one row of the queue's current working set. */
export interface LiveTask {
  id: string;
  type: string;
  state: 'pending' | 'leased';
  resourceId: string;
  attempts: number;
  maxAttempts: number;
  /** Claim tier: 0 = user intent (edge), -100 = reconciler resync. */
  priority: number;
  availableAt: Date;
  /** Visibility timeout while leased; null for pending tasks. */
  leaseExpiry: Date | null;
  leaseHolder: string;
  /** Most recent failure cause; set while a task waits out retry backoff. */
  lastError: string;
  updatedAt: Date;
}

/**
 * The lease the whole periodic plane elects on. Every background loop runs on
 * whichever instance holds this row. It must stay the string the backend elects
 * under (leaderelection.BackgroundPlaneLease); a row named anything else is one
 * no running loop gates on.
 */
export const BACKGROUND_PLANE_LEASE = 'background-plane';

/** One leader-election lease; the periodic plane runs under a single one. */
export interface LeaderLease {
  name: string;
  /** Instance currently leading; empty when the lease is unheld. */
  holder: string;
  acquiredAt: Date | null;
  expiry: Date | null;
  renewedAt: Date;
}

/**
 * What each background loop may do on its own. The three sweeps are "may
 * act"; reaperDryRun is "stop acting", so its safe value is the opposite of
 * the other three.
 */
export interface SweepPolicy {
  deleteOrphanVms: boolean;
  orphanCvds: boolean;
  zombieRecreate: boolean;
  reaperDryRun: boolean;
  /** False means the compiled defaults alone apply, and nobody chose this. */
  isSet: boolean;
}

/** The switches an operator can move; nil fields are left alone. */
export type SweepPolicyPatch = Partial<Omit<SweepPolicy, 'isSet'>>;

/** How much of the fleet autoscaler is switched on. */
export type AutoscaleMode = 'off' | 'provision_only' | 'on';

/**
 * A recurring wall-clock window that raises the target while it is open. It is
 * the operator declaring demand they know is coming, where the demand signal
 * can only report demand that already arrived.
 */
export interface FleetSchedule {
  name: string;
  hosts: number;
  /** Wall-clock start in this window's own zone, as HH:MM. */
  start: string;
  durationSec: number;
  /** Weekdays it opens on, 0 = Sunday. Empty is every day, not "none". */
  days: number[];
  /** IANA zone name the start is read in; never 'Local'. */
  location: string;
}

/**
 * The pool every machine belongs to until a create says otherwise, and the one
 * key an unconfigured policy populates. It keys the `tiers` map on the wire;
 * everything on screen calls it a pool.
 */
export const DEFAULT_FLEET_TIER = 'default';

/**
 * The pools this platform partitions its machines into, in the order the
 * console offers them. A closed set, because a pool exists only where the
 * platform can route a create into it and knows what machine to buy for it, and
 * both are compiled into the server.
 *
 * The server stays the authority: a write naming a pool it does not have is
 * refused by name, so this list can only ever offer too little.
 */
export const FLEET_POOL_NAMES = [DEFAULT_FLEET_TIER, 'gpu', 'arm'] as const;

/**
 * One pool's standing-capacity intent. Every value here is that pool's own: the
 * keeper holds each pool to its own bounds against its own machines, so a pool
 * short of its floor buys while another sits at target.
 */
export interface FleetTier {
  standby: number;
  /** Machines this pool keeps running whether or not devices fill them. */
  floor: number;
  /**
   * The shapes this pool's machines are built as, in preference order. Which
   * shapes are admissible depends on the pool that owns them, which is why a
   * ladder is a property of a pool rather than a list pools point at.
   */
  ladder: string[];
  schedules: FleetSchedule[];
}

/**
 * The autoscaler policy: what the operator asked for. The compiled bounds ride
 * alongside so nothing here is hardcoded console-side.
 */
export interface FleetPolicy {
  mode: AutoscaleMode;
  tiers: Record<string, FleetTier>;
  /**
   * The DEFAULT pool's floor, published beside the map for the surfaces that
   * ask about a fleet rather than about a pool. The keeper holds each pool to
   * the floor on its own entry above; this one decides nothing.
   */
  floor: number;
  demandHorizonSec: number;
  drainGraceSec: number;
  /** The default pool's shape order, published beside the map for the same reason. */
  shapeLadder: string[];
  /** False means at least one field is the compiled default, chosen by nobody. */
  isSet: boolean;
}

/** The compiled bounds the fleet settings endpoint publishes. */
interface FleetBounds {
  maxStandby: number;
  maxFloor: number;
  maxDemandHorizonSec: number;
  minDrainGraceSec: number;
  maxDrainGraceSec: number;
  maxSchedules: number;
  maxLadderRungs: number;
}

/** The policy as the settings endpoint serves it: intent plus its bounds. */
export type FleetSettings = FleetPolicy & FleetBounds;

/**
 * Every miss the demand window held, attributed. These are COUNTS, where
 * FleetStatus.misses over the same window is a peak-concurrent figure: how much
 * demand arrived and how much was in flight at once are different questions, so
 * the two are never added and a difference between them is not a discrepancy.
 */
export interface DemandSplit {
  /** Misses a standby host could have absorbed, the only ones that move the target. */
  servable: number;
  unservable: number;
  /**
   * The axes. A miss failing two is counted under both, so these do not sum to
   * unservable. Each names a separate thing to fix, never a share of a whole.
   */
  gpuRequired: number;
  needsLargerHost: number;
  oversizedGroup: number;
}

/**
 * Creates that wanted a GPU and were placed without one, over the demand window.
 * Both terms, because a degradation count decides nothing without the population
 * it came out of.
 */
export interface GpuDegradation {
  degraded: number;
  wanted: number;
}

/**
 * What the autoscaler currently computes: the composed target beside what the
 * fleet actually holds. Every number is server-derived, and the console never
 * recomputes a target from parts.
 */
export interface FleetStatus {
  mode: AutoscaleMode;
  target: number;
  standby: number;
  draining: number;
  misses: number;
  /** Every miss the window held, including the ones misses above excludes. */
  demand: DemandSplit;
  /**
   * What the open schedule windows asked for at this instant; 0 when none is
   * open. Served rather than re-derived, so a surface can tell a schedule that
   * lost the max from one that was never configured.
   */
  schedule: number;
  /** Machines that stand whether or not devices fill them; reclamation stops here. */
  floor: number;
  /** Machines the fleet is running right now, occupied or free, which is what the floor is held against. */
  total: number;
  /**
   * Which input is spending. `floor` is the one that is not a term of the
   * target. It says the buffer was satisfied and a machine is being bought
   * anyway, because the floor is short.
   */
  signal: 'standby' | 'schedule' | 'demand' | 'floor';
  /** Set when a compiled bound clipped the target. */
  limitedBy: string;
  /** The demand window held more misses than one read returns. */
  partial: boolean;
}

/** The autoscaler card: the composed status beside the policy behind it. */
export interface FleetPanel {
  status: FleetStatus;
  policy: FleetPolicy;
  /** Device capacity the target stands for (target × auto-host density), server-derived. */
  deviceSlots: number;
  gpuDegradation: GpuDegradation;
}

/**
 * The media plane, read-only. Terraform owns every value, so the page states
 * facts, with the combinations that cannot stream as explicit problems.
 */
export interface Connectivity {
  ingressMode: 'public' | 'private' | 'relay';
  transportPolicy: 'all' | 'relay';
  stunServers: string[];
  /** The same relay machines at their two addresses, which is the point. */
  turnClientServers: string[];
  turnDeviceServers: string[];
  turnRealm: string;
  credentialTtlSeconds: number;
  credentialsMintable: boolean;
  problems: string[];
}

/** One-call control-plane snapshot for the Operations page. */
export interface ControlPlaneSnapshot {
  /** Server time of the snapshot; ages/countdowns are computed against it. */
  now: Date;
  queue: QueueDepth;
  typeCounts: TypeStateCount[];
  liveTasks: LiveTask[];
  leaders: LeaderLease[];
  workersPerInstance: number;
  sweeps: SweepPolicy;
  fleet: FleetPanel;
  connectivity: Connectivity;
}

/** A pending task still waiting out its retry backoff. */
export function isRetrying(task: LiveTask): boolean {
  return task.state === 'pending' && task.attempts > 0;
}

/** Reconciler-enqueued periodic work (claims behind user intent). */
export function isResync(task: LiveTask): boolean {
  return task.priority < 0;
}
