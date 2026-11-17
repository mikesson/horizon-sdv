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

import {ChangeDetectionStrategy, Component, computed, input, output} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import type {AutoscalerSnapshot, StandbyRecommendation} from '../../models/autoscaler.model';
import type {DemandSplit} from '../../models/control-plane.model';
import {MissChartComponent} from './miss-chart.component';

/**
 * The unservable axes, named for the thing to buy or change rather than for the
 * field carrying them. They are populations and never a breakdown. A miss
 * failing two appears under both, so they do not sum to the unservable total.
 */
const AXES: ReadonlyArray<{key: keyof DemandSplit; label: string}> = [
  {key: 'gpuRequired', label: 'needed a GPU'},
  {key: 'needsLargerHost', label: 'needed a larger host'},
  {key: 'oversizedGroup', label: 'wanted more devices than one standby host holds'},
];

/**
 * What the demand window saw, and the number a week of it recommends.
 *
 * The peak and the split describe the same window and answer different
 * questions, how much was in flight at once and how much arrived, so they sit
 * on separate lines and are never added. A difference between them is not a
 * discrepancy.
 */
@Component({
  selector: 'app-demand-band',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatButtonModule, MissChartComponent],
  template: `
    <div class="band-head">
      <h3>Demand <span class="sub">what the window saw</span></h3>
      <span class="window">last {{ horizonLabel() }}</span>
    </div>

    <p class="fact">
      Peak of {{ status().misses }} concurrent servable
      {{ status().misses === 1 ? 'miss' : 'misses' }}
      @if (status().partial) {
        <span class="caveat">· a lower bound, because the window held more than one read returns</span>
      }
    </p>
    <p class="fact">
      {{ demand().servable }} servable · {{ demand().unservable }} beyond standing capacity
    </p>
    @for (axis of axes(); track axis.label) {
      <p class="axis">· {{ axis.count }} {{ axis.label }}</p>
    }
    @if (gpu().wanted > 0) {
      <!-- Both terms, never a percentage. Four of ten and four of four hundred
           are different facts, and the size of the denominator is itself how
           much the number is worth. -->
      <p class="fact">
        {{ gpu().degraded }} of {{ gpu().wanted }} creates that wanted a GPU got software rendering
      </p>
    }

    <h4>The week's decision</h4>
    @if (error(); as message) {
      <p class="fact caveat">{{ message }}</p>
    } @else if (recommendation(); as rec) {
      @if (rec.sufficient) {
        <p class="suggestion">the last {{ windowDays(rec) }} days suggest standby {{ rec.standby }}</p>
        <p class="derivation">{{ derivation(rec) }}</p>
      } @else {
        <!-- A week with little traffic is not a recommendation of zero.
             Rendering the peak as evidence here is the one way this feature
             does harm. 0 would read as "measured, and the answer is none", and
             0 is also a perfectly ordinary thing to set on purpose. -->
        <p class="suggestion quiet">not enough evidence to suggest a number</p>
        <p class="derivation">
          {{ rec.servable }} servable
          {{ rec.servable === 1 ? 'miss' : 'misses' }} in {{ windowDays(rec) }} days is too few for a
          peak to have a shape. Drive the queue-load harness to see the shape under load, then set
          the free-machine count from judgement, and record it as judgement.
        </p>
      }
      <app-miss-chart [recommendation]="rec" />
      @if (rec.sufficient && rec.standby !== storedStandby()) {
        <button type="button" mat-stroked-button (click)="adopt.emit(rec.standby)">
          Keep {{ rec.standby }} machines free
        </button>
      }
    } @else {
      <p class="fact">Reading the observation window…</p>
    }
  `,
  styles: `
    :host {
      display: block;
    }

    .band-head {
      display: flex;
      align-items: baseline;
      justify-content: space-between;
      gap: var(--cco-space-16);
      margin-bottom: var(--cco-space-8);
    }

    h3 {
      margin: 0;
      font-size: var(--cco-text-body);
      font-weight: 600;
    }

    h4 {
      margin: var(--cco-space-16) 0 var(--cco-space-4);
      font-size: var(--cco-text-label);
      font-weight: 600;
      color: var(--cco-text-secondary);
    }

    .sub {
      font-weight: 400;
      color: var(--cco-text-secondary);
    }

    .window,
    .fact,
    .axis,
    .derivation {
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .fact,
    .axis,
    .derivation,
    .suggestion {
      margin: 0 0 var(--cco-space-4);
    }

    .axis {
      padding-left: var(--cco-space-12);
    }

    .caveat {
      color: var(--cco-warning);
    }

    .suggestion {
      font-family: var(--cco-font-mono);
      font-size: var(--cco-text-body);
      color: var(--cco-text);
    }

    .suggestion.quiet {
      color: var(--cco-text-secondary);
    }

    button {
      margin-top: var(--cco-space-8);
    }
  `,
})
export class DemandBandComponent {
  readonly snapshot = input.required<AutoscalerSnapshot>();
  readonly recommendation = input.required<StandbyRecommendation | null>();
  readonly error = input.required<string | null>();
  readonly storedStandby = input.required<number>();

  /** Adopting the suggestion writes the free-machine count, nothing else. */
  readonly adopt = output<number>();

  protected readonly status = computed(() => this.snapshot().fleet.status);
  protected readonly demand = computed(() => this.status().demand);
  protected readonly gpu = computed(() => this.snapshot().fleet.gpuDegradation);

  protected readonly horizonLabel = computed(() => {
    const seconds = this.snapshot().demandHorizonSec;
    if (seconds === 0) {
      return 'window · the demand signal is off';
    }
    return seconds >= 3600 ? `${Math.round(seconds / 3600)}h` : `${Math.round(seconds / 60)}m`;
  });

  /** Only the axes that actually fired; a zero row is noise. */
  protected readonly axes = computed(() =>
    AXES.map(axis => ({label: axis.label, count: this.demand()[axis.key]})).filter(
      axis => axis.count > 0
    )
  );

  protected windowDays(rec: StandbyRecommendation): number {
    return Math.round((rec.to.getTime() - rec.from.getTime()) / 86_400_000);
  }

  /** The suggestion's derivation, stated so it is never a bare figure. */
  protected derivation(rec: StandbyRecommendation): string {
    const interval =
      rec.peakFrom && rec.peakTo
        ? `${timeLabel(rec.peakFrom)}-${timeLabel(rec.peakTo)} on ${dayLabel(rec.peakFrom)}`
        : 'no located interval';
    const unservable =
      rec.unservable > 0
        ? ` · the ${rec.unservable} unservable would not have been absorbed by any buffer`
        : '';
    return `peak of ${rec.standby} concurrent servable misses · ${interval} · ${
      rec.servable + rec.unservable
    } misses, ${rec.servable} servable${unservable}`;
  }
}

function timeLabel(at: Date): string {
  return at.toLocaleTimeString(undefined, {hour: '2-digit', minute: '2-digit'});
}

function dayLabel(at: Date): string {
  return at.toLocaleDateString(undefined, {day: 'numeric', month: 'short'});
}
