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

import {poolFamily} from '../../../shared/models/pool-copy';
import type {
  AutoscalerSnapshot,
  FleetHost,
  FleetPool,
  GateReason,
  HostClass,
} from '../../models/autoscaler.model';
import {reclaims, stockedOut} from '../../models/autoscaler.model';
import {DEFAULT_FLEET_TIER} from '../../models/control-plane.model';
import {bandTotals, fleetOrder, groupByClass, ordinal} from './fleet-band.model';
import type {CanvasTier, MachineCard, MachineState} from './machine-card.model';
import {cardFor, ghostsFor, STATE_WORD, stateOf} from './machine-card.model';

/**
 * One snapshot in, the whole drawing out. A region per pool, its banks, cards
 * and clusters, the engine, the strip, and every sentence they say.
 *
 * Derived here rather than in the components so the rules that cost defects
 * (a machine counted in the pool it was bought for, a gap that is each pool's
 * own and not the fleet's, a lease-lowering never drawn as permitted under
 * provision_only) are assertable as arithmetic. The components render this and
 * compute nothing.
 *
 * Pools are a grouping inside the one pass this always was. The hosts are
 * walked once into their pools, and nothing is re-scanned per pool.
 */

export type {CanvasTier, MachineCard} from './machine-card.model';

/**
 * The breakpoints, in machines. Count arithmetic and never measurement. The
 * same snapshot has to draw identically on every refresh, and a layout that
 * reads its own rendered size is a feedback loop that cannot promise that.
 *
 * They also bound the DOM. Full cards stop at a few hundred slot nodes, compact
 * cards are one small card each, clusters are one tile per state, so the worst
 * case is bounded at every fleet size instead of optimized for.
 */
const FULL_CARDS_MAX = 16;
const COMPACT_MAX = 48;

export function tierFor(machines: number): CanvasTier {
  if (machines <= FULL_CARDS_MAX) {
    return 'cards';
  }
  return machines <= COMPACT_MAX ? 'compact' : 'clusters';
}

/**
 * Machines that are not the autoscaler's collapse one tier early. They are
 * context rather than subject, so they lose fidelity first.
 */
function coarser(tier: CanvasTier): CanvasTier {
  return tier === 'cards' ? 'compact' : 'clusters';
}

/** Machines of one state, rolled up. */
interface ClusterTile {
  readonly key: string;
  readonly count: number;
  readonly word: string;
  readonly detail: string;
  readonly members: readonly MachineCard[];
}

/** One side of a pool's region. */
interface Bank {
  readonly key: 'standing' | 'changing';
  readonly heading: string;
  /** The heading with the pool on it, because every region repeats the same two. */
  readonly label: string;
  readonly cards: readonly MachineCard[];
  readonly clusters: readonly ClusterTile[];
}

/** One class of machine the autoscaler does not own. */
interface StripGroup {
  readonly hostClass: HostClass;
  readonly label: string;
  readonly line: string;
  readonly cards: readonly MachineCard[];
}

/** One shape of a pool's order, as its one line draws it. */
interface LadderStep {
  readonly shape: string;
  /** No zone left to try, so the walk steps past it. */
  readonly out: boolean;
  readonly building: boolean;
}

/** Which shapes a pool builds, in order, and what the order is doing. */
interface LadderModel {
  readonly steps: readonly LadderStep[];
  readonly sentence: string;
}

/**
 * One pool: what it is held to, what is limiting it, and the machines it holds.
 *
 * A labelled region on the one rail rather than a tab, because the fact pools
 * create is a CROSS-pool one (one starving while another idles) and a switcher
 * hides exactly that.
 */
export interface PoolRegion {
  readonly key: string;
  readonly pool: string;
  readonly family: string;
  /** This pool's own fidelity, chosen from the machines standing in it. */
  readonly tier: CanvasTier;
  readonly target: number;
  /** Where this pool's target came from, in the words of the winning signal. */
  readonly from: string;
  /** `keeps 2 running · binding: machines kept free` */
  readonly contracts: string;
  readonly gap: string;
  readonly short: boolean;
  /** What is limiting this pool; empty when the cause belongs to the engine. */
  readonly cause: string;
  /** Null when this pool stores no order, which is the catalog's own shape. */
  readonly ladder: LadderModel | null;
  readonly banks: readonly Bank[];
  readonly totals: string;
  /** Said instead of the banks when no machine of this pool's stands. */
  readonly empty: string;
}

