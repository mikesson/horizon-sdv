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

import {DatePipe, SlicePipe} from '@angular/common';
import {ChangeDetectionStrategy, Component, computed, input, output} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import {MatTooltipModule} from '@angular/material/tooltip';
import {RouterModule} from '@angular/router';
import type {
  Reclaimable,
  ReclaimableDevice,
  ReclaimableHost,
} from '../../models/operator-lens.model';

/**
 * Reclaimable-capacity worklist: pinned devices, stopped resources, and
 * hosts about to expire. Row actions are emitted to the container.
 */
@Component({
  selector: 'app-reclaimable-panel',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [DatePipe, SlicePipe, MatButtonModule, MatIconModule, MatTooltipModule, RouterModule],
  template: `
    @if (isEmpty()) {
      <div class="all-clear">
        <mat-icon>check_circle</mat-icon>
        <span>Nothing to reclaim. No long-held pins, stopped resources, or expiring hosts.</span>
      </div>
    } @else {
      @if (reclaimable().pinnedDevices.length > 0) {
        <section class="bucket">
          <h3 class="bucket-title">Pinned devices <span class="bucket-hint">oldest pin first, exempt from TTL reaping</span></h3>
          @for (device of reclaimable().pinnedDevices; track device.id) {
            <div class="row">
              <a class="row-name" [routerLink]="['/devices', device.id]">{{ device.name }}</a>
              <span class="row-detail">
                pinned {{ device.pinnedAt | date: 'MMM d, HH:mm' }}
                @if (device.pinnedBy) {
                  by {{ device.pinnedBy }}
                }
                @if (device.pinnedReason) {
                  · {{ device.pinnedReason }}
                }
              </span>
              <button type="button" mat-stroked-button (click)="unpinDevice.emit(device)">Unpin</button>
            </div>
          }
        </section>
      }

      @if (reclaimable().stoppedDevices.length > 0) {
        <section class="bucket">
          <h3 class="bucket-title">Stopped devices <span class="bucket-hint">stopped but never deleted</span></h3>
          @for (device of reclaimable().stoppedDevices; track device.id) {
            <div class="row">
              <a class="row-name" [routerLink]="['/devices', device.id]">{{ device.name }}</a>
              <span class="row-detail">stopped since {{ device.updatedAt | date: 'MMM d, HH:mm' }}</span>
              <button type="button" mat-stroked-button class="warn-action" (click)="deleteDevice.emit(device)">Delete</button>
            </div>
          }
        </section>
      }

      @if (reclaimable().stoppedHosts.length > 0) {
        <section class="bucket">
          <h3 class="bucket-title">Stopped hosts <span class="bucket-hint">the VM disk still bills while stopped</span></h3>
          @for (host of reclaimable().stoppedHosts; track host.id) {
            <div class="row">
              <a class="row-name" [routerLink]="['/hosts', host.id]">{{ host.name }}</a>
              <span class="row-detail">
                {{ host.zone }} · {{ host.currentDeviceCount }} devices ·
                stopped since {{ host.updatedAt | date: 'MMM d, HH:mm' }}
              </span>
              <button type="button" mat-stroked-button class="warn-action" (click)="deleteHost.emit(host)">Delete</button>
            </div>
          }
        </section>
      }

      @if (reclaimable().expiringHosts.length > 0) {
        <section class="bucket">
          <h3 class="bucket-title">Expiring within the hour <span class="bucket-hint">these free themselves, informational</span></h3>
          @for (host of reclaimable().expiringHosts; track host.id) {
            <div class="row">
              <a class="row-name" [routerLink]="['/hosts', host.id]">{{ host.name }}</a>
              <span class="row-detail" [matTooltip]="host.id | slice: 0 : 8">
                {{ host.zone }} · expires {{ host.expiresAt | date: 'HH:mm' }}
              </span>
            </div>
          }
        </section>
      }
    }
  `,
  styles: `
    .all-clear {
      display: flex;
      align-items: center;
      gap: 8px;
      padding: 16px;
      color: var(--cco-text-secondary);
      font-size: 12px;

      mat-icon {
        color: var(--cco-success);
      }
    }

    .bucket {
      margin-bottom: 16px;
    }

    .bucket-title {
      font-size: 12px;
      font-weight: 500;
      margin: 0 0 8px;
    }

    .bucket-hint {
      font-weight: 400;
      font-size: 11px;
      color: var(--cco-text-secondary);
      margin-left: 8px;
    }

    .row {
      display: flex;
      align-items: center;
      gap: 12px;
      padding: 8px 0;
      border-bottom: 1px solid var(--cco-border);
      font-size: 12px;

      &:last-child {
        border-bottom: none;
      }
    }

    .row-name {
      min-width: 160px;
      font-family: 'Roboto Mono', monospace;
      font-size: 12px;
    }

    .row-detail {
      flex: 1;
      color: var(--cco-text-secondary);
      font-size: 12px;
    }

    .warn-action {
      color: var(--cco-error);
    }
  `,
})
export class ReclaimablePanelComponent {
  readonly reclaimable = input.required<Reclaimable>();
  readonly unpinDevice = output<ReclaimableDevice>();
  readonly deleteDevice = output<ReclaimableDevice>();
  readonly deleteHost = output<ReclaimableHost>();

  protected readonly isEmpty = computed(() => {
    const r = this.reclaimable();
    return (
      r.pinnedDevices.length === 0 &&
      r.stoppedDevices.length === 0 &&
      r.stoppedHosts.length === 0 &&
      r.expiringHosts.length === 0
    );
  });
}
