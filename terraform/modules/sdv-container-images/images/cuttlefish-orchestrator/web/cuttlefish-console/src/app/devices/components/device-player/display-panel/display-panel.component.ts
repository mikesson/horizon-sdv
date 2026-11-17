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

import {ChangeDetectionStrategy, Component, computed, inject, input, resource} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatChipsModule} from '@angular/material/chips';
import {MatDividerModule} from '@angular/material/divider';
import {MatIconModule} from '@angular/material/icon';
import {MatMenuModule} from '@angular/material/menu';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import {MatSnackBar, MatSnackBarModule} from '@angular/material/snack-bar';
import {MatTooltipModule} from '@angular/material/tooltip';
import {firstValueFrom} from 'rxjs';
import {LoggingService} from '../../../../shared/services';
import type {DisplayInfo, DisplayPreset} from '../../../models/device.model';
import {DeviceApiService} from '../../../services/device-api.service';
import {DeviceInputService} from '../../../services/input/device-input.service';
import {DeviceStreamService} from '../../../services/streaming/device-stream.service';

/**
 * Display Panel Component
 *
 * Allows users to manage displays on a device for multi-display WebRTC streaming.
 * Shows current displays and allows adding/removing displays using presets.
 */
@Component({
  selector: 'app-display-panel',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    MatButtonModule,
    MatIconModule,
    MatMenuModule,
    MatTooltipModule,
    MatProgressSpinnerModule,
    MatChipsModule,
    MatDividerModule,
    MatSnackBarModule,
  ],
  template: `
    <div class="display-panel">
      <div class="panel-header">
        <mat-icon>monitor</mat-icon>
        <span>Displays</span>
        @if (loading()) {
          <mat-spinner aria-label="Loading" diameter="16" />
        }
      </div>

      <div class="display-list">
        @for (display of displays(); track display.display_number) {
          <div class="display-item">
            <div class="display-info">
              <span class="display-number">#{{ display.display_number }}</span>
              <span class="display-resolution">{{ display.width }}x{{ display.height }}</span>
              <span class="display-dpi">{{ display.dpi }} DPI</span>
            </div>
            <button type="button"
              mat-icon-button
              (click)="onRemoveDisplay(display.display_number)"
              matTooltip="Remove display"
              [disabled]="loading() || displays().length === 1" aria-label="Remove display">
              <mat-icon>close</mat-icon>
            </button>
          </div>
        }

        @if (displays().length === 0 && !loading()) {
          <div class="no-displays">
            No displays attached
          </div>
        }
      </div>

      <mat-divider />

      <div class="add-display">
        <button type="button" mat-button [matMenuTriggerFor]="presetMenu" [disabled]="loading()">
          <mat-icon>add</mat-icon>
          Add Display
        </button>

        <mat-menu #presetMenu="matMenu">
          @for (preset of presets(); track preset.name) {
            <button type="button" mat-menu-item (click)="onAddDisplay(preset.name)">
              <mat-icon>{{ preset.icon }}</mat-icon>
              <span>{{ preset.name }} ({{ preset.width }}x{{ preset.height }})</span>
            </button>
          }
        </mat-menu>
      </div>
    </div>
  `,
  styles: `
    .display-panel {
      background: rgba(0, 0, 0, 0.85);
      border-radius: 8px;
      padding: 12px;
      min-width: 200px;
      color: white;
    }

    .panel-header {
      display: flex;
      align-items: center;
      gap: 8px;
      margin-bottom: 12px;
      font-weight: 500;
    }

    .panel-header mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
    }

    .display-list {
      display: flex;
      flex-direction: column;
      gap: 8px;
      margin-bottom: 12px;
    }

    .display-item {
      display: flex;
      align-items: center;
      justify-content: space-between;
      background: rgba(255, 255, 255, 0.1);
      border-radius: 4px;
      padding: 8px 8px 8px 12px;
    }

    .display-info {
      display: flex;
      align-items: center;
      gap: 8px;
      font-size: 12px;
    }

    .display-number {
      font-weight: 600;
      color: var(--cco-accent);
    }

    .display-resolution {
      font-family: monospace;
    }

    .display-dpi {
      color: rgba(255, 255, 255, 0.6);
      font-size: 11px;
    }

    .no-displays {
      text-align: center;
      padding: 16px;
      color: rgba(255, 255, 255, 0.5);
      font-size: 12px;
    }

    .add-display {
      display: flex;
      justify-content: center;
      padding-top: 8px;
    }

    .add-display button {
      color: white;
    }

    mat-divider {
      border-color: rgba(255, 255, 255, 0.2);
    }
  `,
})
export class DisplayPanelComponent {
  private readonly deviceApi = inject(DeviceApiService);
  private readonly inputService = inject(DeviceInputService);
  private readonly streaming = inject(DeviceStreamService);
  private readonly snackBar = inject(MatSnackBar);
  private readonly logger = inject(LoggingService);