/** One pool's gap as the engine states it. */
interface PoolGap {
  readonly pool: string;
  readonly line: string;
  readonly short: boolean;
}

/** The loop itself: what it may do, what is stopping it, and what it has done. */
interface EngineModel {
  readonly provisions: boolean;
  readonly lowers: boolean;
  /** Why nothing is moving, or that it is. The server picks which cause holds. */
  readonly cause: string;
  readonly activity: string;
  /** One line per pool, the same fact each region states about itself. */
  readonly gaps: readonly PoolGap[];
}

/** Requests that found no machine, attributed to the pool they were judged into. */
interface UnplacedPool {
  readonly pool: string;
  readonly line: string;
}

/** The whole canvas. */
export interface CanvasModel {
  readonly regions: readonly PoolRegion[];
  readonly strip: readonly StripGroup[];
  readonly engine: EngineModel;
  readonly unplaced: readonly UnplacedPool[];
  /** One line per pool: what it holds against what its target stands for. */
  readonly totals: readonly string[];
}

/**
 * One sentence per gate reason. The server decides WHICH one holds, and that
 * is the part that must not be re-derived here. The console owns the wording.
 *
 * The quota cause matters most because it is otherwise silent and permanent.
 * The keeper never spends the last hosts of platform quota, so near the ceiling
 * top-ups simply stop with nothing erroring anywhere.
 */
const GATE_COPY: Record<GateReason, string> = {
  acting: 'The autoscaler is acting on this target.',
  no_leader:
    'No instance holds the autoscaler’s lease, so no cycle is running anywhere. Nothing moves until one does.',
  mode_off:
    'A target is composed every cycle and nothing is permitted to act on it. That is what off means, and it is the state an observation week runs in, not a failure.',
  provision_only:
    'There are more machines than the target asks for, and this mode may only add. Those machines run their leases out instead of being given back.',
  quota_reserved:
    'The host quota’s last places are kept for a person’s create, so buying is held back. Near the ceiling this is silent and permanent. Nothing errors, buying simply stops.',
  capped:
    'The gap is larger than one cycle may move, so the fleet reaches the target over several cycles rather than at once.',
  target_met: 'The fleet holds what the target asks for.',
};

/**
 * The two causes no pool may claim: nothing holds the lease, and the mode
 * permits nothing. Both are one per deployment and gate every pool at once, so
 * a region repeating them would state the same fault once per pool.
 */
const ENGINE_SCOPED: readonly GateReason[] = ['no_leader', 'mode_off'];

/** Where a target came from, in the words of the signal that produced it. */
const SIGNAL_COPY: Record<string, string> = {
  standby: 'the machines you asked to keep free',
  schedule: 'an open schedule window',
  demand: 'requests the window measured finding no machine',
  // The one signal that is not a term of the target. The free-machine count was
  // satisfied and a machine is being bought anyway, because the fleet is below
  // the number you asked to keep running. Without its own words it fell back to
  // printing the bare word "floor", which explains nothing.
  floor: 'the machines you asked to keep running, whatever fills them',
};

/** What each class is and what gives it back, for the strip. */
const CLASS_COPY: Record<HostClass, {label: string; reclaimer: string}> = {
  fleet: {label: 'The autoscaler’s', reclaimer: ''},
  dedicated: {
    label: 'Exclusive',
    reclaimer: 'fenced to one device group, and given back once that group ends',
  },
  batch: {label: 'Batch', reclaimer: 'given back when its batch settles'},
  reserved: {label: 'User-made', reclaimer: 'given back only by a person deleting it'},
};

/** Machines of one state as one tile, with the members kept for expansion. */
function clusterOf(cards: readonly MachineCard[]): ClusterTile {
  const orders = cards.map(card => card.order).filter(order => order > 0);
  return {
    key: cards[0].state,
    count: cards.length,
    word: STATE_WORD[cards[0].state],
    detail:
      orders.length > 0
        ? `${ordinal(Math.min(...orders))}-${ordinal(Math.max(...orders))} to give back`
        : '',
    members: cards,
  };
}

function clustersOf(cards: readonly MachineCard[]): ClusterTile[] {
  const order: MachineState[] = ['free', 'carrying', 'idle', 'arriving', 'leaving', 'ghost'];
  return order
    .map(state => cards.filter(card => card.state === state))
    .filter(group => group.length > 0)
    .map(clusterOf);
}

/**
 * One bank at the tier the count chose, and where that is clusters, its
 * members at full fidelity, because a cluster expands into full cards rather
 * than into a second listing of what the tile already said.
 */
