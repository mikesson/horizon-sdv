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
import {MatInputModule} from '@angular/material/input';
import {
  extendedExpiry,
  HOST_EXTEND_MAX_HOURS,
  HOST_EXTEND_MIN_HOURS,
  isValidExtendHours,
  passesVmStop,
} from '../../models/host-extend';

export interface HostExtendDialogData {
  hostName: string;
  expiresAt: Date;
  vmTerminatesAt?: Date;
}

/** Closes with the hours to add, or undefined when cancelled. */
@Component({
  selector: 'app-host-extend-dialog',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [DatePipe, MatButtonModule, MatDialogModule, MatFormFieldModule, MatInputModule],
  template: `
    <h2 mat-dialog-title>Extend {{ data.hostName }}</h2>
    <mat-dialog-content>
      <p class="current">Lease expires {{ data.expiresAt | date: 'medium' }}</p>
      <mat-form-field class="hours">
        <mat-label>Hours to add</mat-label>
        <input
          matInput
          type="number"
          [min]="minHours"
          [max]="maxHours"
          step="1"
          [value]="hours()"
          (input)="setHours($event)"
        />
        @if (!valid()) {
          <mat-hint class="invalid">Enter a whole number from {{ minHours }} to {{ maxHours }}</mat-hint>
        }
      </mat-form-field>
      @if (newExpiry(); as expiry) {
        <p class="preview">New expiry {{ expiry | date: 'medium' }}</p>
        @if (passesVmStop()) {
          <p class="invalid">
            This passes the VM stop time ({{ data.vmTerminatesAt | date: 'medium' }}), so the server
            will refuse it.
          </p>
        }
      }
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button type="button" mat-button mat-dialog-close>Cancel</button>
      <button type="button" mat-flat-button [disabled]="!valid()" (click)="submit()">Extend</button>
    </mat-dialog-actions>
  `,
  styles: `
    .current,
    .preview {
      margin: 0 0 var(--cco-space-8);
    }
    .preview {
      font-weight: 500;
    }
    .hours {
      width: 100%;
    }
    .invalid {
      color: var(--cco-error);
    }
  `,
})
export class HostExtendDialogComponent {
  private readonly dialogRef =
    inject<MatDialogRef<HostExtendDialogComponent, number>>(MatDialogRef);
  protected readonly data = inject<HostExtendDialogData>(MAT_DIALOG_DATA);

  protected readonly minHours = HOST_EXTEND_MIN_HOURS;
  protected readonly maxHours = HOST_EXTEND_MAX_HOURS;

  readonly hours = signal(24);
  readonly valid = computed(() => isValidExtendHours(this.hours()));

  readonly newExpiry = computed(() =>
    this.valid() ? extendedExpiry(this.data.expiresAt, this.hours(), new Date()) : undefined
  );

  protected readonly passesVmStop = computed(() => {
    const expiry = this.newExpiry();
    const stop = this.data.vmTerminatesAt;
    return expiry !== undefined && stop !== undefined && passesVmStop(expiry, stop);
  });

  protected setHours(event: Event): void {
    const raw = (event.target as HTMLInputElement).value;
    this.hours.set(raw === '' ? Number.NaN : Number(raw));
  }

  protected submit(): void {
    if (this.valid()) {
      this.dialogRef.close(this.hours());
    }
  }
}
