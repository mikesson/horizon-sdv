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

import {ChangeDetectionStrategy, Component, computed, input} from '@angular/core';
import type {DemandSplit} from '../../models/control-plane.model';

/** One drawn request, and whether a spare machine could have taken it. */
interface RequestTile {
  index: number;
  servable: boolean;
  label: string;
}

/** One pool's share of the window, as the column names it. */
interface PoolShare {
  readonly pool: string;
  readonly line: string;
}

/** Tiles stop here; past it the count carries the rest. */
const MAX_TILES = 12;

/**
 * What asked for a machine and found none, the left half of the picture.
 *
 * Requests are drawn from COUNTS and never from rows. The snapshot carries how
 * many misses the window held and why, never the misses themselves. Every tile
 * is therefore anonymous and says so; a named one would imply a record this
 * page does not have.
 */
@Component({
  selector: 'app-demand-column',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <h3>Unplaced requests</h3>
    <p class="window">creates in the last {{ minutes() }} minutes that found no machine</p>

    @if (total() === 0) {
      <p class="clear">
        Every create landed on a machine. Nothing waited, so nothing here asks the
        autoscaler for more.
      </p>
    } @else {
      <ol class="tiles" [attr.aria-label]="total() + ' requests that found no machine'">
        @for (tile of tiles(); track tile.index) {
          <li class="tile" [class.servable]="tile.servable" [title]="tile.label">
            <span class="sr">{{ tile.label }}</span>
          </li>
        }
        @if (total() > tiles().length) {
          <li class="more">+{{ total() - tiles().length }}</li>
        }
      </ol>

      <p class="line">
        <strong>{{ split().servable }}</strong> would have started at once on a spare
        machine. That is what standby buys.
      </p>
      <p class="line">
        <strong>{{ split().unservable }}</strong> no spare machine could have taken, because
        more capacity of the same shape would not have helped:
      </p>
      <ul class="reasons">
        @for (reason of reasons(); track reason) {
          <li>{{ reason }}</li>
        }
      </ul>
      <p class="caveat">
        Counted per reason: one request can hit two, so these do not add up to
        {{ split().unservable }}.
      </p>

      <!-- Which pool each request was judged into, because that is the pool
           whose free machines it was waiting on. Drawn only where there is more
           than one, since with one pool it is the totals above said again. -->
      @if (pools().length > 0) {
        <ul class="pools">
          @for (share of pools(); track share.pool) {
            <li><span class="pool-name">{{ share.pool }}</span> {{ share.line }}</li>
          }
        </ul>
      }
    }
  `,
  styles: `
    :host {
      display: block;
      align-self: start;
      padding: var(--cco-space-12) var(--cco-space-16);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-lg);
      /* The canvas ground and the console's raised surface are the same grey in
         light, so the panel takes the card role to stay a panel in both. */
      background: var(--cco-canvas-card);
    }

    h3 {
      margin: 0;
      font-size: var(--cco-text-body);
      font-weight: 600;
    }

    .window,
    .line,
    .caveat,
    .clear {
      margin: var(--cco-space-4) 0 0;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .line strong {
      color: var(--cco-text);
    }

    .tiles {
      display: flex;
      flex-wrap: wrap;
      gap: var(--cco-space-4);
      margin: var(--cco-space-12) 0;
      padding: 0;
      list-style: none;
    }

    /* Smaller than a slot tile inside a machine, because these are requests
       that never landed anywhere, and drawing them the size of an occupied slot
       would suggest they are running somewhere. */
    .tile {
      width: 1.5rem;
      height: 1.5rem;
      border: 1px solid var(--cco-error);
      border-radius: var(--cco-radius-xs);
      background: repeating-linear-gradient(
        45deg,
        transparent,
        transparent 3px,
        color-mix(in srgb, var(--cco-error) 20%, transparent) 3px,
        color-mix(in srgb, var(--cco-error) 20%, transparent) 6px
      );
    }

    /* The ones a buffer would have answered. Same shape, in the accent that
       means the autoscaler can do something about it. */
    .tile.servable {
      border-color: var(--cco-accent);
      background: repeating-linear-gradient(
        45deg,
        transparent,
        transparent 3px,
        color-mix(in srgb, var(--cco-accent) 22%, transparent) 3px,
        color-mix(in srgb, var(--cco-accent) 22%, transparent) 6px
      );
    }

    .more {
      align-self: center;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .reasons {
      margin: var(--cco-space-4) 0 0;
      padding-inline-start: var(--cco-space-16);
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .pools {
      margin: var(--cco-space-8) 0 0;
      padding: 0;
      list-style: none;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .pool-name {
      font-family: var(--cco-font-mono);
      color: var(--cco-text);
    }

    .sr {
      position: absolute;
      width: 1px;
      height: 1px;
      overflow: hidden;
      clip-path: inset(50%);
      white-space: nowrap;
    }
  `,
})
export class DemandColumnComponent {
  readonly split = input.required<DemandSplit>();
  readonly horizonSeconds = input.required<number>();
  /** Each pool's share of the same window; empty where there is one pool. */
  readonly pools = input<readonly PoolShare[]>([]);

  protected readonly minutes = computed(() => Math.round(this.horizonSeconds() / 60));
  protected readonly total = computed(() => this.split().servable + this.split().unservable);

  /** Written out rather than templated, so each line reads as English at 0 and 1. */
  protected readonly reasons = computed(() => {
    const {gpuRequired, needsLargerHost, oversizedGroup} = this.split();
    return [
      `${gpuRequired} wanted a GPU no machine here has`,
      needsLargerHost === 1
        ? '1 wanted a bigger machine than any standing'
        : `${needsLargerHost} wanted a bigger machine than any standing`,
      oversizedGroup === 1
        ? '1 was a device group larger than one machine holds'
        : `${oversizedGroup} were device groups larger than one machine holds`,
    ];
  });

  protected readonly tiles = computed<RequestTile[]>(() => {
    const {servable} = this.split();
    return Array.from({length: Math.min(this.total(), MAX_TILES)}, (_, index) => ({
      index,
      servable: index < servable,
      label:
        index < servable
          ? 'a request a spare machine would have served'
          : 'a request no spare machine could have served',
    }));
  });
}
