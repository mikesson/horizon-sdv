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

import {inject, Service} from '@angular/core';
import {MatDialog} from '@angular/material/dialog';
import {MatSnackBar} from '@angular/material/snack-bar';
import {Router} from '@angular/router';
import {AuthStore} from '../../auth/store/auth.signal-store';
import type {ConfirmDialogData} from '../../shared/components';
import {ConfirmDialogComponent} from '../../shared/components';
import {foreignCreatorWarning} from '../../shared/utils';
import type {ExtendDialogResult} from '../components/lifecycle/extend-dialog.component';
import {ExtendDialogComponent} from '../components/lifecycle/extend-dialog.component';
import type {PinDialogResult} from '../components/lifecycle/pin-dialog.component';
import {PinDialogComponent} from '../components/lifecycle/pin-dialog.component';
import type {Device} from '../models/device.model';
import type {Snapshot} from '../models/snapshot.model';
import {DeviceStore} from '../store/device.signal-store';
import {DeviceApiService} from './device-api.service';

/**
 * The device lifecycle commands invoked from the list and detail views:
 * start/stop/delete plus pin/unpin/extend and the connectivity shortcuts. Each
 * confirming action owns its dialog and dispatches to the DeviceStore, so both
 * views share one copy of the flow instead of duplicating it.
 */
@Service()
export class DeviceActionsService {
  private readonly deviceStore = inject(DeviceStore);
  private readonly deviceApi = inject(DeviceApiService);
  private readonly authStore = inject(AuthStore);
  private readonly dialog = inject(MatDialog);
  private readonly snackBar = inject(MatSnackBar);
  private readonly router = inject(Router);

  start(device: Device): void {
    this.deviceStore.start(device.id);
  }

  stop(device: Device): void {
    this.confirm(
      {
        title: 'Stop Device',
        message: `Are you sure you want to stop "${device.name}"?`,
        confirmText: 'Stop',
        confirmColor: 'warn',
        icon: 'stop',
        iconColor: 'warn',
      },
      () => this.deviceStore.stop(device.id)
    );
  }

  restart(device: Device, instance: string): void {
    this.confirm(
      {
        title: 'Restart Instance',
        message: `Restart "${this.subject(device, instance)}"? It reboots in place; apps and userdata are kept.`,
        confirmText: 'Restart',
        confirmColor: 'primary',
        icon: 'restart_alt',
        iconColor: 'primary',
      },
      () => this.deviceStore.restart({deviceId: device.id, instance})
    );
  }

  powerwash(device: Device, instance: string): void {
    this.confirm(
      {
        title: 'Powerwash Instance',
        message: `Powerwash "${this.subject(device, instance)}"? All userdata is wiped and it returns to first-boot state on the same build. This cannot be undone.`,
        confirmText: 'Powerwash',
        confirmColor: 'warn',
        icon: 'restore',
        iconColor: 'warn',
      },
      () => this.deviceStore.powerwash({deviceId: device.id, instance})
    );
  }

  powerbtn(device: Device, instance: string): void {
    this.deviceStore.powerbtn({deviceId: device.id, instance});
  }

  /**
   * What a confirmation should call the thing it is about to act on.
   *
   * The device's name at N=1, where the two are the same thing to the user;
   * `device / instance` once a group has members that differ, so a powerwash
   * dialog never reads as if it were about all of them.
   */
  private subject(device: Device, instance: string): string {
    const count = device.instances?.length ?? 1;
    return count > 1 ? `${device.name} / ${instance}` : device.name;
  }

  deleteSnapshot(device: Device, snapshot: Snapshot): void {
    this.confirm(
      {
        title: 'Delete Snapshot',
        message: `Delete this snapshot of "${device.name}"? Its saved state is removed from the host and cannot be restored afterwards.`,
        confirmText: 'Delete',
        confirmColor: 'warn',
        icon: 'delete',
        iconColor: 'warn',
      },
      () => this.deviceStore.deleteSnapshot({deviceId: device.id, snapshotId: snapshot.id})
    );
  }

  recordStart(device: Device, instance: string): void {
    this.deviceStore.recordStart({deviceId: device.id, instance});
  }

  recordStop(device: Device, instance: string): void {
    this.deviceStore.recordStop({deviceId: device.id, instance});
  }

  /** Captures one instance's display 0 and downloads it as a file. */
  screenshot(device: Device, instance: string): void {
    this.deviceApi.getScreenshot({deviceId: device.id, instance}, 0).subscribe({
      next: shot => {
        const bytes = Uint8Array.from(atob(shot.screenshot_bytes), c => c.charCodeAt(0));
        const url = URL.createObjectURL(new Blob([bytes], {type: shot.screenshot_mime_type}));
        const a = document.createElement('a');
        a.href = url;
        a.download = `${instance}-display0.png`;
        a.click();
        URL.revokeObjectURL(url);
      },
      error: () => {
        this.snackBar.open('Failed to capture screenshot', 'Dismiss', {duration: 4000});
      },
    });
  }