  readonly deviceId = input('');
  /** The Android VM whose displays these are; empty until the host answers. */
  readonly instance = input('');

  private readonly displayData = resource({
    params: () =>
      this.deviceId() && this.instance()
        ? {deviceId: this.deviceId(), instance: this.instance()}
        : undefined,
    loader: async ({params: target}) => {
      try {
        return await firstValueFrom(this.deviceApi.listDisplays(target));
      } catch (error) {
        this.logger.error('DisplayPanel', 'Failed to load displays', error);
        throw error instanceof Error ? error : new Error(String(error));
      }
    },
  });

  protected readonly loading = this.displayData.isLoading;

  protected readonly displays = computed<DisplayInfo[]>(() =>
    this.displayData.hasValue() ? this.displayData.value().displays : []
  );

  protected readonly presets = computed(() => {
    const presets: DisplayPreset[] = this.displayData.hasValue()
      ? this.displayData.value().presets
      : [];
    return presets.map(preset => ({...preset, icon: PRESET_ICONS[preset.name] ?? 'monitor'}));
  });

  protected onAddDisplay(presetName: string): void {
    const preset = this.presets().find(p => p.name === presetName);
    if (!preset) {
      this.snackBar.open(`Unknown preset: ${presetName}`, 'Dismiss', {duration: 3000});
      return;
    }

    const success = this.inputService.addDisplay(
      this.deviceId(),
      preset.width,
      preset.height,
      preset.dpi,
      preset.refresh_rate_hz
    );

    if (success) {
      this.snackBar.open(`Adding ${preset.name} display...`, 'Dismiss', {duration: 3000});
      this.refreshAfterDeviceApplies();
    } else {
      this.snackBar.open('WebRTC not connected. Cannot add display.', 'Dismiss', {duration: 5000});
    }
  }

  protected onRemoveDisplay(displayNumber: number): void {
    const success = this.inputService.removeDisplay(this.deviceId(), displayNumber);

    if (success) {
      this.snackBar.open(`Removing display #${displayNumber}...`, 'Dismiss', {duration: 3000});
      this.refreshAfterDeviceApplies();
    } else {
      this.snackBar.open('WebRTC not connected. Cannot remove display.', 'Dismiss', {
        duration: 5000,
      });
    }
  }

  /** Display changes are applied device-side with a delay; refresh after a beat. */
  private refreshAfterDeviceApplies(): void {
    // The device answers a display change with a renegotiation offer, so let
    // the polling signaling fallback pick it up promptly.
    this.streaming
      .session({deviceId: this.deviceId(), instance: this.instance()})()
      ?.expectSignalingSoon();
    setTimeout(() => this.displayData.reload(), 1000);
  }
}

const PRESET_ICONS: Record<string, string> = {
  Phone: 'smartphone',
  Tablet: 'tablet',
  Monitor: 'desktop_windows',
  'Foldable (Outer)': 'smartphone',
  'Foldable (Inner)': 'tablet_android',
  Automotive: 'directions_car',
};
