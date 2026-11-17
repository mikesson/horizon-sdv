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
import {ChangeDetectionStrategy, Component, computed, inject, signal} from '@angular/core';
import {MatBadgeModule} from '@angular/material/badge';
import {MatButtonModule} from '@angular/material/button';
import {MatCardModule} from '@angular/material/card';
import {MatChipsModule} from '@angular/material/chips';
import {MatDialog, MatDialogModule} from '@angular/material/dialog';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatMenuModule} from '@angular/material/menu';
import {MatSelectModule} from '@angular/material/select';
import {MatTableModule} from '@angular/material/table';
import {MatTooltipModule} from '@angular/material/tooltip';
import {Router, RouterModule} from '@angular/router';
import type {ConfirmDialogData, KpiItem} from '../../../shared/components';
import {
  ActionBarComponent,
  ConfirmDialogComponent,
  KpiStripComponent,
  PageHeaderComponent,
  PageLayoutComponent,
} from '../../../shared/components';
import {EmptyStateComponent} from '../../../shared/components/empty-state/empty-state.component';
import {ErrorBannerComponent} from '../../../shared/components/error-banner/error-banner.component';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';
import {collectionNote} from '../../../shared/utils/paged-collection';
import type {Build} from '../../models/build.model';
import {isTrackingBuild} from '../../models/build.model';
import {BuildStore} from '../../store/build.signal-store';
import type {BuildEditDialogResult} from '../build-edit-dialog/build-edit-dialog.component';
import {BuildEditDialogComponent} from '../build-edit-dialog/build-edit-dialog.component';

