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

import {ChangeDetectionStrategy, Component, computed, input, model} from '@angular/core';

/**
 * How many machines are kept FREE, drawn as a track rather than typed into a
 * bare field.
 *
 * A bare number hides three things an operator needs while choosing it: how much
 * of the range they are using, where the fleet actually stands, and how close
 * the far end is to quota they must not spend. The track carries all three, and
 * the slot conversion rides the control because the number is in MACHINES while
 * capacity is in DEVICE SLOTS. A conversion that lived in another band would be
 * needed here and read there.
 */
@Component({
  selector: 'app-standby-track',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <label class="row">
      <span class="label">Machines to keep free</span>
      <input
        class="value"
        type="number"
        min="0"
        [max]="max()"
        [value]="value()"
        [disabled]="busy()"
        [attr.aria-describedby]="describedBy() || null"
        aria-label="Machines the fleet keeps free, with no device on them"
        (input)="onInput($event)" />
      <span class="unit">machines</span>
    </label>

    <div class="track" aria-hidden="true">
      @for (slot of slots(); track slot.index) {
        <span
          class="cell"
          [class.filled]="slot.index < value()"
          [class.reserved]="slot.reserved"
          [class.held]="slot.index === held() - 1"></span>
      }
    </div>
    <p class="scale">
      <span>0</span>
      <span>{{ max() }} · ceiling</span>
    </p>

    <p class="note">
      {{ value() }} {{ value() === 1 ? 'machine' : 'machines' }} = {{ slotsPromised() }} device
      slots · {{ held() }} free now
    </p>
    @if (quotaWarning(); as warning) {
      <p class="warning">{{ warning }}</p>
    }
  `,
  styles: `
    :host {
      display: block;
    }

    .row {
      display: flex;
      align-items: center;
      gap: var(--cco-space-8);
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .value {
      width: 64px;
      padding: 2px 6px;
      font: inherit;
      font-family: var(--cco-font-mono);
      color: var(--cco-text);
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-sm);
    }

    .track {
      display: flex;
      gap: 2px;
      margin: var(--cco-space-8) 0 2px;
    }

    .cell {
      flex: 1 1 0;
      height: 14px;
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-xs);
      background: transparent;
    }

    .cell.filled {
      background: color-mix(in srgb, var(--cco-accent) 45%, transparent);
      border-color: var(--cco-accent);
    }

    /* Hatched at the far end: quota the buffer must never spend, so the last
       stretch of the range is drawn as somewhere the target should not go. */
    .cell.reserved {
      border-color: var(--cco-warning);
      background: repeating-linear-gradient(
        45deg,
        transparent,
        transparent 2px,
        color-mix(in srgb, var(--cco-warning) 30%, transparent) 2px,
        color-mix(in srgb, var(--cco-warning) 30%, transparent) 4px
      );
    }

    /* Where the fleet actually stands, which is not where the intent points. */
    .cell.held {
      box-shadow: inset 0 -3px 0 0 var(--cco-text);
    }

    .scale,
    .note,
    .warning {
      display: flex;
      justify-content: space-between;
      margin: 0;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .note {
      display: block;
      margin-top: var(--cco-space-4);
    }

    .warning {
      display: block;
      margin-top: var(--cco-space-4);
      color: var(--cco-warning);
    }
  `,
})
export class StandbyTrackComponent {
  readonly value = model.required<number>();
  readonly max = input.required<number>();
  /** What the fleet holds right now, server-derived, never the draft. */
  readonly held = input.required<number>();
  readonly density = input.required<number>();
  /** Quota the buffer may actually spend: available minus the reserve. */
  readonly quotaSpendable = input.required<number>();
  readonly quotaReserve = input.required<number>();
  readonly busy = input<boolean>(false);
  /**
   * Element carrying this control's explanation. Passed in rather than owned
   * here because the explanation is revealed by the wrapper the caller draws,
   * and a description on an element the control cannot see is a description
   * nothing announces.
   */
  readonly describedBy = input<string>('');

  protected readonly slots = computed(() =>
    Array.from({length: this.max()}, (_, index) => ({
      index,
      // The reserve is counted from the top of what quota still allows, not
      // from the top of the range: the ceiling is compiled and the quota moves.
      reserved: index >= this.quotaSpendable(),
    }))
  );

  protected readonly slotsPromised = computed(() => this.value() * this.density());

  /**
   * The quota interaction, stated while the number is being typed rather than
   * simulated. It is the other non-obvious consequence of raising the intent,
   * and a save that simply does less than asked explains nothing by itself.
   */
  protected readonly quotaWarning = computed(() => {
    const wanted = this.value();
    const spendable = this.quotaSpendable();
    if (wanted <= spendable) {
      return '';
    }
    return `Quota leaves room for ${spendable}: the autoscaler never spends the last ${this.quotaReserve()} machine of platform quota, so a person's create is never refused to keep a machine free. Above ${spendable} the number is still worked out and buying simply stops.`;
  });

  protected onInput(event: Event): void {
    const parsed = Number.parseInt((event.target as HTMLInputElement).value, 10);
    this.value.set(Number.isNaN(parsed) ? 0 : Math.min(this.max(), Math.max(0, parsed)));
  }
}
