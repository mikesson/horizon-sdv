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
import {RouterModule} from '@angular/router';

/**
 * A pointer to the page that owns a setting this page only reads.
 *
 * `fragment` is what makes it worth a component: these deep-link to a section
 * of a settings page, not to its top, and a link that dumps the reader at the
 * top of a four-section page has not answered their question.
 */
@Component({
  selector: 'app-cross-link',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatIconModule, RouterModule],
  template: `
    <a class="cross-link" [routerLink]="link()" [fragment]="fragment() || undefined">
      <span>{{ label() }}</span>
      <mat-icon>arrow_forward</mat-icon>
    </a>
  `,
  styles: `
    .cross-link {
      display: inline-flex;
      align-items: center;
      gap: var(--cco-space-4);
      color: var(--cco-accent);
      font-size: var(--cco-text-sm);
      text-decoration: none;
    }

    .cross-link:hover {
      text-decoration: underline;
      text-decoration-thickness: 1px;
      text-underline-offset: 2px;
    }

    .cross-link mat-icon {
      font-size: var(--cco-icon-sm);
      width: var(--cco-icon-sm);
      height: var(--cco-icon-sm);
    }
  `,
})
export class CrossLinkComponent {
  readonly label = input.required<string>();
  readonly link = input.required<string>();
  readonly fragment = input('');
}
