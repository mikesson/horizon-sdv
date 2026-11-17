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
import {MatButtonModule} from '@angular/material/button';
import {MAT_DIALOG_DATA, MatDialogModule, MatDialogRef} from '@angular/material/dialog';
import {MatIconModule} from '@angular/material/icon';
import {MatListModule} from '@angular/material/list';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import type {InstanceTarget} from '../../models/instance-target';
import {primaryInstance} from '../../models/instance-target';
import {DeviceStore} from '../../store/device.signal-store';

export interface InstallDevicePickerData {
  fileName: string;
}

/** Picks a running device to install an artifact onto; returns its id. */
@Component({
  selector: 'app-install-device-picker',
  imports: [
    MatButtonModule,
    MatDialogModule,
    MatIconModule,
    MatListModule,
    MatProgressSpinnerModule,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <h2 mat-dialog-title>Install {{ data.fileName }}</h2>
    <mat-dialog-content>
      @if (loading()) {
        <div class="center"><mat-spinner aria-label="Loading" diameter="28" /></div>
      } @else if (runningDevices().length === 0) {
        <p class="empty">No running devices. Start a device first.</p>
      } @else {
        <mat-selection-list [multiple]="false">
          @for (device of runningDevices(); track device.id) {
            <mat-list-option [value]="device.id" (click)="selected.set(device.id)">
              <mat-icon matListItemIcon>smartphone</mat-icon>
              <span matListItemTitle>{{ device.name }}</span>
              <span matListItemLine>{{ device.buildTarget || 'AOSP' }}</span>
            </mat-list-option>
          }
        </mat-selection-list>
      }
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button type="button" mat-button (click)="cancel()">Cancel</button>
      <button type="button" mat-flat-button [disabled]="!selected()" (click)="confirm()">
        Install
      </button>
    </mat-dialog-actions>
  `,
  styles: `
      mat-dialog-content {
        min-width: 360px;
      }

      .center {
        display: flex;
        justify-content: center;
        padding: 24px;
      }

      .empty {
        color: var(--cco-text-secondary);
      }
    `,
})
export class InstallDevicePickerComponent {
  protected readonly data = inject<InstallDevicePickerData>(MAT_DIALOG_DATA);
  private readonly dialogRef =
    inject<MatDialogRef<InstallDevicePickerComponent, InstanceTarget>>(MatDialogRef);
  private readonly store = inject(DeviceStore);

  protected readonly loading = this.store.loading;
  protected readonly selected = signal<string | null>(null);

  protected readonly runningDevices = computed(() =>
    this.store.entities().filter(device => device.status === 'running')
  );

  constructor() {
    this.store.loadAll(undefined);
  }

  protected cancel(): void {
    this.dialogRef.close();
  }

  // Closes with an instance, not a device id, because an APK installs into one
  // Android VM's package manager. This dialog offers no instance choice, so it
  // picks the group's first. Closing with the id alone would leave the caller
  // holding a value its own type says is a target.
  protected confirm(): void {
    const id = this.selected();
    const device = this.runningDevices().find(d => d.id === id);
    if (device) {
      this.dialogRef.close({deviceId: device.id, instance: primaryInstance(device)});
    }
  }
}
