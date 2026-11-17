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
import type {StandbyRecommendation} from '../../models/autoscaler.model';

const WIDTH = 720;
const HEIGHT = 132;
/** Room for the reference line's label without it colliding with the plot. */
const PAD_TOP = 12;
const PAD_BOTTOM = 18;

/**
 * Concurrent servable misses over the observation window, as a step chart.
 *
 * One series, axis-light, no legend. The peak interval is shaded and the
 * suggested standby is a labelled reference line, so "peak of 3, here, for
 * fourteen minutes" becomes visible rather than something to take on trust.
 *
 * A window statistic and never an activity feed, so nothing here renders who
 * did what. The GPU degradation ratio is NOT plotted beside it, because a bar
 * is a part-to-whole mark, and it would smuggle back the percentage reading
 * that publishing both terms exists to forbid.
 */
@Component({
  selector: 'app-miss-chart',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <!-- aria-hidden: the derivation beneath the chart states the peak, the
         interval and both counts as text, so announcing the drawing would read
         the same facts twice. -->
    <svg [attr.viewBox]="viewBox" preserveAspectRatio="none" aria-hidden="true" focusable="false">
      @if (peakBand(); as band) {
        <rect class="peak-band" [attr.x]="band.x" y="0" [attr.width]="band.width" [attr.height]="plotHeight" />
      }
      <path class="series" [attr.d]="path()" />
      @if (referenceY() !== null) {
        <line class="reference" x1="0" [attr.y1]="referenceY()" [attr.x2]="width" [attr.y2]="referenceY()" />
      }
      <line class="axis" x1="0" [attr.y1]="plotHeight" [attr.x2]="width" [attr.y2]="plotHeight" />
    </svg>
    <div class="axis-labels">
      <span>{{ from() }}</span>
      <span class="ceiling">peak {{ recommendation().standby }}</span>
      <span>{{ to() }}</span>
    </div>
  `,
  styles: `
    :host {
      display: block;
    }

    svg {
      width: 100%;
      height: 132px;
      display: block;
    }

    .series {
      fill: none;
      stroke: var(--cco-accent);
      stroke-width: 1.5;
      vector-effect: non-scaling-stroke;
    }

    .peak-band {
      fill: color-mix(in srgb, var(--cco-accent) 14%, transparent);
    }

    .reference {
      stroke: var(--cco-text-secondary);
      stroke-dasharray: 4 4;
      stroke-width: 1;
      vector-effect: non-scaling-stroke;
    }

    .axis {
      stroke: var(--cco-border);
      stroke-width: 1;
      vector-effect: non-scaling-stroke;
    }

    .axis-labels {
      display: flex;
      justify-content: space-between;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
      font-variant-numeric: tabular-nums;
    }

    .ceiling {
      font-family: var(--cco-font-mono);
    }
  `,
})
export class MissChartComponent {
  readonly recommendation = input.required<StandbyRecommendation>();

  protected readonly width = WIDTH;
  protected readonly plotHeight = HEIGHT - PAD_BOTTOM;
  protected readonly viewBox = `0 0 ${WIDTH} ${HEIGHT}`;

  /**
   * The vertical scale. At least 1 so a flat-zero week draws its baseline
   * rather than dividing by nothing, and the peak sits below the top edge.
   */
  private readonly ceiling = computed(() => Math.max(1, this.recommendation().standby));

  private readonly y = computed(() => (value: number) => {
    const usable = HEIGHT - PAD_BOTTOM - PAD_TOP;
    return PAD_TOP + usable - (value / this.ceiling()) * usable;
  });

  /**
   * A step path: each bucket holds its value across its own width, then steps.
   * Interpolating between buckets would draw arrivals that did not happen.
   */
  protected readonly path = computed(() => {
    const series = this.recommendation().series;
    if (series.length === 0) {
      return '';
    }
    const step = WIDTH / series.length;
    const y = this.y();
    const parts: string[] = [];
    series.forEach((sample, i) => {
      const x = i * step;
      parts.push(`${i === 0 ? 'M' : 'L'} ${x.toFixed(1)} ${y(sample.peak).toFixed(1)}`);
      parts.push(`L ${(x + step).toFixed(1)} ${y(sample.peak).toFixed(1)}`);
    });
    return parts.join(' ');
  });

  /** The reference line only exists where there is a peak to draw one at. */
  protected readonly referenceY = computed(() =>
    this.recommendation().standby > 0 ? this.y()(this.recommendation().standby) : null
  );

  /** The shaded interval that produced the peak, in the series' own geometry. */
  protected readonly peakBand = computed(() => {
    const rec = this.recommendation();
    if (!(rec.peakFrom && rec.peakTo) || rec.series.length === 0) {
      return null;
    }
    const span = rec.to.getTime() - rec.from.getTime();
    if (span <= 0) {
      return null;
    }
    const x = ((rec.peakFrom.getTime() - rec.from.getTime()) / span) * WIDTH;
    const end = ((rec.peakTo.getTime() - rec.from.getTime()) / span) * WIDTH;
    // A burst inside one bucket is zero pixels wide; a floor keeps it visible
    // as the moment it was rather than vanishing at exactly the peak.
    return {x, width: Math.max(3, end - x)};
  });

  protected readonly from = computed(() => dayLabel(this.recommendation().from));
  protected readonly to = computed(() => dayLabel(this.recommendation().to));
}

/** "14 Aug". The axis wants a date, never a timestamp. */
function dayLabel(at: Date): string {
  return at.toLocaleDateString(undefined, {day: 'numeric', month: 'short'});
}
