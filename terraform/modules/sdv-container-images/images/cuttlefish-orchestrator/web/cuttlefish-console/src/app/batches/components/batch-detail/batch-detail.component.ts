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
import {ChangeDetectionStrategy, Component, computed, inject, input} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatCardModule} from '@angular/material/card';
import {MatDialog} from '@angular/material/dialog';
import {MatIconModule} from '@angular/material/icon';
import {MatTableModule} from '@angular/material/table';
import {MatTooltipModule} from '@angular/material/tooltip';
import {Router, RouterModule} from '@angular/router';
import type {ConfirmDialogData} from '../../../shared/components';
import {
  ConfirmDialogComponent,
  PageHeaderComponent,
  PageLayoutComponent,
} from '../../../shared/components';
import {ErrorBannerComponent} from '../../../shared/components/error-banner/error-banner.component';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';
import {SummaryFactComponent} from '../../../shared/components/summary-pane/summary-fact.component';
import {SummaryPaneComponent} from '../../../shared/components/summary-pane/summary-pane.component';
import type {Batch} from '../../models/batch.model';
import {BatchStore} from '../../store/batch.signal-store';

/**
 * Batch detail: the persisted policy, the derived rollup, and the member
 * devices with their connection info (adb serials, the CI payload).
 */
@Component({
  selector: 'app-batch-detail',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    LoadingStateComponent,
    ErrorBannerComponent,
    DatePipe,
    RouterModule,
    MatButtonModule,
    MatCardModule,
    MatIconModule,
    MatTableModule,
    MatTooltipModule,
    PageLayoutComponent,
    PageHeaderComponent,
    SummaryPaneComponent,
    SummaryFactComponent,
  ],
  template: `
    <app-page-layout variant="detail">
      <div page-header>
        <app-page-header
          [title]="batch()?.namePrefix || id()"
          [subtitle]="'Batch ' + id()">
          <div primary-action>
            <button type="button" mat-icon-button (click)="refresh()" matTooltip="Refresh" aria-label="Refresh">
              <mat-icon>refresh</mat-icon>
            </button>
            <button type="button" mat-flat-button color="warn" (click)="confirmDelete()">
              <mat-icon>delete</mat-icon>
              Delete batch
            </button>
          </div>
        </app-page-header>
      </div>

      @if (batch(); as b) {
        <div page-sidebar>
          <app-summary-pane heading="Summary">
            <app-summary-fact label="Status">
              <span [class]="'status-chip ' + b.status">{{ b.status }}</span>
            </app-summary-fact>
            <app-summary-fact label="Members">
              {{ b.running }} / {{ b.total }} running
              @if (b.errored > 0) {
                <span class="errored">({{ b.errored }} in error)</span>
              }
            </app-summary-fact>
            <app-summary-fact label="Fill policy">
              {{ b.fillPolicy || '-' }}
              @if (b.readyTimeoutSeconds) {
                <span class="muted">(ready timeout {{ b.readyTimeoutSeconds }}s)</span>
              }
            </app-summary-fact>
            <app-summary-fact label="Placement">{{ b.placement || '-' }}</app-summary-fact>
            <app-summary-fact label="Created">
              {{ b.createdAt | date: 'MMM d, y HH:mm:ss' }} by {{ b.createdBy || '-' }}
            </app-summary-fact>
            @if (b.readyAt) {
              <app-summary-fact label="Filled">{{ b.readyAt | date: 'MMM d, y HH:mm:ss' }}</app-summary-fact>
            }
          </app-summary-pane>
        </div>
      }

      <div page-content>
        @if (store.error(); as error) {
          <app-error-banner [message]="error" />
        }

        @if (batch(); as b) {
          @if (b.failedAt) {
            <div class="failure-banner">
              <mat-icon>error</mat-icon>
              <div>
                <strong>Failed {{ b.failedAt | date: 'MMM d, y HH:mm:ss' }}</strong>
                <div>{{ b.failureReason }}</div>
              </div>
            </div>
          }
        }

        @if (batch(); as b) {

          <mat-card>
            <mat-card-header>
              <mat-card-title>Member devices</mat-card-title>
            </mat-card-header>
            <mat-card-content>
              @if (store.membersLoading()) {
                <app-loading-state size="section" />
              } @else if (store.members().length > 0) {
                <table mat-table [dataSource]="store.members()" class="members-table">
                  <ng-container matColumnDef="status">
                    <th mat-header-cell *matHeaderCellDef></th>
                    <td mat-cell *matCellDef="let m">
                      <div [class]="'status-indicator ' + m.status" [matTooltip]="m.status"></div>
                    </td>
                  </ng-container>
                  <ng-container matColumnDef="name">
                    <th mat-header-cell *matHeaderCellDef>Name</th>
                    <td mat-cell *matCellDef="let m">
                      <a [routerLink]="['/devices', m.deviceId]">{{ m.name }}</a>
                    </td>
                  </ng-container>
                  <ng-container matColumnDef="adb">
                    <th mat-header-cell *matHeaderCellDef>ADB serial</th>
                    <td mat-cell *matCellDef="let m">
                      @if (m.adbSerial) {
                        <code>{{ m.adbSerial }}</code>
                      } @else {
                        <span class="muted">-</span>
                      }
                    </td>
                  </ng-container>
                  <ng-container matColumnDef="host">
                    <th mat-header-cell *matHeaderCellDef>Host</th>
                    <td mat-cell *matCellDef="let m">
                      <a [routerLink]="['/hosts', m.hostId]" class="host-link">{{ m.hostId }}</a>
                    </td>
                  </ng-container>

                  <tr mat-header-row *matHeaderRowDef="memberColumns"></tr>
                  <tr mat-row *matRowDef="let row; columns: memberColumns"></tr>
                </table>
              } @else {
                <p class="muted">No live members. The batch has been torn down.</p>
              }
            </mat-card-content>
          </mat-card>
        } @else if (store.loading()) {
          <app-loading-state />
        }
      </div>
    </app-page-layout>
  `,
  styles: `

      .label {
        font-size: 11px;
        text-transform: uppercase;
        letter-spacing: 0.5px;
        color: var(--cco-text-secondary);
      }

      .status-chip {
        text-transform: capitalize;
        font-weight: 500;
      }

      .status-chip.ready {
        color: var(--cco-success);
      }

      .status-chip.provisioning,
      .status-chip.deleting {
        color: var(--cco-accent);
      }

      .status-chip.degraded {
        color: var(--cco-warning);
      }

      .status-chip.failed {
        color: var(--cco-error);
      }

      .failure-banner {
        display: flex;
        align-items: center;
        gap: 12px;
        margin-top: 16px;
        padding: 12px 16px;
        border-radius: 8px;
        background: color-mix(in srgb, var(--cco-error) 12%, transparent);
        color: var(--cco-error);
      }

      .members-table {
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

      .status-indicator.pending,
      .status-indicator.creating,
      .status-indicator.starting,
      .status-indicator.stopping,
      .status-indicator.deleting {
        background: var(--cco-accent);
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

      .errored {
        color: var(--cco-error);
      }

      .muted {
        color: var(--cco-text-secondary);
      }


    `,
})
export class BatchDetailComponent {
  protected readonly store = inject(BatchStore);
  private readonly dialog = inject(MatDialog);
  private readonly router = inject(Router);

