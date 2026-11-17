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
import {ChangeDetectionStrategy, Component, inject, signal} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatDialog} from '@angular/material/dialog';
import {MatIconModule} from '@angular/material/icon';
import {MatSlideToggleModule} from '@angular/material/slide-toggle';
import {MatTableModule} from '@angular/material/table';
import {MatTooltipModule} from '@angular/material/tooltip';
import {RouterModule} from '@angular/router';
import {AuthStore} from '../../../auth/store/auth.signal-store';
import type {ConfirmDialogData} from '../../../shared/components';
import {
  ConfirmDialogComponent,
  PageHeaderComponent,
  PageLayoutComponent,
} from '../../../shared/components';
import {EmptyStateComponent} from '../../../shared/components/empty-state/empty-state.component';
import {ErrorBannerComponent} from '../../../shared/components/error-banner/error-banner.component';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';
import {foreignCreatorWarning} from '../../../shared/utils';
import type {Batch} from '../../models/batch.model';
import {BatchStore} from '../../store/batch.signal-store';

/** Batch list: the fleets created and torn down as one unit. */
@Component({
  selector: 'app-batch-list',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    EmptyStateComponent,
    LoadingStateComponent,
    ErrorBannerComponent,
    DatePipe,
    RouterModule,
    MatButtonModule,
    MatIconModule,
    MatSlideToggleModule,
    MatTableModule,
    MatTooltipModule,
    PageLayoutComponent,
    PageHeaderComponent,
  ],
  template: `
    <app-page-layout variant="list">
      <div page-header>
        <app-page-header
          title="Batches"
          subtitle="Device fleets created and torn down as one unit (CI)">
          <div primary-action>
            <button type="button" mat-flat-button routerLink="create">
              <mat-icon>add</mat-icon>
              Create batch
            </button>
            <mat-slide-toggle
                class="my-batches-toggle"
                [checked]="onlyMyBatches()"
                (change)="onMineToggle($event.checked)"
                color="primary">
                My batches only
            </mat-slide-toggle>
            <button type="button" mat-icon-button (click)="refresh()" matTooltip="Refresh" aria-label="Refresh">
              <mat-icon>refresh</mat-icon>
            </button>
          </div>
        </app-page-header>
      </div>

      <div page-content>
        @if (store.loading()) {
          <app-loading-state />
        }

        @if (store.error(); as error) {
          <app-error-banner [message]="error" />
        }

        @if (!store.loading() && store.entities().length > 0) {
          <table mat-table [dataSource]="store.entities()" class="batch-table">
            <ng-container matColumnDef="status">
              <th mat-header-cell *matHeaderCellDef></th>
              <td mat-cell *matCellDef="let batch">
                <div [class]="'status-indicator ' + batch.status" [matTooltip]="batch.status"></div>
              </td>
            </ng-container>
            <ng-container matColumnDef="prefix">
              <th mat-header-cell *matHeaderCellDef>Name prefix</th>
              <td mat-cell *matCellDef="let batch">
                <a [routerLink]="[batch.id]" class="batch-name">{{ batch.namePrefix || batch.id }}</a>
                <div class="batch-id">{{ batch.id }}</div>
              </td>
            </ng-container>
            <ng-container matColumnDef="members">
              <th mat-header-cell *matHeaderCellDef>Members</th>
              <td mat-cell *matCellDef="let batch">
                {{ batch.running }} / {{ batch.total }} running
                @if (batch.errored > 0) {
                  <span class="errored">({{ batch.errored }} in error)</span>
                }
              </td>
            </ng-container>
            <ng-container matColumnDef="policy">
              <th mat-header-cell *matHeaderCellDef>Policy</th>
              <td mat-cell *matCellDef="let batch">
                {{ batch.fillPolicy || '-' }} · {{ batch.placement || '-' }}
              </td>
            </ng-container>
            <ng-container matColumnDef="created">
              <th mat-header-cell *matHeaderCellDef>Created</th>
              <td mat-cell *matCellDef="let batch">
                {{ batch.createdAt | date: 'MMM d, HH:mm' }}
              </td>
            </ng-container>
            <ng-container matColumnDef="actions">
              <th mat-header-cell *matHeaderCellDef></th>
              <td mat-cell *matCellDef="let batch">
                <button type="button"
                  mat-icon-button
                  (click)="confirmDelete(batch)"
                  matTooltip="Delete batch (tears down all members)" aria-label="Delete batch (tears down all members)">
                  <mat-icon>delete</mat-icon>
                </button>
              </td>
            </ng-container>

            <tr mat-header-row *matHeaderRowDef="displayedColumns"></tr>
            <tr mat-row *matRowDef="let row; columns: displayedColumns"></tr>
          </table>
        }

        @if (!store.loading() && store.entities().length === 0 && !store.error()) {
          <app-empty-state icon="dataset" title="No batches">
            <p>A batch is N devices created, tracked and torn down as one unit.</p>
            <button type="button" mat-flat-button routerLink="create">
              <mat-icon>add</mat-icon>
              Create batch
            </button>
          </app-empty-state>
        }
      </div>
    </app-page-layout>
  `,
  styles: `
      .my-batches-toggle {
        margin: 0 var(--cco-space-12);
      }

      .batch-table {
        width: 100%;
      }

      .status-indicator {
        width: 12px;
        height: 12px;
        border-radius: 50%;
        background: var(--cco-text-secondary);
      }

      .status-indicator.ready {
        background: var(--cco-success);
      }

      .status-indicator.provisioning,
      .status-indicator.deleting {
        background: var(--cco-accent);
        animation: pulse 1.5s infinite;
      }

      .status-indicator.degraded {
        background: var(--cco-warning);
      }

      .status-indicator.failed {
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

      .batch-name {
        font-weight: 500;
      }

      .batch-id {
        font-size: 11px;
        color: var(--cco-text-secondary);
        font-family: monospace;
      }

      .errored {
        color: var(--cco-error);
      }



    `,
})
export class BatchListComponent {
  protected readonly store = inject(BatchStore);
  private readonly authStore = inject(AuthStore);
  private readonly dialog = inject(MatDialog);

  /**
   * Mine/All scope, filtered server-side. Defaults to Mine because the console
   * is a human surface. cfctl keeps the fleet-wide default (batches are
   * usually CI-created, so All is one toggle away).
   */
  protected readonly onlyMyBatches = signal(true);

  protected readonly displayedColumns = [
    'status',
    'prefix',
    'members',
    'policy',
    'created',
    'actions',
  ];

  constructor() {
    this.load();
  }

  protected refresh(): void {
    this.load();
  }

  protected onMineToggle(checked: boolean): void {
    this.onlyMyBatches.set(checked);
    this.load();
  }

  private load(): void {
    this.store.loadAll({mine: this.onlyMyBatches()});
  }

  protected confirmDelete(batch: Batch): void {
    const data: ConfirmDialogData = {
      title: 'Delete batch?',
      message: `All ${batch.total} member device(s) of "${batch.namePrefix || batch.id}" will be torn down.`,
      warning: foreignCreatorWarning(batch.createdBy, this.authStore.user()),
      confirmText: 'Delete batch',
      confirmColor: 'warn',
    };
    this.dialog
      .open(ConfirmDialogComponent, {data})
      .afterClosed()
      .subscribe((confirmed: boolean | undefined) => {
        if (confirmed) {
          this.store.delete(batch.id);
        }
      });
  }
}
