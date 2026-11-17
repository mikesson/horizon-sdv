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

import {ChangeDetectionStrategy, Component, inject, signal} from '@angular/core';

import {FormField, form, minLength, required} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {provideNativeDateAdapter} from '@angular/material/core';
import {MatDatepickerModule} from '@angular/material/datepicker';
import {MAT_DIALOG_DATA, MatDialogModule, MatDialogRef} from '@angular/material/dialog';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatSelectModule} from '@angular/material/select';

import type {PinReason} from '../../models/device.model';
import {PIN_REASON_LABELS} from '../../models/device.model';

interface PinFormModel {
  reason: PinReason | '';
  reasonDetail: string;
  pinnedUntil: Date | null;
}

export interface PinDialogData {
  deviceId: string;
  deviceName: string;
}

export interface PinDialogResult {
  reason: PinReason;
  reasonDetail?: string;
  pinnedUntil?: Date;
}

/**
 * Pin Dialog Component
 *
 * Modal dialog for pinning a device with justification.
 * - Requires selecting a reason for pinning
 * - Optional detailed explanation for "other" reason
 * - Optional target unpinning date
 */
@Component({
  selector: 'app-pin-dialog',
  imports: [
    FormField,
    MatDialogModule,
    MatFormFieldModule,
    MatInputModule,
    MatSelectModule,
    MatButtonModule,
    MatIconModule,
    MatDatepickerModule,
  ],
  providers: [provideNativeDateAdapter()],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <h2 mat-dialog-title>
      <mat-icon class="title-icon">push_pin</mat-icon>
      Pin Device
    </h2>
    
    <mat-dialog-content>
      <div class="device-info">
        <span class="label">Device:</span>
        <span class="value">{{ data.deviceName }}</span>
      </div>

      <form class="pin-form">
        <mat-form-field appearance="outline" class="full-width">
          <mat-label>Reason for Pinning</mat-label>
          <mat-select [formField]="form.reason">
            @for (reason of reasons; track reason) {
              <mat-option [value]="reason.value">
                {{ reason.label }}
              </mat-option>
            }
          </mat-select>
          @for (error of form.reason().errors(); track error.kind) {
            <mat-error>{{ error.message }}</mat-error>
          }
        </mat-form-field>

        @if (form.reason().value() === 'other') {
          <mat-form-field
            appearance="outline"
            class="full-width"
            >
            <mat-label>Please specify</mat-label>
            <textarea
              matInput
              [formField]="form.reasonDetail"
              rows="2"
              placeholder="Describe why this device needs to be pinned..."
            ></textarea>
            @for (error of form.reasonDetail().errors(); track error.kind) {
              <mat-error>{{ error.message }}</mat-error>
            }
          </mat-form-field>
        }

        <mat-form-field appearance="outline" class="full-width">
          <mat-label>Pin Until (Optional)</mat-label>
          <input
            matInput
            [matDatepicker]="picker"
            [formField]="form.pinnedUntil"
            [min]="minDate"
            [max]="maxDate"
            >
          <mat-datepicker-toggle matIconSuffix [for]="picker" />
          <mat-datepicker #picker />
          <mat-hint>Leave empty for indefinite pinning</mat-hint>
        </mat-form-field>

        @if (!form.pinnedUntil().value()) {
          <div class="duration-chips">
            <span class="chips-label">Quick select:</span>
            @for (preset of durationPresets; track preset) {
              <button
                mat-stroked-button
                type="button"
                (click)="setDuration(preset.days)"
                class="duration-chip"
                >
                {{ preset.label }}
              </button>
            }
          </div>
        }
      </form>
    </mat-dialog-content>
    
    <mat-dialog-actions align="end">
      <button type="button" mat-button mat-dialog-close>Cancel</button>
      <button type="button"
        mat-flat-button
        [disabled]="form().invalid()"
        (click)="submit()"
        >
        <mat-icon>push_pin</mat-icon>
        Pin Device
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

    .pin-form {
      display: flex;
      flex-direction: column;
      gap: 8px;
      min-width: 400px;
    }

    .full-width {
      width: 100%;
    }

    .duration-chips {
      display: flex;
      align-items: center;
      gap: 8px;
      flex-wrap: wrap;
      margin-top: -8px;
      padding: 4px 0;
    }

    .chips-label {
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .duration-chip {
      font-size: 12px;
      padding: 0 12px;
      height: 28px;
      line-height: 28px;
      border-radius: 14px;
      border-color: var(--cco-border);
      color: var(--cco-accent);
    }

    .duration-chip:hover {
      background: var(--cco-accent-surface);
      border-color: var(--cco-accent);
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
export class PinDialogComponent {
  private readonly dialogRef =
    inject<MatDialogRef<PinDialogComponent, PinDialogResult>>(MatDialogRef);
  protected readonly data = inject<PinDialogData>(MAT_DIALOG_DATA);

  private readonly model = signal<PinFormModel>({
    reason: '',
    reasonDetail: '',
    pinnedUntil: null,
  });

  protected readonly form = form(this.model, pin => {
    required(pin.reason, {message: 'Please select a reason'});
    required(pin.reasonDetail, {
      message: 'Please provide details',
      when: ctx => ctx.valueOf(pin.reason) === 'other',
    });
    minLength(pin.reasonDetail, 10, {
      message: 'Please provide at least 10 characters',
      when: ctx => ctx.valueOf(pin.reason) === 'other',
    });
  });

  protected readonly minDate = new Date();
  protected readonly maxDate = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000); // 30 days from now

  protected readonly reasons: Array<{value: PinReason; label: string}> = Object.entries(
    PIN_REASON_LABELS
  ).map(([value, label]) => ({value: value as PinReason, label}));

  protected readonly durationPresets = [
    {label: '24 hours', days: 1},
    {label: '3 days', days: 3},
    {label: '1 week', days: 7},
    {label: '2 weeks', days: 14},
  ];

  protected setDuration(days: number): void {
    const date = new Date();
    date.setDate(date.getDate() + days);
    this.model.update(value => ({...value, pinnedUntil: date}));
  }

  protected submit(): void {
    if (this.form().invalid()) {
      return;
    }

    const {reason, reasonDetail, pinnedUntil} = this.model();
    this.dialogRef.close({
      reason: reason as PinReason,
      reasonDetail: reasonDetail || undefined,
      pinnedUntil: pinnedUntil ?? undefined,
    });
  }
}