  readonly id = input.required<string>();

  // Explicitly optional: the entity map is keyed by id and the route can name
  // one that has not loaded (or no longer exists). Without the annotation the
  // index signature types this as always-present, which makes the template's
  // `?.` read as redundant when it is the only thing preventing a crash.
  protected readonly batch = computed<Batch | undefined>(() => this.store.entityMap()[this.id()]);
  protected readonly memberColumns = ['status', 'name', 'adb', 'host'];

  constructor() {
    // Reactive sources: rxMethod re-fires when the route param signal changes
    // (and safely after input binding, unlike a constructor-time read).
    this.store.loadOne(this.id);
    this.store.loadMembers(this.id);
  }

  protected refresh(): void {
    this.store.loadOne(this.id());
    this.store.loadMembers(this.id());
  }

  protected confirmDelete(): void {
    const b = this.batch();
    const data: ConfirmDialogData = {
      title: 'Delete batch?',
      message: `All ${b?.total ?? 0} member device(s) will be torn down.`,
      confirmText: 'Delete batch',
      confirmColor: 'warn',
    };
    this.dialog
      .open(ConfirmDialogComponent, {data})
      .afterClosed()
      .subscribe((confirmed: boolean | undefined) => {
        if (confirmed) {
          this.store.delete(this.id());
          void this.router.navigate(['/batches']);
        }
      });
  }
}