@Component({
  selector: 'app-build-list',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    EmptyStateComponent,
    LoadingStateComponent,
    ErrorBannerComponent,
    DatePipe,
    RouterModule,
    MatTableModule,
    MatButtonModule,
    MatIconModule,
    MatChipsModule,
    MatMenuModule,
    MatFormFieldModule,
    MatSelectModule,
    MatTooltipModule,
    MatCardModule,
    MatBadgeModule,
    MatDialogModule,
    PageLayoutComponent,
    PageHeaderComponent,
    KpiStripComponent,
    ActionBarComponent,
  ],
  template: `
    <app-page-layout variant="list">
      <div page-header>
        <app-page-header
          title="Builds"
          subtitle="Manage Android builds for Cuttlefish devices">
          <div primary-action>
            <button type="button" mat-flat-button routerLink="create">
              <mat-icon>add</mat-icon>
              Register Build
            </button>
          </div>
        </app-page-header>
      </div>

      <div page-content>
        <!-- KPI Strip -->
        <app-kpi-strip [items]="kpiItems()" (cardClick)="onKpiClick($event)" />

        <!-- Action Bar with Filters -->
        <app-action-bar>
          <div filters>
            <mat-form-field appearance="outline">
              <mat-label>Source</mat-label>
              <mat-select [value]="selectedSource()" (selectionChange)="selectedSource.set($event.value)">
                <mat-option value="">All Sources</mat-option>
                <mat-option value="android_build">Official AOSP</mat-option>
                <mat-option value="url">Custom</mat-option>
              </mat-select>
            </mat-form-field>
          </div>

          <div actions>
            <button type="button" mat-icon-button (click)="refresh()" matTooltip="Refresh" aria-label="Refresh">
              <mat-icon>refresh</mat-icon>
            </button>
          </div>
        </app-action-bar>

        <!-- Loading State -->
      @if (loading()) {
        <app-loading-state />
      }
    
      <!-- Error State -->
      @if (error(); as error) {
        <app-error-banner [message]="error" />
      }
    
      <!-- Build Table -->
      @if (!loading()) {
        <table
          mat-table
          [dataSource]="filteredBuilds()"
          class="build-table"
          >
          <ng-container matColumnDef="name">
            <th mat-header-cell *matHeaderCellDef>Name</th>
            <td mat-cell *matCellDef="let build">
              <div class="build-name">
                {{ build.name }}
                @if (build.isDefault) {
                  <mat-chip class="default-badge">Default</mat-chip>
                }
              </div>
              @if (build.target) {
                <div class="build-target">{{ build.target }}</div>
              }
            </td>
          </ng-container>
          <ng-container matColumnDef="source">
            <th mat-header-cell *matHeaderCellDef>Source</th>
            <td mat-cell *matCellDef="let build">
              <mat-chip [class]="build.source === 'android_build' ? 'official' : 'custom'">
                {{ formatSource(build.source) }}
              </mat-chip>
            </td>
          </ng-container>
          <ng-container matColumnDef="version">
            <th mat-header-cell *matHeaderCellDef>Version</th>
            <td mat-cell *matCellDef="let build">
              @if (isTracking(build)) {
                <div class="version-cell">
                  <span
                    class="tracking-chip"
                    matTooltip="Resolves to the newest build on {{ build.branch }} at every device launch">
                    <mat-icon>autorenew</mat-icon>
                    Tracks latest
                  </span>
                  <span class="version branch">{{ build.branch }}</span>
                </div>
              } @else if (build.buildId) {
                <span class="version pinned" matTooltip="Pinned to a specific Android CI build">
                  <mat-icon>tag</mat-icon>
                  {{ build.buildId }}
                </span>
              } @else {
                <span class="version">-</span>
              }
            </td>
          </ng-container>
          <ng-container matColumnDef="updated">
            <th mat-header-cell *matHeaderCellDef>Updated</th>
            <td mat-cell *matCellDef="let build">
              {{ build.updatedAt | date: 'short' }}
            </td>
          </ng-container>
          <ng-container matColumnDef="actions">
            <th mat-header-cell *matHeaderCellDef></th>
            <td mat-cell *matCellDef="let build">
              <a
                mat-icon-button
                matTooltip="Create device with this build"
                aria-label="Create device with this build"
                [routerLink]="['/devices/create']"
                [queryParams]="{buildId: build.id}">
                <mat-icon>add_to_queue</mat-icon>
              </a>
              <button type="button" mat-icon-button aria-label="Build actions" [matMenuTriggerFor]="menu">
                <mat-icon>more_vert</mat-icon>
              </button>
              <mat-menu #menu="matMenu">
                @if (!build.isDefault) {
                  <button type="button"
                    mat-menu-item
                    (click)="setAsDefault(build)"
                    >
                    <mat-icon>star</mat-icon>
                    <span>Set as Default</span>
                  </button>
                }
                <button type="button" mat-menu-item (click)="editBuild(build)">
                  <mat-icon>edit</mat-icon>
                  <span>Edit</span>
                </button>
                <button type="button"
                  mat-menu-item
                  (click)="deleteBuild(build)"
                  >
                  <mat-icon>delete</mat-icon>
                  <span>Delete</span>
                </button>
              </mat-menu>
            </td>
          </ng-container>
          <tr mat-header-row *matHeaderRowDef="displayedColumns()"></tr>
          <tr mat-row *matRowDef="let row; columns: displayedColumns()"></tr>
        </table>
      }

        @if (!loading() && truncationNote()) {
          <div class="truncation-hint">{{ truncationNote() }}</div>
        }

        <!-- Empty State -->
        @if (!loading() && filteredBuilds().length === 0) {
          <app-empty-state
            icon="build"
            title="No builds registered"
            message="Register an Android build to deploy on Cuttlefish devices"
          >
            <button type="button" action mat-flat-button routerLink="create">
              Register Build
            </button>
          </app-empty-state>
        }
      </div>
    </app-page-layout>
    `,
  styles: `
      /* Table styles */
      .build-table {
        width: 100%;
      }

      .truncation-hint {
        padding: 8px 4px;
        font-size: 12px;
        color: var(--cco-text-secondary);
      }

      .build-name {
        display: flex;
        align-items: center;
        gap: 8px;
        font-weight: 500;
      }

      .build-target {
        font-family: monospace;
        font-size: 11px;
        color: var(--cco-text-secondary);
        margin-top: 2px;
      }

      .default-badge {
        font-size: 11px;
        min-height: 20px;
        padding: 0 8px;
        background: var(--cco-warning) !important;
        color: var(--cco-canvas) !important;
      }

      mat-chip.official {
        background: var(--cco-accent) !important;
        color: white !important;
      }

      mat-chip.custom {
        background: var(--cco-warning) !important;
        color: var(--cco-canvas) !important;
      }

      .version {
        font-family: monospace;
        font-size: 12px;
      }

      .version-cell {
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        gap: 2px;
      }

      .tracking-chip {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 2px 12px;
        border: 1px solid var(--cco-accent);
        border-radius: 12px;
        color: var(--cco-accent);
        font-size: 12px;
        white-space: nowrap;
      }

      .tracking-chip mat-icon {
        font-size: 14px;
        width: 14px;
        height: 14px;
      }

      .version.branch {
        color: var(--cco-text-secondary);
      }

      .version.pinned {
        display: inline-flex;
        align-items: center;
        gap: 4px;
      }

      .version.pinned mat-icon {
        font-size: 14px;
        width: 14px;
        height: 14px;
        color: var(--cco-text-secondary);
      }




    `,
})
export class BuildListComponent {
  private readonly router = inject(Router);
  private readonly buildStore = inject(BuildStore);
  private readonly dialog = inject(MatDialog);

