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

import {ChangeDetectionStrategy, Component, computed, input, output} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatCheckboxModule} from '@angular/material/checkbox';
import {MatIconModule} from '@angular/material/icon';
import {MatMenuModule} from '@angular/material/menu';
import {MatTableModule} from '@angular/material/table';
import {MatTooltipModule} from '@angular/material/tooltip';
import {RouterModule} from '@angular/router';
import {GpuTypePipe} from '../../../shared/pipes';
import type {Host} from '../../models/host.model';

/**
 * The host table: pure projection of a host list. Navigation stays as router
 * links; every mutating row action is emitted for the container to run through
 * HostActionsService.
 */
@Component({
  selector: 'app-host-table',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    RouterModule,
    MatTableModule,
    MatButtonModule,
    MatCheckboxModule,
    MatIconModule,
    MatMenuModule,
    MatTooltipModule,
    GpuTypePipe,
  ],
  template: `
    <table mat-table [dataSource]="hosts()" class="host-table">
      <ng-container matColumnDef="select">
        <th mat-header-cell *matHeaderCellDef>
          <mat-checkbox
            [checked]="allSelected()"
            [indeterminate]="someSelected()"
            (change)="toggleAll.emit($event.checked)"
            aria-label="Select all hosts" />
        </th>
        <td mat-cell *matCellDef="let host">
          <mat-checkbox
            [checked]="selectedIds().has(host.id)"
            (change)="toggleRow.emit(host.id)"
            (click)="$event.stopPropagation()"
            [aria-label]="'Select ' + host.name" />
        </td>
      </ng-container>
      <ng-container matColumnDef="status">
        <th mat-header-cell *matHeaderCellDef></th>
        <td mat-cell *matCellDef="let host">
          <div [class]="'status-indicator ' + host.status"></div>
        </td>
      </ng-container>
      <ng-container matColumnDef="name">
        <th mat-header-cell *matHeaderCellDef>Name</th>
        <td mat-cell *matCellDef="let host">
          <a [routerLink]="[host.id]" class="host-name">{{ host.name }}</a>
          @if (host.gceInstanceName) {
            <div class="host-instance-name">{{ host.gceInstanceName }}</div>
          }
        </td>
      </ng-container>
      <ng-container matColumnDef="zone">
        <th mat-header-cell *matHeaderCellDef>Zone</th>
        <td mat-cell *matCellDef="let host">{{ host.zone }}</td>
      </ng-container>
      <ng-container matColumnDef="gpu">
        <th mat-header-cell *matHeaderCellDef>GPU</th>
        <td mat-cell *matCellDef="let host">
          <div class="gpu-info">
            <mat-icon class="gpu-icon">memory</mat-icon>
            {{ host.gpuCount }}x {{ host.gpuType | appGpuType }}
          </div>
        </td>
      </ng-container>
      <ng-container matColumnDef="devices">
        <th mat-header-cell *matHeaderCellDef>Devices</th>
        <td mat-cell *matCellDef="let host">
          <div class="devices-cell">
            <span [class.at-capacity]="host.currentDevices >= host.maxDevices">
              {{ host.currentDevices }} / {{ host.maxDevices }}
            </span>
            @if (host.lifecycle?.mode === 'protected') {
              <span class="protected-badge" matTooltip="Protected: {{ host.lifecycle?.protectedDeviceCount }} pinned device(s)">
                <mat-icon>shield</mat-icon>
              </span>
            }
          </div>
        </td>
      </ng-container>
      <ng-container matColumnDef="provisioning">
        <th mat-header-cell *matHeaderCellDef>Type</th>
        <td mat-cell *matCellDef="let host">
          <div [class]="'provisioning-badge ' + (host.provisioningModel?.toLowerCase() ?? 'standard')">
            @if (host.provisioningModel === 'SPOT') {
              <mat-icon>savings</mat-icon>
              <span>Spot</span>
              @if (host.preemptionCount > 0) {
                <span class="preemption-count" matTooltip="Preempted {{ host.preemptionCount }} time(s)">
                  ({{ host.preemptionCount }})
                </span>
              }
            } @else {
              <mat-icon>check_circle</mat-icon>
              <span>Standard</span>
            }
          </div>
        </td>
      </ng-container>
      <ng-container matColumnDef="class">
        <th mat-header-cell *matHeaderCellDef>Class</th>
        <td mat-cell *matCellDef="let host">
          @switch (host.hostClass) {
            @case ('fleet') {
              <span class="class-badge" matTooltip="Managed standing capacity: the fleet counts it as inventory and may reclaim it when it is not needed">fleet</span>
            }
            @case ('dedicated') {
              <span class="class-badge" matTooltip="Fenced to one group, so nothing else can be placed here; reclaimed once it empties">dedicated</span>
            }
            @case ('batch') {
              <span class="class-badge" matTooltip="Provisioned for a batch; batch settlement owns its whole lifecycle">batch</span>
            }
            @case ('reserved') {
              <span class="class-badge" matTooltip="Runs its full lease; only a delete reclaims it">reserved</span>
            }
            @default {
              <span class="class-unknown">-</span>
            }
          }
        </td>
      </ng-container>
      <ng-container matColumnDef="ip">
        <th mat-header-cell *matHeaderCellDef>IP Address</th>
        <td mat-cell *matCellDef="let host">
          {{ host.externalIP || host.internalIP || '-' }}
        </td>
      </ng-container>
      <ng-container matColumnDef="actions">
        <th mat-header-cell *matHeaderCellDef></th>
        <td mat-cell *matCellDef="let host">
          <button type="button" mat-icon-button aria-label="Host actions" [matMenuTriggerFor]="menu">
            <mat-icon>more_vert</mat-icon>
          </button>
          <mat-menu #menu="matMenu">
            <button type="button" mat-menu-item [routerLink]="[host.id]">
              <mat-icon>visibility</mat-icon>
              <span>View Details</span>
            </button>
            @if (host.status === 'stopped') {
              <button type="button" mat-menu-item (click)="startHost.emit(host)">
                <mat-icon>play_arrow</mat-icon>
                <span>Start</span>
              </button>
            }
            @if (host.status === 'running') {
              <button type="button" mat-menu-item (click)="stopHost.emit(host)">
                <mat-icon>stop</mat-icon>
                <span>Stop</span>
              </button>
            }
            @if (host.status === 'preempted' || (host.status === 'error' && host.error?.retryable)) {
              <button type="button" mat-menu-item (click)="retryHost.emit({host, fallbackToStandard: false})">
                <mat-icon>refresh</mat-icon>
                <span>{{ host.provisioningModel === 'SPOT' ? 'Retry as Spot VM' : 'Retry' }}</span>
              </button>
              <!-- Retrying a preempted Spot host as Spot can be preempted
                   again straight away; detail has offered the escape hatch all
                   along and the row is where a preemption is usually noticed. -->
              @if (host.provisioningModel === 'SPOT') {
                <button type="button" mat-menu-item (click)="retryHost.emit({host, fallbackToStandard: true})">
                  <mat-icon>shield</mat-icon>
                  <span>Retry as Standard VM</span>
                </button>
              }
            }
            <button type="button" mat-menu-item (click)="syncHost.emit(host)">
              <mat-icon>sync</mat-icon>
              <span>Sync Status</span>
            </button>
            <button type="button" mat-menu-item (click)="deleteHost.emit(host)">
              <mat-icon>delete</mat-icon>
              <span>Delete</span>
            </button>
          </mat-menu>
        </td>
      </ng-container>
      <tr mat-header-row *matHeaderRowDef="displayedColumns()"></tr>
      <tr mat-row *matRowDef="let row; columns: displayedColumns()"></tr>
    </table>
  `,
  styles: `
      .host-table {
        width: 100%;
      }

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

      .status-indicator.provisioning,
      .status-indicator.starting,
      .status-indicator.stopping,
      .status-indicator.deleting {
        background: var(--cco-accent);
        animation: pulse 1.5s infinite;
      }

      .status-indicator.error {
        background: var(--cco-error);
      }

      .status-indicator.preempted {
        background: var(--cco-warning);
        animation: pulse 1.5s infinite;
      }

      .status-indicator.deleted {
        background: var(--cco-text-secondary);
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

      .host-name {
        font-weight: 500;
      }

      .host-instance-name {
        font-family: 'Roboto Mono', monospace;
        font-size: 12px;
        color: var(--cco-text-secondary);
        margin-top: 2px;
      }

      .gpu-info {
        display: flex;
        align-items: center;
        gap: 4px;
      }

      .gpu-icon {
        font-size: 18px;
        width: 18px;
        height: 18px;
        color: var(--cco-accent);
      }

      .at-capacity {
        color: var(--cco-error);
        font-weight: bold;
      }

      .devices-cell {
        display: flex;
        align-items: center;
        gap: 8px;
      }

      .protected-badge {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        background: var(--cco-accent-surface);
        border-radius: 50%;
        cursor: help;
        transition: all var(--cco-duration-slow) cubic-bezier(0.4, 0, 0.2, 1);
      }

      .protected-badge:hover {
        background: var(--cco-accent-surface);
        transform: scale(1.1);
      }

      .protected-badge mat-icon {
        font-size: 16px;
        width: 16px;
        height: 16px;
        color: var(--cco-accent);
      }

      .provisioning-badge {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 4px 8px;
        border-radius: 16px;
        font-size: 12px;
        font-weight: 500;
      }

      .provisioning-badge mat-icon {
        font-size: 16px;
        width: 16px;
        height: 16px;
      }

      .provisioning-badge.standard {
        background: var(--cco-accent-surface);
        color: var(--cco-accent);
      }

      .provisioning-badge.standard mat-icon {
        color: var(--cco-accent);
      }

      .provisioning-badge.spot {
        background: var(--cco-success-surface);
        color: var(--cco-success);
      }

      .provisioning-badge.spot mat-icon {
        color: var(--cco-success);
      }

      .preemption-count {
        font-size: 11px;
        color: var(--cco-warning);
        margin-left: 2px;
      }

      /* One badge for every class rather than a variant per value. The class
         says whose rules apply, which is a fact to read, not a severity. */
      .class-badge {
        display: inline-block;
        padding: 2px 8px;
        border-radius: 12px;
        background: var(--cco-accent-surface);
        color: var(--cco-accent);
        font-size: 12px;
        font-weight: 500;
        cursor: help;
      }

      .class-unknown {
        color: var(--cco-text-secondary);
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
export class HostTableComponent {
  readonly hosts = input.required<Host[]>();
  readonly displayedColumns = input.required<string[]>();
  readonly selectedIds = input<ReadonlySet<string>>(new Set<string>());

  readonly startHost = output<Host>();
  readonly stopHost = output<Host>();
  readonly syncHost = output<Host>();
  readonly retryHost = output<{host: Host; fallbackToStandard: boolean}>();
  readonly deleteHost = output<Host>();
  readonly toggleRow = output<string>();
  readonly toggleAll = output<boolean>();

  protected readonly allSelected = computed(() => {
    const rows = this.hosts();
    const sel = this.selectedIds();
    return rows.length > 0 && rows.every(h => sel.has(h.id));
  });

  protected readonly someSelected = computed(
    () => this.hosts().some(h => this.selectedIds().has(h.id)) && !this.allSelected()
  );
}
