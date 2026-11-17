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

import {ChangeDetectionStrategy, Component} from '@angular/core';

/**
 * Action bar component for list pages that provides a consistent layout
 * for filters on the left and actions on the right.
 *
 * Follows the GCP Console pattern of placing filter controls on the left
 * side and action buttons (refresh, view toggle, etc.) on the right.
 *
 * @example
 * ```html
 * <app-action-bar>
 *   <div filters>
 *     <mat-form-field>...</mat-form-field>
 *     <mat-form-field>...</mat-form-field>
 *   </div>
 *   <div actions>
 *     <button type="button" mat-icon-button><mat-icon>refresh</mat-icon></button>
 *   </div>
 * </app-action-bar>
 * ```
 */
@Component({
  selector: 'app-action-bar',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [],
  template: `
    <div class="action-bar">
      <div class="action-bar-left">
        <ng-content select="[filters]" />
      </div>
      <div class="action-bar-right">
        <ng-content select="[actions]" />
      </div>
    </div>
  `,
  styles: `
    .action-bar {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 16px;
      flex-wrap: wrap;
    }

    .action-bar-left {
      display: flex;
      align-items: center;
      gap: 16px;
      flex-wrap: wrap;
    }

    .action-bar-right {
      display: flex;
      align-items: center;
      gap: 8px;
    }

    /* Compact form fields in action bar */
    :host ::ng-deep .mat-mdc-form-field {
      min-width: 160px;
    }

    :host ::ng-deep .mat-mdc-form-field-subscript-wrapper {
      display: none;
    }
  `,
})
export class ActionBarComponent {}