function bankOf(
  key: Bank['key'],
  heading: string,
  pool: string,
  tier: CanvasTier,
  build: (at: CanvasTier) => MachineCard[]
): Bank {
  return {
    key,
    heading,
    label: `${heading}, ${pool} pool`,
    cards: tier === 'clusters' ? [] : build(tier),
    clusters: tier === 'clusters' ? clustersOf(build('cards')) : [],
  };
}

/**
 * One pool's totals.
 *
 * Backed slots, never Σ max_devices. On 16-slot machines against a 10-slot
 * image the promise overstates capacity by 60%, and the figure would look
 * authoritative. Planned capacity is the pool's target at the density it buys
 * at, which is the only figure comparable with it.
 */
function totalsOf(pool: FleetPool, hosts: readonly FleetHost[], density: number): string {
  const totals = bandTotals(hosts);
  const planned = pool.status.target * density;
  const parts = [
    `${pool.pool} · ${totals.live} of ${totals.backed} backed slots in use`,
    `${totals.devices} ${totals.devices === 1 ? 'device' : 'devices'} live`,
    `${planned} slots planned (target ${pool.status.target} × ${density})`,
  ];
  if (totals.errored > 0) {
    parts.splice(1, 0, `${totals.errored} held by rows that died`);
  }
  return parts.join(' · ');
}

/**
 * The density a pool's machines are bought at.
 *
 * Its own order's landing shape where it has one, because that is the machine
 * the next buy lands on; otherwise the fleet-wide figure, which is what a pool
 * with no order of its own is packed to.
 */
function densityOf(pool: FleetPool, snap: AutoscalerSnapshot): number {
  const landing = pool.ladder.find(rung => rung.landing);
  return landing ? landing.slots : snap.effectiveDensity;
}

function engineOf(snap: AutoscalerSnapshot): EngineModel {
  const status = snap.fleet.status;
  const hours = Math.round(snap.activityWindowSec / 3600);
  const a = snap.activity;
  return {
    provisions: status.mode !== 'off',
    lowers: reclaims(status.mode),
    cause: GATE_COPY[snap.gate.reason],
    activity: `over the last ${hours}h · ${a.provisions} bought · ${a.rescues} renewed · ${a.shortens} given back · ${a.collapses} collapsed`,
    gaps: snap.pools.map(pool => ({
      pool: pool.pool,
      line: gapOf(pool) || 'no gap',
      short: pool.deficit > 0,
    })),
  };
}

/** What a pool is short of or spare by; empty when both its bounds are met. */
function gapOf(pool: FleetPool): string {
  if (pool.deficit > 0) {
    return `${pool.deficit} short`;
  }
  return pool.surplus > 0 ? `${pool.surplus} spare` : '';
}

/**
 * A pool's shape order as one line, with what the walk did about it.
 *
 * The walk steps past the shape a pool prefers for one reason only, that every
 * zone it could go to is out of capacity. A landing below the first shape is
 * what lets the sentence name the preferred one as stocked out.
 */
function ladderOf(pool: FleetPool): LadderModel | null {
  const rungs = pool.ladder;
  if (rungs.length === 0) {
    return null;
  }
  const landing = rungs.findIndex(rung => rung.landing);
  const preferred = rungs[0].machineType;
  const sentence =
    landing < 0
      ? 'every shape in the order is stocked out, so nothing can be built'
      : landing === 0
        ? `building ${preferred}, the preferred shape`
        : `${preferred} stocked out, fell back to ${rungs[landing].machineType}`;
  return {
    steps: rungs.map(rung => ({
      shape: rung.machineType,
      out: stockedOut(rung),
      building: rung.landing,
    })),
    sentence,
  };
}

/**
 * One region per pool, each drawn from the machines stamped with its name.
 *
 * Semantic zoom is per pool. A twenty-machine default renders compact beside a
 * two-machine gpu pool's full cards, because fidelity answers "can this many
 * cards be read", which is a question about one region.
 */
