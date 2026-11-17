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
import {MatIconModule} from '@angular/material/icon';
import {MatTooltipModule} from '@angular/material/tooltip';

import type {DeviceLifecycle} from '../../models/device.model';

/**
 * Pin Button Component
 *
 * Toggle button for pinning/unpinning devices.
 * - Shows filled pin icon when pinned
 * - Shows outlined pin icon when ephemeral
 * - Emits pin/unpin events for parent handling
 */
@Component({
  selector: 'app-pin-button',
  imports: [MatIconModule, MatButtonModule, MatTooltipModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <button type="button"
      mat-icon-button
      class="pin-button"
      [class.pinned]="isPinned()"
      [class.disabled]="disabled()"
      [disabled]="disabled()"
      [matTooltip]="tooltip()"
      (click)="onClick($event)" [attr.aria-label]="tooltip()">
      <mat-icon [class.rotate]="isPinned()">
        {{ isPinned() ? 'push_pin' : 'push_pin' }}
      </mat-icon>
    </button>
  `,
  styles: `
    .pin-button {
      width: 36px;
      height: 36px;
      line-height: 36px;
      transition: all var(--cco-duration-slow) cubic-bezier(0.4, 0, 0.2, 1);
    }

    .pin-button mat-icon {
      font-size: 20px;
      width: 20px;
      height: 20px;
      color: var(--cco-text-secondary);
      transition: all var(--cco-duration-slow) cubic-bezier(0.4, 0, 0.2, 1);
    }

    .pin-button:hover mat-icon {
      color: var(--cco-accent);
    }

    .pin-button.pinned mat-icon {
      color: var(--cco-accent);
    }

    .pin-button.pinned mat-icon.rotate {
      transform: rotate(45deg);
    }

    .pin-button.disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    .pin-button.disabled mat-icon {
      color: var(--cco-text-secondary);
    }

    /* GCP-style hover with subtle background */
    .pin-button:not(.disabled):hover {
      background-color: var(--cco-accent-surface);
    }

    .pin-button:not(.disabled):active {
      transform: scale(0.95);
    }

    /* Pinned state has persistent background */
    .pin-button.pinned {
      background-color: var(--cco-accent-surface);
    }
  `,
})
export class PinButtonComponent {
  // Signal inputs
  readonly lifecycle = input<DeviceLifecycle | undefined>();
  readonly disabled = input(false);

  // Signal outputs
  readonly pin = output<void>();
  readonly unpin = output<void>();

  // Computed signals
  readonly isPinned = computed(() => this.lifecycle()?.mode === 'pinned');

  readonly tooltip = computed(() => {
    if (this.disabled()) {
      return 'Cannot modify pin status';
    }
    return this.isPinned()
      ? 'Unpin device (return to ephemeral)'
      : 'Pin device (prevent auto-termination)';
  });

  onClick(event: Event): void {
    event.stopPropagation();
    if (this.disabled()) {
      return;
    }

    if (this.isPinned()) {
      this.unpin.emit();
    } else {
      this.pin.emit();
    }
  }
}
