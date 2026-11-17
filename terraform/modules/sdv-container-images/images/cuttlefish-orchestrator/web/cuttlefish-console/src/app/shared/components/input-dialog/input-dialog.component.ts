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
import {FormField, form, maxLength, minLength, pattern, required} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MAT_DIALOG_DATA, MatDialogModule, MatDialogRef} from '@angular/material/dialog';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';

/**
 * Configuration options for the input dialog.
 */
export interface InputDialogData {
  title: string;
  message: string;
  inputLabel: string;
  placeholder?: string;
  initialValue?: string;
  confirmText?: string;
  cancelText?: string;
  required?: boolean;
  icon?: string;
  iconColor?: 'primary' | 'accent' | 'warn';
  inputType?: 'text' | 'textarea';
  minLength?: number;
  maxLength?: number;
  validationPattern?: string;
  validationMessage?: string;
}

/**
 * A reusable input dialog component using Angular Material.
 * Replaces native prompt() with a styled Material Dialog.
 *
 * Usage:
 * ```typescript
 * const dialogRef = this.dialog.open(InputDialogComponent, {
 *   data: {
 *     title: 'Pin Device',
 *     message: 'Please provide a reason for pinning this device.',
 *     inputLabel: 'Pin Reason',
 *     placeholder: 'Enter reason...',
 *     confirmText: 'Pin',
 *     icon: 'push_pin',
 *     iconColor: 'warn'
 *   }
 * });
 *
 * dialogRef.afterClosed().subscribe(result => {
 *   if (result) {
 *     // User submitted with value in result
 *   }
 * });
 * ```
 */
@Component({
  selector: 'app-input-dialog',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    FormField,
    MatDialogModule,
    MatButtonModule,
    MatFormFieldModule,
    MatInputModule,
    MatIconModule,
  ],
  template: `
    <h2 mat-dialog-title class="dialog-title">
      @if (data.icon) {
        <mat-icon [class]="'dialog-icon ' + (data.iconColor || '')">
          {{ data.icon }}
        </mat-icon>
      }
      {{ data.title }}
    </h2>
    <mat-dialog-content>
      <p class="dialog-message">{{ data.message }}</p>
      <mat-form-field appearance="outline" class="full-width">
        <mat-label>{{ data.inputLabel }}</mat-label>
        @if (data.inputType === 'textarea') {
          <textarea
            matInput
            [formField]="form"
            [placeholder]="data.placeholder || ''"
            rows="3"
            cdkFocusInitial
          ></textarea>
        } @else {
          <input
            matInput
            [formField]="form"
            [placeholder]="data.placeholder || ''"
            cdkFocusInitial
          />
        }
        @for (error of form().errors(); track error.kind) {
          @if (error.message) {
            <mat-error>{{ error.message }}</mat-error>
          }
        }
        @if (data.maxLength) {
          <mat-hint align="end">
            {{ form().value().length }} / {{ data.maxLength }}
          </mat-hint>
        }
      </mat-form-field>
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button type="button" mat-button (click)="onCancel()">
        {{ data.cancelText || 'Cancel' }}
      </button>
      <button type="button"
        mat-flat-button
        (click)="onSubmit()"
        [disabled]="form().invalid()"
      >
        {{ data.confirmText || 'Submit' }}
      </button>
    </mat-dialog-actions>
  `,
  styles: `
      .dialog-title {
        display: flex;
        align-items: center;
        gap: 12px;
        margin: 0;
        padding: 16px 24px;
      }

      .dialog-icon {
        font-size: 24px;
        width: 24px;
        height: 24px;
      }

      .dialog-icon.warn {
        color: var(--cco-error);
      }

      .dialog-icon.primary {
        color: var(--cco-accent);
      }

      .dialog-icon.accent {
        color: var(--cco-accent);
      }

      .dialog-message {
        font-size: 14px;
        line-height: 1.5;
        color: var(--cco-text);
        margin: 0 0 16px;
      }

      mat-dialog-content {
        padding: 0 24px 24px;
        min-width: 400px;
      }

      .full-width {
        width: 100%;
      }

      mat-dialog-actions {
        padding: 8px 16px 16px;
        gap: 8px;
      }
    `,
})
export class InputDialogComponent {
  readonly dialogRef = inject(MatDialogRef<InputDialogComponent>);
  readonly data: InputDialogData = inject<InputDialogData>(MAT_DIALOG_DATA);

  private readonly value = signal(this.data.initialValue ?? '');

  // Dialog data is fixed for the dialog's lifetime, so rules register once.
  protected readonly form = form(this.value, input => {
    if (this.data.required !== false) {
      required(input);
    }
    if (this.data.minLength) {
      minLength(input, this.data.minLength);
    }
    if (this.data.maxLength) {
      maxLength(input, this.data.maxLength);
    }
    if (this.data.validationPattern) {
      pattern(input, anchoredPattern(this.data.validationPattern), {
        message: this.data.validationMessage,
      });
    }
  });

  protected onCancel(): void {
    this.dialogRef.close(null);
  }

  protected onSubmit(): void {
    if (this.form().valid()) {
      this.dialogRef.close(this.value().trim());
    }
  }
}

/** Anchors a pattern source like `Validators.pattern` did, so existing callers keep exact-match semantics. */
function anchoredPattern(source: string): RegExp {
  const anchored = (source.startsWith('^') ? '' : '^') + source + (source.endsWith('$') ? '' : '$');
  return new RegExp(anchored);
}
