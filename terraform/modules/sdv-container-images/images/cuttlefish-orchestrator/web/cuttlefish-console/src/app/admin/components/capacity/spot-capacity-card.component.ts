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
import type {SpotCapacity} from '../../models/capacity.model';

interface Fact {
  label: string;
  value: string;
  alarming: boolean;
}

/**
 * How healthy the fleet's Spot capacity is. How much of the fleet runs on Spot,
 * how much Google took back and gave back, and whether the capacity advice
 * steering placement is fresh. Every number is server-derived, and the churn
 * counts audit entries, because host rows are torn down with the fleet.
 */
@Component({
  selector: 'app-spot-capacity-card',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="facts">
      @for (fact of facts(); track fact.label) {
        <div class="fact">
          <span class="fl">{{ fact.label }}</span>
          <span class="fv num" [class.alarming]="fact.alarming">{{ fact.value }}</span>
        </div>
      }
    </div>
  `,
  styles: `
    .facts {
      display: grid;
      gap: var(--cco-space-8);
    }

    .fact {
      display: flex;
      justify-content: space-between;
      gap: var(--cco-space-12);
      font-size: var(--cco-text-sm);
    }

    .fl {
      color: var(--cco-text-secondary);
    }

    .num {
      font-variant-numeric: tabular-nums;
    }

    .alarming {
      color: var(--cco-error);
    }
  `,
})
export class SpotCapacityCardComponent {
  readonly spot = input.required<SpotCapacity>();
  /** Server time of the snapshot; advice freshness is judged against it. */
  readonly serverNow = input.required<Date>();

  protected readonly facts = computed<Fact[]>(() => {
    const spot = this.spot();
    return [
      {
        label: 'Machines by model',
        value: `${spot.spotMachines} Spot · ${spot.standardMachines} on-demand`,
        alarming: false,
      },
      {
        label: 'Taken back by Google, last 7 days',
        value:
          spot.unrecovered7d > 0
            ? `${spot.preempted7d} · ${spot.unrecovered7d} not recovered`
            : `${spot.preempted7d}`,
        alarming: spot.unrecovered7d > 0,
      },
      {
        label: 'Typical recovery',
        value: recoveryLabel(spot),
        alarming: false,
      },
      this.adviceFact(spot),
    ];
  });

  private adviceFact(spot: SpotCapacity): Fact {
    const {enabled, stale, cells, newestSyncedAt} = spot.advice;
    if (!enabled) {
      return {label: 'Capacity advice', value: 'off (CCO_SPOT_ADVICE)', alarming: false};
    }
    // Advice that never synced is stale on the server's own judgement, so the
    // fresh line always has a sync time to age.
    if (stale || !newestSyncedAt) {
      return {
        label: 'Capacity advice',
        value: 'stale, ranking on quota and health only',
        alarming: true,
      };
    }
    const age = ageLabel(this.serverNow().getTime() - newestSyncedAt.getTime());
    return {
      label: 'Capacity advice',
      value: `fresh · ${cells} cells · synced ${age} ago`,
      alarming: false,
    };
  }
}

function recoveryLabel(spot: SpotCapacity): string {
  if (spot.recovered7d === 0) {
    return 'nothing to recover';
  }
  return spot.typicalRecoverySeconds > 0
    ? formatSeconds(spot.typicalRecoverySeconds)
    : 'all recovered';
}

/** "34s" / "2m". Recovery durations are seconds-to-minutes. */
function formatSeconds(seconds: number): string {
  if (seconds < 90) {
    return `${seconds}s`;
  }
  return `${Math.round(seconds / 60)}m`;
}

/** "40s" / "4m" / "2h" ago. */
function ageLabel(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) {
    return `${s}s`;
  }
  if (s < 3600) {
    return `${Math.round(s / 60)}m`;
  }
  return `${Math.floor(s / 3600)}h`;
}
