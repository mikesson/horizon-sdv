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

import {ChangeDetectionStrategy, Component, input} from '@angular/core';
import {MatIconModule} from '@angular/material/icon';

/**
 * The "nothing here" state.
 *
 * `compact` is the single-line row the inline pickers use; `page` is the
 * stacked layout for a whole list or panel. The default content slot takes a
 * message that needs markup. A plain message should use the `message` input.
 */
@Component({
  selector: 'app-empty-state',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatIconModule],
  // A static attribute that matches an input is written to the DOM as well as
  // bound, so `title="…"` left a native title on the host and the browser drew
  // its own tooltip of the heading over the empty state's own button.
  host: {'[attr.title]': 'null'},
  template: `
    <div class="empty-state" [class.compact]="variant() === 'compact'">
      <mat-icon>{{ icon() }}</mat-icon>
      @if (variant() === 'compact') {
        <span>{{ message() }}</span>
      } @else {
        @if (title()) {
          <h3>{{ title() }}</h3>
        }
        @if (message()) {
          <p>{{ message() }}</p>
        }
      }
      <ng-content />
      <ng-content select="[action]" />
    </div>
  `,
  styles: `
    .empty-state {
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      gap: var(--cco-space-8);
      padding: var(--cco-space-48) var(--cco-space-24);
      text-align: center;
      color: var(--cco-text-secondary);
    }

    .empty-state mat-icon {
      font-size: var(--cco-icon-display);
      width: var(--cco-icon-display);
      height: var(--cco-icon-display);
      color: var(--cco-text-disabled);
    }

    .empty-state h3 {
      margin: 0;
      font-size: var(--cco-text-title);
      font-weight: 400;
      color: var(--cco-text);
    }

    .empty-state p {
      margin: 0;
      max-width: 46ch;
      font-size: var(--cco-text-sm);
    }

    /* Projected rich messages carry the parent's encapsulation id, so the
       scoped selectors above cannot reach them. */
    .empty-state ::ng-deep p {
      margin: 0;
      max-width: 46ch;
      font-size: var(--cco-text-sm);
    }

    .empty-state ::ng-deep code {
      font-family: var(--cco-font-mono);
      font-size: var(--cco-text-sm);
      background: var(--cco-surface-variant);
      padding: var(--cco-space-2) var(--cco-space-4);
      border-radius: var(--cco-radius-xs);
    }

    .compact {
      flex-direction: row;
      gap: var(--cco-space-8);
      padding: var(--cco-space-16);
      font-size: var(--cco-text-sm);
      text-align: left;
    }

    .compact mat-icon {
      font-size: var(--cco-icon-lg);
      width: var(--cco-icon-lg);
      height: var(--cco-icon-lg);
    }
  `,
})
export class EmptyStateComponent {
  readonly icon = input('inbox');
  readonly title = input('');
  readonly message = input('');
  readonly variant = input<'page' | 'compact'>('page');
}
