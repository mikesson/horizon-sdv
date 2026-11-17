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
import {MatCardModule} from '@angular/material/card';
import {MatIconModule} from '@angular/material/icon';

/**
 * Represents a KPI item for display in the horizontal strip.
 */
export interface KpiItem {
  label: string;
  value: string | number;
  icon?: string;
  iconColor?: string;
  /** Optional breakdown text (e.g., "5 running, 2 stopped") */
  breakdown?: string;
  route?: string;
  /** Optional CSS class for styling (e.g., 'running', 'stopped', 'error') */
  colorClass?: string;
}

/**
 * Horizontal KPI strip for dashboard and list pages following GCP Console pattern.
 *
 * Displays key metrics in a horizontally scrollable row that spans
 * the full width of the content area.
 *
 * @example
 * ```html
 * <app-kpi-strip [items]="kpiItems" (cardClick)="navigateTo($event)"></app-kpi-strip>
 * ```
 */
@Component({
  selector: 'app-kpi-strip',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatCardModule, MatIconModule],
  template: `
    <div class="kpi-strip">
      @for (item of items(); track item.label) {
        <mat-card
          [class]="['kpi-card', item.colorClass ?? '']"
          [class.clickable]="!!item.route"
          (click)="cardClick.emit(item)">
          <div class="kpi-content">
            @if (item.icon) {
              <div class="kpi-icon" [style.color]="item.iconColor">
                <mat-icon>{{ item.icon }}</mat-icon>
              </div>
            }
            <div class="kpi-data">
              <span class="kpi-value">{{ item.value }}</span>
              <span class="kpi-label">{{ item.label }}</span>
              @if (item.breakdown) {
                <span class="kpi-breakdown">{{ item.breakdown }}</span>
              }
            </div>
          </div>
        </mat-card>
      }
    </div>
  `,
  styles: `
    .kpi-strip {
      display: flex;
      gap: 16px;
      overflow-x: auto;
      padding-bottom: 4px;
      margin-bottom: 12px;
      scrollbar-width: none;
    }

    .kpi-strip::-webkit-scrollbar {
      display: none;
    }

    .kpi-card {
      min-width: 180px;
      flex-shrink: 0;
      padding: 16px 24px;
      border: 1px solid var(--cco-border);
      box-shadow: none;
      transition: all var(--cco-duration-base) ease;
    }

    .kpi-card.clickable {
      cursor: pointer;
    }

    .kpi-card.clickable:hover {
      background: var(--cco-surface-variant);
      border-color: var(--cco-text-secondary);
    }

    .kpi-content {
      display: flex;
      align-items: flex-start;
      gap: 12px;
    }

    .kpi-icon {
      width: 40px;
      height: 40px;
      border-radius: 8px;
      display: flex;
      align-items: center;
      justify-content: center;
      background: var(--cco-surface-variant);
    }

    .kpi-icon mat-icon {
      font-size: 24px;
      width: 24px;
      height: 24px;
    }

    .kpi-data {
      display: flex;
      flex-direction: column;
    }

    .kpi-value {
      font-size: 28px;
      font-weight: 600;
      line-height: 1.2;
      color: var(--cco-text);
    }

    .kpi-label {
      font-size: 12px;
      color: var(--cco-text-secondary);
      margin-top: 2px;
    }

    .kpi-breakdown {
      font-size: 11px;
      color: var(--cco-text-secondary);
      margin-top: 4px;
    }

    /* Color variants */
    .kpi-card.running {
      background: var(--cco-success-surface);
      border-color: var(--cco-success-surface);
    }

    .kpi-card.stopped {
      background: var(--cco-warning-surface);
      border-color: var(--cco-warning-surface);
    }

    .kpi-card.error {
      background: var(--cco-error-surface);
      border-color: var(--cco-error-surface);
    }

    .kpi-card.creating {
      background: var(--cco-accent-surface);
      border-color: var(--cco-accent-surface);
    }

    .kpi-card.protected {
      background: var(--cco-accent-surface);
      border-color: var(--cco-accent-surface);
    }
  `,
})
export class KpiStripComponent {
  readonly items = input<KpiItem[]>([]);
  readonly cardClick = output<KpiItem>();
}
