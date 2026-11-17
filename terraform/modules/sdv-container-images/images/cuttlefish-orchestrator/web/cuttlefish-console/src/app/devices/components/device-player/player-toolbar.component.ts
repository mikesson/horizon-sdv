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

import {
  ChangeDetectionStrategy,
  Component,
  computed,
  inject,
  input,
  output,
  signal,
} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatDialog, MatDialogModule} from '@angular/material/dialog';
import {MatDividerModule} from '@angular/material/divider';
import {MatIconModule} from '@angular/material/icon';
import {MatMenuModule} from '@angular/material/menu';
import {MatSnackBar} from '@angular/material/snack-bar';
import {MatTooltipModule} from '@angular/material/tooltip';
import {Router, RouterModule} from '@angular/router';
import type {Adb} from '@yume-chan/adb';
import {firstValueFrom} from 'rxjs';
import type {ConfirmDialogData} from '../../../shared/components';
import {ConfirmDialogComponent, ConnectivityHudComponent} from '../../../shared/components';
import type {HudMetrics} from '../../../shared/components/connectivity-hud/connectivity-hud.component';
import {RegionFlagPipe} from '../../../shared/pipes/region-flag.pipe';
import {LoggingService} from '../../../shared/services';
import {resolveBasePath} from '../../../shared/utils';
import {downloadBlob} from '../../../shared/utils/download';
import type {Device} from '../../models/device.model';
import {AdbService} from '../../services/adb/adb.service';
import {
  areAnimationsEnabled,
  captureBugReport,
  isAdbRoot,
  restartAdbd,
  setAnimationsEnabled,
} from '../../services/adb/adb-diagnostics';
import {exec} from '../../services/adb/adb-exec';
import {DeviceInstallService} from '../../services/adb/device-install.service';
import {DeviceStreamService} from '../../services/streaming/device-stream.service';
import {DeviceStore} from '../../store/device.signal-store';
import type {ExtendDialogData, ExtendDialogResult} from '../lifecycle/extend-dialog.component';
import {ExtendDialogComponent} from '../lifecycle/extend-dialog.component';
import {LifecycleTimerComponent} from '../lifecycle/lifecycle-timer.component';
import {PinButtonComponent} from '../lifecycle/pin-button.component';
import type {PinDialogData, PinDialogResult} from '../lifecycle/pin-dialog.component';
import {PinDialogComponent} from '../lifecycle/pin-dialog.component';

const GPU_CHIP_LABELS: Record<string, string> = {
  gfxstream: 'GPU',
  drm_virgl: 'VirGL',
  swiftshader: 'SW',
  auto: 'Auto',
};

/**
 * The player's header bar: device identity, install/HUD indicators, lifecycle
 * controls, and the device-actions menu (ADB tools, reboot, stop).
 *
 * Owns every action it renders (dialogs, ADB calls, store mutations), so the
 * player container stays a pure session orchestrator.
 */
