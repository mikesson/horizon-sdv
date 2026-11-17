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

import type {OnInit} from '@angular/core';
import {ChangeDetectionStrategy, Component, computed, inject} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatCardModule} from '@angular/material/card';
import {MatDialog} from '@angular/material/dialog';
import {MatDividerModule} from '@angular/material/divider';
import {MatIconModule} from '@angular/material/icon';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import {MatSnackBar} from '@angular/material/snack-bar';
import {MatTooltipModule} from '@angular/material/tooltip';
import type {KpiItem} from '../../../shared/components';
import {
  ConfirmDialogComponent,
  type ConfirmDialogData,
  KpiStripComponent,
} from '../../../shared/components';
import type {HostImageConfig, MachineFamilyCategory} from '../../models/host-image-config.model';
import {getCategoryLabel, getMachineFamilyDisplayName} from '../../models/host-image-config.model';
import {HostImageConfigStore} from '../../store/host-image-config.signal-store';
import type {EditDialogResult} from './edit-dialog.component';
import {HostImageConfigEditDialogComponent} from './edit-dialog.component';

/**
 * Host-image configuration panel: the Host Images tab of the admin Catalog
 * page. Page chrome (header, refresh) is owned by CatalogComponent.
 */
@Component({
  selector: 'app-host-image-config',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    MatButtonModule,
    MatCardModule,
    MatDividerModule,
    MatIconModule,
    MatProgressSpinnerModule,
    MatTooltipModule,
    KpiStripComponent,
  ],
  template: `
    @if (store.loading()) {
      <div class="loading-container">
        <mat-spinner aria-label="Loading" diameter="48" />
      </div>
    } @else if (store.error()) {
      <div class="error-banner">
        <mat-icon>error_outline</mat-icon>
        <span>{{ store.error() }}</span>
      </div>
    } @else {
      <div class="configs-toolbar">
        <app-kpi-strip [items]="kpis()" />
        <button type="button" mat-flat-button (click)="addConfig()">
          <mat-icon>add</mat-icon>
          Add configuration
        </button>
      </div>

      <!-- Category sections (the outer Catalog page owns the tab strip) -->
      @for (group of store.byCategory(); track group.category) {
        <section class="category-section">
          <h3 class="category-header">{{ group.label }}</h3>
          <div class="config-grid">
            @for (config of group.configs; track config.machineFamily) {
                <div class="config-card" [class.disabled]="!config.enabled">
                  <div class="config-header">
                    <div class="family-badge" [class.disabled]="!config.enabled">
                      {{ config.machineFamily }}
                    </div>
                    <div class="config-actions">
                      <button type="button"
                        mat-icon-button
                        matTooltip="Edit configuration"
                        (click)="editConfig(config)" aria-label="Edit configuration">
                        <mat-icon>edit</mat-icon>
                      </button>
                      <button type="button"
                        mat-icon-button
                        class="delete-btn"
                        matTooltip="Delete configuration"
                        (click)="confirmDelete(config)" aria-label="Delete configuration">
                        <mat-icon>delete</mat-icon>
                      </button>
                    </div>
                  </div>

                  <div class="config-title">{{ getFamilyDisplayName(config.machineFamily) }}</div>

                  @if (config.description) {
                    <div class="config-description">{{ config.description }}</div>
                  }

                  <mat-divider />

                  <div class="config-details">
                    <div class="detail-row">
                      <span class="detail-label">Image Family</span>
                      <span class="detail-value">{{ config.imageFamily }}</span>
                    </div>
                    <div class="detail-row">
                      <span class="detail-label">Source Project</span>
                      <span class="detail-value">
                        @if (config.sourceProject) {
                          {{ config.sourceProject }}
                        } @else {
                          {{ config.resolved?.project }} (deployment)
                        }
                      </span>
                    </div>
                    @if (config.resolved; as resolved) {
                      <div class="detail-row">
                        <span class="detail-label">Image</span>
                        @if (resolved.ok) {
                          <span class="detail-value image-ok">{{ resolved.imageName }}</span>
                        } @else {
                          <span class="detail-value image-unavailable" [matTooltip]="resolved.message ?? ''">
                            {{ resolved.reason ?? 'lookup failed' }}
                          </span>
                        }
                      </div>
                    }
                    <div class="detail-row">
                      <span class="detail-label">Status</span>
                      <span class="detail-value" [class.enabled]="config.enabled" [class.disabled]="!config.enabled">
                        {{ config.enabled ? 'Enabled' : 'Disabled' }}
                      </span>
                    </div>
                  </div>

                  <div class="config-meta">
                    Updated {{ formatDate(config.updatedAt) }}
                    @if (config.updatedBy) {
                      by {{ config.updatedBy }}
                    }
                  </div>
                </div>
              }
          </div>
        </section>
      }
    }
  `,
  styles: `
    .loading-container {
      display: flex;
      justify-content: center;
      padding: 64px;
    }

    .error-banner {
      display: flex;
      align-items: center;
      gap: 12px;
      padding: 16px 24px;
      background: var(--cco-error-surface);
      border: 1px solid var(--cco-error-surface);
      border-radius: 8px;
      color: var(--cco-error);
    }

    .error-banner mat-icon {
      color: var(--cco-error);
    }

    .error-banner span {
      flex: 1;
    }

    .configs-toolbar {
      display: flex;
      align-items: flex-start;
      gap: 16px;

      app-kpi-strip {
        flex: 1;
        min-width: 0;
      }

      button {
        flex-shrink: 0;
        margin-top: 8px;
      }
    }

    /* Category Sections */
    .category-section {
      margin-top: 24px;
    }

    .category-header {
      font-size: 12px;
      font-weight: 500;
      text-transform: uppercase;
      letter-spacing: 0.5px;
      color: var(--cco-text-secondary);
      margin: 0 0 12px;
    }

    /* Config Grid */
    .config-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
      gap: 16px;
    }

    /* Config Card */
    .config-card {
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: 8px;
      padding: 16px;
      transition: box-shadow var(--cco-duration-slow);
    }

    .config-card:hover {
      box-shadow: 0 1px 3px rgba(0, 0, 0, 0.15), 0 4px 8px rgba(0, 0, 0, 0.1);
    }

    .config-card.disabled {
      background: var(--cco-surface-variant);
      opacity: 0.8;
    }

    .config-header {
      display: flex;
      justify-content: space-between;
      align-items: flex-start;
      margin-bottom: 8px;
    }

    .config-actions {
      display: flex;
    }

    .delete-btn mat-icon {
      color: var(--cco-text-secondary);
    }

    .delete-btn:hover mat-icon {
      color: var(--cco-error);
    }

    .family-badge {
      display: inline-flex;
      align-items: center;
      padding: 4px 8px;
      background: var(--cco-accent-surface);
      color: var(--cco-accent);
      border-radius: 4px;
      font-size: 12px;
      font-weight: 500;
      text-transform: uppercase;
    }

    .family-badge.disabled {
      background: var(--cco-surface-variant);
      color: var(--cco-text-secondary);
    }

    .config-title {
      font-size: 14px;
      font-weight: 500;
      color: var(--cco-text);
      margin-bottom: 4px;
    }

    .config-description {
      font-size: 12px;
      color: var(--cco-text-secondary);
      margin-bottom: 12px;
    }

    .config-details {
      padding: 12px 0;
    }

    .detail-row {
      display: flex;
      justify-content: space-between;
      font-size: 12px;
      margin-bottom: 8px;
    }

    .detail-row:last-child {
      margin-bottom: 0;
    }

    .detail-label {
      color: var(--cco-text-secondary);
    }

    .detail-value {
      color: var(--cco-text);
      font-family: 'Roboto Mono', monospace;
      font-size: 12px;
    }

    .detail-value.enabled {
      color: var(--cco-success);
    }

    .detail-value.disabled {
      color: var(--cco-text-secondary);
    }

    .detail-value.image-ok {
      color: var(--cco-success);
    }

    .detail-value.image-unavailable {
      color: var(--cco-error);
    }

    .config-meta {
      font-size: 11px;
      color: var(--cco-text-secondary);
      margin-top: 12px;
    }

    mat-divider {
      margin: 8px 0;
    }
  `,
})
export class HostImageConfigComponent implements OnInit {
  readonly store = inject(HostImageConfigStore);
  private readonly dialog = inject(MatDialog);
  private readonly snackBar = inject(MatSnackBar);

