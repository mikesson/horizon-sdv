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

import {ChangeDetectionStrategy, Component, input, output} from '@angular/core';
import type {AutoscaleMode} from '../../models/control-plane.model';

/**
 * The rungs, monotone in risk, each stating its permission DELTA rather than
 * repeating the whole set, because the delta is what an operator decides about.
 */
const RUNGS: ReadonlyArray<{value: AutoscaleMode; label: string; delta: string}> = [
  {value: 'off', label: 'off', delta: 'composes a target and acts on none of it'},
  {
    value: 'provision_only',
    label: 'provision only',
    delta: '+ renews draining hosts, and buys new ones',
  },
  {value: 'on', label: 'on', delta: '+ gives surplus back by shortening leases'},
];

/**
 * The autoscale mode, drawn as a ladder.
 *
 * It is the only control on this page that changes what the system is PERMITTED
 * to do; everything else changes a number it pursues. Drawn as a rail with three
 * rungs because the ordering is what matters. Each rung adds a permission
 * and none takes one away, so risk climbs monotonically and the last rung is the
 * only one that can shorten a host's life.
 *
 * The reversibility line is the arc's actual safety property and was written
 * down nowhere a user could reach. Half the reluctance to enable an autoscaler
 * is not knowing whether it can be undone.
 */
@Component({
  selector: 'app-mode-ladder',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="ladder" role="radiogroup" aria-label="Autoscaler mode">
      @for (rung of rungs; track rung.value) {
        <button
          type="button"
          class="rung"
          role="radio"
          [class.selected]="rung.value === mode()"
          [attr.aria-checked]="rung.value === mode()"
          [disabled]="busy()"
          (click)="pick(rung.value)">
          <span class="pip" aria-hidden="true"></span>
          <span class="rung-label">{{ rung.label }}</span>
          <span class="rung-delta">{{ rung.delta }}</span>
        </button>
      }
    </div>
    <p class="reversible">Every rung is reversible by setting the mode back to off.</p>
  `,
  styles: `
    :host {
      display: block;
    }

    /* A vertical rail, drawn once behind the rungs: the rungs are the ordering
       and the rail is what makes it read as one. */
    .ladder {
      position: relative;
      display: flex;
      flex-direction: column;
      gap: var(--cco-space-8);
      padding-left: var(--cco-space-16);
    }

    .ladder::before {
      content: '';
      position: absolute;
      left: 5px;
      top: 10px;
      bottom: 10px;
      width: 1px;
      background: var(--cco-border);
    }

    .rung {
      display: grid;
      grid-template-columns: auto auto 1fr;
      align-items: baseline;
      gap: var(--cco-space-8);
      padding: var(--cco-space-4) var(--cco-space-8);
      border: 1px solid transparent;
      border-radius: var(--cco-radius-sm);
      background: none;
      color: var(--cco-text-secondary);
      font: inherit;
      font-size: var(--cco-text-label);
      text-align: start;
      cursor: pointer;
    }

    .rung:disabled {
      cursor: default;
      opacity: 0.6;
    }

    .pip {
      width: 11px;
      height: 11px;
      margin-left: -16px;
      border: 1px solid var(--cco-border);
      border-radius: 50%;
      background: var(--cco-surface);
    }

    .rung.selected {
      border-color: var(--cco-border);
      color: var(--cco-text);
    }

    .rung.selected .pip {
      background: var(--cco-accent);
      border-color: var(--cco-accent);
    }

    .rung-label {
      font-family: var(--cco-font-mono);
    }

    .rung.selected .rung-label {
      font-weight: 600;
    }

    .rung:focus-visible {
      outline: 2px solid var(--cco-accent);
      outline-offset: 1px;
    }

    .reversible {
      margin: var(--cco-space-8) 0 0;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }
  `,
})
export class ModeLadderComponent {
  readonly mode = input.required<AutoscaleMode>();
  readonly busy = input<boolean>(false);
  readonly modeChange = output<AutoscaleMode>();

  protected readonly rungs = RUNGS;

  protected pick(mode: AutoscaleMode): void {
    if (mode !== this.mode()) {
      this.modeChange.emit(mode);
    }
  }
}
