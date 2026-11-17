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

import type {FleetPanel, FleetStatus} from './control-plane.model';

/**
 * The autoscaler page's own snapshot, from GET /v2/admin/autoscaler.
 *
 * Every number here is server-derived. The page's whole rule is that nothing it
 * shows was computed by adding two figures together on this side, because a
 * console that recomputes a target is a console that can disagree with the loop
 * it describes.
 */

/** Whose lifecycle rules apply to a host. Only `fleet` is the autoscaler's. */
export type HostClass = 'reserved' | 'fleet' | 'dedicated' | 'batch';

/** Which path created a host. `auto` and `pool` are both fleet class. */
type HostOrigin = 'user' | 'auto' | 'pool' | 'batch';

/**
 * The term of the standby predicate that excluded a host. Derived server-side
 * beside the predicate itself, because re-implemented here it would disagree
 * with the keeper's count the first time a term moved.
 */
export type StandbyTerm = 'class' | 'placeable' | 'not_degraded' | 'shareable' | 'idle' | 'lease';

/** What is limiting the current keeper cycle; `acting` means nothing is. */
export type GateReason =
  | 'acting'
  | 'no_leader'
  | 'mode_off'
  | 'provision_only'
  | 'quota_reserved'
  | 'capped'
  | 'target_met';

/**
 * The compiled limits. They are shown and cannot be set, which tells the next
 * person not to add environment variables for them. They also explain an
 * apparently stuck fleet, since a target of 10 against a standby of 2
 * converges over several cycles.
 */
export interface AutoscalerLimits {
  maxStandbyHosts: number;
  maxTopUpsPerCycle: number;
  maxShortensPerCycle: number;
  maxCollapsesPerCycle: number;
  quotaReserveHosts: number;
  cycleSeconds: number;
  provisionLeadSeconds: number;
  /** Slots the host image preallocates a TAP set for; past this a slot is promised, not backed. */
  hostImageBackedSlots: number;
  minShortenAgeSeconds: number;
}

/** Why the keeper is or is not moving, with the arithmetic behind it. */
export interface GateStatus {
  reason: GateReason;
  deficit: number;
  surplus: number;
  quotaAvailable: number;
  /** Available minus the reserve: what the buffer may actually spend. */
  quotaSpendable: number;
  leaderHeld: boolean;
  leaderHolder: string;
}

/**
 * One device on a host, sized by the instance slots it costs. Never a unit: a
 * device can want eight slots, and drawing it as one is the arithmetic that
 * oversubscribes a host silently.
 */
export interface FleetOccupant {
  name: string;
  instances: number;
  /** An error row still holds its slots, so the host is full while running nothing. */
  errored: boolean;
}

/** One host as the autoscaler's lens sees it. */
export interface FleetHost {
  id: string;
  name: string;
  zone: string;
  machineType: string;
  gpuType: string;
  gpuCount: number;
  status: string;
  hostClass: HostClass;
  origin: HostOrigin | '';
  /** Which subset of the fleet it was bought for; every machine names one. */
  pool: string;
  /** Slots the host promises. */
  maxDevices: number;
  /** Slots it can actually run. Every total sums THIS, never the promise. */
  backedSlots: number;
  liveInstances: number;
  errorInstances: number;
  occupants: FleetOccupant[];
  standby: boolean;
  /** Empty when the host is counted as standby. */
  failingTerm: StandbyTerm | '';
  /** 1-based position in the order a reduction would reclaim; 0 = not next. */
  shortenOrder: number;
  expiresAt: Date | null;
  createdAt: Date;
}

/**
 * One shape of the fleet's order, as the provisioning walk sees it right now.
 *
 * The two counts are zones rather than observations because that is the question
 * the walk asks: a shape is stepped past only when every zone it could go to is
 * already known to be out of capacity, and one zone left is a zone worth trying.
 */
