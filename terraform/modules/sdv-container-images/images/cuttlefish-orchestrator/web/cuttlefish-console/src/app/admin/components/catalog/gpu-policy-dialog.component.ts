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

import {ChangeDetectionStrategy, Component, computed, inject, signal} from '@angular/core';
import {FormField, form} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MAT_DIALOG_DATA, MatDialogModule, MatDialogRef} from '@angular/material/dialog';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatSelectModule} from '@angular/material/select';
import type {AdminFamily} from '../../models/machine-family.model';

/** What a GPU host of this family gets when the caller does not say. */
export interface GpuPolicyResult {
  maxGpus: number;
  defaultGpuType: string;
  defaultGpuCount: number;
}

/** GPU types the catalog can attach; the backend validates the choice too. */
const GPU_TYPES = [
  'nvidia-tesla-t4',
  'nvidia-tesla-p4',
  'nvidia-tesla-v100',
  'nvidia-tesla-p100',
  'nvidia-l4',
];

/**
 * Edits the GPU policy of a family whose GPUs attach. Families with an
 * integrated accelerator never reach here: that count is what the hardware has,
 * discovered rather than curated, and the backend rejects a policy written onto
 * it.
 */
@Component({
  selector: 'app-gpu-policy-dialog',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    FormField,
    MatButtonModule,
    MatDialogModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatSelectModule,
  ],
  template: `
    <h2 mat-dialog-title class="dialog-title">
      <mat-icon class="dialog-icon">memory</mat-icon>
      GPU policy for {{ data.family }}
    </h2>
    <mat-dialog-content>
      <p class="dialog-message">
        Applies when a caller asks a host of this family for a GPU without naming one.
      </p>

      <mat-form-field appearance="outline" class="full-width">
        <mat-label>Maximum GPUs</mat-label>
        <mat-select [formField]="policyForm.maxGpus">
          @for (n of maxChoices; track n) {
            <mat-option [value]="n">{{ n }}</mat-option>
          }
        </mat-select>
        <mat-hint>The ceiling host create will accept.</mat-hint>
      </mat-form-field>

      <mat-form-field appearance="outline" class="full-width">
        <mat-label>Default GPU type</mat-label>
        <mat-select [formField]="policyForm.defaultGpuType">
          <mat-option value="">None</mat-option>
          @for (type of gpuTypes; track type) {
            <mat-option [value]="type">{{ type }}</mat-option>
          }
        </mat-select>
      </mat-form-field>

      <mat-form-field appearance="outline" class="full-width">
        <mat-label>Default GPU count</mat-label>
        <input matInput type="number" [formField]="policyForm.defaultGpuCount" />
        <mat-hint>0 attaches no GPU unless the caller asks for one.</mat-hint>
      </mat-form-field>

      @if (countInvalid()) {
        <p class="dialog-error">
          The default count must be between 0 and the maximum.
        </p>
      }
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button type="button" mat-button (click)="dialogRef.close()">Cancel</button>
      <button type="button" mat-flat-button [disabled]="countInvalid()" (click)="save()">
        Update
      </button>
    </mat-dialog-actions>
  `,
  styles: `
    .dialog-title {
      display: flex;
      align-items: center;
      gap: var(--cco-space-8);
    }

    .full-width {
      width: 100%;
    }

    .dialog-message {
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-sm);
    }

    .dialog-error {
      color: var(--cco-error);
      font-size: var(--cco-text-sm);
    }
  `,
})
export class GpuPolicyDialogComponent {
  protected readonly data = inject<AdminFamily>(MAT_DIALOG_DATA);
  protected readonly dialogRef =
    inject<MatDialogRef<GpuPolicyDialogComponent, GpuPolicyResult>>(MatDialogRef);

  protected readonly gpuTypes = GPU_TYPES;
  protected readonly maxChoices = [1, 2, 3, 4];

  private readonly draft = signal<GpuPolicyResult>({
    maxGpus: this.data.maxGpus || 1,
    defaultGpuType: this.data.defaultGpuType ?? '',
    defaultGpuCount: this.data.defaultGpuCount ?? 0,
  });
  protected readonly policyForm = form(this.draft);

  // `min`/`max` attributes cannot ride a [formField] input (NG8022), so the
  // bound is a guard on the value rather than markup on the control.
  protected readonly countInvalid = computed(() => {
    const {maxGpus, defaultGpuCount} = this.draft();
    const count = Number(defaultGpuCount);
    return !Number.isInteger(count) || count < 0 || count > Number(maxGpus);
  });

  protected save(): void {
    const {maxGpus, defaultGpuType, defaultGpuCount} = this.draft();
    this.dialogRef.close({
      maxGpus: Number(maxGpus),
      defaultGpuType,
      defaultGpuCount: Number(defaultGpuCount),
    });
  }
}
