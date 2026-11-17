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
import type {QueueSample} from '../../store/control-plane.signal-store';

/**
 * Tufte-minimal sparkline of session queue depth (pending + in flight):
 * one muted line, no axes, current value alongside.
 */
@Component({
  selector: 'app-queue-sparkline',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (samples().length > 1) {
      <div class="spark" title="Queue depth (pending + in flight) this session">
        <svg viewBox="0 0 120 28" preserveAspectRatio="none" aria-hidden="true">
          <polyline [attr.points]="points()" />
        </svg>
        <span class="current">{{ current() }}</span>
      </div>
    }
  `,
  styles: `
    .spark {
      display: inline-flex;
      align-items: center;
      gap: 8px;
    }

    svg {
      width: 120px;
      height: 28px;
    }

    polyline {
      fill: none;
      stroke: var(--cco-text-secondary);
      stroke-width: 1.5;
      vector-effect: non-scaling-stroke;
    }

    .current {
      font-size: 12px;
      font-weight: 500;
      color: var(--cco-text);
      min-width: 2ch;
    }
  `,
})
export class QueueSparklineComponent {
  readonly samples = input.required<QueueSample[]>();

  protected readonly current = computed(() => {
    const s = this.samples();
    const last = s[s.length - 1];
    return last ? last.pending + last.leased : 0;
  });

  protected readonly points = computed(() => {
    const s = this.samples();
    if (s.length < 2) {
      return '';
    }
    const depths = s.map(p => p.pending + p.leased);
    const max = Math.max(1, ...depths);
    const stepX = 120 / (depths.length - 1);
    // 2px vertical padding so a flat line at 0 or max stays visible.
    return depths
      .map((d, i) => `${(i * stepX).toFixed(1)},${(26 - (d / max) * 24).toFixed(1)}`)
      .join(' ');
  });
}