export interface LadderRung {
  machineType: string;
  /** Stockouts are observed per family, so shapes sharing one share their state. */
  machineFamily: string;
  /** Device slots a machine built here is packed to. */
  slots: number;
  zones: number;
  stockedOutZones: number;
  /** When the newest standing stockout was seen; null when none stands. */
  newestStockoutAt: Date | null;
  /** GPU a machine built here carries; empty when none does. */
  gpuType: string;
  gpuCount: number;
  /** True on at most one shape, and on none when every one is stocked out. */
  landing: boolean;
  /**
   * Why the walk no longer takes this stored shape; '' on one it still does.
   * Every other field is zero when it is set.
   */
  refused: string;
}

/**
 * One pool: the two contracts it is held to, the gap either leaves, and what is
 * limiting it.
 *
 * The gap is served rather than subtracted here because it is not a
 * subtraction. Two independent bounds on one population compose into it, and
 * a page that re-derived it would be free to disagree with the loop it draws.
 */
export interface FleetPool {
  pool: string;
  /** This pool's composed picture: its target, what it holds, and the derivation. */
  status: FleetStatus;
  /** Machines this pool is short of; 0 when both its bounds are met. */
  deficit: number;
  /** Standing machines beyond both bounds; 0 when there are none. */
  surplus: number;
  /** What is limiting this pool's half of the cycle; `acting` means nothing is. */
  reason: GateReason;
  /** This pool's own shape order, judged; empty when it stores none. */
  ladder: LadderRung[];
}

/** One cell of the fleet's shape census. */
export interface ShapeCensusCell {
  machineType: string;
  gpuType: string;
  gpuCount: number;
  hostClass: HostClass;
  status: string;
  count: number;
}

/** What the keeper actually did over the activity window, from the audit log. */
export interface KeeperActivity {
  provisions: number;
  rescues: number;
  shortens: number;
  collapses: number;
}

/** The autoscaler page's whole snapshot. */
export interface AutoscalerSnapshot {
  /** Server time of the snapshot; ages and countdowns are computed against it. */
  now: Date;
  fleet: FleetPanel;
  limits: AutoscalerLimits;
  gate: GateStatus;
  /**
   * One entry per pool, the default first, the composition split the way the
   * keeper acts on it. With one pool its single entry restates the fleet block,
   * which is what makes the page's regions and its masthead reconcile by
   * construction rather than by agreement.
   */
  pools: FleetPool[];
  hosts: FleetHost[];
  shapes: ShapeCensusCell[];
  /** The default pool's shape order, judged; empty when it stores none. */
  ladder: LadderRung[];
  activity: KeeperActivity;
  activityWindowSec: number;
  /** Whether an arm64 host can be built here: an arm family with an enabled host image. */
  armImageReady: boolean;
  /** The multiplier from a target in hosts to capacity in device slots. */
  effectiveDensity: number;
  /** The shape the effective density was bounded by; '' when nothing judged it. */
  densityMachineType: string;
  demandHorizonSec: number;
}

/** One point of the concurrency series the step chart draws. */
export interface DemandSample {
  at: Date;
  peak: number;
}

/**
 * The observation window's one decision, from
 * GET /v2/admin/autoscaler/recommendation.
 *
 * Its own request rather than a field on the polled snapshot: it is a weekly
 * statistic over a multi-day scan, and the snapshot polls every few seconds.
 */
export interface StandbyRecommendation {
  standby: number;
  /**
   * False means the window held too few servable misses for the peak to mean
   * anything. A page MUST NOT render `standby` as a recommendation then. 0 from
   * a week with little traffic reads as "measured, and the answer is none", and
   * 0 is also a perfectly ordinary thing to set on purpose.
   */
  sufficient: boolean;
  peakFrom: Date | null;
  peakTo: Date | null;
  servable: number;
  unservable: number;
  series: DemandSample[];
  from: Date;
  to: Date;
  /** The read hit its sample bound, so every count is a floor. */
  partial: boolean;
}

/** Whether a mode may lower a lease, the console's copy of the reclaim gate. */
export function reclaims(mode: FleetPanel['status']['mode']): boolean {
  return mode === 'on';
}

/**
 * Whether a shape has no zone left to try, the console's copy of the walk's
 * rule, including its edge. A shape no zone offers at all is stepped past for
 * the same reason it is when every zone is out.
 */
export function stockedOut(rung: LadderRung): boolean {
  return rung.stockedOutZones >= rung.zones;
}
