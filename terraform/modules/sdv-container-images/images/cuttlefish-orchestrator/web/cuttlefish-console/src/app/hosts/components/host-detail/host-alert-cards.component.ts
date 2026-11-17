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

import {DatePipe} from '@angular/common';
import {ChangeDetectionStrategy, Component, input, output} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatCardModule} from '@angular/material/card';
import {MatChipsModule} from '@angular/material/chips';
import {MatIconModule} from '@angular/material/icon';
import {type HostErrorType, toHostErrorType} from '../../../shared/constants';
import type {Host} from '../../models/host.model';

/**
 * Preemption and error alert cards shown above the host details when the
 * host needs operator attention.
 */
@Component({
  selector: 'app-host-alert-cards',
  imports: [DatePipe, MatButtonModule, MatCardModule, MatChipsModule, MatIconModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @let h = host();
          <!-- Preemption Card (shown when host is preempted) -->
          @if (h.status === 'preempted') {
            <mat-card class="preempted-card">
              <mat-card-header>
                <mat-icon matCardAvatar class="preempted-icon">sync_problem</mat-icon>
                <mat-card-title>Host Preempted</mat-card-title>
                <mat-card-subtitle>{{ h.preemptedAt | date: 'medium' }}</mat-card-subtitle>
              </mat-card-header>
              <mat-card-content>
                <p class="preemption-message">
                  This Spot VM was reclaimed by Google Cloud because the capacity was needed elsewhere.
                  The VM has been deleted and can be recreated by clicking Retry.
                </p>
                @if (h.preemptionCount > 1) {
                  <div class="preemption-history">
                    <mat-icon>history</mat-icon>
                    <span>This host has been preempted {{ h.preemptionCount }} times.</span>
                  </div>
                }
              </mat-card-content>
              <mat-card-actions>
                <button type="button" mat-flat-button (click)="retryRequested.emit(false)">
                  <mat-icon>refresh</mat-icon>
                  Retry as Spot VM
                </button>
                <button type="button" mat-button (click)="retryRequested.emit(true)">
                  <mat-icon>check_circle</mat-icon>
                  Retry as Standard VM
                </button>
                <button type="button" mat-button color="warn" (click)="deleteRequested.emit()">
                  <mat-icon>delete</mat-icon>
                  Delete
                </button>
              </mat-card-actions>
            </mat-card>
          }

          <!-- Error Card (shown when host is in error state) -->
          @if (h.status === 'error' && h.error) {
            <mat-card class="error-card">
              <mat-card-header>
                <mat-icon matCardAvatar class="error-icon">error</mat-icon>
                <mat-card-title>{{ getErrorTypeLabel(h.error.type) }}</mat-card-title>
                <mat-card-subtitle>{{ h.error.occurredAt | date: 'medium' }}</mat-card-subtitle>
              </mat-card-header>
              <mat-card-content>
                <p class="error-message-text">{{ h.error.message }}</p>

                @if (h.error.code) {
                  <div class="error-detail">
                    <span class="label">Error Code:</span>
                    <code>{{ h.error.code }}</code>
                  </div>
                }

                @if (h.error.suggestion) {
                  <div class="error-suggestion">
                    <mat-icon>lightbulb</mat-icon>
                    <span>{{ h.error.suggestion }}</span>
                  </div>
                }

                @if (h.error.alternativeZones && h.error.alternativeZones.length > 0) {
                  <div class="alternative-zones">
                    <span class="label">Available in zones:</span>
                    <div class="zone-chips">
                      @for (zone of h.error.alternativeZones; track zone) {
                        <mat-chip>{{ zone }}</mat-chip>
                      }
                    </div>
                  </div>
                }
              </mat-card-content>
              <mat-card-actions>
                @if (h.error.retryable) {
                  <button type="button" mat-button color="primary" (click)="retryRequested.emit(false)">
                    <mat-icon>refresh</mat-icon>
                    Retry
                  </button>
                }
                <button type="button" mat-button color="warn" (click)="deleteRequested.emit()">
                  <mat-icon>delete</mat-icon>
                  Delete
                </button>
              </mat-card-actions>
            </mat-card>
          }

  `,
  styles: `


      /* Preempted Card */
      .preempted-card {
        margin-bottom: 24px;
        border-left: 4px solid var(--cco-warning);
        background: var(--cco-warning-surface);
      }

      .preempted-card mat-card-header {
        padding-bottom: 8px;
      }

      .preempted-card .preempted-icon {
        color: var(--cco-warning);
        font-size: 48px;
        width: 40px;
        height: 40px;
        background: none;
      }

      .preempted-card mat-card-title {
        color: var(--cco-warning);
        font-weight: 500;
      }

      .preempted-card mat-card-subtitle {
        color: var(--cco-text-secondary);
      }

      .preemption-message {
        font-size: 14px;
        color: var(--cco-text);
        margin-bottom: 16px;
        line-height: 1.5;
      }

      .preemption-history {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 12px;
        background: rgba(0, 0, 0, 0.05);
        border-radius: 8px;
        font-size: 12px;
        color: var(--cco-text-secondary);
      }

      .preemption-history mat-icon {
        color: var(--cco-warning);
        font-size: 20px;
        width: 20px;
        height: 20px;
      }

      .preempted-card mat-card-actions {
        padding: 8px 16px 16px;
        display: flex;
        gap: 8px;
        flex-wrap: wrap;
      }

      /* Error Card Styles */
      .error-card {
        margin-bottom: 24px;
        border-left: 4px solid var(--cco-error);
        background: var(--cco-error-surface);
      }

      .error-card mat-card-header {
        padding-bottom: 8px;
      }

      .error-card .error-icon {
        color: var(--cco-error);
        font-size: 48px;
        width: 40px;
        height: 40px;
        background: none;
      }

      .error-card mat-card-title {
        color: var(--cco-error);
        font-weight: 500;
      }

      .error-card mat-card-subtitle {
        color: var(--cco-text-secondary);
      }

      .error-message-text {
        font-size: 14px;
        color: var(--cco-text);
        margin-bottom: 16px;
        line-height: 1.5;
      }

      .error-detail {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-bottom: 12px;
      }

      .error-detail .label {
        font-size: 12px;
        color: var(--cco-text-secondary);
        text-transform: uppercase;
      }

      .error-detail code {
        background: rgba(0, 0, 0, 0.05);
        padding: 2px 8px;
        border-radius: 4px;
        font-family: monospace;
        font-size: 12px;
      }

      .error-suggestion {
        display: flex;
        align-items: flex-start;
        gap: 8px;
        padding: 12px;
        background: var(--cco-warning-surface);
        border-radius: 8px;
        margin-bottom: 12px;
      }

      .error-suggestion mat-icon {
        color: var(--cco-warning);
        font-size: 20px;
        width: 20px;
        height: 20px;
        flex-shrink: 0;
      }

      .error-suggestion span {
        font-size: 14px;
        color: var(--cco-text);
        line-height: 1.4;
      }

      .alternative-zones {
        margin-top: 12px;
      }

      .alternative-zones .label {
        display: block;
        font-size: 12px;
        color: var(--cco-text-secondary);
        text-transform: uppercase;
        margin-bottom: 8px;
      }

      .zone-chips {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
      }

      .zone-chips mat-chip {
        background: var(--cco-accent-surface);
        color: var(--cco-accent);
        --mat-chip-label-text-color: var(--cco-accent);
      }

      .error-card mat-card-actions {
        padding: 8px 16px 16px;
      }
  `,
})
export class HostAlertCardsComponent {
  readonly host = input.required<Host>();

  /** Retry the host; true = fall back to a standard (non-Spot) VM. */
  readonly retryRequested = output<boolean>();
  readonly deleteRequested = output<void>();

  /**
   * Human-readable label for a classified provisioning failure.
   *
   * Keyed on what the API actually sends. The previous map used an invented
   * vocabulary that shared two values with the backend's, so nearly every real
   * failure fell through to the generic fallback below.
   */
  getErrorTypeLabel(errorType: string): string {
    const labels: Record<HostErrorType, string> = {
      zone_not_found: 'Zone Not Found',
      zone_down: 'Zone Unavailable',
      machine_type_not_in_zone: 'Machine Type Unavailable in Zone',
      gpu_not_in_zone: 'GPU Unavailable in Zone',
      gpu_count_exceeded: 'Too Many GPUs Requested',
      gpu_not_attachable: 'GPU Not Attachable to This Machine Type',
      quota_cpu_exceeded: 'CPU Quota Exceeded',
      quota_gpu_exceeded: 'GPU Quota Exceeded',
      quota_ip_exceeded: 'IP Address Quota Exceeded',
      quota_ssd_exceeded: 'SSD Quota Exceeded',
      quota_hyperdisk_exceeded: 'Hyperdisk Quota Exceeded',
      quota_instances_exceeded: 'Instance Quota Exceeded',
      permission_denied: 'Permission Denied',
      resource_exhausted: 'Zone Capacity Exhausted',
      resource_not_ready: 'Resource Not Ready',
      rate_limited: 'Rate Limited by GCE',
      network_not_found: 'Network Not Found',
      subnet_not_found: 'Subnet Not Found',
      image_not_found: 'Host Image Not Found',
      unknown: 'Error',
    };
    return labels[toHostErrorType(errorType)];
  }
}
