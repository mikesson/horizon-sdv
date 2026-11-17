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

import {ChangeDetectionStrategy, Component, computed, inject, signal} from '@angular/core';
import {FormField, form} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatSelectModule} from '@angular/material/select';
import {MatSlideToggleModule} from '@angular/material/slide-toggle';
import {MatTooltipModule} from '@angular/material/tooltip';
import {Router, RouterModule} from '@angular/router';
import type {KpiItem} from '../../../shared/components';
import {
  ActionBarComponent,
  KpiStripComponent,
  PageHeaderComponent,
  PageLayoutComponent,
} from '../../../shared/components';
import {EmptyStateComponent} from '../../../shared/components/empty-state/empty-state.component';
import {ErrorBannerComponent} from '../../../shared/components/error-banner/error-banner.component';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';
import {collectionNote} from '../../../shared/utils/paged-collection';
import type {HostClass} from '../../models/host.model';
import {HostActionsService} from '../../services/host-actions.service';
import {HostStore} from '../../store/host.signal-store';
import {buildHostKpis} from './host-kpis';
import {HostTableComponent} from './host-table.component';

/**
 * Smart container for the host feature: owns list loading, the filter form, and
 * the KPI strip; delegates the table to app-host-table and every row command to
 * HostActionsService.
 */
@Component({
  selector: 'app-host-list',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    EmptyStateComponent,
    LoadingStateComponent,
    ErrorBannerComponent,
    RouterModule,
    FormField,
    MatButtonModule,
    MatIconModule,
    MatFormFieldModule,
    MatSelectModule,
    MatSlideToggleModule,
    MatTooltipModule,
    PageLayoutComponent,
    PageHeaderComponent,
    KpiStripComponent,
    ActionBarComponent,
    HostTableComponent,
  ],
  template: `
    <app-page-layout variant="list">
      <div page-header>
        <app-page-header
          title="Hosts"
          subtitle="Manage Cuttlefish host VMs">
          <div primary-action>
            <button type="button" mat-flat-button routerLink="create">
              <mat-icon>add</mat-icon>
              Create Host
            </button>
          </div>
        </app-page-header>
      </div>

      <div page-content>
        <app-kpi-strip [items]="kpiItems()" (cardClick)="onKpiClick($event)" />

        <app-action-bar>
          <div filters>
            <mat-form-field appearance="outline">
              <mat-label>Zone</mat-label>
              <mat-select [formField]="filterForm.zone">
                <mat-option value="">All Zones</mat-option>
                @for (zone of zones(); track zone) {
                  <mat-option [value]="zone">
                    {{ zone }}
                  </mat-option>
                }
              </mat-select>
            </mat-form-field>

            <mat-form-field appearance="outline">
              <mat-label>Status</mat-label>
              <mat-select [formField]="filterForm.status">
                <mat-option value="">All Statuses</mat-option>
                <mat-option value="running">Running</mat-option>
                <mat-option value="stopped">Stopped</mat-option>
                <mat-option value="provisioning">Provisioning</mat-option>
                <mat-option value="preempted">Preempted</mat-option>
                <mat-option value="error">Error</mat-option>
              </mat-select>
            </mat-form-field>

            <mat-form-field appearance="outline">
              <mat-label>Class</mat-label>
              <!-- Server-side: the class is derived from three columns, so a
                   client-side filter would page over the wrong row set. -->
              <mat-select [value]="hostClass() ?? ''" (selectionChange)="onClassChange($event.value)">
                <mat-option value="">All Classes</mat-option>
                <mat-option value="fleet">Fleet</mat-option>
                <mat-option value="reserved">Reserved</mat-option>
                <mat-option value="dedicated">Dedicated</mat-option>
                <mat-option value="batch">Batch</mat-option>
              </mat-select>
            </mat-form-field>

            <mat-slide-toggle
                class="my-hosts-toggle"
                [checked]="onlyMyHosts()"
                (change)="onMineToggle($event.checked)"
                color="primary">
                My hosts only
            </mat-slide-toggle>
          </div>

          <div actions>
            @if (selectedCount() > 0) {
              <button type="button"
                mat-stroked-button
                color="warn"
                (click)="deleteSelected()"
                matTooltip="Delete selected hosts">
                <mat-icon>delete</mat-icon>
                Delete ({{ selectedCount() }})
              </button>
            }
            <button type="button" mat-icon-button (click)="refresh()" matTooltip="Refresh" aria-label="Refresh">
              <mat-icon>refresh</mat-icon>
            </button>
          </div>
        </app-action-bar>

        @if (loading()) {
          <app-loading-state />
        }

        @if (error(); as error) {
          <app-error-banner [message]="error" />
        }

        @if (!loading()) {
          <app-host-table
            [hosts]="filteredHosts()"
            [displayedColumns]="displayedColumns"
            [selectedIds]="selectedIds()"
            (startHost)="actions.start($event)"
            (stopHost)="actions.stop($event)"
            (syncHost)="actions.sync($event)"
            (retryHost)="actions.retry($event.host, $event.fallbackToStandard)"
            (deleteHost)="actions.delete($event)"
            (toggleRow)="onToggleRow($event)"
            (toggleAll)="onToggleAll($event)" />
        }

        @if (!loading() && truncationNote()) {
          <div class="truncation-hint">{{ truncationNote() }}</div>
        }

        @if (!loading() && filteredHosts().length === 0) {
          <app-empty-state
            icon="dns"
            title="No hosts found"
            message="Create a host to get started with Cuttlefish"
          >
            <button type="button" action mat-flat-button routerLink="create">
              Create Host
            </button>
          </app-empty-state>
        }
      </div>
    </app-page-layout>
    `,
  styles: `
      .my-hosts-toggle {
        margin-left: 12px;
      }

      .truncation-hint {
        padding: 8px 4px;
        font-size: 12px;
        color: var(--cco-text-secondary);
      }




    `,
})
export class HostListComponent {
  private readonly router = inject(Router);
  private readonly hostStore = inject(HostStore);
  protected readonly actions = inject(HostActionsService);

