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
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';

/** Spinner diameters, named by where the spinner sits rather than by px. */
const DIAMETER = {inline: 20, section: 32, page: 40} as const;

/**
 * The pending state.
 *
 * Takes a placement rather than a diameter: the console had accumulated eight
 * distinct spinner sizes, and a free-form number would let a ninth in.
 */
@Component({
  selector: 'app-loading-state',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatProgressSpinnerModule],
  template: `
    <div class="loading" [class.inline]="size() === 'inline'">
      <mat-spinner aria-label="Loading" [diameter]="diameter()" />
      @if (label()) {
        <span>{{ label() }}</span>
      }
    </div>
  `,
  styles: `
    .loading {
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      gap: var(--cco-space-12);
      padding: var(--cco-space-48) var(--cco-space-24);
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-sm);
    }

    .inline {
      flex-direction: row;
      gap: var(--cco-space-8);
      padding: var(--cco-space-16);
    }
  `,
})
export class LoadingStateComponent {
  readonly size = input<keyof typeof DIAMETER>('page');
  readonly label = input('');

  protected readonly diameter = computed(() => DIAMETER[this.size()]);
}
