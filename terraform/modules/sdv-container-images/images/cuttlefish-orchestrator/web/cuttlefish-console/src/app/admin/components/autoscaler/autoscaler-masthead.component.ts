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
import type {AutoscalerSnapshot} from '../../models/autoscaler.model';
import {buildContracts, ceilingReached} from './pool-contract.model';

const MODE_COPY: Record<string, string> = {
  off: 'composes a target and acts on none of it',
  provision_only: 'may add capacity, never give it back',
  on: 'may add capacity and give it back',
};

/**
 * Why this many machines, as the page's headline rather than a band of its own.
 *
 * The operations card already showed the winning signal and the clamp. What was
 * missing was the LOSING inputs and the arithmetic joining them, so the question
 * "why is the target 3?" had no answer anywhere. This typesets the formula live.
 *
 * Two formulas per pool, because a pool answers to two promises: one about how
 * many machines stand FREE, one about how many stand at all. Both are drawn
 * whatever the fleet is doing, and the one currently deciding is marked.
 * Printing only the deciding half would leave an operator who set the other
 * unable to find it, which is the confusion this whole contract came out of.
 *
 * One row per pool, and no fold above them. The pools' figures do not add into
 * one contract. A pool short by two beside one spare by two nets to nothing in
 * the sums while the keeper is about to buy two and release two.
 */
@Component({
  selector: 'app-autoscaler-masthead',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <p class="mode">
      mode · <strong>{{ mode() }}</strong> · {{ modeCopy() }}
    </p>

    @for (row of rows(); track row.pool) {
      <div class="masthead" [attr.data-pool]="row.pool">
        <p class="pool">
          <span class="pool-name">{{ row.pool }}</span>
          <span class="pool-family">{{ row.family }}</span>
        </p>
        <div class="numbers">
          <div class="target">
            <span class="numeral">{{ row.target }}</span>
            <span class="caption">machines kept free</span>
          </div>
          <div class="target">
            <span class="numeral">{{ row.floor }}</span>
            <span class="caption">machines kept running</span>
          </div>
        </div>

        <div class="detail">
          <!-- Small mono, losing inputs at secondary ink, the winner at full ink
               carrying the WORD "won". Colour alone may not signal, and this is
               the single fact the line exists to deliver. -->
          <p class="equation" [class.binding]="!row.floorBinds">
            <span class="op">free · max(</span>
            @for (term of row.terms; track term.label) {
              <span class="term" [class.won]="term.won">
                {{ term.label }} {{ term.value }}@if (term.won) {<span class="won-word"> won</span>}
              </span>
              @if (!$last) {
                <span class="op">·</span>
              }
            }
            <span class="op">) → {{ row.target }}, {{ row.standby }} free now</span>
          </p>
          <p class="equation" [class.binding]="row.floorBinds">
            <span class="op">running · floor {{ row.floor }} → {{ row.total }} now</span>
          </p>
          <!-- Which promise is deciding is the one with less slack, which is the
               same comparison the server's own buy-and-release arithmetic makes.
               A comparison of numbers it served, never a target recomputed here. -->
          <p class="which">
            binding · <strong>{{ row.binding }}</strong> · {{ row.bindingCopy }}
            @if (row.gap) {
              <span class="gap" [class.short]="row.short">· {{ row.gap }}</span>
            }
          </p>
        </div>
      </div>
    }

    <!-- The ceiling term never disappears. Muted when it did not bite, full ink
         when it did. A clamp that only rendered while active is a fact nobody
         learns exists until it changes their number. -->
    <p class="ceiling" [class.clamped]="clamped()">
      ceiling {{ limits().maxStandbyHosts }} · {{ clamped() ? 'clamped' : 'not reached' }}
    </p>
  `,
  styles: `
    :host {
      display: block;
    }

    .masthead {
      display: flex;
      align-items: flex-start;
      gap: var(--cco-space-24);
      flex-wrap: wrap;
      padding-block: var(--cco-space-12);
    }

    /* A rule between pools rather than a card each. The rows are one comparison,
       and boxing them would say they are separate readings. */
    .masthead + .masthead {
      border-top: 1px solid var(--cco-border);
    }

    .pool {
      display: flex;
      flex-direction: column;
      gap: 2px;
      min-width: 7rem;
      margin: 0;
      padding-top: var(--cco-space-8);
    }

    .pool-name {
      font-family: var(--cco-font-mono);
      font-weight: 600;
      color: var(--cco-text);
    }

    .numbers {
      display: flex;
      gap: var(--cco-space-24);
    }

    .target {
      display: flex;
      flex-direction: column;
    }

    /* The only display-size type on the page. One point of visual dominance;
       every band sits below it. */
    .numeral {
      font-size: 4rem;
      line-height: 1;
      font-weight: 600;
      font-variant-numeric: tabular-nums;
      color: var(--cco-text);
    }

    .pool-family,
    .caption,
    .mode,
    .equation,
    .which,
    .ceiling {
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .detail {
      display: flex;
      flex-direction: column;
      gap: var(--cco-space-4);
      padding-top: var(--cco-space-8);
    }

    .mode,
    .equation,
    .which,
    .ceiling {
      margin: 0;
    }

    .mode {
      padding-bottom: var(--cco-space-8);
    }

    .mode strong,
    .which strong {
      font-family: var(--cco-font-mono);
      color: var(--cco-text);
    }

    .equation {
      font-family: var(--cco-font-mono);
      font-variant-numeric: tabular-nums;
    }

    /* The deciding line at full ink. Never colour alone, because the line below
       names it in words, which is the fact this mark only reinforces. */
    .equation.binding .op {
      color: var(--cco-text);
    }

    .term.won {
      color: var(--cco-text);
      font-weight: 600;
    }

    .won-word {
      font-weight: 400;
    }

    /* Secondary ink, never disabled ink. These glyphs are the arithmetic's own
       structure (the max, its separators, the arrow) so they have to clear the
       contrast floor; the axe scan fails the page on them at disabled ink. */
    .op {
      color: var(--cco-text-secondary);
    }

    /* A shortfall is a fact about this pool alone, and the words carry it. The
       colour only agrees with them. */
    .gap {
      margin-inline-start: var(--cco-space-4);
    }

    .gap.short {
      color: var(--cco-warning);
    }

    .ceiling {
      padding-top: var(--cco-space-12);
    }

    .ceiling.clamped {
      color: var(--cco-warning);
    }
  `,
})
export class AutoscalerMastheadComponent {
  readonly snapshot = input.required<AutoscalerSnapshot>();

  protected readonly rows = computed(() => buildContracts(this.snapshot()));
  protected readonly limits = computed(() => this.snapshot().limits);
  protected readonly clamped = computed(() => ceilingReached(this.snapshot()));
  /** The mode is the whole loop's, not a pool's, so it is stated once. */
  protected readonly mode = computed(() => this.snapshot().fleet.status.mode);
  protected readonly modeCopy = computed(() => MODE_COPY[this.mode()] ?? '');
}
