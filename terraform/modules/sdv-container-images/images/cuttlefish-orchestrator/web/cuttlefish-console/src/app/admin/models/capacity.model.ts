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
 * The capacity snapshot from GET /v2/admin/capacity: where the fleet can grow
 * right now. Every verdict and every number here is server-derived, and the
 * page sorts and renders while computing nothing.
 */

/** The zone/cell verdict vocabulary, worst-signal-wins. */
type CapacityVerdict = 'clear' | 'cooling' | 'blocked' | 'constrained' | 'no_signal' | 'disabled';

/** How much room a quota pool has left against the reserve margin. */
type PoolState = 'unknown' | 'open' | 'near_reserve' | 'at_wall';

/** A region's rail chip: the tightest pool it has, as a word. */
type RegionQuotaState = 'comfortable' | 'tight' | 'blocked' | 'unknown';

/** Spot obtainability bands, as the capacity-advice API documents them. */
type SpotBand = 'high' | 'medium' | 'low';

/** One synced Spot capacity-advice cell; absent means no signal. */
export interface SpotAdviceCell {
  machineType: string;
  /** Likelihood a single-VM Spot create succeeds (0.0-1.0). */
  obtainability: number;
  band: SpotBand;
  /** Estimated minimum run time before preemption; 0 = unreported. */
  uptimeTierSeconds: number;
  syncedAt: Date | null;
}

/** One zone_health cell, live or lapsed. */
export interface ZoneCooldown {
  machineFamily: string;
  bindingMetric: string;
  /** The binding metric as English, composed server-side. */
  metricLabel: string;
  /** Quota metrics cool every zone in the region, and not this one alone. */
  regional: boolean;
  consecutiveFailures: number;
  /** Whether the cell still repels placement at the snapshot's now. */
  inCooldown: boolean;
  cooldownUntil: Date | null;
  lastFailureAt: Date | null;
  lastReason: string;
}

/** One (zone × machine family) cell: every signal, composed into one verdict. */
export interface CapacityCell {
  machineFamily: string;
  machines: number;
  /** A subset of machines: the ones still coming up. */
  arriving: number;
  devices: number;
  verdict: CapacityVerdict;
  /** The composed reason, ready to render. */
  verdictDetail: string;
  coolingUntil: Date | null;
  /** The regional CPU pool this family is charged to; '' if it spends none. */
  quotaPool: string;
  quotaState: PoolState;
  spot: SpotAdviceCell | null;
  cooldown: ZoneCooldown | null;
}

/** One zone of the placement space, drawn as one tile on the board. */
export interface CapacityZone {
  zone: string;
  region: string;
  /** In the placement space (admin-curated enabled catalog). */
  enabled: boolean;
  /** What Compute Engine last said (UP/DOWN); '' when the snapshot is silent. */
  status: string;
  machines: number;
  arriving: number;
  devices: number;
  verdict: CapacityVerdict;
  verdictDetail: string;
  coolingUntil: Date | null;
  /** The zone's weakest advice cell, which is the tile's single Spot line. */
  spot: SpotAdviceCell | null;
  cells: CapacityCell[];
}

/** One region rail, carrying the quota verdict its tiles inherit. */
export interface CapacityRegion {
  region: string;
  machines: number;
  devices: number;
  zonesEnabled: number;
  zones: number;
  quotaState: RegionQuotaState;
  quotaPool: string;
  quotaUsage: number;
  quotaLimit: number;
}

/** One regional quota pool the fleet draws, with its reserve marked. */
export interface CapacityPool {
  region: string;
  /** Quota metric, e.g. CPUS or CPUS_PER_VM_FAMILY:C4. */
  pool: string;
  kind: 'cpu' | 'disk';
  /** The fleet's machine families that spend this pool. */
  families: string[];
  /** The preemptible twin, which Spot VMs draw. */
  spot: boolean;
  state: PoolState;
  usage: number;
  limit: number;
  /** Headroom ranking keeps back; a demand crossing it is refused. */
  reserve: number;
  syncedAt: Date | null;
}

/** The page's four-number answer about the fleet. */
export interface CapacityFleet {
  machines: number;
  devices: number;
  regions: number;
  zonesEnabled: number;
  /** Enabled zones whose verdict is clear. */
  zonesClear: number;
  /** Enabled zones a live cooldown is holding back. */
  zonesCooling: number;
  /** Enabled zones a regional quota wall condemns. */
  zonesBlocked: number;
  /** Enabled zones whose Spot obtainability is low; on-demand is unaffected. */
  zonesConstrained: number;
}

/** One provisioning-model × status cell of the fleet census. */
export interface SpotFleetCount {
  model: 'SPOT' | 'STANDARD';
  status: string;
  count: number;
}

/** The Spot capacity-advice sync's health. */
export interface SpotAdviceHealth {
  /** Whether the advice sync is enabled (CCO_SPOT_ADVICE). */
  enabled: boolean;
  /** Synced (zone × machine type) advice cells. */
  cells: number;
  newestSyncedAt: Date | null;
  /** Aged past the bound ranking consults, so placement no longer steers on it. */
  stale: boolean;
}

/**
 * The fleet's Spot capacity: the model split, 7-day preemption churn
 * (audit-derived, because rows die with the fleet), and advice-sync health.
 */
export interface SpotCapacity {
  fleet: SpotFleetCount[];
  /** The census above as the two halves the card states. */
  spotMachines: number;
  standardMachines: number;
  preempted7d: number;
  recovered7d: number;
  /** Preemptions in the window with no recovery recorded yet. */
  unrecovered7d: number;
  /** Mean recovery duration across the window's newest recoveries (0 = no data). */
  typicalRecoverySeconds: number;
  advice: SpotAdviceHealth;
}

/** One-call capacity snapshot for the Capacity page. */
export interface CapacitySnapshot {
  /** Server time of the snapshot; ages/countdowns are computed against it. */
  now: Date;
  fleet: CapacityFleet;
  regions: CapacityRegion[];
  zones: CapacityZone[];
  pools: CapacityPool[];
  spot: SpotCapacity;
  /** Percent of each quota limit ranking keeps in reserve. */
  reserveMarginPercent: number;
}