  /**
   * Detail view passes `navigateToListAfter` so a deletion from the (now gone)
   * detail page returns to the list; the list view stays put.
   */
  delete(device: Device, opts: {navigateToListAfter?: boolean} = {}): void {
    this.confirm(
      {
        title: 'Delete Device',
        message: `Are you sure you want to delete "${device.name}"? This action cannot be undone.`,
        warning: foreignCreatorWarning(device.createdBy, this.authStore.user()),
        confirmText: 'Delete',
        confirmColor: 'warn',
        icon: 'delete',
        iconColor: 'warn',
      },
      () => {
        this.deviceStore.delete(device.id);
        if (opts.navigateToListAfter) {
          void this.router.navigate(['/devices']);
        }
      }
    );
  }

  /**
   * Bulk-deletes the selected devices after one confirmation. There is no batch
   * endpoint, so the store fans out to the per-device delete.
   */
  deleteMany(devices: Device[]): void {
    if (devices.length === 0) {
      return;
    }
    const user = this.authStore.user();
    const foreign = devices.filter(d => foreignCreatorWarning(d.createdBy, user)).length;
    const noun = devices.length === 1 ? 'device' : 'devices';
    this.confirm(
      {
        title: `Delete ${devices.length} ${noun}`,
        message: `Delete ${devices.length} selected ${noun}? This action cannot be undone.`,
        warning:
          foreign > 0
            ? `${foreign} of these ${foreign === 1 ? 'was' : 'were'} created by someone else.`
            : undefined,
        confirmText: 'Delete',
        confirmColor: 'warn',
        icon: 'delete',
        iconColor: 'warn',
      },
      () => this.deviceStore.deleteMany(devices.map(d => d.id))
    );
  }

  pin(device: Device): void {
    const dialogRef = this.dialog.open(PinDialogComponent, {
      width: '480px',
      data: {deviceId: device.id, deviceName: device.name},
    });
    dialogRef.afterClosed().subscribe((result: PinDialogResult | undefined) => {
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
    });
  }

  unpin(device: Device): void {
    this.confirm(
      {
        title: 'Unpin Device',
        message: `Unpin "${device.name}"? It will return to ephemeral mode and start counting down.`,
        confirmText: 'Unpin',
        confirmColor: 'primary',
        icon: 'push_pin',
        iconColor: 'primary',
      },
      () => this.deviceStore.unpin(device.id)
    );
  }

  extend(device: Device): void {
    if (!device.lifecycle) {
      return;
    }
    const dialogRef = this.dialog.open(ExtendDialogComponent, {
      width: '420px',
      data: {deviceId: device.id, deviceName: device.name, lifecycle: device.lifecycle},
    });
    dialogRef.afterClosed().subscribe((result: ExtendDialogResult | undefined) => {
      if (result) {
        this.deviceStore.extend({
          id: device.id,
          request: {seconds: result.additionalHours * 3600},
        });
      }
    });
  }

  /**
   * Copies an `adb connect` command; private-IP hosts need an IAP tunnel, so the
   * command targets localhost and the toast says so.
   */
  connectAdb(device: Device): void {
    const hostIp = device.hostIp ?? '';
    const command = this.isPrivateIP(hostIp)
      ? `adb connect localhost:${device.adbPort}`
      : `adb connect ${hostIp}:${device.adbPort}`;
    const message = this.isPrivateIP(hostIp)
      ? 'Copied localhost command. Requires IAP Tunnel (see Device Details).'
      : 'Copied ADB connect command';

    void navigator.clipboard.writeText(command);
    this.snackBar.open(message, 'Dismiss', {duration: 4000});
  }

  copyId(device: Device): void {
    // writeText rejects without a secure context or clipboard permission.
    navigator.clipboard.writeText(device.id).then(
      () => this.snackBar.open(`Copied the ID of "${device.name}"`, 'Dismiss', {duration: 2000}),
      () =>
        this.snackBar.open(`Could not copy the ID of "${device.name}"`, 'Dismiss', {duration: 4000})
    );
  }

  /** Toast shown when a device's lifecycle timer reaches zero. */
  notifyExpired(device: Device): void {
    this.snackBar
      .open(`Device "${device.name}" has expired and will be terminated`, 'View', {duration: 5000})
      .onAction()
      .subscribe(() => {
        void this.router.navigate(['/devices', device.id]);
      });
  }

  private confirm(data: ConfirmDialogData, onConfirmed: () => void): void {
    const dialogRef = this.dialog.open(ConfirmDialogComponent, {data});
    dialogRef.afterClosed().subscribe(confirmed => {
      if (confirmed) {
        onConfirmed();
      }
    });
  }

  private isPrivateIP(ip: string): boolean {
    return ip.startsWith('10.') || ip.startsWith('192.168.') || ip.startsWith('172.');
  }
}
