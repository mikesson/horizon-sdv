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

import {DatePipe} from '@angular/common';
import {ChangeDetectionStrategy, Component, computed, input, output} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatCheckboxModule} from '@angular/material/checkbox';
import {MatChipsModule} from '@angular/material/chips';
import {MatIconModule} from '@angular/material/icon';
import {MatMenuModule} from '@angular/material/menu';
import {MatTableModule} from '@angular/material/table';
import {MatTooltipModule} from '@angular/material/tooltip';
import {RouterModule} from '@angular/router';
import {GpuModePipe} from '../../../shared/pipes';
import {RegionFlagPipe} from '../../../shared/pipes/region-flag.pipe';
import type {Device} from '../../models/device.model';
import {LifecycleTimerComponent} from '../lifecycle/lifecycle-timer.component';
import {PinButtonComponent} from '../lifecycle/pin-button.component';

/**
 * The device table: pure projection of a device list. Navigation stays as
 * router links; every other row action is emitted for the container to run
 * through DeviceActionsService.
 */
@Component({
  selector: 'app-device-table',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    DatePipe,
    RouterModule,
    MatTableModule,
    MatButtonModule,
    MatCheckboxModule,
    MatIconModule,
    MatChipsModule,
    MatMenuModule,
    MatTooltipModule,
    GpuModePipe,
    RegionFlagPipe,
    LifecycleTimerComponent,
    PinButtonComponent,
  ],
  template: `
    <div class="table-scroll">
    <table mat-table [dataSource]="devices()" class="device-table">
      <ng-container matColumnDef="select" sticky>
        <th mat-header-cell *matHeaderCellDef>
          <mat-checkbox
            [checked]="allSelected()"
            [indeterminate]="someSelected()"
            (change)="toggleAll.emit($event.checked)"
            aria-label="Select all devices" />
        </th>
        <td mat-cell *matCellDef="let device">
          <mat-checkbox
            [checked]="selectedIds().has(device.id)"
            (change)="toggleRow.emit(device.id)"
            (click)="$event.stopPropagation()"
            [aria-label]="'Select ' + device.name" />
        </td>
      </ng-container>
      <ng-container matColumnDef="status" sticky>
        <th mat-header-cell *matHeaderCellDef></th>
        <td mat-cell *matCellDef="let device">
          <div [class]="'status-indicator ' + device.status"></div>
        </td>
      </ng-container>
      <ng-container matColumnDef="name" sticky>
        <th mat-header-cell *matHeaderCellDef>Name</th>
        <td mat-cell *matCellDef="let device">
          <a [routerLink]="[device.id]">{{ device.name }}</a>
          <!-- A device is one leased, placed and billed row whatever it holds,
               so the count belongs under the name rather than in a column of
               its own that would be blank for almost every row. -->
          @if ((device.instanceCount ?? 1) > 1) {
            <span class="instance-count">{{ device.instanceCount }} instances</span>
          }
        </td>
      </ng-container>
      <ng-container matColumnDef="host">
        <th mat-header-cell *matHeaderCellDef>Host</th>
        <td mat-cell *matCellDef="let device">
          <a [routerLink]="['/hosts', device.hostId]">
            {{ device.hostName || device.hostId }}
          </a>
        </td>
      </ng-container>
      <ng-container matColumnDef="region">
        <th mat-header-cell *matHeaderCellDef>Region</th>
        <td mat-cell *matCellDef="let device">
          @if (device.hostZone) {
            <span class="region-display">
              {{ device.hostZone | appRegionFlag:'short' }}
            </span>
          }
          @if (!device.hostZone) {
            <span class="region-display unknown">-</span>
          }
        </td>
      </ng-container>
      <ng-container matColumnDef="lifecycle">
        <th mat-header-cell *matHeaderCellDef>TTL</th>
        <td mat-cell *matCellDef="let device">
          <div class="lifecycle-cell">
            <app-lifecycle-timer
              [lifecycle]="device.lifecycle"
              [showExtendButton]="device.status === 'running'"
              (extend)="extend.emit(device)"
              (expired)="expired.emit(device)"
            />
          </div>
        </td>
      </ng-container>
      <ng-container matColumnDef="pin">
        <th mat-header-cell *matHeaderCellDef></th>
        <td mat-cell *matCellDef="let device">
          <app-pin-button
            [lifecycle]="device.lifecycle"
            [disabled]="device.status !== 'running'"
            (pin)="pin.emit(device)"
            (unpin)="unpin.emit(device)"
          />
        </td>
      </ng-container>
      <ng-container matColumnDef="build">
        <th mat-header-cell *matHeaderCellDef>Build</th>
        <td mat-cell *matCellDef="let device">
          @if (device.buildTarget) {
            <span class="build-info">
              {{ device.buildTarget }}
            </span>
          } @else {
            <span class="build-info default">-</span>
          }
        </td>
      </ng-container>
      <!-- GPU mode and the ADB port are per INSTANCE. At N=1 the device's flat
           field is that instance's; past one there is no single answer, and a
           hyphen says so in the idiom the rest of this table already uses. -->
      <ng-container matColumnDef="gpu">
        <th mat-header-cell *matHeaderCellDef>GPU Mode</th>
        <td mat-cell *matCellDef="let device">
          @if ((device.instanceCount ?? 1) > 1) {
            <span class="per-instance" matTooltip="Set per instance; open the device to see each">-</span>
          } @else {
            <mat-chip [class]="device.gpuMode">
              {{ device.gpuMode | appGpuMode }}
            </mat-chip>
          }
        </td>
      </ng-container>
      <ng-container matColumnDef="ports">
        <th mat-header-cell *matHeaderCellDef>Ports</th>
        <td mat-cell *matCellDef="let device">
          @if ((device.instanceCount ?? 1) > 1) {
            <span class="per-instance" matTooltip="One port per instance; open the device to see each">-</span>
          } @else {
            @if (device.adbPort) {
              <span class="port-badge adb" matTooltip="ADB Port">
                ADB: {{ device.adbPort }}
              </span>
            }
            @if (device.webrtcPort) {
              <span class="port-badge webrtc" matTooltip="WebRTC Port">
                WebRTC: {{ device.webrtcPort }}
              </span>
            }
          }
        </td>
      </ng-container>
      <ng-container matColumnDef="created">
        <th mat-header-cell *matHeaderCellDef>Created</th>
        <td mat-cell *matCellDef="let device">
          {{ device.createdAt | date: 'short' }}
        </td>
      </ng-container>
      <ng-container matColumnDef="actions">
        <th mat-header-cell *matHeaderCellDef></th>
        <td mat-cell *matCellDef="let device">
          @if (device.status === 'running') {
            <a
              mat-icon-button
              class="open-player"
              [routerLink]="[device.id, 'player']"
              matTooltip="Open player"
              aria-label="Open player">
              <mat-icon>play_circle</mat-icon>
            </a>
          }
          <button type="button" mat-icon-button aria-label="Device actions" [matMenuTriggerFor]="menu">
            <mat-icon>more_vert</mat-icon>
          </button>
          <mat-menu #menu="matMenu">
            <button type="button" mat-menu-item [routerLink]="[device.id]">
              <mat-icon>visibility</mat-icon>
              <span>View Details</span>
            </button>
            <!-- The table has no ID column, and the CLI takes an ID where a
                 name is shared by several devices. -->
            <button type="button" mat-menu-item (click)="copyId.emit(device)">
              <mat-icon>content_copy</mat-icon>
              <span>Copy ID</span>
            </button>
            @if (device.status === 'running' && device.adbPort) {
              <button type="button" mat-menu-item (click)="connectAdb.emit(device)">
                <mat-icon>terminal</mat-icon>
                <span>Connect ADB</span>
              </button>
            }
            @if (device.status === 'running') {
              <a mat-menu-item [routerLink]="[device.id, 'player']">
                <mat-icon>play_circle</mat-icon>
                <span>Open player</span>
              </a>
            }
            @if (device.status === 'stopped') {
              <button type="button" mat-menu-item (click)="startDevice.emit(device)">
                <mat-icon>play_arrow</mat-icon>
                <span>Start</span>
              </button>
            }
            @if (device.status === 'running') {
              <button type="button" mat-menu-item (click)="stopDevice.emit(device)">
                <mat-icon>stop</mat-icon>
                <span>Stop</span>
              </button>
            }
            <button type="button" mat-menu-item (click)="deleteDevice.emit(device)">
              <mat-icon>delete</mat-icon>
              <span>Delete</span>
            </button>
          </mat-menu>
        </td>
      </ng-container>
      <tr mat-header-row *matHeaderRowDef="displayedColumns()"></tr>
      <tr mat-row *matRowDef="let row; columns: displayedColumns()"></tr>
    </table>
    </div>
  `,
  styles: `
      /* The table scrolls, not the page. Nine columns do not fit a 1280 laptop,
         and without this the whole page (header, filters and all) slid
         sideways with them. */
      .table-scroll {
        overflow-x: auto;
      }

      /* Fixed, so the columns take the widths below rather than the widths the
         longest cell happens to want. Auto layout was giving the name (the one
         column every row is identified by) 86px and 240px to the TTL bar,
         then wrapping the name over three lines and tripling the row height. */
      .device-table {
        width: 100%;
        /* Every column below is a fixed width except the name, which takes
           whatever is left; the floor is the fixed columns plus a name wide
           enough to read. Under that the table scrolls itself, not the page. */
        min-width: 1320px;
        table-layout: fixed;
      }

      /* One line with an ellipsis. Multi-line rows are what destroy the
         vertical rhythm scanning depends on, and with the name and build
         columns now taking the table's slack a truncated value is rare, and a
         tooltip repeating the cell's own text would only duplicate it into the
         accessible description. */
      .device-table th,
      .device-table td {
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
      }

      .mat-column-select {
        width: 56px;
      }

      .mat-column-status {
        width: 32px;
      }

      /* Name and build carry the two values long enough to matter, so they are
         the two with no width here. A fixed-layout table hands the leftover
         width to the columns that do not ask for any. */
      .mat-column-host {
        width: 148px;
      }

      .mat-column-region {
        width: 96px;
      }

      .mat-column-lifecycle {
        width: 152px;
      }

      .mat-column-pin {
        width: 48px;
      }


      .mat-column-gpu {
        width: 116px;
      }

      .mat-column-ports {
        width: 80px;
      }

      .mat-column-created {
        width: 132px;
      }

      .mat-column-actions {
        width: 56px;
      }

      /* The select, status and name columns are sticky, so the identifier stays
         anchored while the rest scrolls past. They need no fill of their own.
         Material inherits every cell's background off the opaque colour on the
         table, and a rule here would only be a second answer to a question
         already settled. */

      .status-indicator {
        width: 12px;
        height: 12px;
        border-radius: 50%;
        background: var(--cco-text-secondary);
      }

      .status-indicator.running {
        background: var(--cco-success);
      }

      .status-indicator.stopped {
        background: var(--cco-warning);
      }

      .status-indicator.pending,
      .status-indicator.creating,
      .status-indicator.starting {
        background: var(--cco-accent);
        animation: pulse 1.5s infinite;
      }

      .status-indicator.stopping,
      .status-indicator.deleting {
        background: var(--cco-warning);
        animation: pulse 1.5s infinite;
      }

      .status-indicator.error {
        background: var(--cco-error);
      }

      @keyframes pulse {
        0% {
          opacity: 1;
        }
        50% {
          opacity: 0.5;
        }
        100% {
          opacity: 1;
        }
      }

      /* The name cell is the one that may take two lines. The count sits under
         the name for a group and is absent otherwise, so only group rows grow.
         Each line ellipsizes on its own, because the td's overflow rule clips
         block children without truncating them. */
      .mat-column-name a,
      .instance-count {
        display: block;
        overflow: hidden;
        text-overflow: ellipsis;
      }

      .instance-count {
        font-size: 11px;
        color: var(--cco-text-secondary);
        font-variant-numeric: tabular-nums;
      }

      .per-instance {
        color: var(--cco-text-secondary);
      }

      .build-info {
        font-family: monospace;
        font-size: 12px;
      }

      .build-info.default {
        color: var(--cco-text-secondary);
        font-style: italic;
      }

      .open-player {
        color: var(--cco-accent);
      }

      /* M3 Accessible Chip Styling: light container + dark text (4.5:1+ contrast) */
      mat-chip.mat-mdc-chip.gfxstream {
        background-color: var(--cco-success-surface);
        color: var(--cco-success);
        --mat-chip-label-text-color: var(--cco-success);
      }

      mat-chip.mat-mdc-chip.drm_virgl {
        background-color: var(--cco-accent-surface);
        color: var(--cco-accent);
        --mat-chip-label-text-color: var(--cco-accent);
      }

      mat-chip.mat-mdc-chip.swiftshader {
        background-color: var(--cco-warning-surface);
        color: var(--cco-warning);
        --mat-chip-label-text-color: var(--cco-warning);
      }

      .port-badge {
        display: inline-block;
        padding: 2px 8px;
        border-radius: 4px;
        font-size: 11px;
        font-family: monospace;
        margin-right: 4px;
      }

      .port-badge.adb {
        background: var(--cco-accent-surface);
        color: var(--cco-accent);
      }

      .port-badge.webrtc {
        background: var(--cco-success-surface);
        color: var(--cco-success);
      }

      .region-display {
        font-size: 12px;
        white-space: nowrap;
      }

      .region-display.unknown {
        color: var(--cco-text-secondary);
      }

      .lifecycle-cell {
        min-width: 140px;
      }

      a {
        color: var(--cco-accent);
        text-decoration: none;
      }

      a:hover {
        text-decoration: underline;
      }
    `,
})
export class DeviceTableComponent {
  readonly devices = input.required<Device[]>();
  readonly displayedColumns = input.required<string[]>();
  readonly selectedIds = input<ReadonlySet<string>>(new Set<string>());

  readonly connectAdb = output<Device>();
  readonly copyId = output<Device>();
  readonly startDevice = output<Device>();
  readonly stopDevice = output<Device>();
  readonly deleteDevice = output<Device>();
  readonly pin = output<Device>();
  readonly unpin = output<Device>();
  readonly extend = output<Device>();
  readonly expired = output<Device>();
  readonly toggleRow = output<string>();
  readonly toggleAll = output<boolean>();

  protected readonly allSelected = computed(() => {
    const rows = this.devices();
    const sel = this.selectedIds();
    return rows.length > 0 && rows.every(d => sel.has(d.id));
  });

  protected readonly someSelected = computed(
    () => this.devices().some(d => this.selectedIds().has(d.id)) && !this.allSelected()
  );
}
