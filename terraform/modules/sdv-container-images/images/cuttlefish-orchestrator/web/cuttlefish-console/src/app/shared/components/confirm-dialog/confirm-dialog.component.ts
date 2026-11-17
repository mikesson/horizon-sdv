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

import {ChangeDetectionStrategy, Component, inject} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MAT_DIALOG_DATA, MatDialogModule, MatDialogRef} from '@angular/material/dialog';
import {MatIconModule} from '@angular/material/icon';

/**
 * Configuration options for the confirm dialog.
 */
export interface ConfirmDialogData {
  title: string;
  message: string;
  /** Emphasized warning line under the message (e.g. a foreign-creator note). */
  warning?: string;
  confirmText?: string;
  cancelText?: string;
  /**
   * Semantic color for the confirm button.
   * Use 'warn' for destructive actions (delete, stop).
   * Use 'primary' for constructive actions (create, start).
   */
  confirmColor?: 'primary' | 'warn';
  /** Optional Material Icon name to display in the header (e.g., 'warning'). */
  icon?: string;
  iconColor?: 'primary' | 'accent' | 'warn';
}

/**
 * A purely presentational dialog component for binary user choices.
 *
 * ARCHITECTURE:
 * This is a "Dumb Component" (Presentational). It has no dependencies on
 * services or stores. It strictly accepts configuration data and returns
 * a boolean result to the "Smart Component" that opened it.
 *
 * USAGE:
 * ```typescript
 * const dialogRef = this.dialog.open(ConfirmDialogComponent, {
 *   data: {
 *     title: 'Stop Device',
 *     message: 'Are you sure you want to stop "my-device"?',
 *     confirmText: 'Stop',
 *     confirmColor: 'warn',
 *     icon: 'stop',
 *     iconColor: 'warn'
 *   }
 * });
 *
 * dialogRef.afterClosed().subscribe(confirmed => {
 *   if (confirmed) {
 *     // User clicked confirm
 *   }
 * });
 * ```
 */
@Component({
  selector: 'app-confirm-dialog',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatDialogModule, MatButtonModule, MatIconModule],
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
      @if (data.warning) {
        <p class="dialog-warning">{{ data.warning }}</p>
      }
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button type="button" mat-button (click)="onCancel()">
        {{ data.cancelText || 'Cancel' }}
      </button>
      <button type="button"
        mat-flat-button
        [color]="data.confirmColor || 'primary'"
        (click)="onConfirm()"
        cdkFocusInitial
      >
        {{ data.confirmText || 'Confirm' }}
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
        margin: 0;
      }

      .dialog-warning {
        font-size: 14px;
        line-height: 1.5;
        color: var(--cco-error);
        font-weight: 500;
        margin: 8px 0 0;
      }

      mat-dialog-content {
        padding: 0 24px 24px;
      }

      mat-dialog-actions {
        padding: 8px 16px 16px;
        gap: 8px;
      }
    `,
})
export class ConfirmDialogComponent {
  readonly dialogRef = inject(MatDialogRef<ConfirmDialogComponent>);
  readonly data: ConfirmDialogData = inject<ConfirmDialogData>(MAT_DIALOG_DATA);

  onCancel(): void {
    this.dialogRef.close(false);
  }

  onConfirm(): void {
    this.dialogRef.close(true);
  }
}