  readonly builds = this.buildStore.entities;
  readonly loading = this.buildStore.loading;
  readonly error = this.buildStore.error;
  readonly stats = this.buildStore.stats;

  // Local state signals
  readonly selectedSource = signal('');

  // Derived state
  readonly filteredBuilds = computed(() => {
    const builds = this.builds();
    const source = this.selectedSource();
    return source ? builds.filter(build => build.source === source) : builds;
  });

  readonly displayedColumns = computed(() => ['name', 'source', 'version', 'updated', 'actions']);

  /**
   * Non-empty when the store holds fewer builds than the server reports. The
   * catalog count above the table is then counting what is loaded, not what
   * exists.
   */
  protected readonly truncationNote = computed(() =>
    collectionNote(
      this.builds().length,
      this.buildStore.total(),
      this.buildStore.complete(),
      'builds'
    )
  );

  readonly kpiItems = computed<KpiItem[]>(() => {
    const s = this.stats();
    if (!s) {
      return [];
    }

    return [
      {
        label: 'Total Builds',
        value: s.total,
        icon: 'inventory_2',
        iconColor: 'var(--cco-accent)',
      },
      {
        label: 'Official AOSP',
        value: s.official,
        icon: 'android',
        iconColor: 'var(--cco-success)',
        colorClass: 'success',
      },
      {
        label: 'Custom',
        value: s.custom,
        icon: 'build',
        iconColor: 'var(--cco-warning)',
        colorClass: 'warning',
      },
    ];
  });

  constructor() {
    this.buildStore.loadAll({});
  }

  refresh(): void {
    this.buildStore.loadAll({});
  }

  onKpiClick(item: KpiItem): void {
    if (item.route) {
      void this.router.navigate([item.route]);
    }
  }

  protected readonly isTracking = isTrackingBuild;

  formatSource(source: string): string {
    const sources: Record<string, string> = {
      android_build: 'Official AOSP',
      url: 'Custom',
    };
    return sources[source] || source;
  }

  setAsDefault(build: Build): void {
    this.buildStore.update({id: build.id, request: {isDefault: true}});
  }

  editBuild(build: Build): void {
    this.dialog
      .open(BuildEditDialogComponent, {data: {build}, width: '480px'})
      .afterClosed()
      .subscribe((result: BuildEditDialogResult | undefined) => {
        if (result) {
          this.buildStore.update({
            id: build.id,
            request: {
              name: result.name,
              description: result.description,
              isDefault: result.isDefault,
            },
          });
        }
      });
  }

  deleteBuild(build: Build): void {
    const data: ConfirmDialogData = {
      title: 'Delete Build',
      message: `Are you sure you want to delete "${build.name}"? This action cannot be undone.`,
      confirmText: 'Delete',
      confirmColor: 'warn',
      icon: 'delete',
      iconColor: 'warn',
    };
    const dialogRef = this.dialog.open(ConfirmDialogComponent, {data});

    dialogRef.afterClosed().subscribe(confirmed => {
      if (confirmed) {
        this.buildStore.delete(build.id);
      }
    });
  }
}
