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
import {MatChipsModule} from '@angular/material/chips';
import {MatDividerModule} from '@angular/material/divider';
import {MatIconModule} from '@angular/material/icon';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import type {StorageObject, VerifyAccessResponse} from '../../models/storage.model';

/** A selected file chip with its precomputed icon. */
export interface SelectedChip {
  object: StorageObject;
  icon: string;
}

/**
 * Selection summary of the GCS browser: the multi-select chip set with the
 * required-files hint, and the single-select URI + access-verification panel.
 */
@Component({
  selector: 'app-gcs-selection-panel',
  imports: [
    MatButtonModule,
    MatChipsModule,
    MatDividerModule,
    MatIconModule,
    MatProgressSpinnerModule,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <!-- Multi-Select Summary -->
    @if (multiSelect() && chips().length > 0) {
      <div class="multi-select-summary">
        <mat-divider />
        <div class="selection-header">
          <mat-icon>check_circle</mat-icon>
          <span>{{ chips().length }} file{{ chips().length > 1 ? 's' : '' }} selected ({{ totalSizeLabel() }})</span>
          <button type="button" mat-button color="warn" (click)="cleared.emit()">Clear All</button>
        </div>
        <mat-chip-set class="selected-chips">
          @for (chip of chips(); track chip.object.name) {
            <mat-chip (removed)="chipRemoved.emit(chip.object)" class="selected-chip">
              <mat-icon matChipAvatar>{{ chip.icon }}</mat-icon>
              {{ chip.object.displayName }}
              <button type="button" matChipRemove><mat-icon>cancel</mat-icon></button>
            </mat-chip>
          }
        </mat-chip-set>
        @if (requiredHint()) {
          <div class="required-hint">
            <mat-icon>info</mat-icon>
            <span>Required: {{ requiredHint() }}</span>
            @if (requiredMet()) {
              <mat-icon class="complete-icon">check_circle</mat-icon>
            }
          </div>
        }
      </div>
    }

    <!-- Selected Object Info -->
    @if (selectedObject(); as selected) {
      <div class="selected-info">
        <mat-divider />
        <div class="artifact-details">
          <div class="artifact-props">
            <div class="prop">
              <span class="label">URI:</span>
              <span class="value uri">{{ selected.gsUri }}</span>
            </div>
          </div>
        </div>
        <!-- Access Verification Section -->
        <div class="access-verification">
          <div class="access-header">
            <mat-icon>security</mat-icon>
            <span>Access Verification</span>
            <button type="button"
              mat-stroked-button
              (click)="verifyRequested.emit()"
              [disabled]="verifying()"
              class="verify-btn"
              >
              @if (!verifying()) {
                <mat-icon>verified_user</mat-icon>
              }
              @if (verifying()) {
                <mat-spinner aria-label="Loading" diameter="18" />
              }
              {{ verifying() ? 'Checking...' : 'Verify Access' }}
            </button>
          </div>
          <!-- Access Results -->
          @if (accessResult(); as result) {
            <div class="access-results">
              <!-- Orchestrator Access -->
              <div class="access-item" [class.has-access]="result.orchestratorAccess.hasAccess"
                [class.no-access]="!result.orchestratorAccess.hasAccess">
                <mat-icon>{{ result.orchestratorAccess.hasAccess ? 'check_circle' : 'cancel' }}</mat-icon>
                <div class="access-info">
                  <span class="access-title">Orchestrator (WebUI)</span>
                  <span class="access-detail">{{ result.orchestratorAccess.serviceAccount }}</span>
                  @if (result.orchestratorAccess.hasAccess) {
                    <span class="access-status">
                      Can read from this bucket
                    </span>
                  }
                  @if (!result.orchestratorAccess.hasAccess) {
                    <span class="access-error">
                      {{ result.orchestratorAccess.error }}
                    </span>
                  }
                </div>
              </div>
              <!-- Host Access (if checked) -->
              @if (result.hostAccess) {
                <div class="access-item"
                  [class.has-access]="result.hostAccess.hasAccess"
                  [class.no-access]="!result.hostAccess.hasAccess">
                  <mat-icon>{{ result.hostAccess.hasAccess ? 'check_circle' : 'cancel' }}</mat-icon>
                  <div class="access-info">
                    <span class="access-title">Host VM Service Account</span>
                    <span class="access-detail">{{ result.hostAccess.serviceAccount }}</span>
                    @if (result.hostAccess.hasAccess) {
                      <span class="access-status">
                        Can download from this bucket
                      </span>
                    }
                    @if (!result.hostAccess.hasAccess) {
                      <span class="access-error">
                        {{ result.hostAccess.error }}
                      </span>
                    }
                  </div>
                </div>
              }
              <!-- Overall Status -->
              <div class="access-summary" [class.can-proceed]="result.canProceed"
                [class.cannot-proceed]="!result.canProceed">
                <mat-icon>{{ result.canProceed ? 'thumb_up' : 'warning' }}</mat-icon>
                <span>{{ result.message }}</span>
              </div>
            </div>
          }
        </div>
      </div>
    }
  `,
  styles: `
    .multi-select-summary {
      margin-top: 16px;
    }

    .selection-header {
      display: flex;
      align-items: center;
      gap: 8px;
      padding: 12px 16px;
      background: var(--cco-accent-surface);
      border-radius: 8px;
      margin-top: 16px;
      font-weight: 500;
      color: var(--cco-accent);
    }

    .selection-header mat-icon {
      color: var(--cco-accent);
    }

    .selection-header button {
      margin-left: auto;
    }

    .selected-chips {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      padding: 12px 0;
    }

    .selected-chip {
      background: var(--cco-accent-surface);
    }

    .required-hint {
      display: flex;
      align-items: center;
      gap: 8px;
      padding: 8px 12px;
      background: var(--cco-warning-surface);
      border-radius: 8px;
      font-size: 12px;
      color: var(--cco-warning);
    }

    .required-hint mat-icon {
      color: var(--cco-warning);
    }

    .required-hint .complete-icon {
      color: var(--cco-success);
      margin-left: auto;
    }

    .selected-info {
      margin-top: 16px;
    }

    .artifact-details {
      padding: 16px;
      background: var(--cco-success-surface);
      border-radius: 8px;
      margin-top: 16px;
    }

    .artifact-props {
      display: flex;
      flex-direction: column;
      gap: 4px;
    }

    .prop {
      display: flex;
      gap: 8px;
      font-size: 12px;
    }

    .prop .label {
      color: var(--cco-text-secondary);
      min-width: 60px;
    }

    .prop .value {
      color: var(--cco-text);
    }

    .prop .value.uri {
      font-family: 'Roboto Mono', monospace;
      font-size: 12px;
      word-break: break-all;
    }

    .access-verification {
      margin-top: 16px;
      padding: 16px;
      background: var(--cco-surface-variant);
      border-radius: 8px;
      border: 1px solid var(--cco-border);
    }

    .access-header {
      display: flex;
      align-items: center;
      gap: 8px;
      margin-bottom: 12px;
      font-weight: 500;
      color: var(--cco-text);
    }

    .access-header mat-icon {
      color: var(--cco-accent);
    }

    .verify-btn {
      margin-left: auto;
    }

    .access-results {
      display: flex;
      flex-direction: column;
      gap: 12px;
    }

    .access-item {
      display: flex;
      align-items: flex-start;
      gap: 12px;
      padding: 12px;
      border-radius: 8px;
      background: var(--cco-surface);
    }

    .access-item.has-access {
      border-left: 4px solid var(--cco-success);
    }

    .access-item.has-access mat-icon {
      color: var(--cco-success);
    }

    .access-item.no-access {
      border-left: 4px solid var(--cco-error);
    }

    .access-item.no-access mat-icon {
      color: var(--cco-error);
    }

    .access-info {
      display: flex;
      flex-direction: column;
      gap: 2px;
      flex: 1;
    }

    .access-title {
      font-weight: 500;
      color: var(--cco-text);
    }

    .access-detail {
      font-size: 11px;
      color: var(--cco-text-secondary);
      font-family: 'Roboto Mono', monospace;
    }

    .access-status {
      font-size: 12px;
      color: var(--cco-success);
    }

    .access-error {
      font-size: 12px;
      color: var(--cco-error);
    }

    .access-summary {
      display: flex;
      align-items: center;
      gap: 8px;
      padding: 12px;
      border-radius: 8px;
      font-weight: 500;
    }

    .access-summary.can-proceed {
      background: var(--cco-success-surface);
      color: var(--cco-success);
    }

    .access-summary.cannot-proceed {
      background: var(--cco-error-surface);
      color: var(--cco-error);
    }
  `,
})
export class GcsSelectionPanelComponent {
  readonly multiSelect = input.required<boolean>();
  readonly chips = input.required<SelectedChip[]>();
  readonly totalSizeLabel = input.required<string>();
  readonly requiredHint = input.required<string>();
  readonly requiredMet = input.required<boolean>();
  readonly selectedObject = input.required<StorageObject | null>();
  readonly accessResult = input.required<VerifyAccessResponse | null>();
  readonly verifying = input.required<boolean>();

  readonly cleared = output<void>();
  readonly chipRemoved = output<StorageObject>();
  readonly verifyRequested = output<void>();
}
