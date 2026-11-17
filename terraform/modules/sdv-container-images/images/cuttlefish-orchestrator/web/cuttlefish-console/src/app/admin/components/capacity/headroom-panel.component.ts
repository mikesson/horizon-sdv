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

import {DecimalPipe} from '@angular/common';
import {ChangeDetectionStrategy, Component, computed, input} from '@angular/core';
import type {CapacityPool} from '../../models/capacity.model';

interface Bar {
  key: string;
  name: string;
  who: string;
  known: boolean;
  fillPct: number;
  fill: 'normal' | 'warm' | 'hot';
  usage: number;
  limit: number;
}

interface RegionBars {
  region: string;
  bars: Bar[];
}

/**
 * Google's regional quota pools, one bar idiom throughout. The track is the
 * limit, the fill is usage, and the numbers at the right are the truth. Track
 * lengths are not to scale, because comparability across pools beat the
 * cleverness.
 *
 * A pool with no series draws a dashed empty track instead of a zero-length
 * fill. Google publishes nothing for a per-family metric until the family is
 * first used, and placement treats what it cannot read as open.
 */
@Component({
  selector: 'app-headroom-panel',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [DecimalPipe],
  template: `
    @for (group of groups(); track group.region) {
      <div class="pool-region">
        <span class="prname mono">{{ group.region }}</span>
        @for (bar of group.bars; track bar.key) {
          <div class="pool" [class.unknown]="!bar.known">
            <span class="pname">
              {{ bar.name }}
              <span class="pwho">{{ bar.who }}</span>
            </span>
            <div class="bar">
              @if (bar.known) {
                <span [class]="['fill', bar.fill]" [style.width.%]="bar.fillPct"></span>
              }
            </div>
            @if (bar.known) {
              <span class="pval num">{{ bar.usage | number }} of {{ bar.limit | number }}</span>
            } @else {
              <span class="pval">no data until first use</span>
            }
          </div>
        }
      </div>
    }
  `,
  styles: `
    :host {
      display: grid;
      gap: var(--cco-space-12);
    }

    .num {
      font-variant-numeric: tabular-nums;
    }

    .mono {
      font-family: var(--cco-font-mono);
    }

    .pool-region {
      display: grid;
      gap: var(--cco-space-4);
    }

    .prname {
      font-size: var(--cco-text-sm);
      font-weight: 500;
      color: var(--cco-text-secondary);
    }

    .pool {
      display: grid;
      grid-template-columns: minmax(200px, 260px) 1fr minmax(150px, auto);
      gap: var(--cco-space-12);
      align-items: center;
    }

    .pname {
      font-size: var(--cco-text-sm);
    }

    .pwho {
      display: block;
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-label);
    }

    .bar {
      height: 10px;
      border-radius: var(--cco-radius-pill);
      background: var(--cco-surface-variant);
      position: relative;
      overflow: hidden;
    }

    .fill {
      position: absolute;
      inset: 0 auto 0 0;
      background: var(--cco-accent);
      border-radius: var(--cco-radius-pill);
    }

    .fill.warm {
      background: var(--cco-warning);
    }

    .fill.hot {
      background: var(--cco-error);
    }

    .pool.unknown .bar {
      background: transparent;
      border: 1px dashed var(--cco-text-disabled);
    }

    .pool.unknown .pval {
      color: var(--cco-text-secondary);
    }

    .pval {
      text-align: right;
      font-size: var(--cco-text-sm);
    }
  `,
})
export class HeadroomPanelComponent {
  readonly pools = input.required<CapacityPool[]>();

  protected readonly groups = computed<RegionBars[]>(() => {
    const byRegion = new Map<string, Bar[]>();
    for (const pool of this.pools()) {
      const bars = byRegion.get(pool.region) ?? [];
      bars.push(barOf(pool));
      byRegion.set(pool.region, bars);
    }
    return [...byRegion.entries()].map(([region, bars]) => ({region, bars}));
  });
}

function barOf(pool: CapacityPool): Bar {
  const known = pool.state !== 'unknown';
  return {
    key: `${pool.region}/${pool.pool}`,
    name: poolName(pool),
    who: poolWho(pool),
    known,
    fillPct: known && pool.limit > 0 ? Math.min(100, (pool.usage / pool.limit) * 100) : 0,
    fill: pool.state === 'at_wall' ? 'hot' : pool.state === 'near_reserve' ? 'warm' : 'normal',
    usage: pool.usage,
    limit: pool.limit,
  };
}

/** Named by what draws it, since Google's metric name is the sub-label. */
function poolName(pool: CapacityPool): string {
  if (pool.kind === 'disk') {
    return 'Boot disk GB';
  }
  if (pool.spot) {
    return 'Spot vCPUs';
  }
  return `${pool.families.join(' · ') || 'Shared'} vCPUs`;
}

function poolWho(pool: CapacityPool): string {
  const families = pool.families.length > 0 ? ` · ${pool.families.join(', ')}` : '';
  return `${pool.pool}${families}`;
}
