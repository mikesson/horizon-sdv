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

import {ChangeDetectionStrategy, Component, computed, input} from '@angular/core';
import {MatIconModule} from '@angular/material/icon';
import {MatProgressBarModule} from '@angular/material/progress-bar';
import {MatTooltipModule} from '@angular/material/tooltip';

import type {Quota} from '../../models/quota.model';
import {getQuotaScopeLabel} from '../../models/quota.model';

/**
 * Displays host quota usage as a gauge/progress bar.
 * Supports both compact (navbar) and expanded (dashboard) views.
 */
@Component({
  selector: 'app-quota-gauge',
  imports: [MatIconModule, MatTooltipModule, MatProgressBarModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="quota-gauge" [class.compact]="compact()" [class.expanded]="!compact()">

      <!-- Compact View (Navbar) -->
      @if (compact() && quota()) {
        <div class="compact-view" [matTooltip]="tooltip()">
          <div class="compact-gauges">
            <div class="mini-bar-row">
              <mat-icon class="mini-icon">dns</mat-icon>
              <mat-progress-bar aria-label="Quota usage"
                class="mini-progress"
                mode="determinate"
                [value]="percentage()"
                [color]="progressColor()"
              />
              <span class="mini-count">{{ quota()?.usedHosts }}/{{ quota()?.maxHosts }}</span>
            </div>
          </div>
        </div>
      }

      <!-- Expanded View (Dashboard) -->
      @if (!compact() && quota()) {
        <div class="expanded-view">
          <div class="gauge-header">
            <mat-icon>speed</mat-icon>
            <span class="gauge-title">Host Quota ({{ scopeLabel() }})</span>
          </div>

          <div class="gauge-section">
            <div class="gauge-label">
              <span class="category">
                <mat-icon class="inline-icon">dns</mat-icon>
                Hosts
              </span>
              <span class="usage-text" [class.exceeded]="isExhausted()">
                {{ quota()?.usedHosts }} / {{ quota()?.maxHosts }}
              </span>
            </div>

            <mat-progress-bar aria-label="Quota usage"
              mode="determinate"
              [value]="percentage()"
              [class.critical-bar]="isCritical()"
              [class.warning-bar]="isWarning()"
            />

            <div class="gauge-available">
              {{ quota()?.available }} available
            </div>

            @if (quota()?.highWaterAt) {
              <div class="gauge-watermark">
                <mat-icon class="inline-icon">trending_up</mat-icon>
                Peaked at {{ quota()?.highWaterUsedHosts }} of {{ quota()?.maxHosts }} in the last 7 days
              </div>
            }

            @if (isCritical()) {
              <div class="gauge-status critical">
                <mat-icon>warning</mat-icon>
                <span>Quota critical - contact admin to increase limit</span>
              </div>
            } @else if (isExhausted()) {
              <div class="gauge-status exhausted">
                <mat-icon>block</mat-icon>
                <span>Quota exhausted - cannot provision more hosts</span>
              </div>
            }
          </div>
        </div>
      }

      <!-- Empty State -->
      @if (!quota()) {
        <div class="no-quota">
          <mat-icon>info_outline</mat-icon>
          <span>Loading quota...</span>
        </div>
      }
    </div>
  `,
  styles: `
    .quota-gauge {
      font-size: 12px;
    }

    /* Compact View */
    .compact-view {
      padding: 4px 8px;
      background: rgba(255,255,255,0.1);
      border-radius: 4px;
      cursor: help;
    }

    .compact-gauges {
      display: flex;
      flex-direction: column;
      gap: 4px;
    }

    .mini-bar-row {
      display: flex;
      align-items: center;
      gap: 8px;
    }

    .mini-icon {
      font-size: 14px;
      width: 14px;
      height: 14px;
      color: rgba(255,255,255,0.7);
    }

    .mini-progress {
      height: 4px;
      border-radius: 2px;
      width: 50px;
    }

    .mini-count {
      font-size: 11px;
      font-family: monospace;
      color: var(--cco-text);
    }

    /* Expanded View */
    .expanded-view {
      background: var(--cco-surface-variant);
      border: 1px solid var(--cco-border);
      border-radius: 8px;
      padding: 16px;
    }

    .gauge-header {
      display: flex;
      align-items: center;
      gap: 8px;
      margin-bottom: 16px;
      color: var(--cco-text);
      font-weight: 500;
    }

    .gauge-section {
      margin-bottom: 12px;
    }

    .gauge-label {
      display: flex;
      justify-content: space-between;
      margin-bottom: 4px;
      color: var(--cco-text-secondary);
    }

    .category {
      display: flex;
      align-items: center;
      gap: 8px;
      font-weight: 500;
    }

    .inline-icon {
      font-size: 16px;
      width: 16px;
      height: 16px;
    }

    .usage-text {
      font-family: monospace;
      font-weight: 500;
    }

    .usage-text.exceeded {
      color: var(--cco-error);
    }

    .gauge-available {
      font-size: 11px;
      color: var(--cco-text-secondary);
      margin-top: 4px;
    }

    .gauge-watermark {
      display: flex;
      align-items: center;
      gap: 4px;
      font-size: 11px;
      color: var(--cco-text-secondary);
      margin-top: 4px;

      .inline-icon {
        font-size: 14px;
        width: 14px;
        height: 14px;
      }
    }

    .critical-bar ::ng-deep .mdc-linear-progress__bar-inner {
      border-color: var(--cco-error) !important;
    }

    .warning-bar ::ng-deep .mdc-linear-progress__bar-inner {
      border-color: var(--cco-warning) !important;
    }

    .gauge-status {
      display: flex;
      align-items: center;
      gap: 4px;
      font-size: 11px;
      margin-top: 8px;
      padding: 8px;
      border-radius: 4px;
    }

    .gauge-status.critical {
      background: rgba(249, 153, 0, 0.1);
      color: var(--cco-warning);
    }

    .gauge-status.exhausted {
      background: rgba(217, 48, 37, 0.1);
      color: var(--cco-error);
    }

    .gauge-status mat-icon {
      font-size: 14px;
      width: 14px;
      height: 14px;
    }

    .no-quota {
      display: flex;
      align-items: center;
      gap: 8px;
      color: var(--cco-text-secondary);
      font-size: 12px;
    }
  `,
})
export class QuotaGaugeComponent {
  readonly quota = input<Quota | null>();

  readonly compact = input(false);

  readonly percentage = computed(() => {
    const q = this.quota();
    return q ? q.percentage : 0;
  });

  readonly scopeLabel = computed(() => {
    const q = this.quota();
    return q ? getQuotaScopeLabel(q.scope) : '';
  });

  readonly tooltip = computed(() => {
    const q = this.quota();
    if (!q) {
      return 'Loading...';
    }
    return `Hosts: ${q.usedHosts}/${q.maxHosts} (${q.percentage.toFixed(0)}%)`;
  });

  /** Whether quota is at warning level (>= 75%) */
  isWarning(): boolean {
    const pct = this.percentage();
    return pct >= 75 && pct < 90;
  }

  /** Whether quota is critical (>= 90%) */
  isCritical(): boolean {
    return this.percentage() >= 90;
  }

  /** Whether quota is fully exhausted (>= 100%) */
  isExhausted(): boolean {
    return this.percentage() >= 100;
  }

  progressColor(): 'primary' | 'accent' | 'warn' {
    if (this.isCritical() || this.isExhausted()) {
      return 'warn';
    }
    if (this.isWarning()) {
      return 'accent';
    }
    return 'primary';
  }
}