function regionsOf(snap: AutoscalerSnapshot, byPool: Map<string, FleetHost[]>): PoolRegion[] {
  const lowers = reclaims(snap.fleet.status.mode);
  const changingStates: MachineState[] = ['arriving', 'leaving', 'free'];
  return snap.pools.map(pool => {
    const hosts = fleetOrder(byPool.get(pool.pool) ?? []);
    const tier = tierFor(hosts.length);
    const standing = hosts.filter(h => !changingStates.includes(stateOf(h, lowers)));
    const changing = hosts.filter(h => changingStates.includes(stateOf(h, lowers)));
    const isDefault = pool.pool === DEFAULT_FLEET_TIER;
    const status = pool.status;
    const floorBinds = status.total - status.floor < status.standby - status.target;
    return {
      key: pool.pool,
      pool: pool.pool,
      family: poolFamily(pool.pool),
      tier,
      target: status.target,
      from: SIGNAL_COPY[status.signal] ?? status.signal,
      contracts: `keeps ${status.floor} running · binding: ${
        floorBinds ? 'machines kept running' : 'machines kept free'
      }`,
      gap: gapOf(pool) || 'no gap',
      short: pool.deficit > 0,
      cause: ENGINE_SCOPED.includes(pool.reason) ? '' : GATE_COPY[pool.reason],
      ladder: ladderOf(pool),
      banks: [
        bankOf('standing', 'Standing · carrying devices', pool.pool, tier, at =>
          standing.map(host => cardFor(host, snap, at))
        ),
        bankOf('changing', 'Free, arriving, on the way out', pool.pool, tier, at => [
          ...changing.map(host => cardFor(host, snap, at)),
          ...ghostsFor(pool, snap, at, isDefault),
        ]),
      ],
      totals: totalsOf(pool, hosts, densityOf(pool, snap)),
      empty:
        hosts.length === 0
          ? `No machine of the autoscaler’s stands in this pool. The target is ${status.target}, from ${SIGNAL_COPY[status.signal] ?? status.signal}.`
          : '',
    };
  });
}

/**
 * The fleet's machines grouped by the pool they were bought for.
 *
 * One walk, keyed by the stamped column. A filter per pool would re-scan the
 * whole fleet once for every pool an operator configured.
 */
function poolHosts(hosts: readonly FleetHost[]): Map<string, FleetHost[]> {
  const byPool = new Map<string, FleetHost[]>();
  for (const host of hosts) {
    if (host.hostClass !== 'fleet') {
      continue;
    }
    const held = byPool.get(host.pool);
    if (held) {
      held.push(host);
    } else {
      byPool.set(host.pool, [host]);
    }
  }
  return byPool;
}

/**
 * Requests that found no machine, named by the pool they were judged into.
 *
 * Drawn only where there is more than one pool. With one, every miss belongs to
 * it, and the line would be the totals beside it said a second time.
 */
function unplacedOf(snap: AutoscalerSnapshot): UnplacedPool[] {
  if (snap.pools.length < 2) {
    return [];
  }
  const attributed: UnplacedPool[] = [];
  for (const pool of snap.pools) {
    const {servable, unservable} = pool.status.demand;
    const total = servable + unservable;
    if (total === 0) {
      continue;
    }
    attributed.push({
      pool: pool.pool,
      line: `${total} ${total === 1 ? 'request' : 'requests'} · ${servable} a free machine in that pool would have taken`,
    });
  }
  return attributed;
}

function stripOf(snap: AutoscalerSnapshot, stripTier: CanvasTier): StripGroup[] {
  return groupByClass(snap.hosts)
    .filter(group => group.hostClass !== 'fleet')
    .map(group => {
      const copy = CLASS_COPY[group.hostClass];
      const machines = `${group.hosts.length} ${group.hosts.length === 1 ? 'machine' : 'machines'}`;
      return {
        hostClass: group.hostClass,
        label: copy.label,
        line: `${machines} · ${group.live} of ${group.backed} backed slots in use · ${copy.reclaimer}`,
        cards:
          stripTier === 'clusters' ? [] : group.hosts.map(host => cardFor(host, snap, stripTier)),
      };
    });
}

/**
 * The whole drawing, from the snapshot and nothing else.
 *
 * Each region's banks split on what a reader is asking. What stands and holds
 * devices goes above; what is arriving, spare or on its way out goes below.
 * Both carry the reclaimer's own order, so the machines a reduction would reach
 * lead.
 */
export function buildCanvas(snap: AutoscalerSnapshot): CanvasModel {
  const regions = regionsOf(snap, poolHosts(snap.hosts));
  // The strip is not in a pool, so its fidelity is the deployment's. It is
  // context for every region at once, and a per-pool answer would be a fidelity
  // for machines no pool owns.
  const stripTier = coarser(tierFor(snap.hosts.length));
  return {
    regions,
    strip: stripOf(snap, stripTier),
    engine: engineOf(snap),
    unplaced: unplacedOf(snap),
    totals: regions.map(region => region.totals),
  };
}
