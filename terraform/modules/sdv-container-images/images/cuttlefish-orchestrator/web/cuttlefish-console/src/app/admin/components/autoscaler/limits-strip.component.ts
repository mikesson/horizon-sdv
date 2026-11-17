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

import {ChangeDetectionStrategy, Component, computed, inject, input} from '@angular/core';
import {QuotaService} from '../../../quota/services/quota.service';
import {CrossLinkComponent} from '../../../shared/components/cross-link/cross-link.component';
import type {AutoscalerLimits} from '../../models/autoscaler.model';

/**
 * The compiled facts, stated as facts, under the one limit that is not
 * compiled, the fleet ceiling.
 *
 * They are not settings. This deployment has no config file, and operational
 * tuning is code. Rendering them is what stops the next person adding
 * environment variables for them, and it is what explains an apparently stuck
 * fleet, because a target of ten against a standby of two converges over cycles
 * rather than at once.
 *
 * One muted strip at the page foot, in small mono at secondary ink, visibly
 * not a control.
 */
@Component({
  selector: 'app-limits-strip',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [CrossLinkComponent],
  template: `
    <div class="ceiling">
      <span class="ceiling-label">Fleet ceiling</span>
      <span class="ceiling-value num">{{ ceiling() }}</span>
      <span class="ceiling-note">
        CCO's own admission quota, a hard limit nothing provisions past, the keeper included
      </span>
      <app-cross-link label="Edit on Capacity" link="/admin/capacity" />
    </div>
    <p class="intro">Compiled into the build. Changing one is a deploy, not a setting.</p>
    <dl>
      @for (fact of facts(); track fact.label) {
        <div class="fact">
          <dt>{{ fact.label }}</dt>
          <dd>{{ fact.value }}</dd>
        </div>
      }
    </dl>
  `,
  styles: `
    :host {
      display: block;
      padding-top: var(--cco-space-12);
      border-top: 1px solid var(--cco-border);
    }

    .ceiling {
      display: flex;
      align-items: baseline;
      flex-wrap: wrap;
      gap: var(--cco-space-8);
      margin-bottom: var(--cco-space-12);
      font-size: var(--cco-text-sm);
    }

    .ceiling-label {
      color: var(--cco-text-secondary);
    }

    .ceiling-value {
      font-weight: 500;
    }

    .num {
      font-variant-numeric: tabular-nums;
    }

    .ceiling-note {
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-label);
    }

    .intro {
      margin: 0 0 var(--cco-space-8);
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    dl {
      display: flex;
      flex-wrap: wrap;
      gap: var(--cco-space-8) var(--cco-space-24);
      margin: 0;
    }

    .fact {
      display: flex;
      gap: var(--cco-space-8);
      font-family: var(--cco-font-mono);
      font-variant-numeric: tabular-nums;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    dt,
    dd {
      margin: 0;
    }

    dd {
      color: var(--cco-text);
    }
  `,
})
export class LimitsStripComponent {
  private readonly quota = inject(QuotaService);

  readonly limits = input.required<AutoscalerLimits>();

  /**
   * Read-only here on purpose. The ceiling is capacity policy, chosen while
   * looking at the fleet, Google's walls and what can be reclaimed, which is
   * the Capacity page. Stating it where scaling is configured is what keeps
   * "CCO never scales past N" visible to whoever sets a target.
   */
  protected readonly ceiling = computed(() => {
    const q = this.quota.currentQuota();
    return q ? `${q.usedHosts} of ${q.maxHosts} hosts` : '-';
  });

  protected readonly facts = computed(() => {
    const l = this.limits();
    return [
      {label: 'target ceiling', value: String(l.maxStandbyHosts)},
      {label: 'cycle', value: `${l.cycleSeconds}s`},
      {label: 'provisions / cycle', value: String(l.maxTopUpsPerCycle)},
      {label: 'shortens / cycle', value: String(l.maxShortensPerCycle)},
      {label: 'collapses / cycle', value: String(l.maxCollapsesPerCycle)},
      {label: 'quota reserve', value: `${l.quotaReserveHosts} host`},
      {label: 'provisioning lead', value: `${Math.round(l.provisionLeadSeconds / 60)}m`},
      {label: 'settle age', value: `${Math.round(l.minShortenAgeSeconds / 60)}m`},
      {label: 'image-backed slots', value: String(l.hostImageBackedSlots)},
    ];
  });
}
