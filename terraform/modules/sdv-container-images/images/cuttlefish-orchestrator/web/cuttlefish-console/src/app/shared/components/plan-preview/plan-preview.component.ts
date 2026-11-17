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
import {MatCardModule} from '@angular/material/card';
import {MatIconModule} from '@angular/material/icon';
import {LoadingStateComponent} from '../loading-state/loading-state.component';

/** One resolved line of a dry-run plan. */
export interface PlanItem {
  icon: string;
  label: string;
  value: string;
  detail?: string;
  /**
   * Renders the detail in the accent colour, marking something the platform
   * will *create* rather than reuse (a build registration, a host provision).
   */
  action?: boolean;
}

/**
 * The rendered result of a `validate_only` dry run.
 *
 * Knows nothing about devices or batches. Callers map their own resolution
 * shape to `PlanItem[]`, so a second resource does not need a second copy of
 * this markup.
 */
@Component({
  selector: 'app-plan-preview',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatCardModule, MatIconModule, LoadingStateComponent],
  template: `
    @if (loading()) {
      <app-loading-state size="section" [label]="loadingLabel()" />
    } @else if (error(); as message) {
      <div class="plan-error">
        <mat-icon>error_outline</mat-icon>
        <div>
          <strong>{{ errorTitle() }}</strong>
          <p>{{ message }}</p>
        </div>
      </div>
    } @else {
      <div class="plan-cards">
        @for (item of items(); track item.label) {
          <mat-card class="plan-card">
            <mat-card-content>
              <div class="plan-item">
                <mat-icon class="plan-icon">{{ item.icon }}</mat-icon>
                <div class="plan-text">
                  <span class="plan-label">{{ item.label }}</span>
                  <span class="plan-value">{{ item.value }}</span>
                  @if (item.detail) {
                    <span class="plan-detail" [class.plan-detail--action]="item.action">
                      {{ item.detail }}
                    </span>
                  }
                </div>
              </div>
            </mat-card-content>
          </mat-card>
        }

        @for (warning of warnings(); track warning) {
          <div class="plan-warning">
            <mat-icon>warning_amber</mat-icon>
            <span>{{ warning }}</span>
          </div>
        }
      </div>

      <ng-content />
    }
  `,
  styles: `
    .plan-error {
      display: flex;
      gap: var(--cco-space-16);
      padding: var(--cco-space-24);
      background: var(--cco-error-surface);
      border: 1px solid var(--cco-error);
      border-radius: var(--cco-radius-sm);
    }

    .plan-error mat-icon {
      color: var(--cco-error);
      font-size: var(--cco-icon-2xl);
      width: var(--cco-icon-2xl);
      height: var(--cco-icon-2xl);
    }

    .plan-error strong {
      color: var(--cco-text);
    }

    .plan-error p {
      margin: var(--cco-space-8) 0 0;
      color: var(--cco-text-secondary);
    }

    .plan-cards {
      display: flex;
      flex-direction: column;
      gap: var(--cco-space-12);
    }

    .plan-card {
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-sm);
    }

    .plan-item {
      display: flex;
      align-items: flex-start;
      gap: var(--cco-space-16);
    }

    .plan-icon {
      color: var(--cco-accent);
      background: var(--cco-accent-surface);
      border-radius: 50%;
      padding: var(--cco-space-8);
      font-size: var(--cco-icon-lg);
      width: 36px;
      height: 36px;
      display: flex;
      align-items: center;
      justify-content: center;
      box-sizing: border-box;
    }

    .plan-text {
      display: flex;
      flex-direction: column;
      gap: var(--cco-space-2);
    }

    .plan-label {
      font-size: var(--cco-text-sm);
      text-transform: uppercase;
      letter-spacing: 0.06em;
      color: var(--cco-text-secondary);
    }

    .plan-value {
      font-size: var(--cco-text-title);
      font-weight: 500;
      color: var(--cco-text);
    }

    .plan-detail {
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .plan-detail--action {
      color: var(--cco-accent);
    }

    .plan-warning {
      display: flex;
      align-items: center;
      gap: var(--cco-space-12);
      padding: var(--cco-space-12) var(--cco-space-16);
      background: var(--cco-warning-surface);
      border: 1px solid var(--cco-warning);
      border-radius: var(--cco-radius-sm);
      color: var(--cco-text);
      font-size: var(--cco-text-sm);
    }

    .plan-warning mat-icon {
      color: var(--cco-warning);
    }
  `,
})
export class PlanPreviewComponent {
  readonly items = input.required<PlanItem[]>();
  readonly warnings = input<string[]>([]);
  readonly loading = input(false);
  readonly error = input<string | null>(null);
  readonly loadingLabel = input('Resolving the plan…');
  readonly errorTitle = input('This launch would fail');
}
