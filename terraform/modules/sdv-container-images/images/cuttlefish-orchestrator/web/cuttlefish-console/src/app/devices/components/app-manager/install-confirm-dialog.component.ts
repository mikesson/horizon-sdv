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
import {MatIconModule} from '@angular/material/icon';
import {MatRadioModule} from '@angular/material/radio';
import type {InstallPlan} from '../../services/adb/install-plan';

export interface InstallConfirmData {
  plan: InstallPlan;
}

/** How the user chose to treat an ambiguous multi-file drop. */
export type InstallGrouping = 'splits' | 'separate';

export interface InstallConfirmResult {
  grouping: InstallGrouping;
}

/**
 * Previews what will be installed before committing: package/version/split
 * per file, archive warnings, and, when grouping is ambiguous, a choice
 * between "splits of one app" and "separate apps".
 */
@Component({
  selector: 'app-install-confirm-dialog',
  imports: [FormField, MatButtonModule, MatDialogModule, MatIconModule, MatRadioModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <h2 mat-dialog-title>Install {{ summary() }}</h2>
    <mat-dialog-content>
      @for (unit of data.plan.units; track $index) {
        <div class="unit">
          <div class="unit-header">
            <mat-icon>{{ unit.files.length > 1 ? 'layers' : 'android' }}</mat-icon>
            <span class="pkg">{{ unit.packageName ?? 'Unknown package' }}</span>
            @if (unit.versionName) {
              <span class="version">v{{ unit.versionName }}</span>
            }
          </div>
          <table class="files">
            @for (file of unit.files; track file.name) {
              <tr>
                <td class="name">{{ file.name }}</td>
                <td class="tag">{{ splitLabel(file.info?.split) }}</td>
                <td class="size">{{ sizeLabel(file.size) }}</td>
              </tr>
            }
          </table>
        </div>
      }

      @if (data.plan.ambiguous) {
        <div class="grouping">
          <p class="grouping-title">These files couldn't be matched by manifest. Install as:</p>
          <mat-radio-group [formField]="groupingForm.grouping" class="grouping-options">
            <mat-radio-button value="splits">Splits of one app</mat-radio-button>
            <mat-radio-button value="separate">{{ data.plan.units[0].files.length }} separate apps</mat-radio-button>
          </mat-radio-group>
        </div>
      }

      @for (warning of data.plan.warnings; track $index) {
        <div class="warning">
          <mat-icon>warning</mat-icon>
          <span>{{ warning }}</span>
        </div>
      }
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button type="button" mat-button (click)="cancel()">Cancel</button>
      <button type="button" mat-flat-button (click)="confirm()">Install</button>
    </mat-dialog-actions>
  `,
  styles: `
      mat-dialog-content {
        min-width: 420px;
        max-width: 560px;
      }

      .unit {
        margin-bottom: 16px;
      }

      .unit-header {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-bottom: 8px;
      }

      .unit-header .pkg {
        font-weight: 500;
      }

      .unit-header .version {
        color: var(--cco-text-secondary);
        font-size: 12px;
      }

      table.files {
        width: 100%;
        border-collapse: collapse;
        font-size: 12px;
      }

      table.files td {
        padding: 2px 8px 2px 0;
      }

      table.files .name {
        font-family: monospace;
      }

      table.files .tag {
        color: var(--cco-text-secondary);
      }

      table.files .size {
        text-align: right;
        color: var(--cco-text-secondary);
        white-space: nowrap;
      }

      .grouping {
        margin-top: 8px;
        padding: 12px;
        border-radius: 6px;
        background: rgba(255, 255, 255, 0.04);
      }

      .grouping-title {
        margin: 0 0 8px;
        font-size: 12px;
      }

      .grouping-options {
        display: flex;
        flex-direction: column;
        gap: 4px;
      }

      .warning {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-top: 8px;
        font-size: 12px;
        color: var(--cco-warning);
      }

      .warning mat-icon {
        font-size: 18px;
        width: 18px;
        height: 18px;
      }
    `,
})
export class InstallConfirmDialogComponent {
  protected readonly data = inject<InstallConfirmData>(MAT_DIALOG_DATA);
  private readonly dialogRef =
    inject<MatDialogRef<InstallConfirmDialogComponent, InstallConfirmResult>>(MatDialogRef);

  private readonly draft = signal<{grouping: InstallGrouping}>({grouping: 'splits'});
  protected readonly groupingForm = form(this.draft);

  protected readonly summary = computed(() => {
    const units = this.data.plan.units;
    if (units.length === 1) {
      const unit = units[0];
      return unit.files.length > 1
        ? `${unit.packageName ?? 'app'} (${unit.files.length} splits)`
        : 'APK';
    }
    return `${units.length} apps`;
  });

  protected splitLabel(split: string | undefined): string {
    return split ? split : 'base';
  }

  protected sizeLabel(bytes: number): string {
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  }

  protected cancel(): void {
    this.dialogRef.close();
  }

  protected confirm(): void {
    this.dialogRef.close({grouping: this.draft().grouping});
  }
}