@Component({
  selector: 'app-player-toolbar',
  imports: [
    RouterModule,
    MatIconModule,
    MatButtonModule,
    MatTooltipModule,
    MatMenuModule,
    MatDividerModule,
    MatDialogModule,
    LifecycleTimerComponent,
    PinButtonComponent,
    RegionFlagPipe,
    ConnectivityHudComponent,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="player-header">
      <div class="header-left">
        <button type="button" mat-icon-button routerLink="/devices" matTooltip="Back to devices" aria-label="Back to devices">
          <mat-icon>arrow_back</mat-icon>
        </button>
        <div class="device-info">
          <h2>{{ device().name }}</h2>
          <div class="device-meta">
            @if (device().hostZone) {
              <span class="region">
                {{ device().hostZone | appRegionFlag:'short' }}
              </span>
            }
            <span class="build">{{ device().buildTarget || 'AOSP' }}</span>
            <!-- gpu_mode is the DEVICE's, which stops being a single answer
                 once its instances carry their own; at N>1 the instance the
                 page is focused on is the fact worth the space instead. -->
            @if (instances().length > 1) {
              <button type="button" mat-button class="instance-switcher" [matMenuTriggerFor]="instanceMenu">
                {{ instance() }}
                <mat-icon iconPositionEnd>arrow_drop_down</mat-icon>
              </button>
              <mat-menu #instanceMenu="matMenu">
                @for (inst of instances(); track inst.name) {
                  <button type="button" mat-menu-item (click)="instanceFocus.emit(inst.name)">
                    <span>{{ inst.name }}</span>
                  </button>
                }
              </mat-menu>
            } @else {
              <span [class]="['gpu-mode', device().gpuMode]">
                {{ gpuModeLabel() }}
              </span>
            }
          </div>
        </div>
      </div>
      <div class="header-center">
        @if (installState()) {
          <button type="button"
            class="install-indicator"
            mat-button
            [matTooltip]="installIndicatorTooltip()"
            (click)="openAppsTab()"
          >
            <mat-icon [class.spin]="installActive()">
              {{ installIndicatorIcon() }}
            </mat-icon>
            <span>{{ installIndicatorLabel() }}</span>
          </button>
          <span class="header-divider"></span>
        }
        <!-- Connectivity HUD (inline mode) -->
        @if (hudMetrics()) {
          <app-connectivity-hud [metrics]="hudMetrics()!" [inline]="true" />
          <span class="header-divider"></span>
        }
        <app-lifecycle-timer
          [lifecycle]="device().lifecycle"
          [showExtendButton]="true"
          (extend)="onExtend()"
        />
        <app-pin-button
          [lifecycle]="device().lifecycle"
          (pin)="onPin()"
          (unpin)="onUnpin()"
        />
      </div>
      <div class="header-right">
        <button type="button"
          mat-icon-button
          [matMenuTriggerFor]="deviceMenu"
          (menuOpened)="refreshDeviceToolState()"
          matTooltip="Device actions" aria-label="Device actions">
          <mat-icon>more_vert</mat-icon>
        </button>
        <mat-menu #deviceMenu="matMenu">
          @if (adbDirectlyReachable()) {
            <button type="button" mat-menu-item (click)="copyAdbCommand()">
              <mat-icon>content_copy</mat-icon>
              <span>Copy ADB command</span>
            </button>
          }
          <button type="button" mat-menu-item (click)="openNewTab()">
            <mat-icon>open_in_new</mat-icon>
            <span>Open in new tab</span>
          </button>
          <mat-divider />
          <button type="button"
            mat-menu-item
            [disabled]="adbState() !== 'connected' || bugReportBusy()"
            (click)="onCaptureBugReport()"
          >
            <mat-icon>bug_report</mat-icon>
            <span>{{ bugReportBusy() ? 'Capturing bug report…' : 'Capture bug report' }}</span>
          </button>
          <button type="button"
            mat-menu-item
            [disabled]="adbState() !== 'connected'"
            (click)="toggleAnimations()"
          >
            <mat-icon>animation</mat-icon>
            <span>{{ animationsEnabled() ? 'Disable animations' : 'Enable animations' }}</span>
          </button>
          <button type="button"
            mat-menu-item
            [disabled]="adbState() !== 'connected'"
            (click)="toggleAdbRoot()"
          >
            <mat-icon>admin_panel_settings</mat-icon>
            <span>{{ adbRoot() ? 'Restart ADB unrooted' : 'Restart ADB as root' }}</span>
          </button>
          <mat-divider />
          <button type="button" mat-menu-item (click)="rebootDevice()">
            <mat-icon>restart_alt</mat-icon>
            <span>Reboot device</span>
          </button>
          <button type="button" mat-menu-item class="danger" (click)="stopDevice()">
            <mat-icon>stop</mat-icon>
            <span>Stop device</span>
          </button>
        </mat-menu>
      </div>
    </div>
  `,
  styles: `
    .player-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding: 8px 16px;
      background: var(--cco-surface);
      border-bottom: 1px solid var(--cco-surface-variant);
      min-height: 56px;
    }

    .header-left, .header-center, .header-right {
      display: flex;
      align-items: center;
      gap: 12px;
    }

    .header-left button, .header-right button {
      color: var(--cco-text-secondary);
    }

    .device-info h2 {
      margin: 0;
      font-size: 16px;
      font-weight: 500;
      color: var(--cco-text);
    }

    .device-meta {
      display: flex;
      gap: 12px;
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .gpu-mode {
      padding: 2px 8px;
      border-radius: 3px;
      background: var(--cco-surface-variant);
    }

    .gpu-mode.gfxstream { background: var(--cco-success); color: white; }
    .gpu-mode.drm_virgl { background: var(--cco-accent); color: white; }

    .instance-switcher {
      min-width: 0;
      height: 24px;
      padding: 0 8px;
      font-size: 12px;
      line-height: 24px;
    }

    .header-divider {
      width: 1px;
      height: 24px;
      background: var(--cco-border);
      margin: 0 4px;
    }

    .install-indicator {
      color: var(--cco-accent);
    }

    .install-indicator mat-icon.spin {
      animation: install-spin 1.2s linear infinite;
    }

    @keyframes install-spin {
      to {
        transform: rotate(360deg);
      }
    }

    button.danger {
      color: var(--cco-error);
    }
  `,
})
export class PlayerToolbarComponent {
  private readonly deviceStore = inject(DeviceStore);
  private readonly streaming = inject(DeviceStreamService);
  private readonly adbService = inject(AdbService);
  private readonly installService = inject(DeviceInstallService);
  private readonly router = inject(Router);
  private readonly dialog = inject(MatDialog);
  private readonly snackBar = inject(MatSnackBar);
  private readonly logger = inject(LoggingService);

  readonly device = input.required<Device>();

  /** The Android VM this toolbar acts on; ADB and installs are per instance. */
  readonly instance = input.required<string>();

  protected readonly instances = computed(() => this.device().instances ?? []);

  private readonly target = computed(() => ({
    deviceId: this.device().id,
    instance: this.instance(),
  }));

  /** The user asked to see install progress; the container opens the panel. */
  readonly appsRequested = output<void>();

  /** The user picked another instance from the switcher. */
  readonly instanceFocus = output<string>();

  protected readonly bugReportBusy = signal(false);
  protected readonly adbRoot = signal(false);
  protected readonly animationsEnabled = signal(true);

  protected readonly adbState = computed(() => this.adbService.state(this.target())());

  protected readonly installState = computed(() => this.installService.state(this.target())());
  protected readonly installActive = computed(() => {
    const phase = this.installState()?.phase;
    return phase === 'preparing' || phase === 'uploading' || phase === 'committing';
  });
  protected readonly installIndicatorIcon = computed(() => {
    switch (this.installState()?.phase) {
      case 'done':
        return 'check_circle';
      case 'error':
        return 'error';
      default:
        return 'install_mobile';
    }
  });
  protected readonly installIndicatorLabel = computed(() => {
    const state = this.installState();
    switch (state?.phase) {
      case 'done':
        return 'Installed';
      case 'error':
        return 'Install failed';
      case 'committing':
        return 'Installing…';
      default: {
        const progress = state?.progress;
        return progress && progress.totalBytes > 0
          ? `Installing ${Math.round((100 * progress.sentBytes) / progress.totalBytes)}%`
          : 'Installing…';
      }
    }
  });
  protected readonly installIndicatorTooltip = computed(() =>
    this.installState()?.phase === 'error'
      ? (this.installState()?.error?.message ?? 'Install failed')
      : 'Open the Apps panel'
  );

  private readonly metrics = computed(
    () => this.streaming.session(this.target())()?.metrics() ?? null
  );

  protected readonly hudMetrics = computed<HudMetrics | null>(() => {
    const m = this.metrics();
    if (!m) {
      return null;
    }
    return {
      rtt: m.rtt,
      jitter: m.jitter,
      packetLossRate: m.packetLossRate,
      protocol: m.protocol,
      bytesReceived: m.bytesReceived,
      frameRate: m.frameRate,
      resolution: m.resolution,
      region: this.device().hostZone,
    };
  });

  protected readonly gpuModeLabel = computed(() => {
    const mode = this.device().gpuMode ?? '';
    return GPU_CHIP_LABELS[mode] ?? mode;
  });

  protected openAppsTab(): void {
    this.appsRequested.emit();
    this.snackBar.open('Install status is shown in the Apps panel', 'Close', {duration: 2000});
  }

  protected async onExtend(): Promise<void> {
    const device = this.device();
    if (!device.lifecycle) {
      return;
    }

    const dialogRef = this.dialog.open<ExtendDialogComponent, ExtendDialogData, ExtendDialogResult>(
      ExtendDialogComponent,
      {
        width: '420px',
        data: {
          deviceId: device.id,
          deviceName: device.name,
          lifecycle: device.lifecycle,
        },
      }
    );

    const result = await firstValueFrom(dialogRef.afterClosed());
    if (result) {
      this.deviceStore.extend({
        id: device.id,
        request: {
          seconds: result.additionalHours * 3600,
        },
      });
    }
  }

  protected async onPin(): Promise<void> {
    const device = this.device();

    const dialogRef = this.dialog.open<PinDialogComponent, PinDialogData, PinDialogResult>(
      PinDialogComponent,
      {
        width: '480px',
        data: {
          deviceId: device.id,
          deviceName: device.name,
        },
      }
    );

    const result = await firstValueFrom(dialogRef.afterClosed());
    if (result) {
      this.deviceStore.pin({
        id: device.id,
        request: {
          reason: result.reason,
          reasonDetail: result.reasonDetail,
          pinnedUntil: result.pinnedUntil,
        },
      });
    }
  }

  protected async onUnpin(): Promise<void> {
    const device = this.device();

    const data: ConfirmDialogData = {
      title: 'Unpin Device',
      message: `Unpin "${device.name}"? It will return to ephemeral mode and start counting down.`,
      confirmText: 'Unpin',
      confirmColor: 'primary',
      icon: 'push_pin',
      iconColor: 'primary',
    };
    const dialogRef = this.dialog.open(ConfirmDialogComponent, {data});

    if (await firstValueFrom(dialogRef.afterClosed())) {
      this.deviceStore.unpin(device.id);
    }
  }

  /**
   * The host firewall opens the first instance's ADB port and no other, so a
   * command for any sibling would be one that always times out. Those reach
   * ADB through this page's own WebSocket bridge instead.
   */
  protected readonly adbDirectlyReachable = computed(() => {
    const instances = this.instances();
    return instances.length === 0 || instances[0]?.name === this.instance();
  });

  protected copyAdbCommand(): void {
    const instances = this.instances();
    const port = instances.find(i => i.name === this.instance())?.adbPort ?? this.device().adbPort;
    const address = `${window.location.hostname}:${port || 6520}`;
    void navigator.clipboard.writeText(`adb connect ${address}`);
  }

  protected openNewTab(): void {
    const instance = encodeURIComponent(this.instance());
    window.open(
      resolveBasePath(`/devices/${this.device().id}/player?instance=${instance}`),
      '_blank'
    );
  }

  protected async rebootDevice(): Promise<void> {
    const data: ConfirmDialogData = {
      title: 'Reboot Device',
      message: 'Are you sure you want to reboot the device?',
      confirmText: 'Reboot',
      confirmColor: 'warn',
      icon: 'restart_alt',
      iconColor: 'warn',
    };
    const dialogRef = this.dialog.open(ConfirmDialogComponent, {data});

    if (await firstValueFrom(dialogRef.afterClosed())) {
      await this.sendAdbCommand('reboot');
    }
  }

  protected async stopDevice(): Promise<void> {
    const device = this.device();
    const data: ConfirmDialogData = {
      title: 'Stop Device',
      message: `Are you sure you want to stop "${device.name}"?`,
      confirmText: 'Stop',
      confirmColor: 'warn',
      icon: 'stop',
      iconColor: 'warn',
    };
    const dialogRef = this.dialog.open(ConfirmDialogComponent, {data});

    if (await firstValueFrom(dialogRef.afterClosed())) {
      this.deviceStore.stop(device.id);
      await this.router.navigate(['/devices']);
    }
  }

  private async sendAdbCommand(command: string): Promise<void> {
    const session = this.adbService.session(this.target())();
    if (session?.state() !== 'connected') {
      this.logger.warn('PlayerToolbar', 'ADB not connected, cannot execute command');
      return;
    }

    try {
      const result = await exec(await session.ensureConnected(), command);
      this.logger.debug('PlayerToolbar', 'ADB command result', result);
    } catch (error) {
      this.logger.error('PlayerToolbar', 'ADB command failed', error);
    }
  }

  private connectedAdb(): Promise<Adb> {
    const session = this.adbService.session(this.target())();
    if (!session) {
      return Promise.reject(new Error('ADB session not available'));
    }
    return session.ensureConnected();
  }

  /** Refreshes root/animation state shown in the actions menu. */
  protected refreshDeviceToolState(): void {
    if (this.adbState() !== 'connected') {
      return;
    }
    void this.connectedAdb()
      .then(async device => {
        this.adbRoot.set(await isAdbRoot(device));
        this.animationsEnabled.set(await areAnimationsEnabled(device));
      })
      .catch(() => {
        // Menu items keep their last-known state.
      });
  }

  protected async onCaptureBugReport(): Promise<void> {
    if (this.bugReportBusy()) {
      return;
    }
    this.bugReportBusy.set(true);
    const busy = this.snackBar.open('Capturing bug report (this can take a minute)…');
    try {
      const result = await captureBugReport(await this.connectedAdb());
      downloadBlob(
        result.blob,
        `bugreport-${this.device().name}-${Date.now()}.${result.extension}`
      );
      busy.dismiss();
      this.snackBar.open('Bug report captured', 'Close', {duration: 3000});
    } catch (error) {
      busy.dismiss();
      const message = error instanceof Error ? error.message : 'Bug report failed';
      this.snackBar.open(`Bug report failed: ${message}`, 'Close', {duration: 5000});
    } finally {
      this.bugReportBusy.set(false);
    }
  }

  protected async toggleAnimations(): Promise<void> {
    const enable = !this.animationsEnabled();
    try {
      await setAnimationsEnabled(await this.connectedAdb(), enable);
      this.animationsEnabled.set(enable);
      this.snackBar.open(enable ? 'Animations enabled' : 'Animations disabled', 'Close', {
        duration: 3000,
      });
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Failed to change animations';
      this.snackBar.open(`Error: ${message}`, 'Close', {duration: 5000});
    }
  }

  protected async toggleAdbRoot(): Promise<void> {
    const toRoot = !this.adbRoot();

    const data: ConfirmDialogData = {
      title: toRoot ? 'Restart ADB as Root' : 'Restart ADB Unrooted',
      message: 'adbd will restart and the stream reconnects briefly. Continue?',
      confirmText: 'Restart',
      confirmColor: 'primary',
      icon: 'admin_panel_settings',
      iconColor: 'primary',
    };
    const dialogRef = this.dialog.open(ConfirmDialogComponent, {data});

    if (await firstValueFrom(dialogRef.afterClosed())) {
      await this.restartAdbRoot(toRoot);
    }
  }

  private async restartAdbRoot(root: boolean): Promise<void> {
    try {
      await restartAdbd(await this.connectedAdb(), root).catch(() => {
        // Expected: adbd restarting kills the transport before replying.
      });
    } catch (error) {
      this.logger.warn('PlayerToolbar', 'ADB root restart request failed', error);
    }

    // The channel behind ADB is dead once adbd restarts; only a stream
    // restart negotiates fresh channels. The desired-state effect reconnects
    // automatically after the disconnect.
    this.snackBar.open('ADB restarting, reconnecting the stream…', 'Close', {duration: 4000});
    this.streaming.disconnect(this.target());
  }
}
