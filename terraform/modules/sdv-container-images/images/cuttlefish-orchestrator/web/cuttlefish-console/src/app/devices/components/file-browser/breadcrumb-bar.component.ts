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
import {parentPath, toPathParts} from './path-parts';

/**
 * The file browser's path navigator: an up button plus cumulative breadcrumbs.
 * Both emit the absolute path to move to via `navigate`, so the parent has a
 * single navigation entry point.
 */
@Component({
  selector: 'app-breadcrumb-bar',
  imports: [MatButtonModule, MatIconModule, MatTooltipModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <button type="button"
      mat-icon-button
      (click)="navigate.emit(parent())"
      [disabled]="!canGoUp()"
      matTooltip="Go up" aria-label="Go up">
      <mat-icon>arrow_upward</mat-icon>
    </button>

    <div class="breadcrumbs">
      @for (part of parts(); track $index) {
        <button type="button" mat-button class="breadcrumb" (click)="navigate.emit(part.path)">
          {{ part.name || '/' }}
        </button>
        @if (!$last) {
          <span class="separator">/</span>
        }
      }
    </div>
  `,
  styles: `
      :host {
        display: flex;
        align-items: center;
        flex: 1;
        min-width: 0;
        gap: 4px;
      }

      button {
        color: var(--cco-text-secondary);
      }

      .breadcrumbs {
        display: flex;
        align-items: center;
        flex-wrap: nowrap;
        overflow-x: auto;
        flex: 1;
        min-width: 0;
      }

      .breadcrumb {
        min-width: auto;
        padding: 4px 8px;
        color: var(--cco-text-secondary);
        font-size: 12px;
      }

      .breadcrumb:hover {
        color: var(--cco-text);
      }

      .separator {
        color: var(--cco-text-disabled);
      }
    `,
})
export class BreadcrumbBarComponent {
  readonly path = input.required<string>();

  readonly navigate = output<string>();

  protected readonly parts = computed(() => toPathParts(this.path()));
  protected readonly parent = computed(() => parentPath(this.path()));
  protected readonly canGoUp = computed(() => this.path() !== '/');
}
