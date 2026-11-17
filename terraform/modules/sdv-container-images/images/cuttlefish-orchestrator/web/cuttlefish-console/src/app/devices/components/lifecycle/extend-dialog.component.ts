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
import {ChangeDetectionStrategy, Component, computed, inject, signal} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MAT_DIALOG_DATA, MatDialogModule, MatDialogRef} from '@angular/material/dialog';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatSliderModule} from '@angular/material/slider';
import {COMPILED_LIFETIMES} from '../../../shared/models/platform-lifetimes.schema';
import {PlatformPolicyService} from '../../../shared/services/platform-policy.service';
import type {DeviceLifecycle} from '../../models/device.model';

export interface ExtendDialogData {
  deviceId: string;
  deviceName: string;
  lifecycle: DeviceLifecycle;
}

export interface ExtendDialogResult {
  additionalHours: number;
}

/**
 * Extend Dialog Component
 *
 * Modal dialog for extending device TTL.
 * - Slider to select additional hours (1-4)
 * - Shows new expiry time preview
 * - Displays quota impact
 * - Warns about maximum extension limit
 */
@Component({
  selector: 'app-extend-dialog',
  imports: [
    DatePipe,
    MatDialogModule,
    MatFormFieldModule,
    MatInputModule,
    MatSliderModule,
    MatButtonModule,
    MatIconModule,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <h2 mat-dialog-title>
      <mat-icon class="title-icon">schedule</mat-icon>
      Extend Session
    </h2>
    
    <mat-dialog-content>
      <div class="device-info">
        <span class="label">Device:</span>
        <span class="value">{{ data.deviceName }}</span>
      </div>
    
      <div class="current-status">
        <div class="status-row">
          <span class="label">Current Expiry:</span>
          <span class="value" [class.warning]="isExpiringSoon">
            {{ currentExpiry | date:'MMM d, HH:mm' }}
            <span class="remaining">({{ timeRemaining }})</span>
          </span>
        </div>
        <div class="status-row">
          <span class="label">Extensions Used:</span>
          <span class="value">{{ data.lifecycle.extendedCount }} / {{ allowance() }}</span>
        </div>
      </div>

      @if (data.lifecycle.extendedCount >= allowance() - 1) {
        <div class="warning-box">
          <mat-icon>info</mat-icon>
          <div class="warning-content">
            <strong>Extension Limit</strong>
            <p>
              This will be your last extension. After that, consider pinning the device
              if you need it longer.
            </p>
          </div>
        </div>
      }
    
      <div class="extend-form">
        <label class="slider-label" for="extend-hours">Add Time</label>

        <div class="slider-container">
          <span class="slider-min">1h</span>
          <mat-slider
            [min]="1"
            [max]="maxExtension"
            [step]="1"
            [discrete]="true"
            class="extend-slider"
            >
            <input matSliderThumb id="extend-hours" [value]="additionalHours()" (valueChange)="additionalHours.set($event)">
          </mat-slider>
          <span class="slider-max">{{ maxExtension }}h</span>
        </div>
    
        <div class="selected-hours">
          <span class="hours-value">+{{ additionalHours() }} hour{{ additionalHours() > 1 ? 's' : '' }}</span>
        </div>
    
        <div class="preview-box">
          <div class="preview-row">
            <span class="label">New Expiry:</span>
            <span class="value highlight">{{ newExpiry | date:'MMM d, HH:mm' }}</span>
          </div>
        </div>
      </div>
    </mat-dialog-content>
    
    <mat-dialog-actions align="end">
      <button type="button" mat-button mat-dialog-close>Cancel</button>
      <button type="button"
        mat-flat-button
        [disabled]="!canExtend"
        (click)="submit()"
        >
        <mat-icon>add_circle</mat-icon>
        Extend by {{ additionalHours() }}h
      </button>
    </mat-dialog-actions>
    `,
  styles: `
    h2[mat-dialog-title] {
      display: flex;
      align-items: center;
      font-size: 22px;
      font-weight: 500;
      color: var(--cco-text);
      margin: 0;
      padding: 24px 24px 16px;
    }

    .title-icon {
      margin-right: 12px;
      color: var(--cco-accent);
      font-size: 24px;
      width: 24px;
      height: 24px;
    }

    mat-dialog-content {
      padding: 0 24px;
    }

    .device-info {
      display: flex;
      gap: 8px;
      margin-bottom: 16px;
      padding: 12px 16px;
      background: var(--cco-surface-variant);
      border-radius: 8px;
      border: 1px solid var(--cco-border);
    }

    .device-info .label {
      color: var(--cco-text-secondary);
      font-size: 12px;
    }

    .device-info .value {
      font-weight: 500;
      font-family: 'Roboto Mono', monospace;
      font-size: 12px;
      color: var(--cco-text);
    }

    .current-status {
      display: flex;
      flex-direction: column;
      gap: 8px;
      margin-bottom: 16px;
      padding: 12px 16px;
      background: var(--cco-surface-variant);
      border-radius: 8px;
      border: 1px solid var(--cco-border);
    }

    .status-row {
      display: flex;
      justify-content: space-between;
      align-items: center;
    }

    .status-row .label {
      color: var(--cco-text-secondary);
      font-size: 12px;
    }

    .status-row .value {
      font-weight: 500;
      color: var(--cco-text);
    }

    .status-row .value.warning {
      color: var(--cco-warning);
    }

    .remaining {
      font-size: 12px;
      color: var(--cco-text-secondary);
      margin-left: 4px;
    }

    .warning-box {
      display: flex;
      gap: 12px;
      padding: 12px 16px;
      background: var(--cco-accent-surface);
      border-radius: 8px;
      margin-bottom: 24px;
      border-left: 4px solid var(--cco-accent);
    }

    .warning-box mat-icon {
      color: var(--cco-accent);
      flex-shrink: 0;
      font-size: 20px;
      width: 20px;
      height: 20px;
    }

    .warning-content {
      flex: 1;
    }

    .warning-content strong {
      display: block;
      margin-bottom: 4px;
      color: var(--cco-accent);
      font-size: 14px;
      font-weight: 500;
    }

    .warning-content p {
      margin: 0;
      font-size: 12px;
      color: var(--cco-text-secondary);
      line-height: 1.5;
    }

    .extend-form {
      min-width: 380px;
    }

    .slider-label {
      display: block;
      font-size: 14px;
      font-weight: 500;
      color: var(--cco-text);
      margin-bottom: 12px;
    }

    .slider-container {
      display: flex;
      align-items: center;
      gap: 12px;
    }

    .slider-min, .slider-max {
      font-size: 12px;
      color: var(--cco-text-secondary);
      min-width: 28px;
      font-weight: 500;
    }

    .extend-slider {
      flex: 1;
    }

    .selected-hours {
      text-align: center;
      margin: 24px 0;
    }

    .hours-value {
      display: inline-block;
      padding: 12px 32px;
      background: var(--cco-success-surface);
      color: var(--cco-success);
      border-radius: 24px;
      font-size: 16px;
      font-weight: 600;
      letter-spacing: 0.25px;
    }

    .preview-box {
      background: var(--cco-surface-variant);
      border-radius: 8px;
      padding: 12px 16px;
      border: 1px solid var(--cco-border);
    }

    .preview-row {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding: 8px 0;
    }

    .preview-row .label {
      color: var(--cco-text-secondary);
      font-size: 12px;
    }

    .preview-row .value {
      font-weight: 500;
      color: var(--cco-text);
    }

    .preview-row .value.highlight {
      color: var(--cco-accent);
      font-size: 16px;
      font-weight: 600;
    }

    mat-dialog-actions {
      padding: 16px 24px 24px;
      margin: 0;
      border-top: 1px solid var(--cco-border);
    }

    mat-dialog-actions button mat-icon {
      margin-right: 8px;
      font-size: 18px;
      width: 18px;
      height: 18px;
    }
  `,
})
export class ExtendDialogComponent {
  private readonly dialogRef =
    inject<MatDialogRef<ExtendDialogComponent, ExtendDialogResult>>(MatDialogRef);
  data = inject<ExtendDialogData>(MAT_DIALOG_DATA);
  private readonly lifetimes = inject(PlatformPolicyService).lifetimes;

  /** Extensions this device is allowed, from operator policy. */
  protected readonly allowance = computed(() => this.lifetimes().extendCount);

  readonly additionalHours = signal(COMPILED_LIFETIMES.extendHours);

  constructor() {
    // Preselect the policy grant, which is what the backend would apply on its own.
    this.additionalHours.set(this.lifetimes().extendHours);
  }

  get currentExpiry(): Date {
    return this.data.lifecycle.expiresAt ?? new Date();
  }

  get newExpiry(): Date {
    const current = this.currentExpiry;
    return new Date(current.getTime() + this.additionalHours() * 60 * 60 * 1000);
  }

  get maxExtension(): number {
    // Per-extension cap. Total life is bounded by the extension allowance,
    // not by the create-time TTL cap. A device at the 8h create cap can
    // still take a full 4h extension.
    return this.lifetimes().maxExtendHours;
  }

  get canExtend(): boolean {
    return this.data.lifecycle.extendedCount < this.allowance() && this.additionalHours() > 0;
  }

  get isExpiringSoon(): boolean {
    const now = Date.now();
    const expiresAt = this.currentExpiry.getTime();
    const remainingMinutes = (expiresAt - now) / (1000 * 60);
    return remainingMinutes < 30;
  }

  get timeRemaining(): string {
    const now = Date.now();
    const expiresAt = this.currentExpiry.getTime();
    const remainingMs = expiresAt - now;

    if (remainingMs <= 0) {
      return 'Expired';
    }

    const remainingMinutes = Math.floor(remainingMs / (1000 * 60));
    const hours = Math.floor(remainingMinutes / 60);
    const minutes = remainingMinutes % 60;

    if (hours > 0) {
      return `${hours}h ${minutes}m remaining`;
    }
    return `${minutes}m remaining`;
  }

  submit(): void {
    this.dialogRef.close({
      additionalHours: this.additionalHours(),
    });
  }
}
