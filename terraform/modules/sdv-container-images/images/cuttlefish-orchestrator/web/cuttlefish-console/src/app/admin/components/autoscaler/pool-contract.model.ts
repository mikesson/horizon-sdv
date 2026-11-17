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
import type {AutoscalerSnapshot, FleetPool} from '../../models/autoscaler.model';

/**
 * The masthead's arithmetic: one contract row per pool, composed from numbers
 * the server served and nothing else.
 *
 * Kept out of the component so the two things this row can get wrong (which
 * promise is binding, and whether a pool is short) are assertable as
 * arithmetic rather than as rendered text.
 */

/** One input of the max, with whether it produced the target. */
interface ContractTerm {
  readonly label: string;
  readonly value: string;
  readonly won: boolean;
}

/** One pool's row: both promises, which binds, and what it is short of. */
export interface ContractRow {
  readonly pool: string;
  readonly family: string;
  readonly target: number;
  readonly standby: number;
  readonly floor: number;
  readonly total: number;
  readonly terms: readonly ContractTerm[];
  /** True when the machines-kept-running promise is the tighter of the two. */
  readonly floorBinds: boolean;
  readonly binding: string;
  readonly bindingCopy: string;
  /** What this pool is short of or spare by, in words; empty when it is met. */
  readonly gap: string;
  /** A gap the pool has to close, which the words are coloured for. */
  readonly short: boolean;
}

const BINDING_FREE = 'machines kept free';
const BINDING_RUNNING = 'machines kept running';

/**
 * One contract row per pool, the default first, in the order the server serves
 * them in, which is the order the keeper acts on.
 */
export function buildContracts(snapshot: AutoscalerSnapshot): ContractRow[] {
  return snapshot.pools.map(pool => rowFor(snapshot, pool));
}

/**
 * Whether the ceiling on the composed target has clipped ANY pool.
 *
 * Asked across the pools because the ceiling the page states is every pool's
 * own added together. A single pool clamped is the fleet meeting a bound, and a
 * line saying otherwise would explain a target that is not the sum of its terms.
 */
export function ceilingReached(snapshot: AutoscalerSnapshot): boolean {
  return snapshot.pools.some(pool => pool.status.limitedBy !== '');
}

function rowFor(snapshot: AutoscalerSnapshot, pool: FleetPool): ContractRow {
  const status = pool.status;
  // The deciding promise is the one this pool has less room above. Every input
  // is a number the server served, and the comparison is the one behind its own
  // buy-and-release arithmetic (the larger shortfall buys, the smaller slack
  // releases) so it cannot disagree with the loop. Ties go to the free-machine
  // count, the cheaper explanation, exactly as they do in the target's own
  // composition.
  const floorBinds = status.total - status.floor < status.standby - status.target;
  return {
    pool: pool.pool,
    family: poolFamily(pool.pool),
    target: status.target,
    standby: status.standby,
    floor: status.floor,
    total: status.total,
    terms: termsOf(snapshot, pool),
    floorBinds,
    binding: floorBinds ? BINDING_RUNNING : BINDING_FREE,
    bindingCopy: floorBinds
      ? 'the fleet stops shrinking here, and a device filling a machine buys nothing'
      : 'a machine a device takes is replaced, so this many stay free',
    gap: gapOf(pool),
    short: pool.deficit > 0,
  };
}

/**
 * The three inputs in composition order. A hyphen rather than 0 for a schedule
 * with no open window, because 0 is a value somebody set and no window is the
 * absence of one.
 */
function termsOf(snapshot: AutoscalerSnapshot, pool: FleetPool): ContractTerm[] {
  const status = pool.status;
  const intent = snapshot.fleet.policy.tiers[pool.pool]?.standby ?? 0;
  return [
    {label: 'intent', value: String(intent), won: status.signal === 'standby'},
    {
      label: 'schedule',
      value: status.schedule > 0 ? String(status.schedule) : '-',
      won: status.signal === 'schedule',
    },
    {label: 'demand', value: String(status.misses), won: status.signal === 'demand'},
  ];
}

/**
 * What the pool is short of or spare by, in the words that carry it.
 *
 * Both directions are stated. A surplus is not a fault and is not coloured, but
 * a pool holding machines neither of its promises needs is the fact that
 * explains a reclamation nobody asked for.
 */
function gapOf(pool: FleetPool): string {
  if (pool.deficit > 0) {
    const machines = pool.deficit === 1 ? 'machine' : 'machines';
    return `${pool.deficit} ${machines} short`;
  }
  if (pool.surplus > 0) {
    const machines = pool.surplus === 1 ? 'machine' : 'machines';
    return `${pool.surplus} ${machines} spare`;
  }
  return '';
}
