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

/**
 * One labelled fact inside a card, label beside value.
 *
 * The card version of app-summary-fact, which stacks instead because it
 * lives in a narrow pane. Both exist because the same two-line Material list
 * item was being used for both, and in a card that spends a full row on the
 * label and another on the value. Six fields ran nearly 400px for about
 * 120px of text.
 *
 * The label column collapses under 480px so a long value is not squeezed into
 * a third of a phone-width card.
 */
@Component({
  selector: 'app-fact-row',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <span class="fact-label">{{ label() }}</span>
    <span class="fact-value"><ng-content /></span>
  `,
  styles: `
    :host {
      display: grid;
      grid-template-columns: minmax(120px, 200px) 1fr;
      gap: var(--cco-space-16);
      align-items: baseline;
      padding: var(--cco-space-4) 0;
    }

    .fact-label {
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .fact-value {
      font-size: var(--cco-text-body);
      color: var(--cco-text);
      overflow-wrap: anywhere;
    }

    @container page (max-width: 480px) {
      :host {
        grid-template-columns: 1fr;
        gap: 0;
      }
    }
  `,
})
export class FactRowComponent {
  readonly label = input.required<string>();
}
