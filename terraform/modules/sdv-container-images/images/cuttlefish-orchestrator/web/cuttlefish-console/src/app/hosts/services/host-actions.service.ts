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
import {Router} from '@angular/router';
import {AuthStore} from '../../auth/store/auth.signal-store';
import type {ConfirmDialogData, InputDialogData} from '../../shared/components';
import {ConfirmDialogComponent, InputDialogComponent} from '../../shared/components';
import {foreignCreatorWarning} from '../../shared/utils';
import type {HostExtendDialogData} from '../components/host-detail/host-extend-dialog.component';
import {HostExtendDialogComponent} from '../components/host-detail/host-extend-dialog.component';
import type {Host} from '../models/host.model';
import {HostStore} from '../store/host.signal-store';

/**
 * The host lifecycle commands invoked from the list and detail views:
 * start/stop/sync/retry/delete. Each confirming action owns its dialog and
 * dispatches to the HostStore, so both views share one copy of the flow.
 */
@Service()
export class HostActionsService {
  private readonly hostStore = inject(HostStore);
  private readonly authStore = inject(AuthStore);
  private readonly dialog = inject(MatDialog);
  private readonly router = inject(Router);

  start(host: Host): void {
    this.hostStore.start(host.id);
  }

  stop(host: Host): void {
    this.confirm(
      {
        title: 'Stop Host',
        message: `Are you sure you want to stop "${host.name}"? Any running devices will be terminated.`,
        confirmText: 'Stop',
        confirmColor: 'warn',
        icon: 'stop',
        iconColor: 'warn',
      },
      () => this.hostStore.stop(host.id)
    );
  }

  sync(host: Host): void {
    this.hostStore.sync(host.id);
  }

  /** Detail view passes fallbackToStandard to recreate a preempted Spot host as standard. */
  /**
   * Reshapes the host's device capacity. The dialog names the devices already
   * on it because that is the floor the server enforces.
   */
  editCapacity(host: Host): void {
    const data: InputDialogData = {
      title: `Device capacity for ${host.name}`,
      message: `This host carries ${host.currentDevices} device(s). The limit cannot go below that.`,
      inputLabel: 'Max devices',
      initialValue: String(host.maxDevices),
      confirmText: 'Update',
      required: true,
      icon: 'tune',
      validationPattern: '([1-9]|1[0-6])',
      validationMessage: 'Enter a whole number from 1 to 16',
    };
    this.dialog
      .open(InputDialogComponent, {data, width: '420px'})
      .afterClosed()
      .subscribe((result: string | null | undefined) => {
        if (result) {
          this.hostStore.updateMaxDevices({id: host.id, maxDevices: Number(result)});
        }
      });
  }

  /** Opens the hours dialog; the caller offers this only where canExtendHost holds. */
  extend(host: Host): void {
    const expiresAt = host.lifecycle?.expiresAt;
    if (!expiresAt) {
      return;
    }
    const data: HostExtendDialogData = {
      hostName: host.name,
      expiresAt,
      vmTerminatesAt: host.vmTerminatesAt,
    };
    this.dialog
      .open(HostExtendDialogComponent, {data, width: '420px'})
      .afterClosed()
      .subscribe((hours: number | undefined) => {
        if (hours) {
          this.hostStore.extend({id: host.id, hours});
        }
      });
  }

  retry(host: Host, fallbackToStandard = false): void {
    this.hostStore.retry({id: host.id, request: {fallbackToStandard}});
  }

  /**
   * Detail view passes `navigateToListAfter` so a deletion from the (now gone)
   * detail page returns to the list; the list view stays put.
   */
  delete(host: Host, opts: {navigateToListAfter?: boolean} = {}): void {
    this.confirm(
      {
        title: 'Delete Host',
        message: `Are you sure you want to delete "${host.name}"? This action cannot be undone and will terminate all devices.`,
        warning: foreignCreatorWarning(host.createdBy, this.authStore.user()),
        confirmText: 'Delete',
        confirmColor: 'warn',
        icon: 'delete',
        iconColor: 'warn',
      },
      () => {
        this.hostStore.delete(host.id);
        if (opts.navigateToListAfter) {
          void this.router.navigate(['/hosts']);
        }
      }
    );
  }

  /**
   * Bulk-deletes the selected hosts after one confirmation. There is no batch
   * endpoint, so the store fans out to the per-host delete; each host tears down
   * its own devices.
   */
  deleteMany(hosts: Host[]): void {
    if (hosts.length === 0) {
      return;
    }
    const user = this.authStore.user();
    const foreign = hosts.filter(h => foreignCreatorWarning(h.createdBy, user)).length;
    const noun = hosts.length === 1 ? 'host' : 'hosts';
    this.confirm(
      {
        title: `Delete ${hosts.length} ${noun}`,
        message: `Delete ${hosts.length} selected ${noun}? This action cannot be undone and will terminate all their devices.`,
        warning:
          foreign > 0
            ? `${foreign} of these ${foreign === 1 ? 'was' : 'were'} created by someone else.`
            : undefined,
        confirmText: 'Delete',
        confirmColor: 'warn',
        icon: 'delete',
        iconColor: 'warn',
      },
      () => this.hostStore.deleteMany(hosts.map(h => h.id))
    );
  }

  private confirm(data: ConfirmDialogData, onConfirmed: () => void): void {
    const dialogRef = this.dialog.open(ConfirmDialogComponent, {data});
    dialogRef.afterClosed().subscribe(confirmed => {
      if (confirmed) {
        onConfirmed();
      }
    });
  }
}
