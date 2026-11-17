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
import {FormField, form, maxLength, required} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MAT_DIALOG_DATA, MatDialogModule, MatDialogRef} from '@angular/material/dialog';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatInputModule} from '@angular/material/input';
import {MatSlideToggleModule} from '@angular/material/slide-toggle';
import type {Build} from '../../models/build.model';

export interface BuildEditDialogData {
  build: Build;
}

export interface BuildEditDialogResult {
  name: string;
  description: string;
  isDefault: boolean;
}

/** Edits build metadata (name, description, default flag) via PATCH /v2/builds/{id}. */
@Component({
  selector: 'app-build-edit-dialog',
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
    <h2 mat-dialog-title>Edit Build</h2>

    <mat-dialog-content>
      <form class="edit-form">
        <mat-form-field appearance="outline" class="full-width">
          <mat-label>Name</mat-label>
          <input matInput [formField]="form.name" />
          @for (error of form.name().errors(); track error.kind) {
            <mat-error>{{ error.message }}</mat-error>
          }
        </mat-form-field>

        <mat-form-field appearance="outline" class="full-width">
          <mat-label>Description</mat-label>
          <textarea matInput [formField]="form.description" rows="2"></textarea>
          <mat-hint>{{ form.description().value().length }}/500</mat-hint>
        </mat-form-field>

        <div class="toggle-row">
          <mat-slide-toggle [formField]="form.isDefault">Default build</mat-slide-toggle>
          <span class="toggle-hint">Preselected when creating devices</span>
        </div>
      </form>
    </mat-dialog-content>

    <mat-dialog-actions align="end">
      <button type="button" mat-button mat-dialog-close>Cancel</button>
      <button type="button"
        mat-flat-button
        [disabled]="form().invalid()"
        (click)="save()">
        Save Changes
      </button>
    </mat-dialog-actions>
  `,
  styles: `
    .edit-form {
      display: flex;
      flex-direction: column;
      gap: 16px;
      min-width: 400px;
      padding-top: 8px;
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
export class BuildEditDialogComponent {
  readonly data = inject<BuildEditDialogData>(MAT_DIALOG_DATA);
  private readonly dialogRef = inject(MatDialogRef<BuildEditDialogComponent>);

  private readonly model = signal<BuildEditDialogResult>({
    name: this.data.build.name,
    description: this.data.build.description ?? '',
    isDefault: this.data.build.isDefault ?? false,
  });

  protected readonly form = form(this.model, build => {
    required(build.name, {message: 'Name is required'});
    maxLength(build.name, 100);
    maxLength(build.description, 500);
  });

  protected save(): void {
    if (this.form().invalid()) {
      return;
    }
    this.dialogRef.close(this.model());
  }
}
