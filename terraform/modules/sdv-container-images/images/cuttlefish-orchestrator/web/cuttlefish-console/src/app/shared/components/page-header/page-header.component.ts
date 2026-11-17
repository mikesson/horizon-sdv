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

import {ChangeDetectionStrategy, Component, input, output} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import type {BreadcrumbItem} from '../breadcrumb/breadcrumb.component';
import {BreadcrumbComponent} from '../breadcrumb/breadcrumb.component';

/**
 * A standard layout component for page titles and actions.
 *
 * This component enforces consistent spacing and typography for top-level page headers.
 * It supports content projection for action buttons (`[primary-action]`, `[secondary-actions]`)
 * and integrates with the `BreadcrumbComponent`.
 */
@Component({
  selector: 'app-page-header',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatButtonModule, MatIconModule, BreadcrumbComponent],
  // See app-empty-state: a static `title` attribute reaches the DOM as well as
  // the input, and the browser then draws a native tooltip over the header.
  host: {'[attr.title]': 'null'},
  template: `
    <div class="gcp-page-header">
      @if (breadcrumbs().length > 0) {
        <app-breadcrumb [items]="breadcrumbs()" />
      }

      <div class="header-row">
        <div class="header-title-section">
          @if (showBackButton()) {
            <button type="button" mat-icon-button aria-label="Back" class="back-button" (click)="back.emit()">
              <mat-icon>arrow_back</mat-icon>
            </button>
          }
          <div class="title-block">
            <h1 class="page-title">{{ title() }}</h1>
            @if (subtitle()) {
              <p class="page-subtitle">{{ subtitle() }}</p>
            }
          </div>
        </div>

        <div class="header-actions">
          <ng-content select="[secondary-actions]" />
          <ng-content select="[primary-action]" />
        </div>
      </div>
    </div>
  `,
  styles: `
    /* The header band is its own surface in the console, distinct from the
       canvas scrolling beneath it. */
    .gcp-page-header {
      padding: 16px 24px;
      background: var(--cco-page-surface);
      border-bottom: 1px solid var(--cco-border);
    }

    .header-row {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 24px;
    }

    .header-title-section {
      display: flex;
      align-items: center;
      gap: 12px;
      min-width: 0;
    }

    .back-button {
      color: var(--cco-text-secondary);
    }

    .title-block {
      min-width: 0;
    }

    .page-title {
      margin: 0;
      font-size: 22px;
      font-weight: 400;
      color: var(--cco-text);
      line-height: 28px;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }

    .page-subtitle {
      margin: 2px 0 0;
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .header-actions {
      display: flex;
      align-items: center;
      gap: 8px;
      flex-shrink: 0;
    }

    /* The slot styles nothing. What used to be here painted an accent container
       on every button projected into [primary-action], which is not a set that
       shares one emphasis. The slot also carries the refresh icon button and
       the low-emphasis Sync and Delete text buttons, and those rendered as
       filled pills indistinguishable from the page's one real action. The
       destructive ones were filled with --cco-error, a foreground colour,
       rather than the destructive container. It existed to give the primary
       action a readable label at all, which the filled variant now does on
       its own. */
  `,
})
export class PageHeaderComponent {
  readonly title = input('');
  readonly subtitle = input('');
  readonly breadcrumbs = input<BreadcrumbItem[]>([]);
  readonly showBackButton = input(false);

  readonly back = output<void>();
}