  protected readonly kpis = computed<KpiItem[]>(() => {
    const stats = this.store.stats();
    return [
      {label: 'Total configs', value: stats.total, icon: 'perm_media'},
      {label: 'Enabled', value: stats.enabled, icon: 'check_circle', colorClass: 'running'},
      {label: 'Disabled', value: stats.disabled, icon: 'do_not_disturb_on'},
    ];
  });

  ngOnInit(): void {
    this.store.loadAll({});
  }

  getFamilyDisplayName(family: string): string {
    return getMachineFamilyDisplayName(family);
  }

  getCategoryLabel(category: MachineFamilyCategory): string {
    return getCategoryLabel(category);
  }

  formatDate(date: Date): string {
    return date.toLocaleDateString('en-US', {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
    });
  }

  addConfig(): void {
    const dialogRef = this.dialog.open(HostImageConfigEditDialogComponent, {
      data: {},
      width: '500px',
    });

    dialogRef.afterClosed().subscribe((result: EditDialogResult | undefined) => {
      if (result) {
        this.store.create({
          machineFamily: result.machineFamily,
          imageFamily: result.imageFamily,
          sourceProject: result.sourceProject || undefined,
          description: result.description || undefined,
          enabled: result.enabled,
        });

        this.snackBar.open(`Configuration for ${result.machineFamily} created`, 'Dismiss', {
          duration: 4000,
          horizontalPosition: 'end',
        });
      }
    });
  }

  editConfig(config: HostImageConfig): void {
    const dialogRef = this.dialog.open(HostImageConfigEditDialogComponent, {
      data: {config},
      width: '500px',
    });

    dialogRef.afterClosed().subscribe((result: EditDialogResult | undefined) => {
      if (result) {
        this.store.update({
          machineFamily: config.machineFamily,
          changes: {
            imageFamily: result.imageFamily,
            sourceProject: result.sourceProject || undefined,
            description: result.description || undefined,
            enabled: result.enabled,
          },
        });

        this.snackBar.open(`Configuration for ${config.machineFamily} updated`, 'Dismiss', {
          duration: 4000,
          horizontalPosition: 'end',
        });
      }
    });
  }

  confirmDelete(config: HostImageConfig): void {
    const data: ConfirmDialogData = {
      title: 'Delete image configuration',
      message:
        `Delete the ${config.machineFamily} image configuration? Hosts of this ` +
        'machine family cannot be created until it has a configuration again.',
      confirmText: 'Delete',
      confirmColor: 'warn',
      icon: 'delete',
      iconColor: 'warn',
    };
    this.dialog
      .open(ConfirmDialogComponent, {data})
      .afterClosed()
      .subscribe((confirmed: boolean) => {
        if (confirmed) {
          this.store.delete(config.machineFamily);
          this.snackBar.open(`Configuration for ${config.machineFamily} deleted`, 'Dismiss', {
            duration: 4000,
            horizontalPosition: 'end',
          });
        }
      });
  }
}
