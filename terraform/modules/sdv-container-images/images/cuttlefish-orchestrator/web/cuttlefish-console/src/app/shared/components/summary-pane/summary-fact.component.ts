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
 * One labelled fact in a summary pane. The value is projected rather than an
 * input because half of them are a chip, a counter with an edit affordance or
 * a badge, not a string.
 *
 * Divs and not a `<dl>`: a description list may only contain dt/dd groups or
 * plain divs, and a component element between the list and its terms is what
 * the axe `definition-list` rule exists to catch.
 */
@Component({
  selector: 'app-summary-fact',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <span class="fact-label">{{ label() }}</span>
    <span class="fact-value"><ng-content /></span>
  `,
  styles: `
    :host {
      display: flex;
      flex-direction: column;
      gap: var(--cco-space-2);
      min-width: 0;
    }

    .fact-label {
      font-size: var(--cco-text-label);
      font-weight: 500;
      letter-spacing: 0.5px;
      text-transform: uppercase;
      color: var(--cco-text-secondary);
    }

    .fact-value {
      display: flex;
      align-items: center;
      gap: var(--cco-space-8);
      font-size: var(--cco-text-body);
      color: var(--cco-text);
      overflow-wrap: anywhere;
    }
  `,
})
export class SummaryFactComponent {
  readonly label = input.required<string>();
}