  private readonly hosts = this.hostStore.entities;
  protected readonly loading = this.hostStore.loading;
  protected readonly error = this.hostStore.error;
  private readonly stats = this.hostStore.stats;
  protected readonly zones = this.hostStore.zones;

  private readonly filterDraft = signal({zone: '', status: ''});
  protected readonly filterForm = form(this.filterDraft);

  /**
   * Mine/All scope, filtered server-side (the loaded page would hide foreign
   * rows a client-side filter thinks it can count). Defaults to Mine because
   * the console is a human surface. cfctl keeps the fleet-wide default.
   */
  protected readonly onlyMyHosts = signal(true);

  /** Unset = every class. Filtered server-side, so changing it reloads. */
  protected readonly hostClass = signal<HostClass | undefined>(undefined);

  private readonly selectedZone = computed(() => this.filterDraft().zone);
  private readonly selectedStatus = computed(() => this.filterDraft().status);

  protected readonly filteredHosts = computed(() => {
    const hosts = this.hosts();
    const zone = this.selectedZone();
    const status = this.selectedStatus();

    return hosts.filter(host => {
      if (zone && host.zone !== zone) {
        return false;
      }
      if (status && host.status !== status) {
        return false;
      }
      return true;
    });
  });

  protected readonly displayedColumns = [
    'select',
    'status',
    'name',
    'zone',
    'class',
    'gpu',
    'devices',
    'provisioning',
    'ip',
    'actions',
  ];

  /**
   * Raw multi-selection. Stale ids (rows since filtered out or deleted) are
   * harmless, because every read intersects with the visible rows, so the
   * derived selection and the table's checkboxes only reflect present rows.
   */
  private readonly selectedIdSet = signal<ReadonlySet<string>>(new Set<string>());
  protected readonly selectedIds = this.selectedIdSet.asReadonly();

  /** The selected hosts still present in the current view. */
  protected readonly selectedHosts = computed(() =>
    this.filteredHosts().filter(h => this.selectedIds().has(h.id))
  );
  protected readonly selectedCount = computed(() => this.selectedHosts().length);

  protected readonly kpiItems = computed<KpiItem[]>(() => buildHostKpis(this.stats()));

  /**
   * Non-empty when the store holds fewer rows than the server reports. The
   * table and the KPIs then describe less than the fleet, which is the
   * difference between a short list and a wrong number.
   */
  protected readonly truncationNote = computed(() =>
    collectionNote(this.hosts().length, this.hostStore.total(), this.hostStore.complete(), 'hosts')
  );

  constructor() {
    this.load();
  }

  protected refresh(): void {
    this.load();
  }

  protected onMineToggle(checked: boolean): void {
    this.onlyMyHosts.set(checked);
    this.load();
  }

  protected onClassChange(value: string): void {
    this.hostClass.set(value === '' ? undefined : (value as HostClass));
    this.load();
  }

  private load(): void {
    this.hostStore.loadAll({mine: this.onlyMyHosts(), hostClass: this.hostClass()});
  }

  protected onKpiClick(item: KpiItem): void {
    if (item.route) {
      void this.router.navigate([item.route]);
    }
  }

  protected onToggleRow(id: string): void {
    const next = new Set(this.selectedIds());
    if (next.has(id)) {
      next.delete(id);
    } else {
      next.add(id);
    }
    this.selectedIdSet.set(next);
  }

  protected onToggleAll(checked: boolean): void {
    const next = new Set(this.selectedIds());
    for (const host of this.filteredHosts()) {
      if (checked) {
        next.add(host.id);
      } else {
        next.delete(host.id);
      }
    }
    this.selectedIdSet.set(next);
  }

  /**
   * Successful deletes leave the store (removeEntities), so the selection
   * shrinks on its own; anything that failed stays selected for a retry.
   */
  protected deleteSelected(): void {
    this.actions.deleteMany(this.selectedHosts());
  }
}
