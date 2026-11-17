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
import {FormField, form, maxLength, pattern, required} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MAT_DIALOG_DATA, MatDialogModule, MatDialogRef} from '@angular/material/dialog';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatInputModule} from '@angular/material/input';
import {MatSlideToggleModule} from '@angular/material/slide-toggle';
import type {HostImageConfig} from '../../models/host-image-config.model';
import {getMachineFamilyDisplayName} from '../../models/host-image-config.model';

export interface EditDialogData {
  /** Absent = create mode: the machine family becomes an editable field. */
  config?: HostImageConfig;
}

export interface EditDialogResult {
  machineFamily: string;
  imageFamily: string;
  sourceProject: string;
  description: string;
  enabled: boolean;
}

@Component({
  selector: 'app-host-image-config-edit-dialog',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    FormField,
    MatDialogModule,
    MatButtonModule,
    MatFormFieldModule,
    MatInputModule,
    MatSlideToggleModule,
  ],
  template: `
    <h2 mat-dialog-title>{{ data.config ? 'Edit' : 'Add' }} Image Configuration</h2>

    <mat-dialog-content>
      @if (data.config) {
        <div class="config-header">
          <span class="family-label">Machine Family</span>
          <span class="family-value">{{ familyDisplayName }}</span>
        </div>
      }

      <form class="config-form">
        @if (!data.config) {
          <mat-form-field appearance="outline" class="full-width">
            <mat-label>Machine Family</mat-label>
            <input matInput [formField]="form.machineFamily" placeholder="e.g., n1, g2, c4a" />
            <mat-hint>GCE machine-family prefix this image applies to</mat-hint>
            @for (error of form.machineFamily().errors(); track error.kind) {
              <mat-error>{{ error.message }}</mat-error>
            }
          </mat-form-field>
        }

        <mat-form-field appearance="outline" class="full-width">
          <mat-label>Image Family</mat-label>
          <input matInput [formField]="form.imageFamily" placeholder="e.g., cco-host" />
          <mat-hint>GCE image family name used for this machine family</mat-hint>
          @for (error of form.imageFamily().errors(); track error.kind) {
            <mat-error>{{ error.message }}</mat-error>
          }
        </mat-form-field>

        <mat-form-field appearance="outline" class="full-width">
          <mat-label>Source Project</mat-label>
          <input matInput [formField]="form.sourceProject" placeholder="e.g., my-image-project" />
          <mat-hint>GCP project containing the image; empty uses the deployment's CCO_HOST_IMAGE_PROJECT</mat-hint>
          @for (error of form.sourceProject().errors(); track error.kind) {
            <mat-error>{{ error.message }}</mat-error>
          }
        </mat-form-field>

        <mat-form-field appearance="outline" class="full-width">
          <mat-label>Description</mat-label>
          <textarea matInput [formField]="form.description" rows="2" placeholder="Optional description"></textarea>
          <mat-hint>{{ form.description().value().length }}/500</mat-hint>
        </mat-form-field>

        <div class="toggle-row">
          <mat-slide-toggle [formField]="form.enabled">Enabled</mat-slide-toggle>
          <span class="toggle-hint">Disabled configurations will not be used for host provisioning</span>
        </div>
      </form>
    </mat-dialog-content>

    <mat-dialog-actions align="end">
      <button type="button" mat-button mat-dialog-close>Cancel</button>
      <button type="button"
        mat-flat-button
        [disabled]="form().invalid()"
        (click)="save()">
        {{ data.config ? 'Save Changes' : 'Create' }}
      </button>
    </mat-dialog-actions>
  `,
  styles: `
    .config-header {
      display: flex;
      flex-direction: column;
      gap: 4px;
      margin-bottom: 24px;
      padding: 16px;
      background: var(--cco-surface-variant);
      border-radius: 8px;
    }

    .family-label {
      font-size: 12px;
      color: var(--cco-text-secondary);
      text-transform: uppercase;
      letter-spacing: 0.5px;
    }

    .family-value {
      font-size: 16px;
      font-weight: 500;
      color: var(--cco-text);
    }

    .config-form {
      display: flex;
      flex-direction: column;
      gap: 16px;
      min-width: 400px;
    }

    .full-width {
      width: 100%;
    }

    .toggle-row {
      display: flex;
      flex-direction: column;
      gap: 4px;
      padding: 8px 0;
    }

    .toggle-hint {
      font-size: 12px;
      color: var(--cco-text-secondary);
      margin-left: 48px;
    }
  `,
})
export class HostImageConfigEditDialogComponent {
  readonly data = inject<EditDialogData>(MAT_DIALOG_DATA);
  private readonly dialogRef = inject(MatDialogRef<HostImageConfigEditDialogComponent>);

  private readonly model = signal<EditDialogResult>({
    machineFamily: this.data.config?.machineFamily ?? '',
    imageFamily: this.data.config?.imageFamily ?? '',
    sourceProject: this.data.config?.sourceProject ?? '',
    description: this.data.config?.description ?? '',
    enabled: this.data.config?.enabled ?? true,
  });

  protected readonly form = form(this.model, config => {
    required(config.machineFamily, {message: 'Machine family is required'});
    pattern(config.machineFamily, /^[a-z][a-z0-9-]{0,49}$/, {
      message: 'Must be lowercase letters, numbers, and hyphens',
    });
    required(config.imageFamily, {message: 'Image family is required'});
    pattern(config.imageFamily, /^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$/, {
      message: 'Must be lowercase letters, numbers, and hyphens',
    });
    pattern(config.sourceProject, /^([a-z][a-z0-9-]{4,28}[a-z0-9])?$/, {
      message: 'Must be a valid GCP project ID',
    });
    maxLength(config.description, 500);
  });

  protected readonly familyDisplayName = this.data.config
    ? getMachineFamilyDisplayName(this.data.config.machineFamily)
    : '';

  protected save(): void {
    if (this.form().invalid()) {
      return;
    }
    this.dialogRef.close(this.model());
  }
}
