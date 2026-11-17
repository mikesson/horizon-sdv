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
 * Layout variant determines the content area structure.
 * - 'list': Full-width for tables and list views
 * - 'form': Left-aligned max-width island for forms
 * - 'detail': Main pane plus a supporting pane; project into [page-sidebar]
 * - 'dashboard': Full-width with KPI strip pattern
 */
export type LayoutVariant = 'list' | 'form' | 'detail' | 'dashboard';

/**
 * Wrapper component that provides consistent GCP Console-style page structure.
 *
 * This component handles the full-bleed page header and appropriate content
 * area constraints based on the layout variant. It eliminates the need for
 * negative margin hacks and ensures consistent spacing.
 *
 * @example
 * ```html
 * <app-page-layout variant="list">
 *   <ng-container page-header>
 *     <app-page-header title="Hosts" subtitle="Manage VMs">
 *       <button type="button" primary-action mat-flat-button>Create</button>
 *     </app-page-header>
 *   </ng-container>
 *   <ng-container page-content>
 *     <!-- Your content here -->
 *   </ng-container>
 * </app-page-layout>
 * ```
 */
@Component({
  selector: 'app-page-layout',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="page-layout" [attr.data-variant]="variant()">
      <header class="page-layout-header">
        <ng-content select="[page-header]" />
      </header>

      <div [class]="['page-layout-body', 'variant-' + variant()]">
        <!-- A div, not a main: the router outlet is already inside the shell's
             <main>, and nesting landmarks is invalid and unnavigable. -->
        <div class="page-layout-content">
          <ng-content select="[page-content]" />
        </div>

        @if (variant() === 'detail') {
          <aside class="page-layout-sidebar">
            <ng-content select="[page-sidebar]" />
          </aside>
        }
      </div>
    </div>
  `,
  styles: `
    .page-layout {
      display: flex;
      flex-direction: column;
      min-height: 100%;
    }

    .page-layout-header {
      flex-shrink: 0;
    }

    .page-layout-body {
      flex: 1;
      display: flex;
    }

    .page-layout-content {
      flex: 1;
      display: flex;
      flex-direction: column;
      /* A flex item will not shrink below its content by default, so a wide
         table pushed the whole page sideways instead of scrolling itself. */
      min-width: 0;
    }

    /* List variant: Full width for tables */
    .page-layout-body.variant-list .page-layout-content {
      padding: 24px;
      gap: 24px;
    }

    /* Form variant: Left-aligned island with max-width */
    .page-layout-body.variant-form .page-layout-content {
      padding: 24px;
      max-width: 640px;
      /* Left-aligned, not centered */
    }

    /* Dashboard variant: Full width with flex column */
    .page-layout-body.variant-dashboard .page-layout-content {
      padding: 24px;
      gap: 24px;
    }

    /* Detail variant: a main pane and a supporting pane beside it. Material's
       supporting-pane layout puts the split at 70/30 once the window is
       expanded, and the pane holds what only makes sense next to the main
       content (status, metadata, a summary). */
    .page-layout-body.variant-detail {
      display: flex;
    }

    .page-layout-body.variant-detail .page-layout-content {
      flex: 1 1 0;
      padding: 24px;
      gap: 24px;
    }

    .page-layout-sidebar {
      flex: 0 1 30%;
      min-width: 280px;
      /* The pane holds short facts. Past this it is just a wider column of the
         same six values, and the width is worth more to the main pane. */
      max-width: 420px;
      padding: 24px;
      border-left: 1px solid var(--cco-border);
      background: var(--cco-surface-variant);
    }

    /* Below this the pane goes under the content rather than beside it. It used
       to be display:none, which was survivable only while nothing was ever
       projected into it.

       The 1000px measures the *page*, not the window. The nav rail and a
       scrollbar take about 255px, so the old 1199 stacked a 1440 laptop, the
       most common desktop size there is, while claiming to be the expanded
       layout. At 1000 the pane still gets its 280px floor and the content
       keeps ~700. */
    @container page (max-width: 999px) {
      .page-layout-body.variant-detail {
        flex-direction: column;
      }

      .page-layout-sidebar {
        /* Above the content, not below it. Stacked, the pane is the only place
           a device can be pinned, and under the content that control sits past
           six cards. A summary you have to scroll to is no summary.

           This is the one width where the DOM order and the reading order
           disagree, and that is the trade being made. The pane keeps its place
           after the content in the tab order, so a keyboard user reaches it
           where the source says it is rather than where the screen shows it.
           Sending it first in the DOM instead would move the mismatch onto the
           expanded layout, which is the one people actually use, and tax every
           desktop visit with a detour through the summary. */
        order: -1;
        max-width: none;
        border-left: none;
        border-bottom: 1px solid var(--cco-border);
      }
    }
  `,
})
export class PageLayoutComponent {
  readonly variant = input<LayoutVariant>('list');
}
