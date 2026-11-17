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
 * The icon-and-title banner at the top of a create-wizard step.
 *
 * Not to be confused with the numbered sub-step header inside build-create,
 * which shares the `step-header` class name but is a different pattern.
 */
@Component({
  selector: 'app-step-header',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatIconModule],
  // See app-empty-state: a static `title` attribute reaches the DOM as well as
  // the input, and the browser then draws a native tooltip over the section.
  host: {'[attr.title]': 'null'},
  template: `
    <div class="step-header">
      <mat-icon>{{ icon() }}</mat-icon>
      <div>
        <h2>{{ title() }}</h2>
        @if (description()) {
          <p>{{ description() }}</p>
        }
      </div>
    </div>
  `,
  styles: `
    .step-header {
      display: flex;
      align-items: flex-start;
      gap: var(--cco-space-16);
      margin-bottom: var(--cco-space-24);
      padding-bottom: var(--cco-space-16);
      border-bottom: 1px solid var(--cco-border);
    }

    .step-header mat-icon {
      font-size: var(--cco-icon-2xl);
      width: var(--cco-icon-2xl);
      height: var(--cco-icon-2xl);
      color: var(--cco-accent);
    }

    .step-header h2 {
      margin: 0;
      font-size: var(--cco-text-title-lg);
      color: var(--cco-text);
    }

    .step-header p {
      margin: var(--cco-space-4) 0 0;
      color: var(--cco-text-secondary);
    }
  `,
})
export class StepHeaderComponent {
  readonly icon = input.required<string>();
  readonly title = input.required<string>();
  readonly description = input('');
}
