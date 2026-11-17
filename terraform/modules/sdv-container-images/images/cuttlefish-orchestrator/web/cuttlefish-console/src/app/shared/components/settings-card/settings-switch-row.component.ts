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

import {ChangeDetectionStrategy, Component, effect, input, output, signal} from '@angular/core';
import {MatSlideToggleModule} from '@angular/material/slide-toggle';

/** Where a flip has got to. `failed` means the switch has snapped back. */
export type SwitchStatus = 'idle' | 'saving' | 'saved' | 'failed';

/** How long the "Saved" acknowledgement stays up. */
const SAVED_LINGER_MS = 2000;

/**
 * One immediately-applied setting.
 *
 * The acknowledgement is part of the component rather than each caller's
 * business: a revert the user cannot see is worse than no optimism at all, so
 * a failed flip snaps the switch back visibly and leaves the error to a
 * snackbar the caller raises.
 */
@Component({
  selector: 'app-settings-switch-row',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatSlideToggleModule],
  template: `
    <div class="switch-row">
      <mat-slide-toggle
        [checked]="checked()"
        [disabled]="status() === 'saving'"
        (change)="toggled.emit($event.checked)"
      >
        {{ label() }}
      </mat-slide-toggle>
      <span class="ack" role="status" [class.visible]="acknowledged()">
        @if (acknowledged()) {
          Saved
        }
      </span>
    </div>
    @if (hint()) {
      <p class="hint">{{ hint() }}</p>
    }
  `,
  styles: `
    .switch-row {
      display: flex;
      align-items: center;
      gap: var(--cco-space-12);
    }

    .ack {
      font-size: var(--cco-text-sm);
      color: var(--cco-success);
      opacity: 0;
      transition: opacity var(--cco-duration-fast) var(--cco-easing-standard);
    }

    .ack.visible {
      opacity: 1;
    }

    .hint {
      margin: var(--cco-space-8) 0 0;
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-sm);
    }
  `,
})
export class SettingsSwitchRowComponent {
  readonly label = input.required<string>();
  readonly hint = input('');
  readonly checked = input.required<boolean>();
  readonly status = input<SwitchStatus>('idle');

  readonly toggled = output<boolean>();

  protected readonly acknowledged = signal(false);

  constructor() {
    // Escaping to setTimeout is why this is an effect: the acknowledgement has
    // to clear on a wall clock, not on a state change.
    effect(onCleanup => {
      if (this.status() !== 'saved') {
        this.acknowledged.set(false);
        return;
      }
      this.acknowledged.set(true);
      const timer = setTimeout(() => this.acknowledged.set(false), SAVED_LINGER_MS);
      onCleanup(() => clearTimeout(timer));
    });
  }
}
