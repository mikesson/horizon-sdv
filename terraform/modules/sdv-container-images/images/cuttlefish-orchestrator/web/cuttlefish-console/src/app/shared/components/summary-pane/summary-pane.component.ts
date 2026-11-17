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
 * The supporting pane of a detail page: what the resource *is* right now,
 * beside what it is made of. Status, placement, capacity and timestamps go
 * here. The things a reader works through (configuration, connection
 * instructions, member lists) stay in the main pane.
 *
 * One column while it sits beside the content, and as many as fit once it has
 * stacked underneath, so a narrow window does not pay for the pane twice: once
 * in the width it no longer has, and again in the scroll a tall single column
 * would add.
 */
@Component({
  selector: 'app-summary-pane',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (heading()) {
      <h2>{{ heading() }}</h2>
    }
    <div class="facts">
      <ng-content />
    </div>
  `,
  styles: `
    :host {
      display: block;
    }

    h2 {
      margin: 0 0 var(--cco-space-16);
      font-size: var(--cco-text-title);
      font-weight: 400;
    }

    .facts {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
      gap: var(--cco-space-16) var(--cco-space-24);
    }
  `,
})
export class SummaryPaneComponent {
  readonly heading = input('');
}
